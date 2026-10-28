#!/usr/bin/env python3
"""Create a new isolated Video Lab identity only after explicit authorization.
Never replaces an existing resource, identity or policy; never prints credentials.
"""
import json
import os
import re
import secrets
import sys
import uuid
from pathlib import Path

from lab_config import (MC_IMAGE, Kubectl, LabError, configure, option, parser_for,
                        prepare_evidence, validate_state_target, write_state)


PROVISION_SCRIPT = '''set +x
set -eu
umask 077
export MC_CONFIG_DIR=/tmp/mc MC_NO_COLOR=1
export MC_HOST_admin="${S3_SCHEME}://${MINIO_ROOT_USER}:${MINIO_ROOT_PASSWORD}@${S3_AUTHORITY}"
if mc --json admin user info admin "$LAB_ACCESS_KEY" >/tmp/check-user 2>&1; then
    printf 'FAIL identity already exists\\n'; exit 1
fi
grep -Eq '"Code"[[:space:]]*:[[:space:]]*"(XMinioAdminNoSuchUser|NoSuchUser)"' /tmp/check-user || { printf 'FAIL identity absence unconfirmed\\n'; exit 1; }
if mc --json admin policy info admin "$POLICY_NAME" >/tmp/check-policy 2>&1; then
    printf 'FAIL policy already exists\\n'; exit 1
fi
grep -Eq '"Code"[[:space:]]*:[[:space:]]*"(XMinioAdminNoSuchPolicy|NoSuchPolicy)"' /tmp/check-policy || { printf 'FAIL policy absence unconfirmed\\n'; exit 1; }
mc admin policy create admin "$POLICY_NAME" /opt/lab/policy.json >/tmp/response 2>&1 || { printf 'FAIL policy creation\\n'; exit 1; }
printf '%s\\n%s\\n' "$LAB_ACCESS_KEY" "$LAB_SECRET_KEY" | mc admin user add admin >/tmp/response 2>&1 || { printf 'FAIL identity creation\\n'; exit 1; }
mc admin policy attach admin "$POLICY_NAME" --user "$LAB_ACCESS_KEY" >/tmp/response 2>&1 || { printf 'FAIL policy attachment\\n'; exit 1; }
printf 'PASS isolated Video Lab identity configured\\n'
'''


def build_parser():
    parser = parser_for(__doc__)
    parser.add_argument("--authorized-new-identity", action="store_true")
    parser.add_argument("--resume-name", help="Existing partial run to inspect and resume; no credential rotation")
    option(parser, "--admin-secret", "VIDEO_LAB_ADMIN_SECRET", default="mansiflix-minio-admin")
    option(parser, "--mc-image", "VIDEO_LAB_MC_IMAGE", default=MC_IMAGE)
    return parser


def policy_manifest(args, state):
    return {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {
        "name": state["secretName"] + "-policy", "namespace": args.namespace,
        "labels": {"mansiflix.local/video-lab-run": state["run"]}}, "immutable": True,
        "data": {"policy.json": Path(__file__).with_name("minio-policy.json").read_text()}}


def provision_job(args, state):
    name = state["secretName"]
    env = [{"name": var, "valueFrom": {"secretKeyRef": {"name": secret, "key": key}}}
           for var, secret, key in [
               ("MINIO_ROOT_USER", args.admin_secret, "MINIO_ROOT_USER"),
               ("MINIO_ROOT_PASSWORD", args.admin_secret, "MINIO_ROOT_PASSWORD"),
               ("LAB_ACCESS_KEY", name, "access-key"), ("LAB_SECRET_KEY", name, "secret-key")]]
    env += [{"name": "POLICY_NAME", "value": name},
            {"name": "S3_SCHEME", "value": args.endpoint_scheme},
            {"name": "S3_AUTHORITY", "value": args.endpoint_authority}]
    pod = {"restartPolicy": "Never", "automountServiceAccountToken": False,
           "securityContext": {"runAsNonRoot": True, "runAsUser": 10001, "runAsGroup": 10001,
                               "fsGroup": 10001, "seccompProfile": {"type": "RuntimeDefault"}},
           "containers": [{"name": "provision", "image": args.mc_image,
               "imagePullPolicy": args.image_pull_policy, "command": ["/bin/sh", "-ec"],
               "args": [PROVISION_SCRIPT], "env": env,
               "resources": {"requests": {"cpu": "100m", "memory": "128Mi"},
                             "limits": {"cpu": "500m", "memory": "512Mi"}},
               "securityContext": {"allowPrivilegeEscalation": False,
                                   "readOnlyRootFilesystem": True, "capabilities": {"drop": ["ALL"]}},
               "volumeMounts": [{"name": "temp", "mountPath": "/tmp"},
                                {"name": "policy", "mountPath": "/opt/lab", "readOnly": True}]}],
           "volumes": [{"name": "temp", "emptyDir": {"sizeLimit": "64Mi"}},
                       {"name": "policy", "configMap": {"name": name + "-policy", "defaultMode": 292}}]}
    if args.node:
        pod["nodeSelector"] = {"kubernetes.io/hostname": args.node}
    return {"apiVersion": "batch/v1", "kind": "Job",
            "metadata": {"name": name + "-provision", "namespace": args.namespace},
            "spec": {"backoffLimit": 0, "activeDeadlineSeconds": 120, "template": {
                "metadata": {"labels": {"app.kubernetes.io/part-of": "mansiflix",
                    "mansiflix.local/minio-access": "true", "mansiflix.local/video-lab-run": state["run"]}},
                "spec": pod}}}


def main(argv=None):
    parser = build_parser()
    args = configure(parser, parser.parse_args(argv))
    if not args.authorized_new_identity:
        parser.error("--authorized-new-identity requires explicit authorization")
    resume = None
    if args.resume_name:
        resume = re.fullmatch(r"mansiflix-video-lab-([a-f0-9]{10})", args.resume_name)
        if not resume:
            parser.error("--resume-name must identify a Video Lab run")
    if args.state_file.exists() and not resume:
        raise LabError("State file already exists; use a new directory or an explicit resume")
    os.umask(0o077)
    prepare_evidence(args)
    kube = Kubectl(args)
    kube.check_target()
    run = resume[1] if resume else uuid.uuid4().hex[:10]
    name = "mansiflix-video-lab-" + run
    state = {"run": run, "secretName": name, "context": args.context, "namespace": args.namespace,
             "endpoint": args.endpoint, "resources": []}
    if resume and args.state_file.exists():
        previous = json.loads(args.state_file.read_text())
        validate_state_target(args, previous)
        if previous["run"] != run:
            raise LabError("Resume name does not match state file")
        state["resources"] = previous["resources"]
    if not resume:
        write_state(args, state)
        # Values exist only in this process and the Secret payload, never evidence/logs.
        secret = {"apiVersion": "v1", "kind": "Secret", "metadata": {
            "name": name, "namespace": args.namespace, "labels": {
                "app.kubernetes.io/name": "mansiflix-video-lab", "mansiflix.local/video-lab-run": run}},
            "type": "Opaque", "stringData": {
                "access-key": "video-lab-" + run, "secret-key": secrets.token_urlsafe(48)}}
        state["resources"].append(kube.create(secret))
        write_state(args, state)
        state["resources"].append(kube.create(policy_manifest(args, state)))
        write_state(args, state)
    else:
        # Inspect only metadata. Do not request Secret data or replace credentials.
        for kind, suffix in [("Secret", ""), ("ConfigMap", "-policy")]:
            uid = kube.call(["get", kind, name + suffix, "-o", "jsonpath={.metadata.uid}"]).strip()
            if not uid:
                raise LabError("Partial resource not found")
            old = next((r for r in state["resources"] if r["kind"] == kind and r["name"] == name + suffix), None)
            if old and old["uid"] != uid:
                raise LabError("Partial resource UID changed; refusing resume")
            if not old:
                state["resources"].append({"kind": kind, "name": name + suffix, "uid": uid})
        write_state(args, state)
    job = provision_job(args, state)
    (args.evidence_dir / "provision-job.json").write_text(json.dumps(job, indent=2) + "\n")
    state["resources"].append(kube.create(job))
    write_state(args, state)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except LabError as error:
        sys.exit(str(error))
    except (OSError, ValueError):
        # File/API errors must not dump Secret-bearing payloads or environment.
        sys.exit("Provisioning failed; inspect target, paths and resource names without dumping Secrets")

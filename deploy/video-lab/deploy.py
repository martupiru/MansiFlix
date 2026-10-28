#!/usr/bin/env python3
"""Deploy new isolated Video Lab resources without replacing existing objects.

A static binary is delivered in immutable ConfigMap chunks and checked by SHA-256.
Target cluster, optional node placement and local paths are explicit configuration.
"""
import base64
import gzip
import hashlib
import json
import os
import shlex
import sys
from pathlib import Path

from lab_config import (WORKER_IMAGE, Kubectl, LabError, configure, option, parser_for,
                        prepare_evidence, validate_state_target, write_state)


def build_parser():
    parser = parser_for(__doc__)
    option(parser, "--binary", "VIDEO_LAB_BINARY", type=Path, required=True,
           help="Path to the previously compiled Linux/amd64 executable")
    option(parser, "--worker-image", "VIDEO_LAB_WORKER_IMAGE", default=WORKER_IMAGE)
    parser.add_argument("--render-only", action="store_true", help="Write manifests locally; do not call kubectl")
    return parser


def render(args, state, binary):
    run = state["run"]
    name = "mansiflix-video-lab-" + run
    packed = gzip.compress(binary, mtime=0)
    digest = hashlib.sha256(binary).hexdigest()
    chunks = [packed[i:i + 650000] for i in range(0, len(packed), 650000)]
    volumes, mounts, manifests = [], [], []
    for i, data in enumerate(chunks):
        part = f"part-{i:02}"
        cm_name = name + "-" + part
        manifests.append({"apiVersion": "v1", "kind": "ConfigMap", "metadata": {
            "name": cm_name, "namespace": args.namespace, "labels": {
                "app.kubernetes.io/name": "mansiflix-video-lab", "mansiflix.local/video-lab-run": run}},
            "immutable": True, "binaryData": {"chunk.gz": base64.b64encode(data).decode()}})
        volumes.append({"name": part, "configMap": {"name": cm_name, "defaultMode": 292}})
        mounts.append({"name": part, "mountPath": "/artifacts/" + part, "readOnly": True})
    security = {"allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True,
                "capabilities": {"drop": ["ALL"]}}
    volumes += [{"name": "runtime", "emptyDir": {"sizeLimit": "32Mi"}},
                {"name": "work", "emptyDir": {"sizeLimit": "1Gi"}},
                {"name": "temp", "emptyDir": {"sizeLimit": "64Mi"}}]
    # /runtime, /work and /tmp are container mounts, not paths on the caller's host.
    script = ("set -eu; umask 077; cat /artifacts/part-*/chunk.gz | gzip -d >/runtime/video-lab; "
              f"printf '%s  /runtime/video-lab\\n' '{digest}' | sha256sum -c -; "
              "chmod 700 /runtime/video-lab")
    pod = {
        "automountServiceAccountToken": False,
        "securityContext": {"runAsNonRoot": True, "runAsUser": 10001, "runAsGroup": 10001,
                            "fsGroup": 10001, "seccompProfile": {"type": "RuntimeDefault"}},
        "initContainers": [{"name": "install-app", "image": args.worker_image,
            "imagePullPolicy": args.image_pull_policy, "command": ["/bin/sh", "-ec"], "args": [script],
            "resources": {"requests": {"cpu": "100m", "memory": "32Mi"},
                          "limits": {"cpu": "500m", "memory": "128Mi"}},
            "securityContext": security, "volumeMounts": mounts + [{"name": "runtime", "mountPath": "/runtime"}]}],
        "containers": [{"name": "web", "image": args.worker_image,
            "imagePullPolicy": args.image_pull_policy, "command": ["/runtime/video-lab"],
            "env": [{"name": "S3_ENDPOINT", "value": args.endpoint},
                    {"name": "WORK_DIR", "value": "/work"}, {"name": "GOMEMLIMIT", "value": "96MiB"},
                    {"name": "S3_ACCESS_KEY", "valueFrom": {"secretKeyRef": {"name": state["secretName"], "key": "access-key"}}},
                    {"name": "S3_SECRET_KEY", "valueFrom": {"secretKeyRef": {"name": state["secretName"], "key": "secret-key"}}}],
            "ports": [{"name": "http", "containerPort": 8080}],
            "startupProbe": {"httpGet": {"path": "/healthz", "port": "http"}, "periodSeconds": 2, "failureThreshold": 30},
            "readinessProbe": {"httpGet": {"path": "/healthz", "port": "http"}, "periodSeconds": 5},
            "livenessProbe": {"httpGet": {"path": "/healthz", "port": "http"}, "periodSeconds": 15},
            "securityContext": security,
            "resources": {"requests": {"cpu": "200m", "memory": "256Mi", "ephemeral-storage": "256Mi"},
                          "limits": {"cpu": "1", "memory": "512Mi", "ephemeral-storage": "2Gi"}},
            "volumeMounts": [{"name": "runtime", "mountPath": "/runtime", "readOnly": True},
                             {"name": "work", "mountPath": "/work"}, {"name": "temp", "mountPath": "/tmp"}]}],
        "volumes": volumes,
    }
    if args.node:
        pod["nodeSelector"] = {"kubernetes.io/hostname": args.node}
    labels = {"app.kubernetes.io/name": "mansiflix-video-lab", "mansiflix.local/video-lab-run": run}
    manifests += [{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {
        "name": name, "namespace": args.namespace, "labels": labels}, "spec": {"replicas": 1,
        "strategy": {"type": "Recreate"}, "selector": {"matchLabels": labels}, "template": {
            "metadata": {"labels": {**labels, "app.kubernetes.io/part-of": "mansiflix",
                                    "mansiflix.local/minio-access": "true"}}, "spec": pod}}},
        {"apiVersion": "v1", "kind": "Service", "metadata": {"name": name, "namespace": args.namespace},
         "spec": {"type": "ClusterIP", "selector": labels,
                  "ports": [{"name": "http", "port": 8080, "targetPort": "http"}]}}]
    return manifests, name, digest


def main(argv=None):
    parser = build_parser()
    args = configure(parser, parser.parse_args(argv))
    args.binary = args.binary.expanduser().resolve()
    # Validate all local inputs before any Kubernetes call or mutation.
    state = json.loads(args.state_file.read_text())
    validate_state_target(args, state)
    binary = args.binary.read_bytes()
    if not binary:
        raise LabError("Binary is empty")
    manifests, name, digest = render(args, state, binary)
    os.umask(0o077)
    prepare_evidence(args)
    (args.evidence_dir / "manifests.json").write_text(json.dumps({
        "apiVersion": "v1", "kind": "List", "items": manifests}, indent=2) + "\n")
    if args.render_only:
        print("RENDERED locally; no Kubernetes calls performed")
        return 0
    kube = Kubectl(args)
    kube.check_target()
    state.update(appName=name, binarySHA256=digest, context=args.context,
                 namespace=args.namespace, endpoint=args.endpoint)
    for obj in manifests:
        state["resources"].append(kube.create(obj))
        write_state(args, state)
    command = args.kubectl_base + ["port-forward", "--address=127.0.0.1", "service/" + name, "8080:8080"]
    prefix = ""
    if args.kubeconfig:
        prefix = "KUBECONFIG=" + shlex.quote(str(args.kubeconfig)) + " "
    print("PORT_FORWARD: " + prefix + shlex.join(command))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except LabError as error:
        sys.exit(str(error))
    except (OSError, ValueError):
        sys.exit("Deployment failed; check local binary/state paths and input format")

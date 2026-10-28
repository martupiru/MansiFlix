"""Portable CLI configuration and credential-safe kubectl helpers for Video Lab."""

import argparse
import json
import os
import re
import subprocess
from pathlib import Path
from urllib.parse import urlsplit

WORKER_IMAGE = "mansiflix/video-worker:demo-88bb1d91e247"
MC_IMAGE = "mansiflix/minio-mc:source-7394ce0dd2a8-amd64-r1"


class LabError(Exception):
    pass


def option(parser, flag, env, **kwargs):
    """CLI flags override environment; required settings never pick a host path."""
    value = os.environ.get(env) or None
    required = kwargs.pop("required", False)
    fallback = kwargs.pop("default", None)
    parser.add_argument(
        flag, default=value if value is not None else fallback,
        required=required and value is None,
        help=kwargs.pop("help", "") + f" (env: {env})", **kwargs,
    )


def parser_for(description):
    parser = argparse.ArgumentParser(description=description)
    option(parser, "--context", "VIDEO_LAB_CONTEXT", required=True,
           help="Explicit target kubeconfig context")
    option(parser, "--namespace", "VIDEO_LAB_NAMESPACE", default="mansiflix")
    option(parser, "--kubeconfig", "VIDEO_LAB_KUBECONFIG", type=Path,
           help="Optional single kubeconfig file; otherwise preserve KUBECONFIG/kubectl defaults")
    option(parser, "--evidence-dir", "VIDEO_LAB_EVIDENCE_DIR", type=Path, required=True)
    option(parser, "--state-file", "VIDEO_LAB_STATE_FILE", type=Path,
           help="Defaults to deployment-state.json inside evidence-dir")
    option(parser, "--node", "VIDEO_LAB_NODE",
           help="Optional kubernetes.io/hostname selector; omitted means normal scheduling")
    option(parser, "--endpoint", "VIDEO_LAB_S3_ENDPOINT",
           help="Defaults to http://minio.<namespace>.svc.cluster.local:9000")
    option(parser, "--image-pull-policy", "VIDEO_LAB_IMAGE_PULL_POLICY",
           choices=["Never", "IfNotPresent", "Always"], default="Never")
    return parser


def configure(parser, args):
    for name in ["context", "namespace"]:
        value = getattr(args, name)
        if not value or value.strip() != value:
            parser.error(f"--{name} must be nonempty with no surrounding whitespace")
    if not re.fullmatch(r"[a-z0-9]([-a-z0-9]*[a-z0-9])?", args.namespace) or len(args.namespace) > 63:
        parser.error("--namespace must be a Kubernetes namespace name")
    args.node = args.node or None
    args.evidence_dir = args.evidence_dir.expanduser().resolve()
    args.state_file = (args.state_file.expanduser().resolve() if args.state_file
                       else args.evidence_dir / "deployment-state.json")
    args.endpoint = args.endpoint or f"http://minio.{args.namespace}.svc.cluster.local:9000"
    endpoint = urlsplit(args.endpoint)
    if (endpoint.scheme not in ["http", "https"] or not endpoint.hostname
            or endpoint.username is not None or endpoint.password is not None
            or endpoint.path not in ["", "/"] or endpoint.query or endpoint.fragment):
        parser.error("--endpoint must be an HTTP(S) origin without credentials, path, query or fragment")
    args.endpoint = args.endpoint.rstrip("/")
    args.endpoint_scheme, args.endpoint_authority = endpoint.scheme, endpoint.netloc
    # Only the subprocess copy is changed. Never overwrite the caller's environment.
    args.kubectl_env = os.environ.copy()
    if args.kubeconfig:
        args.kubeconfig = args.kubeconfig.expanduser().resolve()
        if not args.kubeconfig.is_file():
            parser.error("--kubeconfig file does not exist")
        args.kubectl_env["KUBECONFIG"] = str(args.kubeconfig)
    args.kubectl_base = ["kubectl", "--context", args.context, "--namespace", args.namespace,
                         "--request-timeout=20s"]
    return args


def prepare_evidence(args):
    args.evidence_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    args.state_file.parent.mkdir(mode=0o700, parents=True, exist_ok=True)


def write_state(args, state):
    # Metadata only; Secret values are never written to state/evidence files.
    args.state_file.write_text(json.dumps(state, indent=2) + "\n")


def validate_state_target(args, state):
    for field in ["context", "namespace", "endpoint"]:
        if field in state and state[field] != getattr(args, field):
            raise LabError(f"State {field} does not match the explicit target; refusing deployment")
    if not re.fullmatch(r"[a-f0-9]{10}", state.get("run", "")):
        raise LabError("State contains an invalid run identifier")
    if state.get("secretName") != "mansiflix-video-lab-" + state["run"]:
        raise LabError("State contains an unexpected Secret reference")
    if not isinstance(state.get("resources"), list):
        raise LabError("State must contain a resource inventory")


class Kubectl:
    def __init__(self, args):
        self.args = args

    def call(self, options, payload=None):
        cmd = self.args.kubectl_base + options
        with (self.args.evidence_dir / "commands.jsonl").open("a") as log:
            log.write(json.dumps(cmd) + "\n")
        try:
            result = subprocess.run(cmd, input=payload, capture_output=True, text=True,
                                    timeout=30, env=self.args.kubectl_env)
        except (OSError, subprocess.TimeoutExpired):
            raise LabError("kubectl unavailable or request timed out") from None
        if result.returncode:
            # API errors can echo submitted objects. Never expose stderr/payload.
            raise LabError("Kubernetes operation failed (details suppressed): " + " ".join(options[:2]))
        return result.stdout

    def check_target(self):
        # Verify the selected context, even if current-context is a different one.
        names = self.call(["config", "get-contexts", self.args.context, "-o", "name"])
        if self.args.context not in names.splitlines():
            raise LabError("Selected kubeconfig context is not configured")
        namespace = json.loads(self.call(["get", "namespace", self.args.namespace, "-o", "json"]))
        if namespace.get("status", {}).get("phase") != "Active":
            raise LabError("Selected namespace is not Active")

    def create(self, obj):
        kind, name = obj["kind"], obj["metadata"]["name"]
        if obj["metadata"]["namespace"] != self.args.namespace:
            raise LabError("Manifest namespace does not match selected namespace")
        if self.call(["get", kind, name, "--ignore-not-found", "-o", "name"]).strip():
            raise LabError("Refusing existing resource: " + name)
        # Request metadata only, including for Secret creation.
        uid = self.call(["create", "-f", "-", "-o", "jsonpath={.metadata.uid}"], json.dumps(obj)).strip()
        if not uid:
            raise LabError("Resource creation did not return a UID")
        print("CREATED", kind, name, "UID", uid, flush=True)
        return {"kind": kind, "name": name, "uid": uid}

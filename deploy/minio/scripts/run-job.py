#!/usr/bin/env python3
"""Create and observe one NEW demo Job; never updates an existing resource.

Credentials are referenced by the manifests, never accepted by this CLI.
Kubernetes commands and non-secret evidence are saved outside the repository.
"""
import argparse
import json
from pathlib import Path
import shlex
import subprocess
import sys
import time
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", choices=["bootstrap-job.yaml", "smoke-test-job.yaml", "seed-demo-job.yaml", "hls-demo-job.yaml", "verify-demo-job.yaml", "network-test-job.yaml"])
    parser.add_argument("--context", required=True)
    parser.add_argument("--ssh-target")
    parser.add_argument("--evidence-dir", type=Path, required=True)
    parser.add_argument("--node")
    parser.add_argument("--env", action="append", default=[])
    parser.add_argument("--network-peer", choices=["allowed", "no-labels", "part-only", "access-only"], default="allowed")
    parser.add_argument("--confirm-create-job", action="store_true")
    args = parser.parse_args()
    if not args.confirm_create_job or not args.context.strip():
        parser.error("explicit context and --confirm-create-job are required")
    args.evidence_dir.mkdir(mode=0o700, parents=True, exist_ok=True)

    def call(options, payload=None, allowed=(0,)):
        cmd = ["kubectl", "--context", args.context, "--request-timeout=20s", "-n", "mansiflix", *options]
        if args.ssh_target:
            cmd = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=12", args.ssh_target,
                   shlex.join(["sudo", "-n", "k0s", *cmd])]
        with (args.evidence_dir / "commands.jsonl").open("a") as log:
            log.write(json.dumps(cmd) + "\n")
        result = subprocess.run(cmd, input=payload, text=True, capture_output=True)
        if result.returncode not in allowed:
            # These payloads contain Secret references only, but still keep
            # arbitrary API errors out of the console.
            sys.exit("Kubernetes operation failed: " + " ".join(options[:2]))
        return result

    path = Path(__file__).resolve().parent.parent / args.manifest
    job = json.loads(call(["create", "--dry-run=client", "--validate=false", "-f", "-", "-o", "json"], path.read_text()).stdout)
    if job.get("kind") != "Job" or job["metadata"].get("namespace") != "mansiflix":
        sys.exit("Refusing an unexpected resource or namespace")
    name = job["metadata"].pop("generateName") + uuid.uuid4().hex[:8]
    job["metadata"]["name"] = name
    job.pop("status", None)
    template = job["spec"]["template"]
    if args.node:
        template["spec"]["nodeSelector"] = {"kubernetes.io/hostname": args.node}
    for item in args.env:
        key, sep, value = item.partition("=")
        if not sep or key not in {"TEST_MODE", "CHECK_UID", "EXPECTED_SHA256"}:
            parser.error("only non-secret verifier parameters may be supplied")
        for c in template["spec"]["containers"]:
            existing = [e for e in c.get("env", []) if e["name"] != key]
            c["env"] = existing + [{"name": key, "value": value}]
    if args.manifest == "network-test-job.yaml":
        labels = template["metadata"]["labels"]
        if args.network_peer in {"no-labels", "access-only"}:
            labels.pop("app.kubernetes.io/part-of", None)
        if args.network_peer in {"no-labels", "part-only"}:
            labels.pop("mansiflix.local/minio-access", None)
        template["spec"]["containers"][0]["env"] = [{"name": "EXPECT_ACCESS", "value": "true" if args.network_peer == "allowed" else "false"}]
    payload = json.dumps(job)
    if call(["get", "job", name, "--ignore-not-found", "-o", "name"]).stdout.strip():
        sys.exit("Refusing an existing Job name")
    diff = call(["diff", "-f", "-"], payload, allowed=(0, 1)).stdout
    if not diff or any(line.startswith("-") and not line.startswith("---") for line in diff.splitlines()):
        sys.exit("Expected a creation-only diff; refusing mutation")
    (args.evidence_dir / (name + ".diff")).write_text(diff)
    print("REVIEW: create only", name, "node=" + (args.node or "scheduler"), flush=True)
    result = call(["create", "-f", "-", "-o", "json"], payload)
    created = json.loads(result.stdout)
    (args.evidence_dir / (name + ".json")).write_text(json.dumps(created, indent=2))
    print("CREATED", name, flush=True)
    deadline = time.monotonic() + 300
    complete = False
    while time.monotonic() < deadline:
        observed = json.loads(call(["get", "job", name, "-o", "json"]).stdout)
        conditions = observed.get("status", {}).get("conditions", [])
        terminal = [c["type"] for c in conditions if c.get("status") == "True" and c["type"] in {"Complete", "Failed"}]
        if terminal:
            complete = "Complete" in terminal
            break
        time.sleep(2)
    (args.evidence_dir / (name + "-final.json")).write_text(json.dumps(observed, indent=2))
    print("CONDITIONS", json.dumps(observed.get("status", {}).get("conditions", [])), flush=True)
    pods = json.loads(call(["get", "pods", "-l", "job-name=" + name, "-o", "json"]).stdout)
    (args.evidence_dir / (name + "-pods.json")).write_text(json.dumps(pods, indent=2))
    for pod in pods["items"]:
        pod_name = pod["metadata"]["name"]
        print("POD", pod_name, "UID", pod["metadata"]["uid"], "NODE", pod["spec"].get("nodeName"), flush=True)
        for container in pod["spec"].get("initContainers", []) + pod["spec"]["containers"]:
            logs = call(["logs", pod_name, "-c", container["name"]], allowed=(0, 1))
            (args.evidence_dir / (name + "-" + container["name"] + ".log")).write_text(logs.stdout)
            print(logs.stdout, end="", flush=True)
    print("RESULT", name, "PASS" if complete else "FAILED OR TIMED OUT", flush=True)
    return 0 if complete else 1


if __name__ == "__main__":
    sys.exit(main())

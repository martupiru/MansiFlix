#!/usr/bin/env python3
"""Explicitly invoked ONLY after approval; no credentials in argv or output."""
import argparse
import json
import os
import secrets
import subprocess
import sys


def kubectl(context, args, payload=None):
    return subprocess.run(
        ["kubectl", "--context", context, "--request-timeout=20s", *args],
        input=payload, text=True, capture_output=True, check=False,
    )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--context", required=True, help="explicit approved kubeconfig context")
    parser.add_argument("--confirm-create-secrets", action="store_true")
    args = parser.parse_args()
    if not args.confirm_create_secrets:
        parser.error("requires --confirm-create-secrets after explicit deployment approval")
    if not args.context.strip() or args.context != args.context.strip():
        parser.error("--context must be non-empty and have no surrounding whitespace")
    os.umask(0o077)
    names = ["mansiflix-minio-admin", "mansiflix-minio-worker"]
    for name in names:
        result = kubectl(args.context, ["get", "secret", name, "-n", "mansiflix", "--ignore-not-found", "-o", "name"])
        if result.returncode:
            sys.exit("Cannot verify Secret names; no values were displayed and nothing was created.")
        if result.stdout.strip():
            sys.exit("A target Secret already exists. Refusing to replace credentials.")
    data = [
        {"MINIO_ROOT_USER": "mansiflix-admin", "MINIO_ROOT_PASSWORD": secrets.token_urlsafe(48)},
        {"access-key": "mansiflix-worker", "secret-key": secrets.token_urlsafe(48)},
    ]
    for name, values in zip(names, data):
        payload = json.dumps({
            "apiVersion": "v1", "kind": "Secret", "type": "Opaque",
            "metadata": {"name": name, "namespace": "mansiflix", "labels": {"app.kubernetes.io/part-of": "mansiflix"}},
            "stringData": values,
        })
        result = kubectl(args.context, ["create", "-n", "mansiflix", "-f", "-"], payload)
        if result.returncode:
            # kubectl error output can include the submitted object. Suppress it.
            sys.exit("Secret creation failed; partial creation is possible. Review names only before retrying.")
    print("Created the two MinIO Secrets; values were not printed or saved locally.")


if __name__ == "__main__":
    main()

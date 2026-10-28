"""Offline portability/targeting tests. kubectl and all mutations are simulated."""
import base64
import contextlib
import gzip
import hashlib
import importlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
import deploy
import provision
from lab_config import Kubectl, LabError, configure, prepare_evidence


class PortableCLI(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="video-lab-cli-")
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name).resolve()
        self.environment = patch.dict(os.environ, {}, clear=True)
        self.environment.start()
        self.addCleanup(self.environment.stop)

    def args(self, script=deploy, extra=()):
        parser = script.build_parser()
        flags = ["--context", "selected-target", "--namespace", "film-lab",
                 "--evidence-dir", str(self.root / "evidence")]
        if script is deploy:
            flags += ["--binary", str(self.root / "app")]
        return configure(parser, parser.parse_args(flags + list(extra)))

    def state(self, **extra):
        return {"run": "0123456789", "secretName": "mansiflix-video-lab-0123456789",
                "context": "selected-target", "namespace": "film-lab", "resources": [], **extra}

    def test_inherited_kubeconfig_is_not_overwritten(self):
        os.environ["KUBECONFIG"] = "first-config" + os.pathsep + "second-config"
        before = dict(os.environ)
        args = self.args()
        self.assertEqual(args.kubectl_env["KUBECONFIG"], before["KUBECONFIG"])
        self.assertEqual(dict(os.environ), before)
        self.assertIsNone(args.kubeconfig)

    def test_explicit_kubeconfig_changes_only_subprocess_environment(self):
        path = self.root / "custom config"
        path.write_text("synthetic placeholder")
        os.environ["KUBECONFIG"] = "inherited-config"
        args = self.args(extra=["--kubeconfig", str(path)])
        self.assertEqual(args.kubectl_env["KUBECONFIG"], str(path))
        self.assertEqual(os.environ["KUBECONFIG"], "inherited-config")

    def test_cli_overrides_environment(self):
        os.environ.update(VIDEO_LAB_CONTEXT="environment-target", VIDEO_LAB_NAMESPACE="env-lab",
                          VIDEO_LAB_NODE="env-node", VIDEO_LAB_BINARY="env-binary",
                          VIDEO_LAB_EVIDENCE_DIR="env-evidence")
        args = self.args(extra=["--node", "cli-node"])
        self.assertEqual(args.context, "selected-target")
        self.assertEqual(args.namespace, "film-lab")
        self.assertEqual(args.node, "cli-node")
        self.assertEqual(args.binary, self.root / "app")
        self.assertEqual(args.evidence_dir, self.root / "evidence")

    def test_environment_only_configuration(self):
        os.environ.update(VIDEO_LAB_CONTEXT="env-target", VIDEO_LAB_NAMESPACE="env-lab",
                          VIDEO_LAB_NODE="env-node", VIDEO_LAB_BINARY=str(self.root / "app"),
                          VIDEO_LAB_EVIDENCE_DIR=str(self.root / "evidence"),
                          VIDEO_LAB_STATE_FILE=str(self.root / "state.json"))
        parser = deploy.build_parser()
        args = configure(parser, parser.parse_args([]))
        self.assertEqual(args.context, "env-target")
        self.assertEqual(args.state_file, self.root / "state.json")
        self.assertEqual(args.endpoint, "http://minio.env-lab.svc.cluster.local:9000")

    def test_missing_context_is_rejected_without_kubernetes_calls(self):
        with patch("subprocess.run") as run, contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit) as exc:
                deploy.build_parser().parse_args(["--binary", "app", "--evidence-dir", "evidence"])
            self.assertEqual(exc.exception.code, 2)
            run.assert_not_called()

    def test_local_paths_are_required(self):
        for flags in [[], ["--evidence-dir", "evidence"], ["--binary", "app"]]:
            with self.subTest(flags=flags), contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    deploy.build_parser().parse_args(["--context", "target"] + flags)

    def test_help_does_not_read_state_or_contact_kubernetes(self):
        for module in [deploy, provision]:
            with self.subTest(script=module.__name__), patch.object(Path, "read_text", side_effect=AssertionError("file read")), patch("subprocess.run") as run, contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaises(SystemExit) as exc:
                    module.main(["--help"])
                self.assertEqual(exc.exception.code, 0)
                run.assert_not_called()

    def test_imports_have_no_file_or_subprocess_side_effects(self):
        with patch.object(Path, "read_text", side_effect=AssertionError("file read")), patch("subprocess.run") as run:
            importlib.reload(deploy)
            importlib.reload(provision)
            run.assert_not_called()

    def test_endpoint_rejects_credentials(self):
        with contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit):
                self.args(extra=["--endpoint", "https://synthetic:dummy@store.example.test"])

    def test_default_scheduling_has_no_node_selector(self):
        args = self.args()
        manifests, _, _ = deploy.render(args, self.state(), b"synthetic binary")
        pod = next(x for x in manifests if x["kind"] == "Deployment")["spec"]["template"]["spec"]
        self.assertNotIn("nodeSelector", pod)
        p_args = self.args(provision)
        self.assertNotIn("nodeSelector", provision.provision_job(p_args, self.state())["spec"]["template"]["spec"])

    def test_custom_target_node_images_endpoint_and_namespace(self):
        args = self.args(extra=["--node", "compute-blue", "--worker-image", "registry.example.test/lab:v2",
                               "--image-pull-policy", "IfNotPresent", "--endpoint", "https://storage.example.test:9443"])
        manifests, name, _ = deploy.render(args, self.state(), b"synthetic binary")
        self.assertTrue(all(x["metadata"]["namespace"] == "film-lab" for x in manifests))
        pod = next(x for x in manifests if x["kind"] == "Deployment")["spec"]["template"]["spec"]
        self.assertEqual(pod["nodeSelector"], {"kubernetes.io/hostname": "compute-blue"})
        for c in pod["containers"] + pod["initContainers"]:
            self.assertEqual(c["image"], "registry.example.test/lab:v2")
            self.assertEqual(c["imagePullPolicy"], "IfNotPresent")
        env = {x["name"]: x.get("value") for x in pod["containers"][0]["env"]}
        self.assertEqual(env["S3_ENDPOINT"], "https://storage.example.test:9443")
        self.assertEqual(name, "mansiflix-video-lab-0123456789")

    def test_provision_uses_custom_admin_reference_and_endpoint(self):
        args = self.args(provision, ["--node", "admin-compute", "--admin-secret", "lab-admin",
                                   "--mc-image", "registry.example.test/mc:v1", "--endpoint", "https://storage.example.test:9443"])
        pod = provision.provision_job(args, self.state())["spec"]["template"]["spec"]
        c = pod["containers"][0]
        env = {x["name"]: x for x in c["env"]}
        self.assertEqual(env["MINIO_ROOT_PASSWORD"]["valueFrom"]["secretKeyRef"]["name"], "lab-admin")
        self.assertEqual(env["S3_AUTHORITY"]["value"], "storage.example.test:9443")
        self.assertEqual(env["S3_SCHEME"]["value"], "https")
        self.assertEqual(c["image"], "registry.example.test/mc:v1")
        self.assertEqual(pod["nodeSelector"], {"kubernetes.io/hostname": "admin-compute"})
        subprocess.run(["sh", "-n"], input=c["args"][0], text=True, check=True)

    def test_binary_chunks_round_trip_and_digest(self):
        binary = os.urandom(1400000)
        manifests, _, digest = deploy.render(self.args(), self.state(), binary)
        chunks = [base64.b64decode(x["binaryData"]["chunk.gz"]) for x in manifests if x["kind"] == "ConfigMap"]
        self.assertTrue(all(len(c) <= 650000 for c in chunks))
        self.assertEqual(gzip.decompress(b"".join(chunks)), binary)
        self.assertEqual(digest, hashlib.sha256(binary).hexdigest())

    def test_render_only_writes_manifests_without_mutation_or_target_access(self):
        args = self.args()
        args.state_file.parent.mkdir(parents=True)
        original = json.dumps(self.state())
        args.state_file.write_text(original)
        args.binary.write_bytes(b"synthetic binary")
        with patch.object(deploy, "Kubectl", side_effect=AssertionError("Kubernetes accessed")), contextlib.redirect_stdout(io.StringIO()):
            code = deploy.main(["--context", args.context, "--namespace", args.namespace,
                                "--evidence-dir", str(args.evidence_dir), "--binary", str(args.binary), "--render-only"])
        self.assertEqual(code, 0)
        self.assertEqual(args.state_file.read_text(), original)
        self.assertEqual(json.loads((args.evidence_dir / "manifests.json").read_text())["kind"], "List")

    def test_state_target_mismatch_fails_before_kubernetes(self):
        args = self.args()
        args.state_file.parent.mkdir(parents=True)
        args.state_file.write_text(json.dumps(self.state(context="different-cluster")))
        args.binary.write_bytes(b"synthetic binary")
        with patch.object(deploy, "Kubectl") as kube:
            with self.assertRaises(LabError):
                deploy.main(["--context", args.context, "--namespace", args.namespace,
                             "--evidence-dir", str(args.evidence_dir), "--binary", str(args.binary)])
            kube.assert_not_called()

    def test_selected_context_does_not_depend_on_current_context(self):
        args = self.args()
        prepare_evidence(args)
        calls = []
        def fake(cmd, **kwargs):
            calls.append((cmd, kwargs["env"]))
            self.assertNotIn("current-context", cmd)
            self.assertEqual(cmd[cmd.index("--context") + 1], "selected-target")
            output = "selected-target\n" if "get-contexts" in cmd else '{"status":{"phase":"Active"}}'
            return subprocess.CompletedProcess(cmd, 0, output, "")
        with patch("subprocess.run", side_effect=fake):
            Kubectl(args).check_target()
        self.assertEqual(len(calls), 2)

    def test_existing_name_never_replaced(self):
        args = self.args()
        with patch.object(Kubectl, "call", return_value="configmap/existing") as call:
            with self.assertRaises(LabError):
                Kubectl(args).create({"kind": "ConfigMap", "metadata": {"name": "existing", "namespace": args.namespace}})
            self.assertEqual(call.call_count, 1)
            self.assertEqual(call.call_args.args[0][0], "get")

    def test_secret_create_returns_metadata_only(self):
        args = self.args()
        with patch.object(Kubectl, "call", side_effect=["", "synthetic-uid"] ) as call, contextlib.redirect_stdout(io.StringIO()) as output:
            Kubectl(args).create({"kind": "Secret", "metadata": {"name": "new-secret", "namespace": args.namespace},
                                  "stringData": {"secret-key": "synthetic-sensitive-marker"}})
        self.assertEqual(call.call_args.args[0][-1], "jsonpath={.metadata.uid}")
        self.assertNotIn("synthetic-sensitive-marker", output.getvalue())

    def test_kubectl_error_does_not_log_payload_or_stderr(self):
        args = self.args()
        prepare_evidence(args)
        with patch("subprocess.run", return_value=subprocess.CompletedProcess([], 1, "", "synthetic-sensitive-marker")):
            with self.assertRaises(LabError) as exc:
                Kubectl(args).call(["create", "-f", "-"], "synthetic-sensitive-marker")
        self.assertNotIn("synthetic-sensitive-marker", str(exc.exception))
        self.assertNotIn("synthetic-sensitive-marker", (args.evidence_dir / "commands.jsonl").read_text())

    def test_provision_main_persists_only_metadata_with_custom_paths(self):
        args = self.args(provision, ["--state-file", str(self.root / "custom-state.json")])
        with patch.object(provision, "Kubectl") as kube, patch.object(provision.secrets, "token_urlsafe", return_value="synthetic-sensitive-marker"), contextlib.redirect_stdout(io.StringIO()):
            kube.return_value.create.side_effect = lambda obj: {
                "kind": obj["kind"], "name": obj["metadata"]["name"], "uid": "synthetic-uid"}
            result = provision.main(["--context", args.context, "--namespace", args.namespace,
                                     "--evidence-dir", str(args.evidence_dir),
                                     "--state-file", str(args.state_file), "--authorized-new-identity"])
            kube.return_value.check_target.assert_called_once()
            self.assertEqual(kube.return_value.create.call_count, 3)
        self.assertEqual(result, 0)
        state = json.loads(args.state_file.read_text())
        self.assertEqual(state["context"], args.context)
        self.assertEqual(state["namespace"], args.namespace)
        self.assertEqual([r["kind"] for r in state["resources"]], ["Secret", "ConfigMap", "Job"])
        for path in [args.state_file, args.evidence_dir / "provision-job.json"]:
            self.assertNotIn("synthetic-sensitive-marker", path.read_text())

    def test_deploy_main_custom_target_and_port_forward(self):
        config = self.root / "custom config"
        config.write_text("synthetic placeholder")
        args = self.args(extra=["--kubeconfig", str(config)])
        args.state_file.parent.mkdir(parents=True)
        args.state_file.write_text(json.dumps(self.state()))
        args.binary.write_bytes(b"synthetic binary")
        with patch.object(deploy, "Kubectl") as kube, contextlib.redirect_stdout(io.StringIO()) as output:
            kube.return_value.create.side_effect = lambda obj: {
                "kind": obj["kind"], "name": obj["metadata"]["name"], "uid": "synthetic-uid"}
            result = deploy.main(["--context", args.context, "--namespace", args.namespace,
                                  "--evidence-dir", str(args.evidence_dir), "--binary", str(args.binary),
                                  "--kubeconfig", str(config)])
            kube.return_value.check_target.assert_called_once()
        self.assertEqual(result, 0)
        self.assertIn("--context selected-target", output.getvalue())
        self.assertIn("--namespace film-lab", output.getvalue())
        self.assertIn("KUBECONFIG=", output.getvalue())
        self.assertIn(str(config), output.getvalue())
        self.assertEqual(len(json.loads(args.state_file.read_text())["resources"]), 3)

    def test_state_endpoint_mismatch_is_rejected(self):
        args = self.args()
        args.state_file.parent.mkdir(parents=True)
        args.state_file.write_text(json.dumps(self.state(endpoint="https://other.example.test")))
        with patch.object(deploy, "Kubectl") as kube:
            with self.assertRaises(LabError):
                deploy.main(["--context", args.context, "--namespace", args.namespace,
                             "--evidence-dir", str(args.evidence_dir), "--binary", str(args.binary)])
            kube.assert_not_called()

    def test_provision_authorization_checked_before_kubernetes(self):
        with patch.object(provision, "Kubectl") as kube, contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit):
                provision.main(["--context", "target", "--evidence-dir", str(self.root / "evidence")])
            kube.assert_not_called()


if __name__ == "__main__":
    unittest.main()

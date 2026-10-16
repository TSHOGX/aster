import importlib.util
import json
import hashlib
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
spec = importlib.util.spec_from_file_location("deploy", SCRIPTS / "deploy.py")
deploy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deploy)


class DeploymentAdmissionTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        (self.root / "studio").mkdir()
        (self.root / "studio/studio-release.json").write_text(json.dumps({"source_commit": "a" * 40}))
        (self.root / "evaluation-space").mkdir()
        evaluation_manifest = self.root / "evaluation-space/evaluation-space-release.json"
        evaluation_manifest.write_text(json.dumps({"format": "evaluation-space-release-v1", "source_commit": "a" * 40}))
        (self.root / "deployment-build.json").write_text(json.dumps({
            "format": "aster-deployment-v1", "source_commit": "a" * 40,
            "evaluation_space_manifest_sha256": hashlib.sha256(evaluation_manifest.read_bytes()).hexdigest(),
            "engine_sha256": "0" * 64, "runtime_manifest_sha256": "0" * 64,
        }))

    def test_mixed_revision_rejected_before_configuration_or_state_access(self):
        with patch.object(deploy.candidate, "verify", return_value={"commit": "b" * 40}), \
                patch.object(deploy, "run"), patch.object(deploy, "clear_state") as clear:
            with self.assertRaisesRegex(ValueError, "mixed source"):
                deploy.validate(self.root, {})
            clear.assert_not_called()

    def test_changed_engine_rejected_before_configuration_or_state_access(self):
        (self.root / "bin").mkdir()
        (self.root / "bin/khora").write_bytes(b"changed bytes")
        with patch.object(deploy.candidate, "verify", return_value={"commit": "a" * 40}), \
                patch.object(deploy, "run"), patch.object(deploy, "clear_state") as clear:
            with self.assertRaisesRegex(ValueError, "build bytes changed"):
                deploy.validate(self.root, {})
            clear.assert_not_called()

    def test_changed_evaluation_manifest_rejected_before_configuration_or_state_access(self):
        manifest = self.root / "evaluation-space/evaluation-space-release.json"
        value = json.loads(manifest.read_text())
        value["changed"] = True
        manifest.write_text(json.dumps(value))
        with patch.object(deploy.candidate, "verify", return_value={"commit": "a" * 40}), \
                patch.object(deploy, "run"), patch.object(deploy, "clear_state") as clear:
            with self.assertRaisesRegex(ValueError, "Evaluation Space release manifest changed"):
                deploy.validate(self.root, {})
            clear.assert_not_called()

    def test_symlink_state_refused_without_touching_target(self):
        target = self.root / "target"
        target.mkdir()
        marker = target / "keep"
        marker.write_text("owned elsewhere")
        link = self.root / "state"
        link.symlink_to(target, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "regular prepared directory"):
            deploy.clear_state(link)
        self.assertEqual(marker.read_text(), "owned elsewhere")

    def test_nested_mount_refused_before_removing_any_state(self):
        state = self.root / "state"
        state.mkdir()
        marker = state / "keep"
        marker.write_text("retained")
        mounted = state / "other-service"
        mounted.mkdir()
        mountinfo = f"1 0 0:1 / {mounted} rw - tmpfs tmpfs rw\n"
        with patch.object(Path, "read_text", return_value=mountinfo), \
                patch.object(deploy.shutil, "rmtree") as remove:
            with self.assertRaisesRegex(ValueError, "nested mount"):
                deploy.clear_state(state)
            remove.assert_not_called()
        self.assertEqual(marker.read_text(), "retained")

    def test_missing_environment_fails_before_services_are_stopped(self):
        with patch.object(deploy.os, "geteuid", return_value=0), \
                patch.object(deploy, "activation_environment", side_effect=FileNotFoundError("owner.env")), \
                patch.object(deploy, "run") as run, patch.object(deploy, "clear_state") as clear:
            with self.assertRaises(FileNotFoundError):
                deploy.activate(self.root, {}, {}, {})
            run.assert_not_called()
            clear.assert_not_called()

    def test_runtime_verification_precedes_host_configuration(self):
        report = json.loads((self.root / "deployment-build.json").read_text())
        report.update(public_brand="Aster", runtime_digest="sha256:" + "1" * 64)
        for name, key in (("bin/khora", "engine_sha256"), ("runtime.manifest.json", "runtime_manifest_sha256"),
                          ("bin/runtime-verify", "runtime_verifier_sha256")):
            file = self.root / name
            file.parent.mkdir(parents=True, exist_ok=True)
            file.write_bytes(b"verified fixture")
            report[key] = hashlib.sha256(file.read_bytes()).hexdigest()
        (self.root / "deployment-build.json").write_text(json.dumps(report))
        (self.root / "runtime.manifest.json.sha256").write_text(report["runtime_digest"])
        guest = self.root / "runtime/etc/project-runtime/manifest.json"
        guest.parent.mkdir(parents=True)
        guest.write_bytes((self.root / "runtime.manifest.json").read_bytes())
        def verify(args, **kwargs):
            if str(args[0]).endswith("runtime-verify"):
                raise ValueError("runtime contents changed")
        with patch.object(deploy.candidate, "verify", return_value={"commit": "a" * 40, "public_brand": "Aster"}), \
                patch.object(deploy, "run", side_effect=verify) as run, patch.object(deploy, "clear_state") as clear:
            with self.assertRaisesRegex(ValueError, "runtime contents changed"):
                deploy.validate(self.root, {})
            self.assertEqual(run.call_args.args[0], [self.root / "bin/runtime-verify", "--root", self.root / "runtime", report["runtime_digest"]])
            clear.assert_not_called()


if __name__ == "__main__":
    unittest.main()

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("candidate", Path(__file__).resolve().parents[1] / "candidate.py")
candidate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(candidate)


class CandidateIntegrityTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        for name in ("bin/orchestrator", "bin/edge-proxy", "frontend/server.js", "frontend/.next/BUILD_ID", "frontend/public/favicon.svg"):
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("fixture")
        self.report = {"format": 1, "commit": "a" * 40, "frontend_build_id": "fixture", "files": candidate.inventory(self.root)}
        (self.root / candidate.MANIFEST).write_text(json.dumps(self.report))

    def test_detects_changed_missing_and_added_files(self):
        candidate.verify(self.root)
        path = self.root / "bin/orchestrator"
        path.write_text("corrupt")
        with self.assertRaisesRegex(ValueError, "differ"):
            candidate.verify(self.root)
        path.unlink()
        with self.assertRaisesRegex(ValueError, "differ"):
            candidate.verify(self.root)
        path.write_text("fixture")
        (self.root / "unexpected-config").write_text("not part of the bundle")
        with self.assertRaisesRegex(ValueError, "differ"):
            candidate.verify(self.root)

    def test_detects_executable_mode_change(self):
        path = self.root / "bin/orchestrator"
        path.chmod(path.stat().st_mode ^ 0o111)
        with self.assertRaisesRegex(ValueError, "differ"):
            candidate.verify(self.root)

    def test_rejects_external_and_dangling_links(self):
        link = self.root / "frontend/link"
        for target in ("../../outside", "/etc/passwd", "missing"):
            link.symlink_to(target)
            with self.assertRaisesRegex(ValueError, "symlink"):
                candidate.inventory(self.root)
            link.unlink()

    def test_records_internal_links_and_rejects_retargeting(self):
        link = self.root / "frontend/link"
        link.symlink_to("server.js")
        self.report["files"] = candidate.inventory(self.root)
        (self.root / candidate.MANIFEST).write_text(json.dumps(self.report))
        candidate.verify(self.root)
        link.unlink()
        link.symlink_to(".next/BUILD_ID")
        with self.assertRaisesRegex(ValueError, "differ"):
            candidate.verify(self.root)

    def test_missing_required_file_cannot_be_hidden_by_inventory(self):
        (self.root / "bin/orchestrator").unlink()
        self.report["files"] = candidate.inventory(self.root)
        (self.root / candidate.MANIFEST).write_text(json.dumps(self.report))
        with self.assertRaisesRegex(ValueError, "missing required"):
            candidate.verify(self.root)


if __name__ == "__main__":
    unittest.main()

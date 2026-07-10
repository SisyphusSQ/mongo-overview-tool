#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = (
    Path(__file__).resolve().parents[1]
    / "scripts"
    / "project_version_release.py"
)


def load_module():
    spec = importlib.util.spec_from_file_location(
        "project_version_release",
        SCRIPT,
    )
    if spec is None or spec.loader is None:
        raise RuntimeError("failed to load project_version_release")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class ProjectVersionReleaseTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.repo = Path(self.tmp.name)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def write_changelog(self, text: str) -> None:
        (self.repo / "changeLog.md").write_text(text, encoding="utf-8")

    def test_classify_uses_project_changelog_name(self) -> None:
        release = load_module()

        result = release.classify(["changeLog.md"])

        self.assertEqual(result["classification"], "changelog-only")
        self.assertIn("changeLog.md changed", result["reasons"])

    def test_changelog_add_and_archive_preserve_history(self) -> None:
        release = load_module()
        self.write_changelog("## v1.1.0(20260214)\nold release\n")

        added = release.changelog_add(
            self.repo,
            issue="",
            category="feature",
            text="新增 SDK",
            write=True,
        )
        archived = release.release_archive(
            self.repo,
            version="v2.0.0",
            date_value="20260710",
            write=True,
        )
        text = (self.repo / "changeLog.md").read_text(encoding="utf-8")

        self.assertTrue(added["changed"])
        self.assertTrue(archived["changed"])
        self.assertIn("## Unreleased", text)
        self.assertIn("### v2.0.0(20260710)", text)
        self.assertIn("新增 SDK", text)
        self.assertIn("## v1.1.0(20260214)", text)

    def test_help_runs(self) -> None:
        completed = subprocess.run(
            ["python3", str(SCRIPT), "--help"],
            check=True,
            capture_output=True,
            text=True,
        )
        self.assertIn("release-archive", completed.stdout)


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Exercise release state transitions without calling GitHub."""

from pathlib import Path
import runpy
import tempfile
import unittest
from unittest.mock import patch


publish = runpy.run_path(str(Path(__file__).with_name("publish_release.py")))["publish"]
TAG, COMMIT, NOTES = "v0.10.0", "a" * 40, "test run URL"
ROOT = "repos/example/app-plug"


class PublishTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.directory = Path(temp.name)
        for name in ["SHA256SUMS", *(f"package-{i}" for i in range(6))]:
            (self.directory / name).write_text(name)
        self.tag = None
        self.release = None
        self.calls = []
        for name, fake in (("api", self.api), ("run", lambda *_: COMMIT), ("live_tag", lambda _: self.tag),
                           ("upload_asset", self.upload_asset)):
            patcher = patch.dict(publish.__globals__, {name: fake})
            patcher.start()
            self.addCleanup(patcher.stop)

    def api(self, path, *args):
        self.calls.append((path, args))
        if path == f"{ROOT}/git/refs":
            self.assertIn(f"sha={COMMIT}", args)
            self.assertIn(f"ref=refs/tags/{TAG}", args)
            self.tag = COMMIT
            return {}
        if path == f"{ROOT}/releases?per_page=100":
            return [[self.release] if self.release else []]
        if path == f"{ROOT}/releases":
            self.assertIn("draft=true", args)
            self.release = {"id": 3, "draft": True, "body": NOTES, "tag_name": TAG, "assets": [],
                            "html_url": "https://github.com/example/app-plug/releases/tag/v0.10.0"}
            return self.release
        if path == f"{ROOT}/releases/3":
            if args:
                self.assertEqual(args, ("--method", "PATCH", "-F", "draft=false"))
                self.assertEqual(len(self.release["assets"]), 7)
                self.release["draft"] = False
            return self.release
        self.fail(f"Unexpected API call: {path}")

    def upload_asset(self, release_path, asset):
        self.calls.append(("upload", (asset.name,)))
        self.assertEqual(release_path, f"{ROOT}/releases/3")
        self.assertTrue(self.release["draft"])
        self.release["assets"].append({"name": asset.name, "state": "uploaded",
                                      "digest": publish.__globals__["asset_digest"](asset)})

    def publish(self):
        publish("example/app-plug", TAG, COMMIT, NOTES, self.directory)

    def test_tags_uploads_publishes_and_then_skips(self):
        self.publish()
        self.assertEqual(self.tag, COMMIT)
        self.assertFalse(self.release["draft"])
        self.calls.clear()
        (self.directory / "package-0").write_text("different full-rerun archive timestamps")
        self.publish()
        self.assertEqual(len(self.calls), 1)

    def test_does_not_move_a_conflicting_tag(self):
        self.tag = "b" * 40
        with self.assertRaisesRegex(ValueError, "refusing to move"):
            self.publish()
        self.assertEqual(self.calls, [])

    def test_resumes_own_draft_with_matching_assets(self):
        self.publish()
        self.release["draft"] = True
        self.release["assets"].pop()
        self.calls.clear()
        self.publish()
        self.assertEqual(sum(path == "upload" for path, _ in self.calls), 1)
        self.assertFalse(self.release["draft"])

    def test_refuses_foreign_draft_or_corrupt_existing_asset(self):
        self.publish()
        self.release.update(draft=True, body="different run")
        with self.assertRaisesRegex(ValueError, "different run"):
            self.publish()
        self.release["body"] = NOTES
        self.release["assets"][0]["digest"] = "sha256:wrong"
        with self.assertRaisesRegex(ValueError, "unverified existing asset"):
            self.publish()
        self.assertTrue(self.release["draft"])


if __name__ == "__main__":
    unittest.main()

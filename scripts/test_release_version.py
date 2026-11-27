#!/usr/bin/env python3
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("release_version.py").resolve()


class ReleaseVersionTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.repo = Path(temp.name)
        self.source = self.repo / "src/server/server.go"
        self.source.parent.mkdir(parents=True)
        self.git("init", "-q")
        self.git("config", "user.name", "CI test")
        self.git("config", "user.email", "ci@example.com")
        self.before = self.commit('const Version = "v0.9.9"')

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.repo, text=True).strip()

    def commit(self, declaration):
        self.source.write_text(declaration + "\n", encoding="utf-8")
        self.git("add", ".")
        git_env = os.environ | {"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.hooksPath",
                                "GIT_CONFIG_VALUE_0": os.devnull}
        subprocess.run(["git", "commit", "--allow-empty", "-qm", "test"], cwd=self.repo,
                       env=git_env, check=True)
        return self.git("rev-parse", "HEAD")

    def detect(self, before, after):
        return subprocess.run([sys.executable, "-IB", str(SCRIPT), before, after], cwd=self.repo,
                              text=True, capture_output=True, check=False)

    def test_bump_anywhere_in_the_push(self):
        self.commit('const Version = "v0.10.0"')
        after = self.commit('const Version = "v0.10.0"')
        result = self.detect(self.before, after)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "v0.10.0")

    def test_unchanged_and_initial_push(self):
        after = self.commit('const Version = "v0.9.9"')
        for before in (self.before, "0" * 40):
            result = self.detect(before, after)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), "")

    def test_rollback_and_malformed_versions_fail(self):
        for tag in ("v0.8.0", "v0.09.10", "v1.0.0-rc1"):
            with self.subTest(tag=tag):
                self.assertNotEqual(self.detect(self.before, self.commit(f'const Version = "{tag}"')).returncode, 0)

    def test_wrong_checkout_or_invalid_before_fails(self):
        after = self.commit('const Version = "v1.0.0"')
        self.assertNotEqual(self.detect(self.before, self.before).returncode, 0)
        self.assertNotEqual(self.detect("HEAD", after).returncode, 0)


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Exercise the release asset gate without GitHub access or release credentials."""

from pathlib import Path
import runpy
import tempfile
import unittest


ASSETS = runpy.run_path(str(Path(__file__).with_name("release_assets.py")))
TAG = "v2.3.4"


class ReleaseAssetsTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        self.download = root / "download"
        self.download.mkdir()
        self.output = root / "verified"
        self.archives = []
        for target in ASSETS["TARGETS"]:
            folder = self.download / f"app-plug-{target}"
            folder.mkdir()
            suffix = "zip" if target.startswith("windows-") else "tar.gz"
            archive = folder / f"app-plug-{TAG}-{target}.{suffix}"
            archive.write_bytes(target.encode("ascii"))
            self.archives.append(archive)
            digest = ASSETS["file_digest"](archive)
            (folder / "SHA256SUMS").write_bytes(f"{digest}  {archive.name}\n".encode("ascii"))

    def collect(self, tag=TAG):
        return ASSETS["collect"](self.download, self.output, tag)

    def assert_rejected(self, tag=TAG):
        with self.assertRaises(ValueError):
            self.collect(tag)
        self.assertFalse(self.output.exists())

    def test_collects_exact_assets_and_checksums(self):
        self.collect()
        self.assertEqual({p.name for p in self.output.iterdir()},
                         {"SHA256SUMS", *(p.name for p in self.archives)})
        expected = b""
        for archive in sorted(self.archives, key=lambda p: p.name):
            copy = self.output / archive.name
            self.assertEqual(copy.read_bytes(), archive.read_bytes())
            expected += f"{ASSETS['file_digest'](copy)}  {copy.name}\n".encode("ascii")
        self.assertEqual((self.output / "SHA256SUMS").read_bytes(), expected)

    def test_untagged_ci_infers_common_version(self):
        self.collect(None)
        self.assertTrue((self.output / "SHA256SUMS").is_file())

    def test_wrong_version_and_nonstable_tags(self):
        for tag in ("v2.3.5", "v02.3.4", "v2.3.4-rc.1"):
            with self.subTest(tag=tag):
                self.assert_rejected(tag)

    def test_missing_target(self):
        self.archives[-1].parent.rename(self.download.parent / "removed")
        self.assert_rejected()

    def test_tampered_archive(self):
        self.archives[-1].write_bytes(b"different")
        self.assert_rejected()

    def test_extra_entry(self):
        (self.archives[-1].parent / "extra.zip").touch()
        self.assert_rejected()

    def test_extra_manifest_line(self):
        manifest = self.archives[-1].parent / "SHA256SUMS"
        with manifest.open("ab") as stream:
            stream.write(b"unexpected\n")
        self.assert_rejected()

    def test_symlink_is_not_an_archive(self):
        archive = self.archives[-1]
        moved = archive.rename(self.download.parent / archive.name)
        archive.symlink_to(moved)
        self.assert_rejected()

    def test_does_not_reuse_output_directory(self):
        self.output.mkdir()
        with self.assertRaises(FileExistsError):
            self.collect()
        self.assertEqual(list(self.output.iterdir()), [])


if __name__ == "__main__":
    unittest.main()

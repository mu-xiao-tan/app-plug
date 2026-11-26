#!/usr/bin/env python3
"""Verify native CI packages and assemble the exact files for a stable Release."""

import argparse
import hashlib
from pathlib import Path
import re
import shutil


TARGETS = tuple(f"{platform}-{arch}" for platform in ("linux", "darwin", "windows")
                for arch in ("amd64", "arm64"))
STABLE_TAG = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")


def file_digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def require_entries(directory, names, *, files):
    if directory.is_symlink() or not directory.is_dir():
        raise ValueError(f"Expected a real directory: {directory}")
    paths = {path.name: path for path in directory.iterdir()}
    if paths.keys() != set(names):
        raise ValueError(f"Unexpected contents in {directory}: {', '.join(sorted(paths))}")
    for path in paths.values():
        if path.is_symlink() or not (path.is_file() if files else path.is_dir()):
            raise ValueError(f"Unexpected file type: {path}")
    return paths


def check_target(directory, tag, target):
    if not STABLE_TAG.fullmatch(tag) or target not in TARGETS:
        raise ValueError("A stable vMAJOR.MINOR.PATCH tag and known target are required")
    extension = "zip" if target.startswith("windows-") else "tar.gz"
    name = f"app-plug-{tag}-{target}.{extension}"
    paths = require_entries(directory, (name, "SHA256SUMS"), files=True)
    archive = paths[name]
    expected = f"{file_digest(archive)}  {name}\n".encode("ascii")
    manifest = paths["SHA256SUMS"]
    with manifest.open("rb") as stream:
        if stream.read(len(expected) + 1) != expected:
            raise ValueError(f"Invalid SHA256SUMS for {name}")
    return archive, expected


def collect(download, output, tag=None):
    names = [f"app-plug-{target}" for target in TARGETS]
    directories = require_entries(download, names, files=False)
    if tag is None:
        # PR/main have no tag. Infer one common version, then verify all six against it.
        matches = [re.fullmatch(r"app-plug-(v[0-9]+\.[0-9]+\.[0-9]+)-linux-amd64\.tar\.gz",
                                path.name) for path in directories["app-plug-linux-amd64"].iterdir()]
        versions = [match[1] for match in matches if match]
        if len(versions) != 1:
            raise ValueError("Could not infer one CI package version")
        tag = versions[0]
    packages = [check_target(directories[name], tag, target)
                for name, target in zip(names, TARGETS, strict=True)]
    output.mkdir()  # Never merge with files left by another run.
    for archive, expected in sorted(packages):
        destination = output / archive.name
        shutil.copyfile(archive, destination)
        if file_digest(destination).encode("ascii") != expected[:64]:
            raise ValueError(f"Digest changed while staging {archive.name}")
    (output / "SHA256SUMS").write_bytes(b"".join(line for _, line in sorted(packages)))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    target_cmd = commands.add_parser("check-target")
    target_cmd.add_argument("directory", type=Path)
    target_cmd.add_argument("tag")
    target_cmd.add_argument("target", choices=TARGETS)
    collect_cmd = commands.add_parser("collect")
    collect_cmd.add_argument("download", type=Path)
    collect_cmd.add_argument("output", type=Path)
    collect_cmd.add_argument("--tag")
    args = parser.parse_args()
    try:
        if args.command == "check-target":
            archive, _ = check_target(args.directory, args.tag, args.target)
            print(f"Verified {archive.name}")
        else:
            collect(args.download, args.output, args.tag)
            print(f"Verified {len(TARGETS)} native archives and staged SHA256SUMS")
    except (ValueError, OSError) as error:
        parser.exit(1, f"{error}\n")


if __name__ == "__main__":
    main()

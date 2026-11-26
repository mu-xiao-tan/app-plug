#!/usr/bin/env python3
"""Build and smoke-test an App Plug archive on its native target."""

import argparse
import hashlib
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import zipfile


ROOT = Path(__file__).resolve().parent.parent
TARGETS = tuple(f"{os_name}-{arch}" for os_name in ("linux", "darwin", "windows")
                for arch in ("amd64", "arm64"))
DOCS = (
    "README.md",
    "docs/releasing.md",
    "examples/app-plug.service",
    "examples/config.json",
    "examples/nginx.conf",
    "skills/app-plug/SKILL.md",
    "skills/app-plug/api.md",
    "skills/app-plug/operations.md",
    "src/server/web/shared/app-plug-lockup.svg",
)


def run(args, *, cwd=ROOT, env=None, capture=False):
    result = subprocess.run(args, cwd=cwd, env=env, check=True,
                            capture_output=capture, encoding="utf-8")
    return result.stdout.strip() if capture else None


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def pack_and_extract(archive, unpacked, files):
    if archive.suffix == ".zip":
        with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as package:
            for name, source in files.items():
                package.write(source, name)
        with zipfile.ZipFile(archive) as package:
            if package.namelist() != list(files) or package.testzip() is not None:
                raise RuntimeError("Invalid ZIP contents")
            package.extractall(unpacked)
    else:
        with tarfile.open(archive, "w:gz") as package:
            for name, source in files.items():
                package.add(source, name, recursive=False)
        with tarfile.open(archive, "r:gz") as package:
            if package.getnames() != list(files) or not all(m.isfile() for m in package):
                raise RuntimeError("Invalid tar contents")
            package.extractall(unpacked, filter="data")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    parser.add_argument("target", choices=TARGETS)
    args = parser.parse_args()
    goos, goarch = args.target.split("-")
    native = run(["go", "env", "GOHOSTOS", "GOHOSTARCH"], capture=True).split()
    if native != [goos, goarch]:
        parser.error(f"{args.target} requires a native Go toolchain; found {'-'.join(native)}")
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    if any(output.iterdir()):
        parser.error(f"Output directory must be empty: {output}")

    windows = goos == "windows"
    binary_name = "app-plug.exe" if windows else "app-plug"
    with tempfile.TemporaryDirectory(prefix="app-plug-ci-") as temp:
        work = Path(temp)
        home = work / "isolated home"
        home.mkdir()
        env = dict(os.environ, HOME=str(home), USERPROFILE=str(home))
        build_env = dict(os.environ, CGO_ENABLED="0", GOOS=goos, GOARCH=goarch,
                         GOAMD64="v1", GOARM64="v8.0")
        binary = work / binary_name
        run(["go", "build", "-trimpath", "-buildvcs=false", "-o", binary,
             "./src/cmd/app-plug"], env=build_env)
        version = run([binary, "version"], cwd=work, env=env, capture=True)
        if not re.fullmatch(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", version):
            raise RuntimeError(f"Invalid application version: {version!r}")
        extension = "zip" if windows else "tar.gz"
        archive = work / f"app-plug-{version}-{args.target}.{extension}"
        unpacked = work / "unpacked"
        unpacked.mkdir()
        files = {binary_name: binary, **{name: ROOT / name for name in DOCS}}
        if any(not path.is_file() or path.is_symlink() for path in files.values()):
            raise RuntimeError("Every package input must be a regular, non-symlink file")
        pack_and_extract(archive, unpacked, files)
        sha256 = digest(archive)
        if any(digest(unpacked / name) != digest(source) for name, source in files.items()):
            raise RuntimeError("Extracted files do not match build inputs")

        cli = unpacked / binary_name
        if run([cli, "version"], cwd=unpacked, env=env, capture=True) != version:
            raise RuntimeError("Extracted CLI version mismatch")
        if "app-plug config" not in run([cli, "help"], cwd=unpacked, env=env, capture=True):
            raise RuntimeError("Extracted CLI did not display expected help")
        run([cli, "config", "init", "--base-domain", "example.test"], cwd=unpacked, env=env)
        run([cli, "config", "check"], cwd=unpacked, env=env)
        run([cli, "config", "set", "--home-host", "next.example.test"], cwd=unpacked, env=env)
        run([cli, "config", "check"], cwd=unpacked, env=env)

        destination = output / archive.name
        with archive.open("rb") as source, destination.open("xb") as out:
            shutil.copyfileobj(source, out)
        if digest(destination) != sha256:
            raise RuntimeError("SHA-256 mismatch after writing the archive")
        with (output / "SHA256SUMS").open("x", encoding="ascii", newline="\n") as manifest:
            manifest.write(f"{sha256}  {archive.name}\n")
        print(f"Verified {destination.name} and SHA256SUMS")


if __name__ == "__main__":
    main()

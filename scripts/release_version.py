#!/usr/bin/env python3
"""Print a release tag only when an existing main ref advances to a new stable version."""

import re
import subprocess
import sys


SOURCE = "src/server/server.go"
SHA = re.compile(r"[0-9a-f]{40}")
VERSION = re.compile(r'^const Version = "v((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))"$', re.M)


def git(*args):
    return subprocess.check_output(["git", *args], text=True).strip()


def version(commit):
    matches = VERSION.findall(git("show", f"{commit}:{SOURCE}"))
    if len(matches) != 1:
        raise ValueError(f"Expected one stable Version declaration in {SOURCE} at {commit}")
    return tuple(map(int, matches[0].split(".")))


def release_tag(before, after):
    if not SHA.fullmatch(before) or not SHA.fullmatch(after) or git("rev-parse", "HEAD") != after:
        raise ValueError("Invalid push SHAs or checkout differs from the push commit")
    if before == "0" * 40:
        return ""  # Initial branch creation is not a release.
    git("merge-base", "--is-ancestor", before, after)
    previous, current = version(before), version(after)
    if previous == current:
        return ""
    if current < previous:
        raise ValueError("App Plug versions must increase")
    return "v" + ".".join(map(str, current))


if __name__ == "__main__":
    try:
        print(release_tag(*sys.argv[1:]))
    except (ValueError, subprocess.CalledProcessError) as exc:
        sys.exit(f"Release version check failed: {exc}")

#!/usr/bin/env python3
"""Publish the protected tag and verified packages from the same main push."""

import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
from http.client import HTTPSConnection
from urllib.parse import urlencode


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def api(path, *args):
    return json.loads(run("gh", "api", path, *args))


def upload_asset(release_path, path):
    # Use the fixed GitHub upload host without following redirects with credentials.
    connection = HTTPSConnection("uploads.github.com", timeout=120)
    try:
        with path.open("rb") as stream:
            connection.request("POST", f"/{release_path}/assets?{urlencode({'name': path.name})}",
                               body=stream, headers={"Authorization": f"Bearer {os.environ['GH_TOKEN']}",
                                                     "Content-Type": "application/octet-stream",
                                                     "Content-Length": str(path.stat().st_size)})
            response = connection.getresponse()
            if response.status != 201:
                raise ValueError(f"Uploading {path.name} failed (HTTP {response.status})")
    finally:
        connection.close()


def live_tag(tag):
    ref = f"refs/tags/{tag}"
    records = dict(line.split("\t", 1)[::-1] for line in
                   run("git", "ls-remote", "origin", ref, f"{ref}^{{}}").splitlines())
    return records.get(f"{ref}^{{}}", records.get(ref))


def asset_digest(path):
    with path.open("rb") as stream:
        return "sha256:" + hashlib.file_digest(stream, "sha256").hexdigest()


def missing_assets(release_assets, files):
    absent = files.copy()
    for asset in release_assets:
        path = absent.pop(asset["name"], None)
        if path is None or asset["state"] != "uploaded" or asset["digest"] != asset_digest(path):
            raise ValueError(f"Unexpected or unverified existing asset: {asset['name']}")
    return absent.values()


def publish(repo, tag, commit, notes, directory):
    if not re.fullmatch(r"v(?:0|[1-9][0-9]*)(?:\.(?:0|[1-9][0-9]*)){2}", tag):
        raise ValueError("Invalid stable release tag")
    if run("git", "rev-parse", "HEAD") != commit:
        raise ValueError("Checkout differs from the tested main push")
    files = {path.name: path for path in sorted(directory.iterdir())}
    if len(files) != 7 or "SHA256SUMS" not in files:
        raise ValueError("Expected six verified packages and their checksum manifest")
    root = f"repos/{repo}"
    existing_tag = live_tag(tag)
    if existing_tag is None:
        api(f"{root}/git/refs", "-f", f"ref=refs/tags/{tag}", "-f", f"sha={commit}")
    if live_tag(tag) != commit:
        raise ValueError(f"{tag} does not point to the tested commit; refusing to move it")

    releases = api(f"{root}/releases?per_page=100", "--paginate", "--slurp")
    matches = [release for page in releases for release in page if release["tag_name"] == tag]
    if len(matches) > 1:
        raise ValueError(f"Multiple releases found for {tag}")
    if matches:
        release = matches[0]
        if not release["draft"]:
            assets = release["assets"]
            # A full rerun can rebuild archives with different timestamps. Never replace
            # an already published release for this commit with those new bytes.
            if (len(assets) != len(files) or {asset["name"] for asset in assets} != files.keys()
                    or any(asset["state"] != "uploaded" or not re.fullmatch(
                        r"sha256:[0-9a-f]{64}", asset.get("digest") or "") for asset in assets)):
                raise ValueError(f"Existing published release for {tag} has an incomplete asset set")
            print(f"Release {tag} already exists for this commit; nothing to publish")
            return
        if release["body"] != notes:
            raise ValueError(f"Draft for {tag} belongs to a different run; not modifying it")
    else:
        release = api(f"{root}/releases", "-f", f"tag_name={tag}", "-f", f"target_commitish={commit}",
                      "-f", f"name=App Plug {tag}", "-f", f"body={notes}", "-F", "draft=true")

    release_path = f"{root}/releases/{release['id']}"
    for path in missing_assets(release["assets"], files):
        upload_asset(release_path, path)
    staged = api(release_path)
    if (not staged["draft"] or staged["tag_name"] != tag or staged["body"] != notes
            or list(missing_assets(staged["assets"], files)) or live_tag(tag) != commit):
        raise ValueError("Draft assets, release metadata or live tag failed the final check")
    published = api(release_path, "--method", "PATCH", "-F", "draft=false")
    if published["draft"] or published["tag_name"] != tag:
        raise ValueError("GitHub did not confirm publication")
    print(published["html_url"])


if __name__ == "__main__":
    try:
        tag, directory = sys.argv[1:]
        repo, commit = os.environ["GITHUB_REPOSITORY"], os.environ["GITHUB_SHA"]
        run_url = f"{os.environ['GITHUB_SERVER_URL']}/{repo}/actions/runs/{os.environ['GITHUB_RUN_ID']}"
        notes = f"Native packages verified for commit {commit} in {run_url}. Check SHA256SUMS before extracting."
        publish(repo, tag, commit, notes, Path(directory))
    except (ValueError, OSError, subprocess.CalledProcessError) as exc:
        sys.exit(f"Release failed: {exc}")

#!/usr/bin/env python3
"""Download the exact official artifacts; verify SHA-256 before publishing files.

This does not install or execute Hermes, Python, uv, or any dependency.
"""
import argparse
import hashlib
import json
import os
import pathlib
import tempfile
import urllib.parse
import urllib.request

HOSTS = {"github.com", "codeload.github.com", "release-assets.githubusercontent.com", "files.pythonhosted.org"}


def checked_url(url):
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme != "https" or parsed.hostname not in HOSTS or parsed.username or parsed.password:
        raise ValueError("Unexpected artifact origin")
    return url


class OfficialRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return super().redirect_request(req, fp, code, msg, headers, checked_url(newurl))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", required=True, type=pathlib.Path)
    args = parser.parse_args()
    directory = args.directory.absolute()
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    lock = json.loads((pathlib.Path(__file__).resolve().parent / "artifacts.lock.json").read_text())
    opener = urllib.request.build_opener(OfficialRedirect())
    for entry in lock["files"]:
        target = directory / entry["path"]
        target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        if target.exists():
            if target.is_symlink() or target.stat().st_size != entry["bytes"] or hashlib.sha256(target.read_bytes()).hexdigest() != entry["sha256"]:
                raise SystemExit("Existing artifact differs; nothing overwritten: " + entry["path"])
            continue
        fd, temporary = tempfile.mkstemp(prefix=".download-", dir=target.parent)
        try:
            digest = hashlib.sha256()
            size = 0
            with os.fdopen(fd, "wb") as output, opener.open(checked_url(entry["url"]), timeout=30) as response:
                while True:
                    block = response.read(1024 * 1024)
                    if not block:
                        break
                    size += len(block)
                    if size > entry["bytes"]:
                        raise ValueError("Artifact exceeds its pinned size")
                    digest.update(block)
                    output.write(block)
            if size != entry["bytes"] or digest.hexdigest() != entry["sha256"]:
                raise ValueError("Artifact digest/size mismatch")
            os.link(temporary, target)  # publish without overwriting existing data
            print("verified " + entry["path"])
        finally:
            os.unlink(temporary)


if __name__ == "__main__":
    main()

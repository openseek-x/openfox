#!/usr/bin/env python3
"""Forced SSH command for a versioned desktop + web release."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
from urllib.request import Request, urlopen
from uuid import uuid4

MAX_BYTES = 768 * 1024 * 1024
VERSION = r"(?:0|[1-9][0-9]{0,8})(?:\.(?:0|[1-9][0-9]{0,8})){2}"


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha(file):
    h = hashlib.sha256()
    with file.open("rb") as src:
        for block in iter(lambda: src.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


def unpack(archive, stage, filename):
    total = 0
    with tarfile.open(archive, "r|gz") as tar:
        for i, item in enumerate(tar):
            name = PurePosixPath(item.name)
            require(i < 10000 and not name.is_absolute() and ".." not in name.parts
                    and name.parts and name.parts[0] in ("web", "desktop"), "unexpected archive path")
            target = stage / name
            if item.isdir():
                target.mkdir(parents=True, exist_ok=True)
                continue
            require(item.isreg() and (name in (PurePosixPath("web/index.html"), PurePosixPath("web/openfox.png"),
                    PurePosixPath("desktop/latest.json"), PurePosixPath("desktop", filename))
                    or name.parts[:2] == ("web", "static")), "unexpected archive member")
            total += item.size
            require(total <= MAX_BYTES, "expanded bundle too large")
            target.parent.mkdir(parents=True, exist_ok=True)
            with tar.extractfile(item) as src, target.open("xb") as dst:
                shutil.copyfileobj(src, dst)
    require((stage / "web/index.html").is_file() and (stage / "web/static").is_dir(), "incomplete web release")


def sudo(*args):
    subprocess.run(["sudo", "-n", *map(str, args)], check=True)


def switch(root, target):
    pointer = root / (".current-" + uuid4().hex)
    sudo("ln", "-s", target, pointer)
    sudo("mv", "-Tf", pointer, root / "current")


def verify_public(origin, stage, filename):
    for route, file in (("/login", stage / "web/index.html"), ("/desktop/latest.json", stage / "desktop/latest.json")):
        with urlopen(Request(origin + route, headers={"Cache-Control": "no-cache"}), timeout=12) as r:
            require(r.status == 200 and r.read(1024 * 1024) == file.read_bytes(), "public mismatch: " + route)
    with urlopen(Request(origin + "/desktop/" + filename, headers={"Range": "bytes=0-3"}), timeout=12) as r:
        require(r.status == 206 and r.read(5) == b"xar!", "public installer range check failed")


def publish(root, stage, origin, version, commit):
    filename = f"OpenFox-{version}-arm64.pkg"
    pkg = stage / "desktop" / filename
    manifest = json.loads((stage / "desktop/latest.json").read_bytes())
    require(manifest.get("version") == version and manifest.get("download_url") == origin + "/desktop/" + filename
            and manifest.get("sha256") == sha(pkg), "release manifest mismatch")
    with pkg.open("rb") as src:
        require(src.read(4) == b"xar!", "not a macOS installer")
    old = os.readlink(root / "current")
    previous = (root / old).resolve(strict=True)
    require(previous.parent == (root / "releases").resolve(), "unexpected current target")
    live = json.loads((previous / "desktop/latest.json").read_bytes())["version"]
    require(re.fullmatch(VERSION, live), "unexpected installed release version")
    if live == version:
        verify_public(origin, stage, filename)
        require(sha(previous / "desktop" / filename) == manifest["sha256"], "version already used")
        print(f"Already published {version} at {old}", flush=True)
        return
    require(tuple(map(int, version.split("."))) > tuple(map(int, live.split("."))), "refusing a version downgrade")
    target = f"releases/desktop-v{version}-{commit[:12]}-{uuid4().hex[:8]}"
    dest = root / target
    require(not dest.exists(), "release directory already exists")
    sudo("mkdir", "-m", "0755", dest)
    # Preserve previously published hashed assets for clients with an old tab open.
    require(not any(p.is_symlink() for p in previous.rglob("*")), "unexpected symlink in live assets")
    sudo("cp", "-a", "--reflink=auto", str(previous) + "/.", dest)
    sudo("cp", "-R", "--no-preserve=ownership", str(stage / "web") + "/.", dest)
    for name in (filename, "latest.json"):
        if name == filename and (dest / "desktop" / name).exists():
            require(sha(dest / "desktop" / name) == manifest["sha256"], "immutable artifact conflict")
        sudo("install", "-m", "0644", stage / "desktop" / name, dest / "desktop" / name)
    for source in (stage / "web").rglob("*"):
        if source.is_file():
            require(sha(source) == sha(dest / source.relative_to(stage / "web")), "web copy mismatch")
    require(sha(dest / "desktop" / filename) == manifest["sha256"], "installer copy mismatch")
    require(os.readlink(root / "current") == old, "current target changed")
    switch(root, target)
    try:
        for attempt in range(3):
            try:
                verify_public(origin, stage, filename)
                break
            except Exception:
                if attempt == 2:
                    raise
                time.sleep(2)
    except Exception:
        switch(root, old)
        raise
    print(f"Published {version} at {target}; previous target {old}", flush=True)


def main():
    args = argparse.ArgumentParser()
    args.add_argument("--web-root", type=Path, required=True)
    args.add_argument("--lock", type=Path, required=True)
    args.add_argument("--origin", required=True)
    config = args.parse_args()
    os.umask(0o022)
    m = re.fullmatch(r"publish (" + VERSION + r") ([0-9a-f]{40}) ([0-9a-f]{64})", os.environ.get("SSH_ORIGINAL_COMMAND", ""))
    require(m, "expected a release version, commit and bundle digest")
    version, commit, digest = m.groups()
    with tempfile.TemporaryDirectory(prefix="openfox-publish-") as tmp:
        archive = Path(tmp) / "release.tar.gz"
        with archive.open("wb") as dst:
            for block in iter(lambda: sys.stdin.buffer.read(1024 * 1024), b""):
                require(dst.tell() + len(block) <= MAX_BYTES, "upload too large")
                dst.write(block)
        require(sha(archive) == digest, "upload checksum mismatch")
        stage = Path(tmp) / "stage"
        unpack(archive, stage, f"OpenFox-{version}-arm64.pkg")
        with config.lock.open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            publish(config.web_root, stage, config.origin.rstrip("/"), version, commit)


if __name__ == "__main__":
    main()

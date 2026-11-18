#!/usr/bin/env python3
"""Prepare ONLY the pinned offline Hermes core/ACP build in a new directory.

Download the exact files in artifacts.lock.json into --artifacts first. This
script never downloads, resolves newer dependencies, launches Hermes, touches
user configuration, or installs a persistent service. The Go adapter separately
checks the embedded per-file manifest before executing this installation.
"""
import argparse
import hashlib
import json
import os
import pathlib
import platform
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifacts", required=True, type=pathlib.Path)
    parser.add_argument("--destination", required=True, type=pathlib.Path)
    args = parser.parse_args()
    if (platform.system(), platform.machine()) != ("Darwin", "arm64"):
        raise SystemExit("This build supports macOS arm64 only")
    here = pathlib.Path(__file__).resolve().parent
    lock = json.loads((here / "artifacts.lock.json").read_text())
    artifacts = args.artifacts.resolve(strict=True)
    destination = args.destination.absolute()
    if destination.exists():
        raise SystemExit("Destination must not exist; existing installations are never overwritten")
    for entry in lock["files"]:
        path = artifacts / entry["path"]
        if path.is_symlink() or path.stat().st_size != entry["bytes"]:
            raise SystemExit("Artifact type/size mismatch: " + entry["path"])
        with path.open("rb") as file:
            digest = hashlib.sha256()
            for block in iter(lambda: file.read(1024 * 1024), b""):
                digest.update(block)
        if digest.hexdigest() != entry["sha256"]:
            raise SystemExit("Artifact digest mismatch: " + entry["path"])
    destination.mkdir(mode=0o700, parents=False)
    destination = destination.resolve()
    for part in ("source-unpack", "runtime", "prep-home", "cache"):
        (destination / part).mkdir(mode=0o700)
    # These exact immutable archives were checked against the official Git
    # tree/release digests. No archive or wheel with a new hash is accepted.
    for archive, output in (("source.tar.gz", "source-unpack"), ("python.tar.gz", "runtime"), ("uv.tar.gz", "runtime"), ("ripgrep.tar.gz", "runtime")):
        subprocess.run(["/usr/bin/tar", "-xzf", str(artifacts / archive), "-C", str(destination / output)], check=True)
    python = destination / "runtime/python/bin/python3.14"
    uv = destination / "runtime/uv-aarch64-apple-darwin/uv"
    venv = destination / "runtime/venv"
    env = {"HOME": str(destination / "prep-home"), "PATH": "/usr/bin:/bin", "UV_CACHE_DIR": str(destination / "cache"), "UV_PYTHON_DOWNLOADS": "never", "LANG": "en_US.UTF-8"}
    subprocess.run([str(uv), "venv", "--offline", "--python", str(python), str(venv)], env=env, check=True)
    # The lock contains the full verified core+ACP dependency closure. --no-deps
    # prohibits resolver expansion and keeps the official Pillow override exact.
    subprocess.run([str(uv), "pip", "install", "--python", str(venv / "bin/python"), "--offline", "--no-index", "--no-build", "--no-deps", "--require-hashes", "--find-links", str(artifacts / "wheels"), "-r", str(here / "requirements.txt")], env=env, check=True)
    # uv's cache indexes encode the artifact directory timestamp. They are not
    # wheel code or Runtime metadata; remove them from this new installation so
    # the trusted tree is independent of the operator's artifact cache location.
    # Verification rejects any such extra file instead of trusting its contents.
    for index in (venv / "lib/python3.14/site-packages").glob("*.dist-info/uv_cache.json"):
        if index.is_symlink() or not index.is_file():
            raise SystemExit("Unexpected uv cache index type")
        index.unlink()
    # No runtime is started here. Device owner can now run the OpenFox probe,
    # which verifies all trusted files and executes ACP inside the sandbox.
    source = destination / ("source-unpack/hermes-agent-" + lock["hermes_commit"])
    print(json.dumps({"OPENFOX_HERMES_ROOT": str(source), "OPENFOX_HERMES_PYTHON": str(venv / "bin/python"), "network_used": False}, indent=2))


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Materialize the two pre-fetched, fixed QQ SDK archives without executing them.
No network, npm, package scripts, dependency resolution, or existing-directory
mutation. Runtime independently checks the same manifest and Node binary.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import tarfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--connector', type=Path, required=True)
parser.add_argument('--qrcode', type=Path, required=True)
parser.add_argument('--target', type=Path, required=True)
args = parser.parse_args()
manifest = json.loads((Path(__file__).resolve().parents[2] / 'internal/integrations/qqconnector/provenance.json').read_text())
if not args.target.is_absolute() or args.target.exists() or args.target.is_symlink():
    raise SystemExit('target must be an absent absolute directory')
files = {}
for archive, prefix, hash_key in [(args.connector, 'connector', 'connector_archive_sha256'), (args.qrcode, 'node_modules/qrcode-terminal', 'qrcode_archive_sha256')]:
    if archive.stat().st_size > 4 * 1024 * 1024 or hashlib.sha256(archive.read_bytes()).hexdigest() != manifest[hash_key]:
        raise SystemExit('archive SHA-256 mismatch')
    with tarfile.open(archive, 'r:gz') as tar:
        for member in tar:
            path = PurePosixPath(member.name)
            if path.is_absolute() or '..' in path.parts or not path.parts or path.parts[0] != 'package':
                raise SystemExit('unexpected archive path')
            if member.isdir():
                continue
            if not member.isfile() or member.size > 2 * 1024 * 1024:
                raise SystemExit('unexpected archive member type or size')
            name = str(PurePosixPath(prefix, *path.parts[1:]))
            if name not in manifest['files'] or name in files:
                raise SystemExit('unmanifested or duplicate file')
            stream = tar.extractfile(member)
            if stream is None:
                raise SystemExit('missing archive file')
            data = stream.read(2 * 1024 * 1024 + 1)
            if hashlib.sha256(data).hexdigest() != manifest['files'][name]:
                raise SystemExit('file SHA-256 mismatch')
            files[name] = data
if set(files) != set(manifest['files']):
    raise SystemExit('incomplete artifact set')
args.target.mkdir(mode=0o700)  # Exclusive creation only; never remove another tree.
try:
    for name, data in sorted(files.items()):
        destination = args.target / name
        destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        fd = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'wb') as output:
            output.write(data)
except BaseException:
    shutil.rmtree(args.target)
    raise
print(json.dumps({'files': len(files), 'target': str(args.target), 'connector': manifest['connector_version'], 'qrcode': manifest['qrcode_version'], 'network': False, 'scripts_executed': False}))

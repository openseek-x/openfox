#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: $0 BACKUP_DIRECTORY" >&2
    exit 2
fi

backup_dir=$1
for command in pg_restore tar shasum mktemp; do
    command -v "$command" >/dev/null 2>&1 || {
        echo "missing required command: $command" >&2
        exit 2
    }
done

[[ -d "$backup_dir" ]] || { echo "backup directory does not exist" >&2; exit 1; }
for file in database.dump artifacts.tar metadata.txt storage.sha256 checksums.sha256; do
    [[ -f "$backup_dir/$file" ]] || { echo "backup is missing $file" >&2; exit 1; }
done

(cd "$backup_dir" && shasum -a 256 -c checksums.sha256 >/dev/null)
pg_restore --list "$backup_dir/database.dump" >/dev/null

while IFS= read -r member; do
    case "$member" in
        /*|../*|*/../*|..)
            echo "unsafe archive member: $member" >&2
            exit 1
            ;;
    esac
done < <(tar -tf "$backup_dir/artifacts.tar")

extract_dir=$(mktemp -d "${TMPDIR:-/tmp}/openfox-backup-verify.XXXXXX")
cleanup() {
    rm -rf "$extract_dir"
}
trap cleanup EXIT INT TERM
tar -xf "$backup_dir/artifacts.tar" -C "$extract_dir"

while IFS= read -r line || [[ -n "$line" ]]; do
    [[ -z "$line" ]] && continue
    expected=${line%%  *}
    relative=${line#*  }
    case "$relative" in
        /*|../*|*/../*|..)
            echo "unsafe manifest path: $relative" >&2
            exit 1
            ;;
    esac
    path="$extract_dir/$relative"
    [[ -f "$path" ]] || { echo "missing storage file: $relative" >&2; exit 1; }
    actual=$(shasum -a 256 "$path" | awk '{print $1}')
    [[ "$actual" == "$expected" ]] || {
        echo "storage checksum mismatch: $relative" >&2
        exit 1
    }
done <"$backup_dir/storage.sha256"

echo "backup verified: $backup_dir"

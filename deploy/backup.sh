#!/usr/bin/env bash
set -euo pipefail

usage() {
    echo "usage: $0 DATABASE_URL_FILE STORAGE_ROOT OUTPUT_DIRECTORY" >&2
    exit 2
}

if [[ $# -ne 3 ]]; then
    usage
fi

database_url_file=$1
storage_root=$2
output_dir=$3

for command in pg_dump tar shasum find sort; do
    command -v "$command" >/dev/null 2>&1 || {
        echo "missing required command: $command" >&2
        exit 2
    }
done

[[ -f "$database_url_file" ]] || { echo "database URL file does not exist" >&2; exit 1; }
[[ -d "$storage_root" ]] || { echo "storage root does not exist" >&2; exit 1; }

umask 077
mkdir -p -- "$(dirname -- "$output_dir")"
mkdir -- "$output_dir" || { echo "cannot reserve output directory" >&2; exit 1; }
cleanup_failed() {
    rm -rf -- "$output_dir"
}
trap cleanup_failed EXIT
trap 'exit 1' INT TERM

database_url=$(cat "$database_url_file")
database_url=${database_url//$'\r'/}
database_url=${database_url//$'\n'/}
[[ -n "$database_url" ]] || { echo "database URL file is empty" >&2; exit 1; }

pg_dump --dbname="$database_url" --format=custom \
    --file="$output_dir/database.dump"
tar -C "$storage_root" -cf "$output_dir/artifacts.tar" .

{
    printf 'format=1\n'
    printf 'created_at=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf 'storage_file_count='
    find "$storage_root" -type f -print | wc -l | tr -d ' '
    printf '\n'
} >"$output_dir/metadata.txt"

(cd -- "$storage_root" && find . -type f -exec shasum -a 256 {} + | LC_ALL=C sort) >"$output_dir/storage.sha256"

(cd "$output_dir" && shasum -a 256 database.dump artifacts.tar metadata.txt storage.sha256 > checksums.sha256)
trap - EXIT INT TERM
echo "backup created: $output_dir"

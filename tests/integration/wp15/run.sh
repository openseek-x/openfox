#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../../.." && pwd)
export GOCACHE="${GOCACHE:-/tmp/openfox-go-cache}"

for command in initdb pg_ctl psql createdb pg_dump pg_restore; do
    command -v "$command" >/dev/null 2>&1 || {
        echo "missing required PostgreSQL command: $command" >&2
        exit 2
    }
done

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/openfox-wp15.XXXXXX")
data_dir="$work_dir/data"
socket_dir="$work_dir/socket"
storage_dir="$work_dir/storage"
port=$((50000 + ($$ % 10000)))
mkdir -p "$socket_dir" "$storage_dir/nested"
cleanup() {
    pg_ctl -D "$data_dir" -w stop -m fast >/dev/null 2>&1 || true
    rm -rf "$work_dir"
}
trap cleanup EXIT INT TERM

initdb -D "$data_dir" -U postgres --auth=trust --no-locale \
    -c shared_memory_type=mmap -c dynamic_shared_memory_type=posix >/dev/null
pg_ctl -D "$data_dir" -w start -l "$work_dir/postgres.log" \
    -o "-k $socket_dir -p $port -c listen_addresses='' -c fsync=off -c shared_memory_type=mmap -c dynamic_shared_memory_type=posix" >/dev/null
export PGHOST="$socket_dir" PGPORT="$port" PGUSER=postgres PGDATABASE=openfox_test
createdb "$PGDATABASE"
printf 'user=postgres host=%s port=%s dbname=%s sslmode=disable\n' "$PGHOST" "$PGPORT" "$PGDATABASE" >"$work_dir/database.url"
chmod 600 "$work_dir/database.url"
printf 'WP-15 backup fixture\n' >"$storage_dir/report.txt"
printf 'nested artifact\n' >"$storage_dir/nested/result.txt"

go run ./cmd/openfox-server migrate --apply --database-url-file "$work_dir/database.url" --dir "$repo_root/db/migrations"
go run ./cmd/openfox-server migrate --apply --database-url-file "$work_dir/database.url" --dir "$repo_root/db/migrations"
"$repo_root/deploy/backup.sh" "$work_dir/database.url" "$storage_dir" "$work_dir/backup"
"$repo_root/deploy/verify-backup.sh" "$work_dir/backup"

export OPENFOX_TEST_DATABASE_URL="user=postgres host=$PGHOST port=$PGPORT dbname=$PGDATABASE sslmode=disable"
(cd "$repo_root" && go test ./tests/integration/wp15 -count=1)

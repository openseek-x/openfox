#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../../.." && pwd)
export GOCACHE="${GOCACHE:-/tmp/openfox-go-cache}"

for command in initdb pg_ctl psql createdb; do
    command -v "$command" >/dev/null 2>&1 || {
        echo "missing required PostgreSQL command: $command" >&2
        exit 2
    }
done

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/openfox-wechat.XXXXXX")
data_dir="$work_dir/data"
socket_dir="$work_dir/socket"
port=$((50000 + ($$ % 10000)))
mkdir -p "$socket_dir"
cleanup() {
    pg_ctl -D "$data_dir" -w stop -m fast >/dev/null 2>&1 || true
    rm -rf "$work_dir"
}
trap cleanup EXIT INT TERM

initialized=false
for attempt in 1 2 3 4 5; do
    rm -rf "$data_dir"
    if initdb -D "$data_dir" -U postgres --auth=trust \
        -c shared_memory_type=mmap -c dynamic_shared_memory_type=mmap >/dev/null 2>"$work_dir/initdb.log"; then
        initialized=true
        break
    fi
    sleep 1
done
if [[ "$initialized" != true ]]; then
    cat "$work_dir/initdb.log" >&2
    exit 1
fi

pg_ctl -D "$data_dir" -w start -l "$work_dir/postgres.log" \
    -o "-k $socket_dir -p $port -c listen_addresses='' -c fsync=off -c shared_memory_type=mmap -c dynamic_shared_memory_type=mmap" >/dev/null
export PGHOST="$socket_dir" PGPORT="$port" PGUSER=postgres PGDATABASE=openfox_test
createdb "$PGDATABASE"
for migration in "$repo_root"/db/migrations/[0-9]*.sql; do
    echo "applying $(basename "$migration")"
    psql -X -v ON_ERROR_STOP=1 -f "$migration"
done
psql -X -v ON_ERROR_STOP=1 -f "$repo_root/tests/integration/db/fixture.sql"
export OPENFOX_TEST_DATABASE_URL="user=postgres host=$PGHOST port=$PGPORT dbname=$PGDATABASE sslmode=disable"
(cd "$repo_root" && go test ./tests/integration/wechat -count=1 ${OPENFOX_TEST_FLAGS:-})

#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../../.." && pwd)
export GOCACHE="${GOCACHE:-/tmp/openfox-go-cache}"

database_url="${OPENFOX_TEST_DATABASE_URL:-}"
work_dir=""

cleanup() {
    if [[ -n "$work_dir" ]]; then
        pg_ctl -D "$work_dir/data" -w stop -m fast >/dev/null 2>&1 || true
        rm -rf "$work_dir"
    fi
}
trap cleanup EXIT INT TERM

run_sql() {
    local file=$1
    echo "running $(basename "$file")"
    if ! psql "$database_url" -X -v ON_ERROR_STOP=1 -f "$file"; then
        echo "WP-02/WP-03/WP-04 SQL baseline failed in $(basename "$file")" >&2
        echo "The database may lack the parent WP-02/WP-03 schema or its contract columns; see the psql diagnostic above." >&2
        exit 1
    fi
}

run_go() {
    echo "running Go/HTTP contract baseline"
    (cd "$repo_root" && go test ./tests/integration/wp02_wp03 -count=1)
}

if [[ -n "$database_url" ]]; then
    command -v psql >/dev/null 2>&1 || {
        echo "OPENFOX_TEST_DATABASE_URL is set but psql is unavailable; install PostgreSQL client tools or use the isolated-cluster path" >&2
        exit 2
    }
    echo "using OPENFOX_TEST_DATABASE_URL without printing its value"
    run_sql "$script_dir/fixture.sql"
    run_sql "$script_dir/assertions.sql"
    run_go
    echo "WP-02/WP-03/WP-04 baseline completed against the configured test database"
    exit 0
fi

missing_command=""
for command in initdb pg_ctl psql createdb; do
    if ! command -v "$command" >/dev/null 2>&1; then
        missing_command+=" $command"
    fi
done

if [[ -n "$missing_command" ]]; then
    echo "PostgreSQL is unavailable ($missing_command); SQL baseline was written but not executed" >&2
    run_go
    echo "Go baseline completed; HTTP tests are expected to skip until the parent adapter is wired"
    exit 0
fi

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/openfox-wp02-wp03.XXXXXX")
data_dir="$work_dir/data"
socket_dir="$work_dir/socket"
port=$((50000 + ($$ % 10000)))
mkdir -p "$socket_dir"

initdb -D "$data_dir" -U postgres --auth=trust --no-locale >/dev/null
server_log="$work_dir/postgres.log"
if ! pg_ctl -D "$data_dir" -w start -l "$server_log" \
    -o "-k $socket_dir -p $port -c listen_addresses='' -c fsync=off" >/dev/null; then
    cat "$server_log" >&2
    exit 1
fi

export PGHOST="$socket_dir"
export PGPORT="$port"
export PGUSER=postgres
export PGDATABASE=openfox_test
createdb "$PGDATABASE"
database_url="user=postgres host=$PGHOST port=$PGPORT dbname=$PGDATABASE sslmode=disable"

for migration in "$repo_root"/db/migrations/[0-9]*.sql; do
    run_sql "$migration"
done
run_sql "$script_dir/fixture.sql"
run_sql "$script_dir/assertions.sql"
run_go
echo "WP-02/WP-03/WP-04 baseline completed against an isolated temporary PostgreSQL cluster"

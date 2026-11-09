#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../../.." && pwd)
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
    psql "$database_url" -X -v ON_ERROR_STOP=1 -f "$1"
}

if [[ -n "$database_url" ]]; then
    run_sql "$repo_root/tests/integration/wp02_wp03/fixture.sql"
    run_sql "$script_dir/assertions.sql"
    export OPENFOX_TEST_DATABASE_URL="$database_url"
    (cd "$repo_root" && go test ./tests/integration/wp05 -count=1 -v)
    echo "WP-05 PostgreSQL assertions passed against the configured database"
    exit 0
fi

missing_command=""
for command in initdb pg_ctl psql createdb; do
    if ! command -v "$command" >/dev/null 2>&1; then
        missing_command+=" $command"
    fi
done
if [[ -n "$missing_command" ]]; then
    echo "PostgreSQL is unavailable ($missing_command); WP-05 SQL assertions were not executed" >&2
    exit 2
fi

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/openfox-wp05.XXXXXX")
mkdir -p "$work_dir/socket"
initdb -D "$work_dir/data" -U postgres --auth=trust --no-locale >/dev/null
port=$((50000 + ($$ % 10000)))
pg_ctl -D "$work_dir/data" -w start -l "$work_dir/postgres.log" \
    -o "-k $work_dir/socket -p $port -c listen_addresses='' -c fsync=off" >/dev/null
export PGHOST="$work_dir/socket"
export PGPORT="$port"
export PGUSER=postgres
export PGDATABASE=openfox_test
createdb "$PGDATABASE"
database_url="user=postgres host=$PGHOST port=$PGPORT dbname=$PGDATABASE sslmode=disable"

for migration in "$repo_root"/db/migrations/[0-9]*.sql; do
    run_sql "$migration"
done
run_sql "$repo_root/tests/integration/wp02_wp03/fixture.sql"
run_sql "$script_dir/assertions.sql"
export OPENFOX_TEST_DATABASE_URL="$database_url"
(cd "$repo_root" && go test ./tests/integration/wp05 -count=1 -v)
echo "WP-05 PostgreSQL assertions passed against an isolated temporary cluster"

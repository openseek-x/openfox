#!/usr/bin/env bash
set -euo pipefail

for command in initdb pg_ctl psql createdb; do
    command -v "$command" >/dev/null 2>&1 || {
        echo "missing required PostgreSQL command: $command" >&2
        exit 2
    }
done

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
export GOCACHE="${GOCACHE:-/tmp/openfox-go-cache}"
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/openfox-db-it.XXXXXX")
data_dir="$work_dir/data"
socket_dir="$work_dir/socket"
port=$((50000 + ($$ % 10000)))
mkdir -p "$socket_dir"

cleanup() {
    pg_ctl -D "$data_dir" -w stop -m fast >/dev/null 2>&1 || true
    rm -rf "$work_dir"
}
trap cleanup EXIT INT TERM

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

for migration in "$repo_root"/db/migrations/[0-9]*.sql; do
    echo "applying $(basename "$migration")"
    psql -X -v ON_ERROR_STOP=1 -f "$migration"
done
psql -X -v ON_ERROR_STOP=1 -f "$repo_root/tests/integration/db/fixture.sql"
psql -X -v ON_ERROR_STOP=1 -f "$repo_root/tests/integration/db/assertions.sql"

echo "running Go pgx/auth/tenant integration"
export OPENFOX_TEST_DATABASE_URL="user=postgres host=$PGHOST port=$PGPORT dbname=$PGDATABASE sslmode=disable"
(
    cd "$repo_root"
    go test ./tests/integration/api -run TestPostgresAuthAndTenantAPI -count=1
)

echo "running concurrent last-slot reservation race"
reserve_a="$work_dir/reserve-a.out"
reserve_b="$work_dir/reserve-b.out"
set +e
psql -X -qAt -v ON_ERROR_STOP=1 -f "$repo_root/tests/integration/db/race_reserve_a.sql" >"$reserve_a" 2>"$work_dir/reserve-a.err" &
pid_a=$!
psql -X -qAt -v ON_ERROR_STOP=1 -f "$repo_root/tests/integration/db/race_reserve_b.sql" >"$reserve_b" 2>"$work_dir/reserve-b.err" &
pid_b=$!
wait "$pid_a"
status_a=$?
wait "$pid_b"
status_b=$?
set -e
successful_reservations=0
if [[ $status_a -eq 0 ]]; then
    successful_reservations=$((successful_reservations + 1))
fi
if [[ $status_b -eq 0 ]]; then
    successful_reservations=$((successful_reservations + 1))
fi
if [[ $successful_reservations -ne 1 ]]; then
    echo "last-slot race did not produce exactly one successful transaction" >&2
    cat "$work_dir/reserve-a.err" "$work_dir/reserve-b.err" >&2
    exit 1
fi
psql -X -v ON_ERROR_STOP=1 -c "DO \$\$ BEGIN IF (SELECT reserved_slots FROM openfox.physical_devices WHERE id = '00000000-0000-0000-0000-000000000011') <> 0 THEN RAISE EXCEPTION 'last-slot cleanup left a reservation'; END IF; END \$\$;"

echo "running concurrent live-offer claim race"
psql -X -v ON_ERROR_STOP=1 -f "$repo_root/tests/integration/db/prepare_claim.sql" >/dev/null
claim_a="$work_dir/claim-a.out"
claim_b="$work_dir/claim-b.out"
psql -X -qAt -v ON_ERROR_STOP=1 -f "$repo_root/tests/integration/db/race_claim_a.sql" >"$claim_a" &
pid_a=$!
psql -X -qAt -v ON_ERROR_STOP=1 -f "$repo_root/tests/integration/db/race_claim_b.sql" >"$claim_b" &
pid_b=$!
wait "$pid_a"
wait "$pid_b"
claim_successes=$(( $(grep -c '^t$' "$claim_a" || true) + $(grep -c '^t$' "$claim_b" || true) ))
if [[ $claim_successes -ne 1 ]]; then
    echo "live-offer claim race produced $claim_successes successful claims" >&2
    exit 1
fi

echo "running concurrent expired-offer reap race"
psql -X -v ON_ERROR_STOP=1 -f "$repo_root/tests/integration/db/prepare_reap.sql" >/dev/null
reap_a="$work_dir/reap-a.out"
reap_b="$work_dir/reap-b.out"
psql -X -qAt -v ON_ERROR_STOP=1 -f "$repo_root/tests/integration/db/race_reap_a.sql" >"$reap_a" &
pid_a=$!
psql -X -qAt -v ON_ERROR_STOP=1 -f "$repo_root/tests/integration/db/race_reap_b.sql" >"$reap_b" &
pid_b=$!
wait "$pid_a"
wait "$pid_b"
reap_successes=$(( $(grep -c '^t$' "$reap_a" || true) + $(grep -c '^t$' "$reap_b" || true) ))
if [[ $reap_successes -ne 1 ]]; then
    echo "expired-offer reap race produced $reap_successes successful reaps" >&2
    exit 1
fi

psql -X -v ON_ERROR_STOP=1 -c "DO \$\$ DECLARE slots integer; BEGIN SELECT reserved_slots INTO slots FROM openfox.physical_devices WHERE id = '00000000-0000-0000-0000-000000000033'; IF slots <> 0 THEN RAISE EXCEPTION 'reap race did not release capacity'; END IF; END \$\$;"
echo "database integration checks passed on an isolated temporary PostgreSQL cluster"

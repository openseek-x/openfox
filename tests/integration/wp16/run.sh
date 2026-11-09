#!/usr/bin/env bash
set -u -o pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../../.." && pwd)
export GOCACHE="${GOCACHE:-/tmp/openfox-go-cache}"

artifact_dir=${OPENFOX_WP16_ARTIFACT_DIR:-$(mktemp -d "${TMPDIR:-/private/tmp}/openfox-wp16-report.XXXXXX")}
mkdir -p "$artifact_dir"
chmod 700 "$artifact_dir"
results_tsv="$artifact_dir/results.tsv"
report_prefix="$artifact_dir/report"
printf 'case\trequired\tstatus\tcommand\tlog\n' >"$results_tsv"

overall_status=0

record_result() {
    local case_id=$1
    local required=$2
    local status=$3
    local command=$4
    local log=$5
    printf '%s\t%s\t%s\t%s\t%s\n' "$case_id" "$required" "$status" "$command" "$log" >>"$results_tsv"
}

run_case() {
    local case_id=$1
    local required=$2
    local command=$3
    shift 3
    local log="$artifact_dir/${case_id}.log"
    echo "[WP-16] $case_id"
    if (cd "$repo_root" && "$@") >"$log" 2>&1; then
        record_result "$case_id" "$required" pass "$command" "$log"
        echo "[WP-16] $case_id: pass"
    else
        local status=fail
        if rg -q 'Operation not permitted|listen EPERM|missing required PostgreSQL command|pnpm is unavailable' "$log"; then
            status=blocked
        fi
        record_result "$case_id" "$required" "$status" "$command" "$log"
        overall_status=1
        echo "[WP-16] $case_id: $status (see $log)" >&2
    fi
}

record_not_run() {
    local case_id=$1
    local required=$2
    local command=$3
    local reason=$4
    local log="$artifact_dir/${case_id}.log"
    printf '%s\n' "$reason" >"$log"
    record_result "$case_id" "$required" not-run "$command" "$log"
    if [[ "$required" == yes ]]; then
        overall_status=1
    fi
    echo "[WP-16] $case_id: not-run ($reason)" >&2
}

run_wp16_schema_shape() (
    set -euo pipefail
    for command in initdb pg_ctl psql createdb; do
        command -v "$command" >/dev/null 2>&1 || {
            echo "missing required PostgreSQL command: $command" >&2
            exit 2
        }
    done

    work_dir=$(mktemp -d "${TMPDIR:-/private/tmp}/openfox-wp16-db.XXXXXX")
    trap 'pg_ctl -D "$work_dir/data" -w stop -m fast >/dev/null 2>&1 || true; rm -rf "$work_dir"' EXIT INT TERM
    mkdir -p "$work_dir/socket"
    initdb -D "$work_dir/data" -U postgres --auth=trust --no-locale \
        -c shared_memory_type=mmap -c dynamic_shared_memory_type=posix >/dev/null
    port=$((50000 + ($$ % 10000)))
    pg_ctl -D "$work_dir/data" -w start -l "$work_dir/postgres.log" \
        -o "-k $work_dir/socket -p $port -c listen_addresses='' -c fsync=off -c shared_memory_type=mmap -c dynamic_shared_memory_type=posix" >/dev/null
    export PGHOST="$work_dir/socket" PGPORT="$port" PGUSER=postgres PGDATABASE=openfox_test
    createdb "$PGDATABASE"
    for migration in "$repo_root"/db/migrations/[0-9]*.sql; do
        psql -X -v ON_ERROR_STOP=1 -f "$migration" >/dev/null
    done
    psql -X -v ON_ERROR_STOP=1 -f "$repo_root/tests/integration/wp02_wp03/fixture.sql" >/dev/null
    export OPENFOX_TEST_DATABASE_URL="user=postgres host=$PGHOST port=$PGPORT dbname=$PGDATABASE sslmode=disable"
    go test ./tests/integration/wp16 -count=1 -v
)

run_wp16_live_web() (
    set -euo pipefail
    for command in initdb pg_ctl createdb; do
        command -v "$command" >/dev/null 2>&1 || {
            echo "missing required PostgreSQL command: $command" >&2
            exit 2
        }
    done

    work_dir=$(mktemp -d "/private/tmp/openfox-wp16-live.XXXXXX")
    trap 'pg_ctl -D "$work_dir/data" -w stop -m fast >/dev/null 2>&1 || true; rm -rf "$work_dir"' EXIT INT TERM
    mkdir -p "$work_dir/socket"
    initdb -D "$work_dir/data" -U postgres --auth=trust --no-locale \
        -c shared_memory_type=mmap -c dynamic_shared_memory_type=posix >/dev/null
    port=$((50000 + ($$ % 10000)))
    pg_ctl -D "$work_dir/data" -w start -l "$work_dir/postgres.log" \
        -o "-k $work_dir/socket -p $port -c listen_addresses='' -c fsync=off -c shared_memory_type=mmap -c dynamic_shared_memory_type=posix" >/dev/null
    export OPENFOX_WP11_PG_SOCKET="$work_dir/socket"
    export OPENFOX_WP11_PG_PORT="$port"
    bash tests/frontend/run-live.sh
)

run_case wp16-schema-shape yes \
    "tests/integration/wp16/run.sh (isolated PostgreSQL + fixture)" \
    run_wp16_schema_shape
run_case database-concurrency yes "bash tests/integration/db/run.sh" \
    bash tests/integration/db/run.sh
run_case auth-membership-tenant yes "bash tests/integration/wp02_wp03/run.sh" \
    bash tests/integration/wp02_wp03/run.sh
run_case device-management yes "bash tests/integration/wp05/run.sh" \
    bash tests/integration/wp05/run.sh
run_case agent-management yes "bash tests/integration/wp06/run.sh" \
    bash tests/integration/wp06/run.sh
run_case events-results yes "bash tests/integration/wp12/run.sh" \
    bash tests/integration/wp12/run.sh
run_case reconciliation yes "bash tests/integration/wp13/run.sh" \
    bash tests/integration/wp13/run.sh
run_case limits-cleanup yes "bash tests/integration/wp14/run.sh" \
    bash tests/integration/wp14/run.sh
run_case operations-backup-recovery yes "bash tests/integration/wp15/run.sh" \
    bash tests/integration/wp15/run.sh
run_case go-full-regression yes "go test ./... -count=1" \
    go test ./... -count=1
run_case go-race-security yes \
    "go test -race ./internal/api ./internal/platform/migrations ./internal/platform/config ./internal/resourcepolicy ./internal/artifacts ./internal/executiondelivery ./tests/integration/wp16" \
    go test -race ./internal/api ./internal/platform/migrations ./internal/platform/config ./internal/resourcepolicy ./internal/artifacts ./internal/executiondelivery ./tests/integration/wp16

if command -v pnpm >/dev/null 2>&1; then
    run_case frontend-typecheck yes "pnpm typecheck" pnpm typecheck
    run_case frontend-unit yes "pnpm test" pnpm test
    run_case frontend-build yes "pnpm build" pnpm build
    run_case frontend-preview-e2e yes "pnpm exec playwright test" pnpm exec playwright test
else
    record_not_run frontend-typecheck yes "pnpm typecheck" "pnpm is unavailable"
    record_not_run frontend-unit yes "pnpm test" "pnpm is unavailable"
    record_not_run frontend-build yes "pnpm build" "pnpm is unavailable"
    record_not_run frontend-preview-e2e yes "pnpm exec playwright test" "pnpm is unavailable"
fi

if [[ "${OPENFOX_WP16_RUN_LIVE:-0}" == 1 ]]; then
    run_case frontend-live-e2e yes "tests/integration/wp16/run.sh (isolated PostgreSQL + live Web/API)" \
        run_wp16_live_web
elif [[ -n "${OPENFOX_WP11_PG_SOCKET:-}" && -n "${OPENFOX_WP11_PG_PORT:-}" ]]; then
    run_case frontend-live-e2e yes "bash tests/frontend/run-live.sh" \
        bash tests/frontend/run-live.sh
else
    record_not_run frontend-live-e2e yes "bash tests/frontend/run-live.sh" \
        "temporary PostgreSQL socket/port not supplied; live Web/API evidence is unavailable"
fi

record_not_run real-runtime-e2e yes "real Runtime dual-link acceptance" \
    "no authorized real Runtime/device fixture was supplied; pseudo Runtime evidence cannot substitute"
run_case release-hygiene yes \
    "bash -n deploy/*.sh tests/integration/wp*/run.sh && git diff --check" \
    bash -c 'bash -n deploy/*.sh tests/integration/wp*/run.sh && git diff --check'

set +e
python3 "$script_dir/report.py" "$results_tsv" "$report_prefix"
report_status=$?
set -e
if [[ $report_status -ne 0 ]]; then
    overall_status=1
fi
echo "[WP-16] evidence directory: $artifact_dir"
exit "$overall_status"

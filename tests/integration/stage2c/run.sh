#!/usr/bin/env bash
# 端到端：自建临时 PostgreSQL 集群，应用全部迁移与夹具，编译 openfox 并以子进程跑
# computer register → daemon run --config → Run / 远程命令 / 令牌续期。不启动真实 Codex，不安装 launchd。
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../../.." && pwd)
export GOCACHE="${GOCACHE:-/tmp/openfox-go-cache}"

for command in initdb pg_ctl psql createdb lsof; do
    command -v "$command" >/dev/null 2>&1 || {
        echo "missing required command: $command" >&2
        exit 2
    }
done

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/openfox-stage2c.XXXXXX")
data_dir="$work_dir/data"
socket_dir="$work_dir/socket"
mkdir -p "$socket_dir"
cleanup() {
    pg_ctl -D "$data_dir" -w stop -m fast >/dev/null 2>&1 || true
    rm -rf "$work_dir"
}
trap cleanup EXIT INT TERM

# 端口只用于 Unix socket 文件名（listen_addresses 为空），仍挑一个当前没有 TCP 监听的端口，避免与本机服务混淆。
port=""
for offset in $(seq 0 50); do
    candidate=$((51000 + ($$ + offset * 37) % 9000))
    if ! lsof -nP -iTCP:"$candidate" -sTCP:LISTEN >/dev/null 2>&1; then
        port=$candidate
        break
    fi
done
if [[ -z "$port" ]]; then
    echo "no free port found" >&2
    exit 1
fi

initialized=false
for attempt in 1 2 3 4 5; do
    rm -rf "$data_dir"
    if initdb -D "$data_dir" -U postgres --auth=trust --no-locale \
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
    psql -X -q -v ON_ERROR_STOP=1 -f "$migration" >/dev/null
done
psql -X -q -v ON_ERROR_STOP=1 -f "$repo_root/tests/integration/db/fixture.sql" >/dev/null
export OPENFOX_TEST_DATABASE_URL="user=postgres host=$PGHOST port=$PGPORT dbname=$PGDATABASE sslmode=disable"
(cd "$repo_root" && go test ./tests/integration/stage2c -count=1 -v ${OPENFOX_TEST_FLAGS:-})

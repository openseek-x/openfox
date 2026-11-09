#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
export PGHOST="${OPENFOX_WP11_PG_SOCKET:?Set a temporary OpenFox test-cluster Unix socket}"
export PGPORT="${OPENFOX_WP11_PG_PORT:?Set the temporary test-cluster port}"
export PGUSER=postgres
export PGDATABASE=postgres
case "$PGHOST" in
  /private/tmp/openfox-*/*|/tmp/openfox-*/*) ;;
  *) echo "Only a temporary local OpenFox cluster is allowed" >&2; exit 1 ;;
esac
# 后端端口可配置：本机 18080 常被其他会话占用，默认改用 18090。
port="${OPENFOX_LIVE_PORT:-18090}"
if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "Port $port is already in use; set OPENFOX_LIVE_PORT to a free port" >&2
  exit 1
fi
database="openfox_wp11_$(date +%s)_$$"
directory=$(mktemp -d /private/tmp/openfox-wp11-browser.XXXXXX)
server_pid=""
created=false
cleanup() {
  if [[ -n "$server_pid" ]]; then kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true; fi
  if [[ "$created" == true ]]; then PGDATABASE=postgres dropdb -- "$database"; fi
  rm -f "$directory/database-url"
  echo "Live-test logs retained in $directory; test database removed"
}
trap cleanup EXIT INT TERM
createdb -- "$database"
created=true
export PGDATABASE="$database"
for migration in db/migrations/[0-9]*.sql; do
  psql -X -v ON_ERROR_STOP=1 -f "$migration" >>"$directory/migrations.log" 2>&1
done
psql -X -v ON_ERROR_STOP=1 -f tests/integration/db/fixture.sql >>"$directory/migrations.log" 2>&1
psql -X -v ON_ERROR_STOP=1 -f tests/frontend/fixture.sql >>"$directory/migrations.log" 2>&1
export OPENFOX_TEST_DATABASE_URL="user=postgres host=$PGHOST port=$PGPORT dbname=$PGDATABASE sslmode=disable"
export GOCACHE="${GOCACHE:-/tmp/openfox-go-cache}"
# 验证码登录：开发环境万能验证码（生产环境会被配置校验拒绝），每次运行随机生成。
export OPENFOX_LIVE_EMAIL=alice@example.test
OPENFOX_LIVE_OTP_CODE=$(printf '%06d' $(( $(od -An -N4 -tu4 /dev/urandom) % 1000000 )))
export OPENFOX_LIVE_OTP_CODE
umask 077
printf '%s\n' "$OPENFOX_TEST_DATABASE_URL" >"$directory/database-url"
go build -o "$directory/server" ./cmd/openfox-server
OPENFOX_ENV=development OPENFOX_SERVER_LISTEN=127.0.0.1:$port \
  OPENFOX_SERVER_PUBLIC_URL=http://127.0.0.1:$port \
  OPENFOX_DATABASE_URL_FILE="$directory/database-url" \
  OPENFOX_AUTH_EMAIL_OTP_UNIVERSAL_CODE="$OPENFOX_LIVE_OTP_CODE" \
  "$directory/server" serve >"$directory/server.log" 2>&1 &
server_pid=$!
for attempt in $(seq 1 50); do
  kill -0 "$server_pid" 2>/dev/null || { cat "$directory/server.log"; exit 1; }
  if curl -fsS http://127.0.0.1:$port/ready >/dev/null 2>&1; then break; fi
  sleep .2
done
curl -fsS http://127.0.0.1:$port/ready >/dev/null
OPENFOX_WEB_PORT="${OPENFOX_WEB_PORT:-4173}" OPENFOX_WEB_API_TARGET=http://127.0.0.1:$port pnpm exec playwright test
# 先停掉服务端再跑 pgx 集成测试：服务端的调度与对账后台任务会并发改写测试造的 Run 与会话，
# 导致 wp10 这类按版本断言的测试偶发冲突。
kill "$server_pid" 2>/dev/null || true
wait "$server_pid" 2>/dev/null || true
server_pid=""
go test -p 1 ./tests/integration/api ./tests/integration/wp10 ./tests/integration/wp12 ./tests/integration/stage1 -count=1 -v

#!/usr/bin/env bash
set -euo pipefail

image=${1:?需要本地中心镜像}
version=${2:?需要预期版本}
scratch=$(mktemp -d)
project="openfox-center-smoke-${RANDOM}-$$"
cleanup() {
  docker rm -fv "$project-api" "$project-pg" >/dev/null 2>&1 || true
  docker network rm "$project" >/dev/null 2>&1 || true
  rm -f "$scratch/database.url"
  rmdir "$scratch"
}
trap cleanup EXIT
test "$(docker run --rm "$image" version)" = "$version"
docker run --rm "$image" migrate --check
docker network create "$project" >/dev/null
docker run -d --name "$project-pg" --network "$project" --network-alias postgres \
  -e POSTGRES_DB=openfox -e POSTGRES_PASSWORD=test-only postgres:17 >/dev/null
for _ in {1..90}; do
  if docker exec "$project-pg" pg_isready -h 127.0.0.1 -U postgres >/dev/null; then break; fi
  sleep 1
done
# 外层临时目录仅脚本所有者可访问，容器内的非 root 用户需能读取此测试连接文件。
echo 'postgresql://postgres:test-only@postgres/openfox?sslmode=disable' >"$scratch/database.url"
chmod 0444 "$scratch/database.url"
args=(--network "$project" -v "$scratch/database.url:/run/secrets/database.url:ro"
      -e OPENFOX_DATABASE_URL_FILE=/run/secrets/database.url)
docker run --rm "${args[@]}" "$image" migrate --apply
docker run -d --name "$project-api" "${args[@]}" -e OPENFOX_ENV=development \
  -p 127.0.0.1::8080 "$image" >/dev/null
address=$(docker port "$project-api" 8080/tcp)
for _ in {1..60}; do
  if curl --silent --fail "http://$address/ready" >/dev/null; then break; fi
  sleep 1
done
curl --fail --silent --show-error "http://$address/live"
curl --fail --silent --show-error "http://$address/ready"

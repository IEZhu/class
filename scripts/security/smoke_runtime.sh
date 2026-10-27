#!/usr/bin/env bash
# Exercise rebuilt runtime entrypoints on disposable, isolated containers.
set -euo pipefail
service=${1:?service}
image=${2:?image}
case "$service" in
  api)
    test "$(docker run --rm --network none --entrypoint id "$image" -u)" = 10001
    ;;
  worker)
    test "$(docker run --rm --network none --entrypoint id "$image" -u)" = 10001
    # The entrypoint must start and refuse to run without its database.
    if output=$(docker run --rm --network none "$image" 2>&1); then
      echo 'worker started without DATABASE_URL' >&2
      exit 1
    fi
    grep -Fq 'worker: DATABASE_URL is required' <<<"$output"
    # S3-4: разовый импорт словаря живёт в том же образе и без базы не стартует
    if output=$(docker run --rm --network none --entrypoint dictimport "$image" -source /dev/null 2>&1); then
      echo 'dictimport started without DATABASE_URL' >&2
      exit 1
    fi
    grep -Fq 'dictimport: DATABASE_URL is required' <<<"$output"
    ;;
  harvester)
    # Subtitle harvester (ADR-022): the worker entrypoint plus exactly the
    # pinned yt-dlp, a JS runtime it detects, and no pip at runtime.
    test "$(docker run --rm --network none --entrypoint id "$image" -u)" = 10001
    pinned=$(sed -n 's/^yt-dlp==\([^ ]*\) .*/\1/p' backend/ytdlp/requirements.txt)
    test -n "$pinned"
    test "$(docker run --rm --network none --entrypoint yt-dlp "$image" --version)" = "$pinned"
    docker run --rm --network none --entrypoint sh "$image" -c '! command -v pip && ! command -v pip3'
    # Without a URL yt-dlp exits non-zero after printing its debug header.
    debug=$(docker run --rm --network none --entrypoint yt-dlp "$image" -v 2>&1 || true)
    grep -Fq '[debug] JS runtimes: node-' <<<"$debug"
    grep -Fq 'yt_dlp_ejs-' <<<"$debug"
    if output=$(docker run --rm --network none "$image" 2>&1); then
      echo 'harvester started without DATABASE_URL' >&2
      exit 1
    fi
    grep -Fq 'worker: DATABASE_URL is required' <<<"$output"
    ;;
  web)
    docker run --rm --network none --entrypoint sh "$image" -c 'test -x /usr/bin/node && ! command -v npm && ! command -v yarn'
    test "$(docker run --rm --network none --entrypoint node "$image" --version | cut -d. -f1)" = v22
    docker run -d --name class-ci-web --network none "$image"
    trap 'docker rm -f -v class-ci-web' EXIT
    for attempt in $(seq 1 30); do
      if docker exec class-ci-web wget -q --spider http://127.0.0.1:3000/login; then break; fi
      sleep 1
    done
    docker exec class-ci-web wget -q --spider http://127.0.0.1:3000/login
    # Dates on the site are ru-RU: the runtime must ship full ICU data.
    docker run --rm -i --network none --entrypoint node "$image" <<'NODE'
const assert = require("node:assert/strict");
assert.deepEqual(Intl.DateTimeFormat.supportedLocalesOf("ru-RU"), ["ru-RU"]);
const formatter = new Intl.DateTimeFormat("ru-RU", {
  timeZone: "UTC", day: "numeric", month: "long", year: "numeric"
});
const parts = Object.fromEntries(
  formatter.formatToParts(new Date("2026-01-15T00:30:00Z")).map(({ type, value }) => [type, value]));
assert.equal(parts.day, "15");
assert.equal(parts.month, "января");
assert.equal(parts.year, "2026");
NODE
    ;;
  caddy)
    docker run --rm --network none -v "$PWD/deploy/Caddyfile:/etc/caddy/Caddyfile:ro" "$image" caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
    ;;
  postgres)
    test "$(docker run --rm --network none --entrypoint gosu "$image" postgres id -u)" != 0
    docker run -d --name class-ci-postgres --network none \
      -e POSTGRES_PASSWORD=ci-disposable-password \
      -e POSTGRES_INITDB_ARGS=--auth-host=scram-sha-256 "$image"
    trap 'docker rm -f -v class-ci-postgres' EXIT
    ready=0
    for attempt in $(seq 1 30); do
      # The initialization server accepts sockets before it shuts down. Only
      # the final server listens on TCP and accepts the configured password.
      if docker exec -e PGPASSWORD=ci-disposable-password class-ci-postgres psql -h 127.0.0.1 -U postgres -v ON_ERROR_STOP=1 -Atc 'SELECT 1' | grep -Fxq 1; then
        ready=1
        break
      fi
      sleep 1
    done
    test "$ready" = 1
    docker exec class-ci-postgres sh -c 'test "$(stat -c %u /proc/1)" != 0'
    if docker exec -e PGPASSWORD=ci-wrong-password class-ci-postgres psql -h 127.0.0.1 -U postgres -v ON_ERROR_STOP=1 -Atc 'SELECT 1'; then
      echo 'Postgres accepted an invalid password' >&2
      exit 1
    fi
    docker exec -e PGPASSWORD=ci-disposable-password class-ci-postgres psql -h 127.0.0.1 -U postgres -v ON_ERROR_STOP=1 -c 'SELECT version();'
    ;;
  *) exit 1 ;;
esac

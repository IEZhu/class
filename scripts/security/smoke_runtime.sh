#!/usr/bin/env bash
# Exercise rebuilt runtime entrypoints on disposable, isolated containers.
set -euo pipefail
service=${1:?service}
image=${2:?image}
case "$service" in
  api)
    test "$(docker run --rm --network none --entrypoint id "$image" -u)" = 10001
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

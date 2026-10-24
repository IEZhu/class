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
    docker run -d --name class-ci-postgres --network none -e POSTGRES_PASSWORD=ci-disposable-password "$image"
    trap 'docker rm -f -v class-ci-postgres' EXIT
    for attempt in $(seq 1 30); do
      if docker exec class-ci-postgres pg_isready -U postgres; then break; fi
      sleep 1
    done
    docker exec class-ci-postgres psql -U postgres -v ON_ERROR_STOP=1 -c 'SELECT version();'
    ;;
  *) exit 1 ;;
esac

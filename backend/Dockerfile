# syntax=docker/dockerfile:1
# Один Go-модуль, целевые образы по бинарям (ADR-002): `api` и `worker`,
# плюс `harvester` — worker с yt-dlp для сборщика субтитров (ADR-022).
# api — последняя стадия, поэтому сборка без --target по-прежнему даёт api.

FROM golang:1.26.10-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/ ./cmd/api ./cmd/worker

FROM alpine:3.24 AS worker
RUN apk upgrade --no-cache && adduser -D -u 10001 app
USER app
COPY --from=build /out/worker /usr/local/bin/worker
ENTRYPOINT ["worker"]

# S5-2: pip живёт только в этой стадии. Колёса закреплены версией и sha256
# (ytdlp/requirements.txt); байткод собирается здесь же, потому что в рантайме
# каталог принадлежит root и пользователю app только на чтение. Скрипт bin/
# от pip не нужен (запуск — обёртка ниже), share/ — man-страницы.
FROM alpine:3.24 AS ytdlp
RUN apk upgrade --no-cache && apk add --no-cache py3-pip
COPY ytdlp/requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir --no-deps --require-hashes --only-binary=:all: \
        --root-user-action=ignore --target /opt/yt-dlp -r /tmp/requirements.txt \
    && rm -r /opt/yt-dlp/bin /opt/yt-dlp/share \
    && python3 -m compileall -q /opt/yt-dlp

# Сборщик субтитров: тот же worker с ролью harvester плюс yt-dlp. JavaScript-
# задачи YouTube решает Node.js (сам yt-dlp по умолчанию ищет только deno);
# yt-dlp запускает его с --permission — без доступа к файлам и процессам.
FROM alpine:3.24 AS harvester
RUN apk upgrade --no-cache && apk add --no-cache python3 nodejs && adduser -D -u 10001 app
COPY --from=ytdlp /opt/yt-dlp /opt/yt-dlp
COPY --chmod=0755 <<'SH' /usr/local/bin/yt-dlp
#!/bin/sh
PYTHONPATH=/opt/yt-dlp exec python3 -m yt_dlp --js-runtimes node "$@"
SH
USER app
ENV WORKER_ROLE=harvester
COPY --from=build /out/worker /usr/local/bin/worker
ENTRYPOINT ["worker"]

FROM alpine:3.24 AS api
RUN apk upgrade --no-cache && adduser -D -u 10001 app
USER app
COPY --from=build /out/api /usr/local/bin/api
EXPOSE 8080
ENTRYPOINT ["api"]

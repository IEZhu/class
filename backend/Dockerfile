# syntax=docker/dockerfile:1
# Один Go-модуль, целевые образы по бинарям (ADR-002): `api` и `worker`
# (в образе worker'а — ещё разовая команда dictimport, S3-4).
# api — последняя стадия, поэтому сборка без --target по-прежнему даёт api.

FROM golang:1.26.10-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/ ./cmd/api ./cmd/worker ./cmd/dictimport

FROM alpine:3.24 AS worker
RUN apk upgrade --no-cache && adduser -D -u 10001 app
USER app
COPY --from=build /out/worker /usr/local/bin/worker
# S3-4: разовый импорт словаря запускается из образа worker'а
# (docker compose run --entrypoint dictimport worker …, deploy/README.md)
COPY --from=build /out/dictimport /usr/local/bin/dictimport
ENTRYPOINT ["worker"]

FROM alpine:3.24 AS api
RUN apk upgrade --no-cache && adduser -D -u 10001 app
USER app
COPY --from=build /out/api /usr/local/bin/api
EXPOSE 8080
ENTRYPOINT ["api"]

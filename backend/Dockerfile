# syntax=docker/dockerfile:1
# Один Go-модуль, целевые образы по бинарям (ADR-002).
# Сейчас есть target `api`; target `worker` добавится на этапе 1 (S1-2).

FROM golang:1.25.12-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.20 AS api
RUN adduser -D -u 10001 app
USER app
COPY --from=build /out/api /usr/local/bin/api
EXPOSE 8080
ENTRYPOINT ["api"]

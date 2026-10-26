# syntax=docker/dockerfile:1
# Retain PostgreSQL16 and its entrypoint, replacing only the outdated gosu
# binary with the same upstream1.19 source and patched build dependencies.
FROM docker.io/library/golang:1.27.3-alpine AS gosu-build
ADD --checksum=sha256:b7d2124c326cc8f2c891044b708483ce2f10c360b0b254a0ddfce345f97b6e19 https://proxy.golang.org/github.com/tianon/gosu/@v/v0.0.0-20250923190938-6456aaa0f3c8.zip /tmp/gosu.zip
WORKDIR /build
COPY deploy/gosu/go.mod deploy/gosu/go.sum ./
RUN go mod download \
    && cmp /tmp/gosu.zip "$(go env GOMODCACHE)/cache/download/github.com/tianon/gosu/@v/v0.0.0-20250923190938-6456aaa0f3c8.zip" \
    && CGO_ENABLED=0 go build -mod=readonly -trimpath -buildvcs=false -o /out/gosu github.com/tianon/gosu \
    && go version -m /out/gosu | grep -F 'github.com/tianon/gosu' | grep -F 'v0.0.0-20250923190938-6456aaa0f3c8' \
    && ! go version -m /out/gosu | grep -F '(devel)'

FROM docker.io/library/postgres:16-alpine
RUN apk upgrade --no-cache
COPY --from=gosu-build /out/gosu /usr/local/bin/gosu

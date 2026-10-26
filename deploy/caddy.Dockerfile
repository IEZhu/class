# syntax=docker/dockerfile:1
# Rebuild the same complete standard Caddy distribution with patched Go,
# chi and net. Keep build/module metadata for package and vulnerability scans.
FROM docker.io/library/golang:1.27.3-alpine AS caddy-build
ADD --checksum=sha256:c5b3a3304999625d751b2d2284cb037b9530bf563d1a62b720a7aa6765b0a5d9 https://proxy.golang.org/github.com/caddyserver/caddy/v2/@v/v2.11.7.zip /tmp/caddy.zip
WORKDIR /build
# Building an entrypoint from a required module retains its real version.
# Building inside the extracted source records (devel), hiding root advisories.
RUN go mod init github.com/IEZhu/class/build/caddy \
    && go get github.com/caddyserver/caddy/v2/cmd/caddy@v2.11.7 \
    && go get github.com/go-chi/chi/v5@v5.4.0 golang.org/x/net@v0.60.0 \
    && cmp /tmp/caddy.zip "$(go env GOMODCACHE)/cache/download/github.com/caddyserver/caddy/v2/@v/v2.11.7.zip" \
    && CGO_ENABLED=0 go build -mod=readonly -trimpath -buildvcs=false -o /out/caddy github.com/caddyserver/caddy/v2/cmd/caddy \
    && go version -m /out/caddy | grep -F 'github.com/caddyserver/caddy/v2' | grep -F 'v2.11.7' \
    && ! go version -m /out/caddy | grep -F '(devel)'

FROM docker.io/library/caddy:2-alpine
RUN apk upgrade --no-cache
COPY --from=caddy-build /out/caddy /usr/bin/caddy

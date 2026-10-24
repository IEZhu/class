# syntax=docker/dockerfile:1

FROM node:22-alpine3.22 AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM node:22-alpine3.22 AS build
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build

# The distribution package makes the actual Node runtime visible to Trivy's
# OS inventory; the upstream Node tarball has no APK package record.
FROM alpine:3.22 AS run
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 HOSTNAME=0.0.0.0 PORT=3000
RUN apk upgrade --no-cache \
    && apk add --no-cache 'nodejs~22' \
    && adduser -D -u 10001 app
USER app
COPY --from=build --chown=app /app/.next/standalone ./
COPY --from=build --chown=app /app/.next/static ./.next/static
COPY --from=build --chown=app /app/public ./public
EXPOSE 3000
CMD ["node", "server.js"]

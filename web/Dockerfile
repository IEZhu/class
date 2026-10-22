# syntax=docker/dockerfile:1

FROM node:22-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM node:22-alpine AS build
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build

FROM node:22-alpine AS run
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 HOSTNAME=0.0.0.0 PORT=3000
# npm в рабочем образе не нужен (запуск - node server.js), а его
# вложенные пакеты дают находки сканера уязвимостей
RUN rm -rf /usr/local/lib/node_modules/npm /usr/local/bin/npm /usr/local/bin/npx \
    && adduser -D -u 10001 app
USER app
COPY --from=build --chown=app /app/.next/standalone ./
COPY --from=build --chown=app /app/.next/static ./.next/static
COPY --from=build --chown=app /app/public ./public
EXPOSE 3000
CMD ["node", "server.js"]

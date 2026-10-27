# Поток: фоновый сборщик субтитров

> Этап 5 (S5-1…S5-3). Offline-процесс worker'а: собирает готовые английские
> субтитры для видео-практики. Ничего не транскрибирует и не скачивает видео.
> Решения — [ADR-021](../decisions.md) (расписание) и
> [ADR-022](../decisions.md) (сборщик); код — `backend/internal/harvester`.

## Шаги

1. **Seed.** Кураторский список каналов/плейлистов в `subtitle_sources`
   (миграция `0011_subtitles`: TED, TED-Ed, BBC Learning English,
   VOA Learning English). Выключить источник — `enabled = false`.
2. **Список видео** — джоба `rss_poll` раз в час: бесплатный RSS-фид
   канала (`https://www.youtube.com/feeds/videos.xml?channel_id=…`, без
   квоты YouTube Data API, 15 последних видео) → новые строки `videos`
   (повтор ничего не дублирует). Shorts пропускаются. Сбой фида — в
   `subtitle_sources.last_error`.
3. **Субтитры** — джоба `subtitles_fetch` раз в 5 минут собирает пакет:
   `yt-dlp --skip-download --write-subs --write-auto-subs --sub-langs en`
   (+ `--sub-format vtt --write-info-json`), одно видео в 15–30 с с jitter.
   Manual-субтитры приоритетнее auto: для одного языка yt-dlp берёт ручные,
   авто — только без них; что вышло, пишется в `videos.subtitles`.
   Неудача по видео — `harvest_attempts`, повтор через 6 ч × попытки,
   после трёх видео выпадает из очереди.
4. **Парсинг VTT** → `subtitle_segments` (start_ms, end_ms, text);
   у автосубтитров убираются повторы «катящихся» строк, tsvector-колонка
   генерируется автоматически. FTS по `tsv` (GIN-индекс) —
   `store.SearchSubtitles`, подбор видео-практики — `store.PracticeVideos`
   (ADR-038).
5. **TED** — разовый импорт дампов (S5-3) не сделан: открытого дампа
   с таймкодами нет (ADR-022); видео TED и TED-Ed идут тем же сборщиком.

## Где и как запускается

Сборщик — роль того же worker-бинаря: `WORKER_ROLE=harvester` берёт только
джобы сборщика и ставит их по расписанию. Образ — target `harvester`
в `backend/Dockerfile` (worker + закреплённый yt-dlp + Node.js для
JS-задач YouTube).

- **VPS**: сервис `harvester` под профилем compose, `make up` его
  не поднимает. Включить:
  `docker compose -f deploy/docker-compose.yml --profile harvester up -d --build harvester`;
  выключить — `… stop harvester`.
- **Другая машина** (тот же образ, та же база через VPN/Tailscale; порт
  Postgres стенда наружу по умолчанию не опубликован — его открывают
  только на адресе tailnet):

  ```bash
  docker build --target harvester -t lingua-harvester backend
  docker run -d --name lingua-harvester --restart unless-stopped \
    --read-only --tmpfs /tmp --tmpfs /home/app:uid=10001,gid=10001,mode=0700 \
    --cap-drop ALL --security-opt no-new-privileges:true --memory 512m --cpus 1 \
    -e DATABASE_URL='postgres://lingua_harvester:…@<адрес в tailnet>:5432/lingua?sslmode=disable' \
    lingua-harvester
  ```

  DSN — своей роли сборщика (`HARVESTER_DATABASE_URL`, роль создаёт
  `deploy/harvester-db-role.sh` на VPS, ADR-022): yt-dlp в контейнере
  видит окружение worker'а, а у роли есть только таблицы сборщика
  и очередь. DSN владельца базы на другую машину не уносят.

  Без Docker: `go build ./cmd/worker` и yt-dlp с JS-движком в `PATH`
  (по умолчанию yt-dlp ищет deno), `WORKER_ROLE=harvester`.
  Сборщиков может быть несколько: расписание общее (ADR-021), джоба
  достаётся одному.

## Митигация IP-блокировок YouTube

- Низкий рейт + jitter: пауза 15–30 с от начала прошлого обращения.
- Признаки блокировки (HTTP 429, «вы не бот», rate limit) — пауза процесса
  30 мин с удвоением до суток; пока она идёт, к YouTube не обращаемся.
  Пауза в памяти процесса: у каждого сборщика свой IP.
- Cookies при необходимости: файл в формате Netscape, читаемый uid 10001
  контейнера, в compose — `HARVESTER_COOKIES_FILE` (путь на хосте),
  с другой машины —
  `-v <файл>:/run/secrets/youtube-cookies.txt:ro -e HARVESTER_COOKIES=/run/secrets/youtube-cookies.txt`.
  Нечитаемый файл — беда окружения (`ErrNoRunner`): джоба повторяется,
  попытки видео не тратятся.
- Fallback: запуск сборщика с домашней машины — **тот же worker-бинарь**,
  пишет в ту же PG через VPN/Tailscale (сборщик не завязан на
  VPS-окружение); на VPS сервис при этом останавливают.
- Состояние видно в базе: `subtitle_sources.polled_at/last_error`,
  `videos.subtitles/harvest_error`, `job_schedules`.

Связано: [practice-engine.md](practice-engine.md) ·
[../03-data-model.md](../03-data-model.md) ·
план [stage-5](../../plans/stage-5-content.md)

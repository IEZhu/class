# backend — Go (api + worker)

Код появляется на этапе 0 (S0-2…S0-4). Один Go-модуль, два бинаря
(ADR-002): в compose это сервисы `api` и `worker` из одного Dockerfile.

## Планируемая структура

```text
cmd/
  api/         REST/JSON API: auth, уроки, presigned, LiveKit tokens, вебхуки
  worker/      фоновые задачи: календарь, ASR, VTT, сборщик субтитров, LLM
internal/
  domain/      сущности и правила: уроки, словарь, SRS
  store/       Postgres (pgx), запросы
  http/        роуты, middleware (auth, роли), webhook-верификация
  jobs/        очередь в PG (FOR UPDATE SKIP LOCKED), ретраи — ADR-003
  integr/      gcal, livekit, assemblyai, libretranslate, llm, s3
migrations/    golang-migrate; порядок таблиц по этапам —
               см. docs/architecture/03-data-model.md
```

## Что появляется на каком этапе

| Этап | В backend |
|---|---|
| 0 | api: auth, CRUD уроков/групп/материалов; миграции ядра |
| 1 | worker + jobs; gcal, livekit, s3 (presigned); вебхук LiveKit |
| 2 | assemblyai, VTT-рендер; вебхук AssemblyAI |
| 3 | terms/лемматизация, libretranslate-клиент, словарный импорт, парсер сцены доски |
| 4 | go-fsrs, practice/queue, reviews, cloze |
| 5 | RSS+yt-dlp сборщик, Tatoeba-импорт, LLM-модуль с валидатором |

Контракты API: `../docs/architecture/04-api.md`.

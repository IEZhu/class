# Lingua Class

Платформа для группы изучающих английский (5 преподавателей / 50 студентов,
hobby-MVP): календарь → видеоуроки (LiveKit) → записи (S3) → транскрипты по
спикерам (AssemblyAI) → словарь урока и личный словарь → SRS-повторения (FSRS)
→ практика (карточки / видео по субтитрам / тексты / cloze-тесты).

Ядро — монолит на одном VPS (Docker Compose: Next.js + Go + Postgres),
тяжёлые подсистемы — managed/OSS. Медиа через VPS не ходит.
Смета: ~$15–25/мес (пилот).

## Навигация

- **Обзор**: [docs/README.md](docs/README.md)
- **Архитектура**: [docs/architecture/](docs/architecture/) — топология,
  сервисы, модель данных, API, S3, интеграции, потоки
- **Планы этапов 0–5**: [docs/plans/](docs/plans/) — каждый план со входными
  и выходными артефактами
- **Индекс задач и статусы**: [docs/tasks/README.md](docs/tasks/README.md)
- **Роль и протокол работы**: [CLAUDE.md](CLAUDE.md)

## Структура

```text
backend/   Go: cmd/api, cmd/worker, миграции   (код появится на этапе 0)
web/       Next.js                             (код появится на этапе 0)
deploy/    docker-compose, Caddyfile, .env     (появится на этапе 0)
docs/      архитектура, планы, индекс задач
```

## Статус

Подготовка: документация нарезана, код не начат.
Текущий этап и задача — в [docs/tasks/README.md](docs/tasks/README.md).

# Lingua Class — обзор

> Платформа для группы изучающих английский: 5 преподавателей / 50 студентов,
> hobby-MVP. Один VPS, бюджет ~$15–25/мес (пилот).

## Что это

Полный контур обучения вокруг живых уроков:

```text
календарь → видеоурок (WebRTC) → запись → транскрипт по спикерам
    → словарь урока → личный словарь студента → SRS-повторения (FSRS)
    → практика: карточки · видео по субтитрам · тексты · cloze-тесты
```

## Главный принцип (вариант C, гибрид)

- **Ядро и модель данных — свои**: монолит на VPS (Docker Compose):
  Next.js + Go + Postgres.
- **Тяжёлые подсистемы — managed / готовые OSS-блоки**: LiveKit Cloud (WebRTC),
  AssemblyAI (ASR + diarization), AWS S3 (записи и файлы), LibreTranslate
  (перевод en↔ru), excalidraw-complete (доска).
- **Видео и аудио никогда не проходят через VPS**: медиапотоки — LiveKit Cloud,
  файлы — S3 → браузер напрямую по presigned URL.

## Роли пользователей

- **teacher** — создаёт уроки и материалы, ведёт занятие, подтверждает словарь
  урока после занятия, маппит спикеров транскрипта на имена.
- **student** — участвует в уроках, кликает слова в любых текстах, ведёт личный
  словарь, проходит повторения и практику.

## Карта документации

| Документ | Что там |
|---|---|
| [architecture/00-source-variant-c.md](architecture/00-source-variant-c.md) | Исходный архитектурный документ (снапшот) |
| [architecture/01-topology.md](architecture/01-topology.md) | Топология: что где живёт, что не живёт на VPS |
| [architecture/02-services.md](architecture/02-services.md) | Сервисы docker-compose, RAM/CPU, требования к VPS |
| [architecture/03-data-model.md](architecture/03-data-model.md) | Модель данных + на каком этапе появляется каждая таблица |
| [architecture/04-api.md](architecture/04-api.md) | Эскиз API + привязка эндпоинтов к этапам |
| [architecture/05-storage-s3.md](architecture/05-storage-s3.md) | S3: layout, presigned, lifecycle, бэкапы |
| [architecture/06-budget.md](architecture/06-budget.md) | Смета и пороги апгрейда |
| [architecture/07-risks.md](architecture/07-risks.md) | Риски и митигации |
| [architecture/decisions.md](architecture/decisions.md) | Журнал решений (ADR) |
| [architecture/flows/](architecture/flows/) | Потоки: жизненный цикл урока, клик по слову, практика, сборщик субтитров |
| [architecture/integrations/](architecture/integrations/) | Google Calendar, LiveKit, AssemblyAI, перевод, LLM |
| [plans/00-flow.md](plans/00-flow.md) | Как работаем по этапам: конвейер артефактов |
| [plans/stage-0…5](plans/) | Планы этапов с входными/выходными артефактами |
| [tasks/INDEX.md](tasks/INDEX.md) | Единый индекс задач, статусов и долгов |

## Как мы работаем

Реализация идёт по этапам 0–5. Каждый этап **потребляет артефакты предыдущих
и оставляет свои** — правила конвейера в [plans/00-flow.md](plans/00-flow.md),
статусы задач в [tasks/INDEX.md](tasks/INDEX.md), роль и протокол ассистента
в [../CLAUDE.md](../CLAUDE.md).

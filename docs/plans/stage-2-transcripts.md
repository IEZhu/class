# Этап 2 — Транскрипты AssemblyAI (оценка: 1 нед)

**Цель:** каждая запись урока автоматически превращается в транскрипт по
спикерам: utterances в PG, VTT в S3, вьюер с кликом-seek. После этапа
платформа закрывает цикл «календарь → урок → запись → транскрипт».

Доки: [assemblyai](../architecture/integrations/assemblyai.md) ·
[жизненный цикл урока](../architecture/flows/lesson-lifecycle.md), шаги 6–7, 9

## Входные артефакты

| Артефакт | Откуда |
|---|---|
| `recording_s3_key` + точка вставки ASR после `egress_ended` | S1-5 |
| Presigned GET механика | S1-3 |
| Worker + очередь `jobs` (ретраи) | S1-2 |
| Паттерн webhook-эндпоинта с верификацией | S1-5 |
| Плеер в `/lesson/{id}` (done) | S1-6 |

## Задачи

### S2-1 Джоба asr_submit
Вместо плейсхолдера S1-5: presigned GET на запись (TTL с запасом) →
AssemblyAI (`speaker_labels: true`, опц. `speakers_expected`,
`language_code: en`, `webhook_url` с секретом). Таблица `transcripts`
(provider, status).
**DoD:** после egress_ended задание уходит в AssemblyAI автоматически.

### S2-2 Webhook completed → utterances
`POST /webhooks/assemblyai` (проверка секрета, идемпотентность) →
`utterances` (speaker_label, start_ms, end_ms, text).
**DoD:** повторная доставка webhook'а не дублирует utterances.

### S2-3 VTT-рендер
worker: utterances → WebVTT (`<v A>…`) → `s3://…/transcripts/{lesson_id}.vtt`,
`transcripts.vtt_s3_key`; `lessons.status=done`.
**DoD:** VTT валиден, скачивается по presigned.

### S2-4 Транскрипт-вьюер
`/lesson/{id}` (done): скользящий транскрипт по спикерам рядом с плеером;
клик по реплике = seek; активная реплика подсвечивается по времени плеера.
Правка текста реплики (teacher) — компенсация ASR-ошибок.
**DoD:** клик по реплике мотает запись; текст правится и сохраняется.

### S2-5 Маппинг спикеров
Селект A/B/C → участники урока (`utterances.user_id`), доступен teacher.
**DoD:** после маппинга вьюер показывает имена вместо меток.

## Выходные артефакты

| Артефакт | Потребитель |
|---|---|
| `utterances` (текст урока с таймкодами) | S3-1 (обернуть в ClickableText), S4-4 (предложения для cloze) |
| `transcripts.vtt_s3_key` (VTT в S3) | плеер/экспорт |
| Транскрипт-вьюер | S3-1 (кликабельные слова в репликах) |
| Маппинг спикеров → user_id | аналитика посещаемости/речи (будущее) |

## Definition of Done этапа

- [ ] S2-1…S2-5 закрыты в [INDEX](../tasks/README.md)
- [ ] Сквозной сценарий: провести урок → дождаться транскрипта → кликнуть реплику → плеер прыгнул
- [ ] «Факт» заполнен

## Факт (заполняется по ходу реализации)

_пока пусто_

## Риски этапа

RU/EN code-switching и перекрывающаяся речь портят текст — принято
([07-risks.md](../architecture/07-risks.md)), компенсация — правка в S2-4;
задержка ASR на длинных записях — статус processing честно показывает
ожидание.

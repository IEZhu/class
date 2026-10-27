# Интеграция: AssemblyAI (ASR + diarization)

> Этап 2 (S2-1, S2-2). Async-транскрибация записей уроков; аудио не проходит
> через VPS — AssemblyAI забирает файл из S3 по presigned URL.

## Параметры запроса (worker, джоба asr_submit)

Клиент — `backend/internal/assemblyai` (REST v2, без SDK, ADR-015).
`POST https://api.assemblyai.com/v2/transcript`, ключ в `Authorization`:

- `audio_url` = presigned S3 GET на запись, TTL 24 ч (запас на очередь ASR);
- `speech_models: ["universal-2"]` — явно: умолчание зависит от тарифа;
- `speaker_labels: true`, `punctuate: true`; `speakers_expected` не задаём
  (снапшот участников ≠ число говоривших, ADR-015);
- `language_code: en`;
- `webhook_url` = `https://<DOMAIN>/api/webhooks/assemblyai`,
  `webhook_auth_header_name: X-Lingua-Webhook-Secret`,
  `webhook_auth_header_value` = `ASSEMBLYAI_WEBHOOK_SECRET`.

Ключи `.env`: `ASSEMBLYAI_API_KEY` (только worker; пусто — транскрибация
выключена, урок с записью сразу `done`), `ASSEMBLYAI_WEBHOOK_SECRET`
(api и worker, ≥ 32 символов).

## Обработка результата

Вебхук несёт только `transcript_id` и `status` (`completed` | `error`);
ответ 2xx нужен за 10 с, на прочее AssemblyAI повторяет до 10 раз через
10 с, на 4xx — перестаёт. Поэтому api лишь переводит транскрипт
в `fetching` и ставит джобу `asr_result` (неизвестный `transcript_id` —
503, чтобы доставка повторилась); worker забирает
`GET /v2/transcript/{id}` → сохраняет `utterances` (speaker_label,
start_ms, end_ms, text) → джоба `vtt_render` рендерит WebVTT (`<v A>…`)
→ S3 `transcripts/{lesson_id}.vtt` → `lessons.status=done`.
Каждый шаг идемпотентен (повторная доставка не дублирует utterances).
Потерянный вебхук: через 2 ч после отправки джоба `asr_poll` сама
спрашивает `GET /v2/transcript/{id}` и, если итог готов, делает тот же
переход; дошедший вебхук её снимает. Сбои — ADR-016: урок доходит
до `done` и без транскрипта.

Статусы `transcripts`: `queued` → `submitted` → `fetching` →
`completed` | `error`.

## Цена

universal-2 $0.15/ч + $0.02/ч диаризация → 60 ч/мес ≈ **$10**
(прайс AssemblyAI на 2026-10-26; universal-3-5-pro — $0.21/ч, ≈ $14).

## Caveats

- Перекрывающаяся речь и RU/EN code-switching деградируют качество —
  **свойство задачи, не сервиса**. Принять; русские вставки будут кривыми.
- Компенсация: ручная правка utterances и ручной маппинг спикеров
  A/B/C → имена в UI (S2-4, S2-5).

Связано: [../flows/lesson-lifecycle.md](../flows/lesson-lifecycle.md) ·
план [stage-2](../../plans/stage-2-transcripts.md)

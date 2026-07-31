# Интеграция: AssemblyAI (ASR + diarization)

> Этап 2 (S2-1, S2-2). Async-транскрибация записей уроков; аудио не проходит
> через VPS — AssemblyAI забирает файл из S3 по presigned URL.

## Параметры запроса (worker, джоба asr_submit)

- `audio_url` = presigned S3 GET на запись (TTL с запасом на очередь ASR);
- `speaker_labels: true` (+ опционально `speakers_expected` = число участников);
- `language_code: en`;
- `webhook_url` = `POST /webhooks/assemblyai` c секретным заголовком.

## Обработка результата

Webhook `completed` → сохранить `utterances` (speaker_label, start_ms,
end_ms, text) → отрендерить WebVTT (`<v A>…`) → S3
`transcripts/{lesson_id}.vtt` → `lessons.status=done`.
Обработчик идемпотентен (повторная доставка не дублирует utterances).

## Цена

~$0.15/ч + $0.02/ч диаризация → 60 ч/мес ≈ **$10**.

## Caveats

- Перекрывающаяся речь и RU/EN code-switching деградируют качество —
  **свойство задачи, не сервиса**. Принять; русские вставки будут кривыми.
- Компенсация: ручная правка utterances и ручной маппинг спикеров
  A/B/C → имена в UI (S2-4, S2-5).

Связано: [../flows/lesson-lifecycle.md](../flows/lesson-lifecycle.md) ·
план [stage-2](../../plans/stage-2-transcripts.md)

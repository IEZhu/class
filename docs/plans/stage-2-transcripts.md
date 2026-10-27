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

- [x] S2-1…S2-5 закрыты в [INDEX](../tasks/README.md)
- [ ] Сквозной сценарий: провести урок → дождаться транскрипта → кликнуть реплику → плеер прыгнул
- [x] «Факт» заполнен

## Факт (заполняется по ходу реализации)

### S2-1 Джоба asr_submit (2026-10-26) — ◐

- Миграция `backend/migrations/0008_transcripts.up.sql` (+ `.down.sql`):
  таблицы `transcripts` (`lesson_id` UNIQUE, `provider`, `status`
  `queued|submitted|fetching|completed|error`, `external_id`
  UNIQUE с провайдером, `vtt_s3_key`, `error`) и `utterances`
  (PK `(transcript_id, idx)`); FK `lessons.transcript_id → transcripts`.
  Откат переводит уроки с идущей транскрибацией в `done` и убирает их
  джобы; накат/откат/накат проверены golang-migrate v4.17.1 на
  одноразовом Postgres 16.
- `backend/internal/assemblyai/` — клиент REST v2 (ADR-015): `New`
  (без ключа — nil), `Submit` (`speech_models: [universal-2]`,
  `language_code: en`, `punctuate`, `speaker_labels`, вебхук
  с заголовком `X-Lingua-Webhook-Secret`), `Transcript`,
  `CheckWebhookSecret` (≥ 32 символов).
- Точка вставки — хендлер `recording_ready` (`backend/internal/worker/worker.go`):
  без ключа урок сразу `done` (плейсхолдер ADR-013), с ключом —
  `store.QueueTranscript`: строка `transcripts` + джоба `asr_submit`
  одной транзакцией. Джоба `asr_submit` (`backend/internal/worker/asr.go`):
  presigned GET записи на 24 ч → AssemblyAI → `submitted` + `external_id`.
  Payload джоб транскрибации — `{"transcript_id": N}`.
- `backend/internal/store/transcripts.go` — `QueueTranscript`,
  `TranscriptByID`, `MarkTranscriptSubmitted`, `FailTranscript` (итог
  сбоя, ADR-016); `jobs.Job.LastAttempt` — хендлер знает, что попытка
  последняя.
- Ключи `.env` (`deploy/.env.example`, compose): `ASSEMBLYAI_API_KEY`
  (worker), `ASSEMBLYAI_WEBHOOK_SECRET` (api и worker); worker теперь
  читает и `DOMAIN`, `S3_*` (`storage.ConfigFromEnv`, общий с api).
  Без ключа api и worker стартуют как раньше; ключ без секрета,
  хранилища или `DOMAIN` — worker не стартует.
- Потерянный вебхук (правка ревью волны A, ADR-016; долг D-9 закрыт):
  `MarkTranscriptSubmitted` одной транзакцией ставит отложенную джобу
  `asr_poll` (`store.JobASRPoll`, через `store.ASRPollAfter` = 2 ч,
  `jobs.EnqueueOnce`); она сама спрашивает `GET /v2/transcript/{id}`
  и делает переход вебхука, пока AssemblyAI распознаёт — повторяется
  с backoff; принятый вебхук снимает её (`jobs.Cancel`).
- Последняя попытка, оборванная остановкой worker'а, транскрипт не роняет:
  `jobs.ErrInterrupted` — причина отмены контекста хендлера
  (`context.Cause`), `transcriptStep` на ней не сдаётся.
- Тесты: `internal/assemblyai`, `internal/httpapi/transcripts_test.go`
  (`TestASRSubmit`, отказы и повтор, джоба без ключа, откат 0008,
  `TestASRPollRecoversLostWebhook`, `TestASRShutdownOnLastAttemptKeepsTranscript`)
  с fake AssemblyAI и fake S3 `internal/storage/storagetest`;
  `internal/jobs` — `TestShutdownCutOfLastAttemptIsNotFinal`,
  `TestCancelRemovesOnlyWaitingJob`.
- **Не подтверждено живьём:** ключа AssemblyAI нет; цепочка проверена
  на fake AssemblyAI и fake S3 (storagetest). Осталось для ☑ (S2-1…S2-3):
  урок с записью на стенде с ключом — транскрипт, VTT в R2, урок `done`.

### S2-2 Webhook completed → utterances (2026-10-26) — ◐

- `POST /webhooks/assemblyai` (`backend/internal/httpapi/transcripts.go`):
  без секрета — 503 `assemblyai_not_configured`; неверный заголовок — 401;
  тело `{transcript_id, status: completed|error}`, иначе 400; неизвестный
  транскрипт — 503 `unknown_transcript` (AssemblyAI повторит доставку;
  до ревью волны A было 404); принятый — `fetching` + джоба `asr_result`,
  повтор доставки — 200 без новой джобы.
- Джоба `asr_result`: `GET /v2/transcript/{id}` → `completed` —
  `utterances` (idx по порядку, `speaker_label`, `start_ms`, `end_ms`,
  `text`) + `completed` + джоба `vtt_render` одной транзакцией; `error` —
  итог по ADR-016. Store: `TranscriptWebhook`, `SaveUtterances`,
  `Utterances`, `LessonTranscript`.
- `GET /lessons/{id}/transcript` — для вьюера S2-4: `{status,
  utterances[{idx, speaker_label, user_id, start_ms, end_ms, text}],
  vtt_url, vtt_expires_at}`; доступ как у `GET /lessons/{id}` (teacher
  урока и участники; прочим 403), нет транскрипта — 404 `no_transcript`.
- Тесты: вебхук и однократные реплики, доступ к транскрипту, ошибка
  AssemblyAI, конфигурация секрета. Живьём не проверено — см. S2-1.

### S2-3 VTT-рендер (2026-10-26) — ◐

- `backend/internal/webvtt/` — `Render`: `WEBVTT`, cue на реплику,
  `HH:MM:SS.mmm --> HH:MM:SS.mmm`, `<v A>`, экранирование `& < >`,
  переводы строк → пробел, порядок по началу, конец строго позже начала.
- Джоба `vtt_render`: реплики → VTT → `transcripts/{lesson_id}.vtt`
  (`text/vtt; charset=utf-8`, `storage.Client.PutObject` — один хелпер
  со снимками досок S3-6, обычный PUT с `ContentLength`, `storage.TranscriptKey`)
  → `transcripts.vtt_s3_key`, `lessons.status=done`
  (`store.FinishTranscriptVTT`). Перерендер перезаписывает тот же ключ.
- Сбои (ADR-016): 8 попыток очереди, на последней — транскрипт `error`
  (или `completed` без VTT), урок всё равно `done`. Без хранилища
  (ключи убрали) `vtt_render` сдаётся сразу, а не через ≈ 2 ч.
- Тесты: `TestASRPipeline` (вся цепочка до VTT в fake S3 и `done`,
  повторы всех джоб), сбой выкладки VTT, `TestVTTRenderWithoutStorageFailsFast`,
  `internal/webvtt`, `storage.TestPutObject` (TLS-сервер: тело без aws-chunked).
  Живьём не проверено — см. S2-1.

### S2-4 Транскрипт-вьюер (2026-10-26)

- Миграция `backend/migrations/0014_utterance_edits.up.sql` (+ `.down.sql`):
  `utterances.edited_at`, `edited_by` (FK users, `SET NULL`), CHECK
  `utterances_editor_has_time`. Откат убирает отметки, исправленный текст
  остаётся; накат/откат/накат проверены golang-migrate v4.17.1 на
  одноразовом Postgres 16.
- `PATCH /lessons/{id}/transcript/utterances/{idx}` `{text}` — teacher
  урока (`backend/internal/httpapi/transcripts.go`, `handleEditUtterance`):
  пробелы схлопываются, пустой/NUL/> 20 000 символов — 400, нет реплики —
  404 `no_utterance`, тот же текст — 200 без изменений. Store:
  `EditUtterance` — текст, `edited_at`/`edited_by` и `vtt_render`
  (`jobs.EnqueueOnce`, через минуту, одна джоба на пачку правок) одной
  транзакцией. Новых kind и ключей `.env` нет; VTT перезаписывается в тот
  же `transcripts/{lesson_id}.vtt`.
- `GET /lessons/{id}/transcript`: у реплики добавились `speaker_name`
  и `edited_at` (`store.Utterances` — одна выборка для вьюера и рендера).
- Web: `web/app/lesson/[id]/lesson-replay.tsx` (плеер + транскрипт одной
  секцией в фазе done), `transcript-panel.tsx` (блоки по спикерам, клик —
  seek и play, подсветка по `currentTime`, прокрутка списка, правка
  реплики и маппинг для teacher), `utterance-text.tsx` (`UtteranceText` —
  единственный рендер текста реплики, место для `<ClickableText>` S3-1);
  `lesson-player.tsx` отдаёт ref на `<video>` и позицию; типы `Utterance`,
  `Transcript` в `web/lib/api.ts`. Решения — ADR-025.
- Тесты: `TestUtteranceEdit` (доступ, валидация, отметка и автор,
  одна джоба на две правки, повтор, VTT с правкой в fake S3),
  `TestUtteranceEditsMigrationReverts`; браузерный смоук (Chromium,
  Plyr, клик-seek, подсветка, прокрутка списка) — вне CI.

### S2-5 Маппинг спикеров (2026-10-26)

- `PUT /lessons/{id}/transcript/speakers/{label}` `{user_id: N | null}` —
  teacher урока (`handleMapSpeaker`, store `MapSpeaker`): все реплики
  метки получают `utterances.user_id`; человек — teacher урока или
  участник снапшота, иначе 400 `not_participant`; нет метки — 404
  `no_speaker`; `null` снимает маппинг; повтор — 200.
- Вьюер показывает `speaker_name` вместо метки, селект «Кто говорит»
  с первой репликой каждой метки — у преподавателя урока.
- VTT несёт метки, маппинг его не перерендеривает — долг D-16.
- Тест: `TestSpeakerMapping` (доступ, посторонние и несуществующие люди,
  неизвестная метка, имена у участника, снятие маппинга, очередь пуста).

## Риски этапа

RU/EN code-switching и перекрывающаяся речь портят текст — принято
([07-risks.md](../architecture/07-risks.md)), компенсация — правка в S2-4;
задержка ASR на длинных записях — статус processing честно показывает
ожидание.

-- Этап 2 (S2-1…S2-3): транскрипты AssemblyAI — transcripts и utterances.
-- Скетч: docs/architecture/03-data-model.md; цепочка джоб и статусы —
-- ADR-015, обработка сбоев — ADR-016.

BEGIN;

CREATE TABLE transcripts (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- Один транскрипт на урок: повторная джоба не заведёт второй
    lesson_id   bigint      NOT NULL UNIQUE REFERENCES lessons (id) ON DELETE CASCADE,
    provider    text        NOT NULL CHECK (provider IN ('assemblyai')),
    -- queued — ждёт отправки (asr_submit); submitted — у провайдера, ждём
    -- вебхук; fetching — вебхук пришёл, забираем результат (asr_result);
    -- completed — utterances в базе; error — транскрипта не будет
    status      text        NOT NULL DEFAULT 'queued'
                CHECK (status IN ('queued', 'submitted', 'fetching', 'completed', 'error')),
    -- id транскрипта у провайдера: по нему вебхук находит строку
    external_id text,
    vtt_s3_key  text,
    -- Причина последнего сбоя — для разбора, наружу api её не отдаёт
    error       text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT transcripts_external_id_unique UNIQUE (provider, external_id),
    -- После отправки id провайдера есть всегда, иначе вебхуку нечем найти
    -- строку; error бывает и до отправки
    CONSTRAINT transcripts_sent_has_external_id
        CHECK (status IN ('queued', 'error') OR external_id IS NOT NULL),
    CONSTRAINT transcripts_error_has_reason CHECK (status <> 'error' OR error IS NOT NULL),
    -- VTT рендерится только из сохранённых реплик
    CONSTRAINT transcripts_vtt_after_completed CHECK (vtt_s3_key IS NULL OR status = 'completed')
);

CREATE TABLE utterances (
    transcript_id bigint  NOT NULL REFERENCES transcripts (id) ON DELETE CASCADE,
    -- Номер реплики в транскрипте с нуля. Первичный ключ (transcript_id, idx)
    -- не даёт повторной доставке вебхука задвоить реплики
    idx           integer NOT NULL CHECK (idx >= 0),
    -- Метка спикера от провайдера: A, B, C…
    speaker_label text    NOT NULL,
    -- Ручной маппинг A → участник урока (S2-5)
    user_id       bigint  REFERENCES users (id) ON DELETE SET NULL,
    start_ms      integer NOT NULL CHECK (start_ms >= 0),
    end_ms        integer NOT NULL,
    text          text    NOT NULL,
    PRIMARY KEY (transcript_id, idx),
    CONSTRAINT utterances_time_order CHECK (end_ms >= start_ms)
);

-- Колонка lessons.transcript_id ждала этой таблицы с 0001 (ADR-005)
ALTER TABLE lessons ADD CONSTRAINT lessons_transcript_fk
    FOREIGN KEY (transcript_id) REFERENCES transcripts (id) ON DELETE SET NULL;

COMMIT;

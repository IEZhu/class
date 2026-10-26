-- Ревью раунда 1, F1 (ADR-031): запись урока — набор сегментов. Каждый
-- запуск Egress пишет свой файл; egress_ended закрывает сегмент, а не урок.
-- lessons.recording_s3_key остаётся выбранной записью — её играет плеер
-- и получает ASR.

BEGIN;

CREATE TABLE lesson_recordings (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    lesson_id  bigint      NOT NULL REFERENCES lessons (id) ON DELETE CASCADE,
    -- id Egress у LiveKit: по нему egress_ended находит сегмент
    egress_id  text        NOT NULL UNIQUE,
    -- sid сессии комнаты LiveKit: новая сессия той же комнаты пишется
    -- новым сегментом, даже если egress_ended прошлой ещё не дошёл.
    -- Пусто — LiveKit sid не прислал
    room_sid   text        NOT NULL DEFAULT '',
    -- Путь в бакете: при запуске — тот, что мы задали, после egress_ended —
    -- тот, что LiveKit записал. NULL — сегмент без файла
    s3_key     text        UNIQUE,
    -- active — Egress пишет; complete — файл в бакете; failed — файла нет
    -- или он битый (EGRESS_FAILED/ABORTED, пустой результат, egress_ended
    -- не пришёл)
    status     text        NOT NULL DEFAULT 'active'
               CHECK (status IN ('active', 'complete', 'failed')),
    size_bytes bigint      CHECK (size_bytes >= 0),
    -- Причина сбоя — для разбора, наружу api её не отдаёт
    error      text,
    created_at timestamptz NOT NULL DEFAULT now(),
    ended_at   timestamptz,
    CONSTRAINT lesson_recordings_complete_has_key CHECK (status <> 'complete' OR s3_key IS NOT NULL),
    CONSTRAINT lesson_recordings_failed_has_reason CHECK (status <> 'failed' OR error IS NOT NULL),
    CONSTRAINT lesson_recordings_ended_at CHECK ((status = 'active') = (ended_at IS NULL))
);

CREATE INDEX lesson_recordings_lesson_idx ON lesson_recordings (lesson_id);

COMMIT;

-- S2-4 (ADR-025): правка текста реплики преподавателем — кто и когда
-- исправил ASR. До первой правки обе колонки пусты: текст — как его отдал
-- провайдер.

BEGIN;

ALTER TABLE utterances
    ADD COLUMN edited_at timestamptz,
    -- Как у utterances.user_id (0008): удалённая учётка не стирает правку
    ADD COLUMN edited_by bigint REFERENCES users (id) ON DELETE SET NULL;

-- Автор правки бывает только у исправленной реплики
ALTER TABLE utterances ADD CONSTRAINT utterances_editor_has_time
    CHECK (edited_by IS NULL OR edited_at IS NOT NULL);

COMMIT;

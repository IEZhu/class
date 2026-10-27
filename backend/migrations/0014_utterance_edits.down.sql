-- Откат S2-4: отметки правок исчезают, исправленный текст остаётся —
-- исходного текста провайдера в базе нет, вернуть его может только
-- повторная транскрибация (ADR-016).

BEGIN;

ALTER TABLE utterances DROP CONSTRAINT utterances_editor_has_time;
ALTER TABLE utterances
    DROP COLUMN edited_by,
    DROP COLUMN edited_at;

COMMIT;

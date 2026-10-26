-- Откат S2-1…S2-3: транскрипты и реплики исчезают, lessons.transcript_id
-- снова без FK и пуст, как до 0008. Урок, чья транскрибация шла, сразу
-- done — как в плейсхолдере S1-5 (ADR-013): иначе его некому вывести
-- из processing. Джобы транскрибации без своих строк не нужны.

BEGIN;

ALTER TABLE lessons DROP CONSTRAINT lessons_transcript_fk;
UPDATE lessons SET status = 'done' WHERE status = 'processing' AND transcript_id IS NOT NULL;
UPDATE lessons SET transcript_id = NULL WHERE transcript_id IS NOT NULL;
DELETE FROM jobs WHERE kind IN ('asr_submit', 'asr_result', 'vtt_render');
DROP TABLE utterances;
DROP TABLE transcripts;

COMMIT;

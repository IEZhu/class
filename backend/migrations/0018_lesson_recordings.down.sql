-- Откат F1 (ADR-031): сегменты исчезают, у урока остаётся только выбранная
-- запись lessons.recording_s3_key. Файлы остальных сегментов лежат
-- в бакете без ссылки. Уборка записей без таблицы не нужна.

BEGIN;

DELETE FROM jobs WHERE kind = 'recording_sweep';
DELETE FROM job_schedules WHERE kind = 'recording_sweep';
DROP TABLE lesson_recordings;

COMMIT;

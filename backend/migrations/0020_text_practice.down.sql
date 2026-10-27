-- Откат S5-7 и D-15 (ADR-035): леммы текстов и индекс уборки сессий
-- исчезают. Код до 0020 джобу session_cleanup не знает: её строка в очереди
-- ждала бы вечно, поэтому уходят и она, и расписание.

BEGIN;

DELETE FROM jobs WHERE kind = 'session_cleanup';
DELETE FROM job_schedules WHERE kind = 'session_cleanup';
DROP INDEX sessions_expires_idx;
ALTER TABLE generated_texts DROP COLUMN lemmas;

COMMIT;

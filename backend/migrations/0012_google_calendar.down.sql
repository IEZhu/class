-- Откат S1-1: токены преподавателей исчезают, календарь придётся
-- подключать заново. Ожидающие calendar_sync удаляются вместе с ними:
-- worker без S1-1 их не возьмёт, и они висели бы в очереди вечно.
-- lessons.gcal_event_id остаётся — колонка из 0001.

BEGIN;

DELETE FROM jobs WHERE kind = 'calendar_sync';
ALTER TABLE users DROP COLUMN google_refresh_token;

COMMIT;

-- Откат S3-6: доски исчезают вместе со сценами. Снимки в хранилище
-- (whiteboards/{lesson_id}.json) остаются — их удаляют руками, если нужно.
-- Ждущие снимки убираем из очереди: снимать им больше нечего.

BEGIN;

DELETE FROM jobs WHERE kind = 'whiteboard_snapshot';
DROP TABLE whiteboards;

COMMIT;

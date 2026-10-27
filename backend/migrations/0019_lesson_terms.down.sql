-- Откат S3-7/S3-8: кандидаты и словари уроков исчезают вместе с ждущими
-- разборами досок. Назначенные студентам слова остаются в user_terms:
-- это уже их личные словари.

BEGIN;

DELETE FROM jobs WHERE kind = 'whiteboard_terms';
DROP TABLE lesson_terms;
DROP TABLE lesson_term_clicks;
DROP TABLE lesson_term_candidates;

COMMIT;

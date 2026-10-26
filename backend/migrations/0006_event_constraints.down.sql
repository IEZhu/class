-- Откат 0006: возвращаем ограничения 0005. Данные не трогаем — всё, что
-- проходит строгие проверки, проходит и прежнюю.

BEGIN;

ALTER TABLE lessons DROP CONSTRAINT lessons_guest_link_event_only;
ALTER TABLE lessons DROP CONSTRAINT lessons_title_event_only;
ALTER TABLE lessons DROP CONSTRAINT lessons_named;
ALTER TABLE lessons ADD CONSTRAINT lessons_named
    CHECK (group_id IS NOT NULL OR title IS NOT NULL);

COMMIT;

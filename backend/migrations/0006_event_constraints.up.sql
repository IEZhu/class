-- Этап 1 (S1-11, ревью PR #11): правила открытого события (ADR-010)
-- держит сама БД, а не только api. 0005 уже применена на стенде, поэтому
-- ужесточение — отдельной миграцией.

BEGIN;

-- Событию без группы нужно непустое название: подписать его больше нечем.
-- Прежний lessons_named пропускал title = ''.
ALTER TABLE lessons DROP CONSTRAINT lessons_named;
ALTER TABLE lessons ADD CONSTRAINT lessons_named
    CHECK (group_id IS NOT NULL OR btrim(coalesce(title, '')) <> '');

-- У урока с группой названия нет: подпись берётся из группы, и чужой
-- title не должен её перебивать.
ALTER TABLE lessons ADD CONSTRAINT lessons_title_event_only
    CHECK (group_id IS NULL OR title IS NULL);

-- Гостевая ссылка — только у открытого события: иначе по ней в групповой
-- урок зашли бы посторонние.
ALTER TABLE lessons ADD CONSTRAINT lessons_guest_link_event_only
    CHECK (group_id IS NULL OR guest_token_hash IS NULL);

COMMIT;

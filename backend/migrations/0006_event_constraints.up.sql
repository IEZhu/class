-- Этап 1 (S1-11, ревью PR #11): правила открытого события (ADR-010)
-- держит сама БД, а не только api. 0005 уже применена на стенде, поэтому
-- ужесточение — отдельной миграцией.

BEGIN;

-- Сначала приводим строки стенда к новым правилам: код времён 0005
-- сохранял title у урока с группой и выдавал ему гостевую ссылку, а CHECK
-- из 0005 пропускал событие с пустым названием. Без этого ADD CONSTRAINT ниже
-- падает и оставляет версию 6 грязной (ревью раунда 1, ADR-032).
-- Подпись урока с группой и так берётся из группы — title лишний.
UPDATE lessons SET title = NULL WHERE group_id IS NOT NULL AND title IS NOT NULL;
-- Гостевая ссылка на урок с группой впускала посторонних (ADR-010):
-- отзываем её, гости, уже вошедшие по ней, остаются участниками.
UPDATE lessons SET guest_token_hash = NULL
    WHERE group_id IS NOT NULL AND guest_token_hash IS NOT NULL;
-- Событию без названия — служебное с номером, чтобы подпись в списках
-- не была пустой и события не сливались.
UPDATE lessons SET title = 'Событие #' || id
    WHERE group_id IS NULL AND btrim(coalesce(title, '')) = '';

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

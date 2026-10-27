-- Долги D-4 и D-5 (ADR-024): файлы материалов в R2 и архивация учёток
-- вместо удаления.

BEGIN;

-- D-4: материал несёт не больше одного файла. Колонка s3_key есть в схеме
-- с этапа 0, рядом встают метаданные, которые api сверяет с объектом
-- в хранилище. Ключ выдаёт api, клиент путь не выбирает.
ALTER TABLE materials
    ADD COLUMN file_name         text,
    ADD COLUMN file_content_type text,
    ADD COLUMN file_size         bigint,
    -- Загрузка подтверждена: объект найден и совпал по размеру и типу.
    -- До этого файл видит только преподаватель урока.
    ADD COLUMN file_uploaded_at  timestamptz,
    -- ETag объекта на момент подтверждения: перед каждой ссылкой на
    -- скачивание api сверяет с ним объект, подменённый в обход api
    ADD COLUMN file_etag         text;

-- Метаданные файла — только целиком и только вместе с ключом. IS NOT NULL
-- обязателен: сравнение NULL с '' даёт NULL, а CHECK с NULL проходит
ALTER TABLE materials ADD CONSTRAINT materials_file_complete CHECK (
    (s3_key IS NULL AND file_name IS NULL AND file_content_type IS NULL
        AND file_size IS NULL AND file_uploaded_at IS NULL AND file_etag IS NULL)
    OR (s3_key IS NOT NULL
        AND file_name IS NOT NULL AND file_name <> ''
        AND file_content_type IS NOT NULL AND file_content_type <> ''
        AND file_size IS NOT NULL AND file_size > 0
        -- подтверждение и ETag ставятся вместе
        AND (file_uploaded_at IS NULL) = (file_etag IS NULL)
        AND (file_etag IS NULL OR file_etag <> '')));

-- Раскладка 05-storage-s3.md: файл урока лежит под его префиксом, и ключ
-- чужого урока (или записи) в материал не попадёт даже ошибкой api
ALTER TABLE materials ADD CONSTRAINT materials_file_key_layout
    CHECK (s3_key LIKE 'materials/' || lesson_id::text || '/%');

-- Два материала на один объект: удаление одного стёрло бы файл другого
CREATE UNIQUE INDEX materials_s3_key_idx ON materials (s3_key)
    WHERE s3_key IS NOT NULL;

-- D-5: учётка в архиве остаётся строкой — на неё ссылаются уроки,
-- участие и вся будущая история, — но войти не может и скрыта из списков.
ALTER TABLE users ADD COLUMN deactivated_at timestamptz;

COMMIT;

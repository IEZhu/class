-- Откат долгов D-4 и D-5. Архивированные учётки снова смогут войти:
-- отметка исчезает вместе с колонкой. Материалы с файлами остаются
-- текстовыми (название и body_md целы), ссылка на объект обнуляется —
-- иначе повторный up не прошёл бы materials_file_complete. Сами объекты
-- остаются в бакете под materials/{lesson_id}/, их чистят вручную.

BEGIN;

ALTER TABLE users DROP COLUMN deactivated_at;

DROP INDEX materials_s3_key_idx;
ALTER TABLE materials DROP CONSTRAINT materials_file_key_layout;
ALTER TABLE materials DROP CONSTRAINT materials_file_complete;
UPDATE materials SET s3_key = NULL WHERE s3_key IS NOT NULL;
ALTER TABLE materials
    DROP COLUMN file_name,
    DROP COLUMN file_content_type,
    DROP COLUMN file_size,
    DROP COLUMN file_uploaded_at,
    DROP COLUMN file_etag;

COMMIT;

-- Откат S5-8 (часть 1): кэш объяснений исчезает, счётчики объяснений
-- удаляются, llm_usage снова считает только генерации.

BEGIN;

DROP TABLE term_explanations;

DELETE FROM llm_usage WHERE kind <> 'generate';
ALTER TABLE llm_usage DROP CONSTRAINT llm_usage_pkey;
ALTER TABLE llm_usage DROP COLUMN kind;
ALTER TABLE llm_usage ADD PRIMARY KEY (user_id, day);

COMMIT;

-- Откат S5-6: сгенерированные тексты и счётчики лимита исчезают;
-- после повторного наката история генераций начинается с нуля.

BEGIN;

DROP TABLE llm_usage;
DROP TABLE generated_texts;

COMMIT;

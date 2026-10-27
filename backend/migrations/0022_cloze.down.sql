-- Откат S4-4/S4-5 и S5-8 (часть 2): пункты cloze, отрицательный кэш,
-- лексемы и индексы поиска по репликам и материалам, кэш
-- LLM-дистракторов и журнал ответов исчезают. Оценки, которые ответы cloze успели
-- записать в srs_reviews (source = 'cloze'), и сдвинутые ими карточки
-- остаются: это история графика FSRS, а не cloze.

BEGIN;

DROP TABLE cloze_attempts;
DROP TABLE cloze_distractors;
DROP INDEX materials_body_tsv_idx;
DROP INDEX utterances_text_tsv_idx;
ALTER TABLE materials DROP COLUMN body_tsv;
ALTER TABLE utterances DROP COLUMN text_tsv;
DROP TABLE cloze_misses;
DROP TABLE cloze_items;

COMMIT;

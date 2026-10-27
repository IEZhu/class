-- Откат S4-4/S4-5 и S5-8 (часть 2): пункты cloze, кэш LLM-дистракторов
-- и журнал ответов исчезают. Оценки, которые ответы cloze успели
-- записать в srs_reviews (source = 'cloze'), и сдвинутые ими карточки
-- остаются: это история графика FSRS, а не cloze.

BEGIN;

DROP TABLE cloze_attempts;
DROP TABLE cloze_distractors;
DROP TABLE cloze_items;

COMMIT;

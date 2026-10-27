-- Откат S4-1…S4-3: карточки FSRS и журнал оценок исчезают; личные
-- словари (user_terms) и их статусы остаются. После повторного наката
-- worker заведёт карточки заново — график начнётся с нуля.

BEGIN;

DROP TABLE srs_reviews;
DROP TABLE srs_cards;

COMMIT;

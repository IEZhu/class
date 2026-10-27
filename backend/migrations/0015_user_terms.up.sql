-- Этап 3 (S3-5): личный словарь — статус слова у пользователя (ADR-027).
-- Термин общий (terms, S3-2); здесь только личное состояние: статус
-- 1..5 (new..known) и урок, на котором слово попало в словарь.

BEGIN;

CREATE TABLE user_terms (
    user_id          bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- Без каскада: термины не удаляются, а слияние терминов (смена
    -- лемматизатора) обязано перенести личные статусы явно
    term_id          bigint      NOT NULL REFERENCES terms (id),
    -- 1 — новое, 5 — знаю (flows/word-click.md); по нему красит ClickableText
    status           smallint    NOT NULL DEFAULT 1 CHECK (status BETWEEN 1 AND 5),
    -- Урок, на котором слово добавили первым; отмена урока обнуляет
    -- источник, слово остаётся в словаре
    source_lesson_id bigint      REFERENCES lessons (id) ON DELETE SET NULL,
    added_at         timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    -- Одно слово у пользователя — одна строка; ключ — он же поиск статусов
    -- для раскраски (пользователь + термины видимого текста)
    PRIMARY KEY (user_id, term_id)
);

-- Страница словаря: новые сверху, курсор по (added_at, term_id)
CREATE INDEX user_terms_user_added_idx ON user_terms (user_id, added_at DESC, term_id DESC);

COMMIT;

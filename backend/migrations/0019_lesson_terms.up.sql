-- Этап 3 (S3-7, S3-8): словарь урока. Кандидаты собираются из трёх
-- источников — клики участников, текст на доске, ручной ввод
-- преподавателя (ADR-033); отмеченные преподавателем становятся словарём
-- урока, который назначается студентам в user_terms (ADR-034).

BEGIN;

CREATE TABLE lesson_term_candidates (
    -- Отмена урока убирает и его кандидатов: они ничьи, кроме урока
    lesson_id     bigint      NOT NULL REFERENCES lessons (id) ON DELETE CASCADE,
    -- Без каскада, как у user_terms: термины не удаляются
    term_id       bigint      NOT NULL REFERENCES terms (id),
    source        text        NOT NULL CHECK (source IN ('click', 'whiteboard', 'manual')),
    -- Форма слова, в которой кандидат встретился первым (Running, ran)
    surface       text        NOT NULL,
    -- click — сколько разных участников кликнули слово (lesson_term_clicks),
    -- whiteboard — вхождения на доске при последнем разборе сцены, manual — 1
    count         integer     NOT NULL DEFAULT 1 CHECK (count > 0),
    -- Кто завёл кандидата: первый кликнувший или преподаватель; у доски — NULL
    added_by      bigint      REFERENCES users (id) ON DELETE SET NULL,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    -- Одно слово урока — одна строка на источник: счётчик растёт upsert'ом
    PRIMARY KEY (lesson_id, term_id, source)
);

-- Кто из участников уже кликнул слово на уроке: счётчик кандидата
-- source='click' растёт на одного человека, а не на каждый клик (ADR-033)
CREATE TABLE lesson_term_clicks (
    lesson_id bigint NOT NULL REFERENCES lessons (id) ON DELETE CASCADE,
    term_id   bigint NOT NULL REFERENCES terms (id),
    user_id   bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (lesson_id, term_id, user_id)
);

-- Подтверждённый преподавателем словарь урока (S3-8)
CREATE TABLE lesson_terms (
    lesson_id    bigint      NOT NULL REFERENCES lessons (id) ON DELETE CASCADE,
    term_id      bigint      NOT NULL REFERENCES terms (id),
    confirmed_by bigint      REFERENCES users (id) ON DELETE SET NULL,
    confirmed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (lesson_id, term_id)
);

COMMIT;

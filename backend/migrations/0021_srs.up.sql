-- Этап 4 (S4-1…S4-3): график повторений FSRS (ADR-036). Карточка —
-- одна на слово личного словаря и живёт, пока слово в словаре; оценки
-- карточек и cloze (S4-5) пишутся в один журнал srs_reviews. Карточки
-- словам, добавленным до этой миграции, заводит worker при старте
-- (store.BackfillSRSCards, идемпотентно).

BEGIN;

CREATE TABLE srs_cards (
    user_id         bigint           NOT NULL,
    term_id         bigint           NOT NULL,
    -- Следующий показ; у новой карточки — момент добавления слова
    due_at          timestamptz      NOT NULL,
    -- Память FSRS; 0 — повторений ещё не было (state = 'new')
    stability       double precision NOT NULL DEFAULT 0 CHECK (stability >= 0),
    difficulty      double precision NOT NULL DEFAULT 0 CHECK (difficulty >= 0),
    -- Интервал последнего планирования в днях; 0 — шаг обучения в минутах
    scheduled_days  integer          NOT NULL DEFAULT 0 CHECK (scheduled_days >= 0),
    reps            integer          NOT NULL DEFAULT 0 CHECK (reps >= 0),
    lapses          integer          NOT NULL DEFAULT 0 CHECK (lapses >= 0),
    state           text             NOT NULL DEFAULT 'new'
                    CHECK (state IN ('new', 'learning', 'review', 'relearning')),
    -- Оставшиеся шаги обучения go-fsrs до перехода в review
    remaining_steps integer          NOT NULL DEFAULT 0 CHECK (remaining_steps >= 0),
    last_review_at  timestamptz,
    created_at      timestamptz      NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, term_id),
    -- Карточка — часть слова словаря: удаление слова (и учётки) уносит
    -- её вместе с журналом, повторное добавление начинает график заново
    FOREIGN KEY (user_id, term_id) REFERENCES user_terms (user_id, term_id) ON DELETE CASCADE
);

-- Очередь повторений: due-карточки пользователя, самые просроченные первыми
CREATE INDEX srs_cards_user_due_idx ON srs_cards (user_id, due_at, term_id);

CREATE TABLE srs_reviews (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id     bigint      NOT NULL,
    term_id     bigint      NOT NULL,
    -- Ключ идемпотентности клиента (POST /reviews): повтор запроса
    -- не двигает график второй раз; NULL — оценка без клиентского ключа
    client_id   uuid,
    rating      text        NOT NULL CHECK (rating IN ('again', 'hard', 'good', 'easy')),
    source      text        NOT NULL CHECK (source IN ('card', 'cloze')),
    reviewed_at timestamptz NOT NULL,
    FOREIGN KEY (user_id, term_id) REFERENCES srs_cards (user_id, term_id) ON DELETE CASCADE,
    -- Ключ уникален в пределах пользователя: чужой ключ не мешает
    -- и не выдаёт чужих оценок
    CONSTRAINT srs_reviews_client_key UNIQUE (user_id, client_id)
);

-- История карточки и каскад удаления карточки
CREATE INDEX srs_reviews_card_idx ON srs_reviews (user_id, term_id, reviewed_at);

COMMIT;

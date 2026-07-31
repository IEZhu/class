# Модель данных

> Единственный источник правды по схеме. Миграции — golang-migrate в
> `backend/migrations/`, нумерация по этапам. Изменения схемы — только вместе
> с обновлением этого файла.
> DDL-конвенции (identity PK, timestamptz, text+CHECK, created_at) —
> [ADR-005](decisions.md).

## Скетч ключевых таблиц

```sql
users(id, email, role /*teacher|student*/, name, google_refresh_token /*только у teachers, шифруется*/)
groups(id, name, level /*CEFR*/);  group_members(group_id, user_id)

lessons(id, group_id, teacher_id, starts_at, ends_at, status
        /*scheduled|live|processing|done*/, gcal_event_id,
        livekit_room, recording_s3_key, transcript_id)
lesson_participants(lesson_id, user_id, attended bool)
materials(id, lesson_id, kind /*homework|material*/, title, body_md, s3_key)

-- очередь фоновых задач (этап 1)
jobs(id, kind, payload jsonb, run_at, attempts, last_error, locked_at, locked_by)

-- словарь
terms(id, lang, lemma, surface, translation_cache jsonb)
user_terms(user_id, term_id, status /*1..5: new..known*/, source_lesson_id, added_at)
lesson_term_candidates(lesson_id, term_id, source /*click|whiteboard|manual*/, added_by)
lesson_terms(lesson_id, term_id)          -- подтверждённый «словарь урока»

-- SRS (FSRS)
srs_cards(user_id, term_id, due_at, stability, difficulty,
          reps, lapses, state, last_review_at, PRIMARY KEY(user_id, term_id))
srs_reviews(id, user_id, term_id, rating /*again|hard|good|easy*/, reviewed_at, source /*card|cloze*/)

-- транскрипты
transcripts(id, lesson_id, provider, status, vtt_s3_key)
utterances(transcript_id, idx, speaker_label, user_id nullable /*ручной маппинг A→Иван*/,
           start_ms, end_ms, text)

-- индекс субтитров (практика-видео)
videos(id, source /*youtube|ted*/, external_id, title, channel, duration_s, lang, added_at)
subtitle_segments(video_id, idx, start_ms, end_ms, text,
                  tsv tsvector GENERATED /*to_tsvector('english', text)*/)
-- + GIN index on tsv
subtitle_sources(id, kind /*channel|playlist*/, external_id, title, enabled)  -- seed сборщика

-- корпус Tatoeba
tatoeba_sentences(id, lang, text, owner_skill smallint)
tatoeba_links(sentence_id, translation_id)

-- практика
generated_texts(id, group_id, level, genre, body_md, target_term_ids int[], model, created_at)
cloze_tests(id, source /*lesson|generated_text*/, source_id, term_ids int[])
cloze_items(test_id, idx, sentence, answer_term_id, distractor_term_ids int[])
cloze_attempts(id, test_id, user_id, answers jsonb, score, finished_at)

whiteboards(lesson_id, scene_s3_key, updated_at)   -- бэкап JSON-сцены Excalidraw
```

## Когда появляется каждая таблица

| Таблицы | Этап | Задача |
|---|---|---|
| users, groups, group_members, lessons, lesson_participants, materials | 0 | S0-2 |
| jobs; колонка users.google_refresh_token (шифрование) | 1 | S1-2 / S1-1 |
| transcripts, utterances | 2 | S2-2 |
| terms, user_terms, lesson_term_candidates, lesson_terms, whiteboards | 3 | S3-2…S3-8 |
| srs_cards, srs_reviews, cloze_tests, cloze_items, cloze_attempts | 4 | S4-1…S4-5 |
| videos, subtitle_segments, subtitle_sources, tatoeba_sentences, tatoeba_links, generated_texts | 5 | S5-1…S5-6 |

## Инварианты модели

- **`<ClickableText>`**: любой текст в системе (материал, транскрипт,
  сгенерированный текст, предложение Tatoeba, субтитр) рендерится одним
  компонентом, который токенизирует, красит слова по `user_terms.status`
  (1..5: new..known) и вешает клик-попап.
- `lessons.status` — единственный источник состояния урока:
  `scheduled → live → processing → done` (см.
  [flows/lesson-lifecycle.md](flows/lesson-lifecycle.md)).
- `terms` — общий словарь (лемма + кэш переводов); персональное состояние —
  только в `user_terms`; словарь конкретного урока — `lesson_terms` (после
  подтверждения преподавателем из `lesson_term_candidates`).
- `srs_cards` — по одной карточке на пару (user, term); cloze-результаты
  пишутся в `srs_reviews` c `source='cloze'` и влияют на тот же график FSRS.
- FTS: `subtitle_segments.tsv` — GENERATED tsvector + GIN-индекс; поиск
  для видео-практики только по нему.
- `users.google_refresh_token` хранится шифрованным (ключ в `.env`),
  есть только у teachers.

Связано: [04-api.md](04-api.md) · планы этапов [../plans/](../plans/)

# Модель данных

> Единственный источник правды по схеме. Миграции — golang-migrate в
> `backend/migrations/`, нумерация по этапам. Изменения схемы — только вместе
> с обновлением этого файла.
> DDL-конвенции (identity PK, timestamptz, text+CHECK, created_at) —
> [ADR-005](decisions.md).

## Скетч ключевых таблиц

```sql
users(id, email, role /*admin|teacher|student|guest, ADR-007/010*/, name,
      password_hash /*bcrypt*/,
      google_refresh_token /*bytea, только у teachers, AES-256-GCM, ADR-023*/,
      deactivated_at /*архив вместо удаления, ADR-024*/)
sessions(id, user_id, token_hash /*sha256*/, created_at, expires_at)  -- ADR-006
invites(id, token_hash /*sha256*/, email, name, role, group_id, created_by,
        created_at, expires_at, accepted_at, accepted_user_id)        -- ADR-008
groups(id, name, level /*CEFR*/);  group_members(group_id, user_id)

lessons(id, group_id /*NULL у открытого события, ADR-010*/,
        title /*непустой у события, NULL у урока с группой*/,
        teacher_id, starts_at, ends_at, status
        /*scheduled|live|processing|done*/,
        gcal_event_id /*id события назначаем сами до вызова Google, ADR-023*/,
        livekit_room, recording_s3_key, transcript_id,
        guest_token_hash /*sha256 гостевой ссылки, только у события*/)
lesson_participants(lesson_id, user_id, attended bool)
materials(id, lesson_id, kind /*homework|material*/, title, body_md,
          s3_key /*materials/{lesson_id}/…, ADR-024*/, file_name, file_content_type,
          file_size, file_uploaded_at /*загрузка подтверждена*/,
          file_etag /*ETag объекта при подтверждении*/)

-- очередь фоновых задач (этап 1); выполненная джоба удаляется (ADR-013)
jobs(id, kind, payload jsonb, run_at, attempts, last_error, locked_at, locked_by,
     failed_at /*попытки исчерпаны — «мёртвая»*/, created_at)
job_schedules(kind, slot /*период, за который джоба уже поставлена*/)  -- ADR-021

-- словарь
terms(id, lang /*en*/, lemma /*ключ, ADR-017*/, surface /*форма первого клика*/,
      translation_cache jsonb /*{"ru": {text, source, at}}*/, created_at,
      UNIQUE(lang, lemma))
-- личный словарь (0015, ADR-027): одна строка на (user_id, term_id);
-- источник — первый урок, отмена урока его обнуляет
user_terms(user_id, term_id, status /*1..5: new..known*/, source_lesson_id nullable,
           added_at, updated_at, PRIMARY KEY(user_id, term_id))
lesson_term_candidates(lesson_id, term_id, source /*click|whiteboard|manual*/, added_by)
lesson_terms(lesson_id, term_id)          -- подтверждённый «словарь урока»
-- словарный датасет из Wiktextract (S3-4, ADR-019); с terms связан по lemma
definitions(id, word, lemma /*lemma.Normalize(word), индекс*/, pos, etymology,
            glosses text[], translations_ru text[], source_line, import_id,
            UNIQUE(word, pos, etymology))
definition_imports(id, source, source_version, lines_done /*контрольная точка*/,
                   entries, finished_at, abandoned_at /*брошен -restart*/, created_at)

-- SRS (FSRS)
srs_cards(user_id, term_id, due_at, stability, difficulty,
          reps, lapses, state, last_review_at, PRIMARY KEY(user_id, term_id))
srs_reviews(id, user_id, term_id, rating /*again|hard|good|easy*/, reviewed_at, source /*card|cloze*/)

-- транскрипты (0008, ADR-015): один на урок; external_id — id у провайдера,
-- error — причина сбоя (ADR-016)
transcripts(id, lesson_id /*UNIQUE*/, provider /*assemblyai*/,
            status /*queued|submitted|fetching|completed|error*/,
            external_id, vtt_s3_key, error, created_at)
utterances(transcript_id, idx, speaker_label, user_id nullable /*ручной маппинг A→Иван*/,
           start_ms, end_ms, text, PRIMARY KEY(transcript_id, idx),
           edited_at, edited_by /*правка текста преподавателем, 0014, ADR-025*/)

-- индекс субтитров (практика-видео); состояние сборщика — ADR-022
videos(id, source /*youtube|ted*/, external_id, title, channel, duration_s, lang, added_at,
       source_id, published_at, subtitles /*manual|auto|none; NULL — не собраны*/,
       harvest_attempts, harvest_error, harvested_at)
subtitle_segments(video_id, idx, start_ms, end_ms, text,
                  tsv tsvector GENERATED /*to_tsvector('english', text)*/)
-- + GIN index on tsv
subtitle_sources(id, kind /*channel|playlist*/, external_id, title, enabled,
                 polled_at, last_error, created_at)  -- seed сборщика в 0011

-- корпус Tatoeba (S5-4, ADR-028): английские предложения с русским переводом;
-- id — id предложения в Tatoeba (ссылка атрибуции, upsert импорта)
tatoeba_sentences(id, lang /*en|ru*/, text, author /*CC BY: имя автора, '' — без владельца*/,
                  lemmas text[] /*только en: леммы internal/lemma, GIN*/, import_id)
tatoeba_links(en_id, ru_id, import_id, PRIMARY KEY(en_id, ru_id))
tatoeba_imports(id, source_version, phase /*ru|en*/, lines_done /*контрольная точка*/,
                finished_at, abandoned_at /*брошен -restart*/, created_at)

-- практика
-- текст LLM по целевым словам (0017, ADR-030): только прошедший валидатор
generated_texts(id, user_id, level, genre, max_words, target_lemmas text[] /*= terms.lemma*/,
                body /*абзацы без разметки*/, word_count, coverage, occurrences jsonb,
                unknown_share, attempts, provider, model, created_at)
llm_usage(user_id, day /*UTC*/, requests)   -- суточный лимит генераций (ADR-029)
cloze_tests(id, source /*lesson|generated_text*/, source_id, term_ids int[])
cloze_items(test_id, idx, sentence, answer_term_id, distractor_term_ids int[])
cloze_attempts(id, test_id, user_id, answers jsonb, score, finished_at)

whiteboards(lesson_id, scene_s3_key, updated_at)   -- бэкап JSON-сцены Excalidraw
           /* + room_id, room_key, scene jsonb, scene_version, snapshot_version,
              created_at (ADR-020) */
```

## Когда появляется каждая таблица

| Таблицы | Этап | Задача |
|---|---|---|
| users, groups, group_members, lessons, lesson_participants, materials | 0 | S0-2 |
| sessions; колонка users.password_hash | 0 | S0-3 |
| invites | 1 | S1-10 |
| колонки lessons.title, guest_token_hash; роль guest; CHECK правил события (0006) | 1 | S1-11 |
| jobs (миграция `0007_jobs`); колонка users.google_refresh_token (миграция `0012_google_calendar`, ADR-023) | 1 | S1-2 / S1-1 |
| колонки файла materials.file_*, users.deactivated_at (`0013_material_files_archive`) | 1 | долги D-4, D-5 |
| transcripts, utterances (миграция `0008_transcripts`); FK lessons.transcript_id | 2 | S2-1…S2-3 |
| колонки utterances.edited_at, edited_by (миграция `0014_utterance_edits`) | 2 | S2-4 |
| terms, definitions, definition_imports (миграция `0009_dictionary`) | 3 | S3-2, S3-4 |
| user_terms (миграция `0015_user_terms`), lesson_term_candidates, lesson_terms, whiteboards (миграция `0010_whiteboards`) | 3 | S3-5…S3-8 |
| srs_cards, srs_reviews, cloze_tests, cloze_items, cloze_attempts | 4 | S4-1…S4-5 |
| videos, subtitle_segments, subtitle_sources, job_schedules (миграция `0011_subtitles`) | 5 | S5-1, S5-2 |
| tatoeba_sentences, tatoeba_links, tatoeba_imports (миграция `0016_tatoeba`) | 5 | S5-4 |
| generated_texts, llm_usage (миграция `0017_generated_texts`) | 5 | S5-6 |

## Инварианты модели

- **`<ClickableText>`**: любой текст в системе (материал, транскрипт,
  сгенерированный текст, предложение Tatoeba, субтитр) рендерится одним
  компонентом, который токенизирует, красит слова по `user_terms.status`
  (1..5: new..known) и вешает клик-попап.
- `lessons.status` — единственный источник состояния урока:
  `scheduled → live → processing → done` (см.
  [flows/lesson-lifecycle.md](flows/lesson-lifecycle.md)).
- Открытое событие — урок без группы (ADR-010): только у него есть
  `title` (непустой) и `guest_token_hash`; у урока с группой оба `NULL`,
  подпись берётся из группы. Держат `CHECK` `lessons_named`,
  `lessons_title_event_only`, `lessons_guest_link_event_only` (0006).
- `terms` — общий словарь (лемма + кэш переводов), один термин на лемму:
  формы run, ran, running делят перевод; лемма — из `internal/lemma`
  (ADR-017). Дефиниции не копируются в terms, а ищутся в `definitions`
  по лемме при клике. Персональное состояние —
  только в `user_terms`; словарь конкретного урока — `lesson_terms` (после
  подтверждения преподавателем из `lesson_term_candidates`).
- `srs_cards` — по одной карточке на пару (user, term); cloze-результаты
  пишутся в `srs_reviews` c `source='cloze'` и влияют на тот же график FSRS.
- FTS: `subtitle_segments.tsv` — GENERATED tsvector + GIN-индекс; поиск
  для видео-практики только по нему.
- Корпус Tatoeba (ADR-028): у английского предложения `lemmas` непустой,
  у русского — `NULL` (`CHECK tatoeba_sentences_lemmas_en`); примеры
  ищутся по `lemmas @> ARRAY[лемма термина]` (GIN `WHERE lang = 'en'`)
  и показываются только через пару в `tatoeba_links` — русское
  предложение без пары, оставшееся от незавершённого импорта, не видно.
- `users.google_refresh_token` хранится шифрованным (AES-256-GCM, ключ
  `GOOGLE_TOKEN_KEY` в `.env`, id пользователя в associated data — ADR-023),
  есть только у teachers; NULL — календарь не подключён.
- Файл материала (ADR-024): метаданные `file_*` заполнены целиком вместе
  с `s3_key` или пусты (`CHECK materials_file_complete`, с явными
  `IS NOT NULL`: сравнение NULL в CHECK проходит), `file_uploaded_at`
  и `file_etag` ставятся вместе; ключ — только под `materials/{lesson_id}/`
  (`materials_file_key_layout`), один объект — один материал (уникальный
  индекс). Пока `file_uploaded_at` пуст, файл видит только преподаватель
  урока.
- Учётки не удаляются: `users.deactivated_at` закрывает вход, гасит сессии
  и скрывает из списков людей и снапшотов новых уроков (ADR-024).
- Транскрипт у урока один (`transcripts.lesson_id` UNIQUE,
  `lessons.transcript_id` — зеркало); реплика одна на
  `(transcript_id, idx)`, поэтому повторная доставка вебхука не
  задваивает `utterances`. Статус транскрипта меняют только условные
  переходы `store` (ADR-015); `vtt_s3_key` есть только у `completed`
  (CHECK в 0008).
- Текст реплики правит только teacher урока, правка ставит перерендер
  VTT; `utterances.user_id` — teacher урока или участник его снапшота,
  одна метка спикера — один человек (ADR-025).

Связано: [04-api.md](04-api.md) · планы этапов [../plans/](../plans/)

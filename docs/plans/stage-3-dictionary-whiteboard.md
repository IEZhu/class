# Этап 3 — Словарь, ClickableText, доска (оценка: 2 нед)

**Цель:** слова становятся кликабельными везде, у студента появляется личный
словарь, у урока — доска и post-lesson выбор слов преподавателем.

Доки: [клик по слову](../architecture/flows/word-click.md) ·
[перевод/дефиниции](../architecture/integrations/translation.md) ·
[жизненный цикл урока](../architecture/flows/lesson-lifecycle.md), шаги 4, 8

## Входные артефакты

| Артефакт | Откуда |
|---|---|
| Тексты для рендера: `materials.body_md` и `utterances` | S0-4, S2-2 |
| Транскрипт-вьюер (реплики оборачиваются в ClickableText) | S2-4 |
| compose (добавить libretranslate, excalidraw) | S0-1 |
| S3 + presigned (бэкап сцен досок) | S1-3 |
| `/lesson/{id}` (панель словаря урока, post-lesson экран) | S0-5 |
| Участники группы (адресаты назначения слов) | S0-2 |

## Задачи

### S3-1 Компонент `<ClickableText>`
Токенизация, раскраска по `user_terms.status` (1..5), клик-попап.
Обернуть все существующие тексты: материалы, транскрипт.
**DoD:** одно место рендера текста; в транскрипте и материалах слова кликаются.

### S3-2 terms + лемматизация + /terms/click
Таблица `terms` (lemma, surface, translation_cache); английская
лемматизация в Go (выбор библиотеки → ADR); `POST /terms/click
{surface, context_sentence}` с порядком источников из
[translation.md](../architecture/integrations/translation.md).
Блок «примеры Tatoeba» в ответе — заглушка (**долг D-1**, закроет S5-4);
кнопка «объясни» скрыта (**долг D-2**, закроет S5-8).
**DoD:** повторный клик по слову не ходит во внешние сервисы (кэш).

### S3-3 LibreTranslate в compose
`--load-only en,ru`, лимит threads, внутренняя сеть; вызов из api при
кэш-миссе.
**DoD:** перевод незнакомого слова < 1 c на стенде; RAM в пределах оценки.

### S3-4 Словарный датасет (дефиниции)
Импорт открытых дампов (kaikki.org / Wiktionary) в PG (worker-джоба или
разовый скрипт); дефиниция в попапе.
**DoD:** у частотных слов в попапе есть англ. дефиниция.

### S3-5 user_terms и личный словарь
`POST /user-terms` («в мой словарь», смена статуса 1..5,
`source_lesson_id`); страница словаря студента с фильтрами по статусу.
**DoD:** добавленное на уроке слово видно в словаре с источником-уроком.

### S3-6 Доска Excalidraw
excalidraw-complete в compose; вкладка доски в `/lesson/{id}` (live):
`?room=lesson-{id}`; бэкап JSON-сцены → `s3://…/whiteboards/{lesson_id}.json`
(таблица `whiteboards`).
**DoD:** двое рисуют одновременно; сцена переживает перезапуск контейнера.

### S3-7 Кандидаты словаря урока
`lesson_term_candidates`: клики участников на уроке (`source='click'`),
парсер JSON-сцены доски — элементы `type:"text"` (`source='whiteboard'`),
ручной ввод преподавателя (`source='manual'`).
**DoD:** после урока список кандидатов собран из всех трёх источников.

### S3-8 Post-lesson word picker
Экран преподавателя: чеклист кандидатов → подтверждение → `lesson_terms` →
назначение всем/выборочно → `user_terms(status=1)`.
Создание `srs_cards` здесь — **точка расширения для S4-1** (пока no-op,
долг D-3).
**DoD:** назначенные слова появились в словарях выбранных студентов.

## Выходные артефакты

| Артефакт | Потребитель |
|---|---|
| `<ClickableText>` | S4-3 (карточки), S5-5 (панель субтитров), S5-7 (тексты) |
| Лемматизация (en) | S5-2 (индексация), S5-6 (валидатор покрытия) |
| `terms` + кэш переводов + дефиниции | S4-3 (лицевая/обратная сторона карточек) |
| `user_terms` со статусами | S4-1 (инициализация карточек), S5-5 («забытые слова») |
| `lesson_terms` (словарь урока) | S4-4 (cloze по уроку) |
| Хук назначения слов в S3-8 | S4-1 (FSRS init + бэкфилл) |
| Доска + бэкап сцен | материалы урока |

## Definition of Done этапа

- [ ] S3-1…S3-8 закрыты в [INDEX](../tasks/README.md)
- [ ] Сквозной сценарий: клик слова в транскрипте → попап → в словарь → после урока преподаватель подтвердил слова → студенты получили
- [ ] Долги D-1, D-2, D-3 записаны в INDEX; «Факт» заполнен

## Факт (заполняется по ходу реализации)

### S3-2 terms + лемматизация + `POST /terms/click` (☑)

- Миграция `backend/migrations/0009_dictionary` (up/down): таблица
  `terms` (id, lang `en`, lemma, surface — форма первого клика,
  translation_cache jsonb `{"ru": {"text", "source", "at"}}`, created_at),
  уникальность и поиск по лемме — `terms_lang_lemma_key (lang, lemma)`;
  в той же миграции `definitions` и `definition_imports` (S3-4).
- Лемматизация — `backend/internal/lemma` (ADR-017): morphy поверх
  встроенных данных Open English WordNet 2025 (`oewn/*.tsv.gz`,
  атрибуция `oewn/LICENSE`, генератор `lemma/gen`); `lemma.Lemma`,
  `lemma.Normalize`, `lemma.IsWord`, `lemma.Known`. Точность на выборке
  `lemma/testdata/inflections.tsv` — 114/119 (95.8%), промахи — только
  неоднозначные формы (building, tired, bit…).
- Эндпоинт `POST /terms/click` (`internal/httpapi/terms.go`, store —
  `internal/store/terms.go`), любой вошедший, включая гостя события.
  Тело `{surface, context_sentence, lesson_id?}`; ответ `{term_id, lemma,
  surface, translation: {text, source} | null, definitions: [{word, pos,
  glosses[], translations_ru[]}], examples: []}`. Порядок источников —
  [translation.md](../architecture/integrations/translation.md): кэш
  термина → `definitions` (по лемме и по самой форме) → LibreTranslate
  при кэш-миссе (переводится лемма, таймаут 3 с) → запись в кэш.
  Неудача перевода не кэшируется, ответ — 200 с `translation: null`.
  400 — не одно слово / контекст длиннее 1000 символов / плохой
  lesson_id; 403/404 — чужой или несуществующий урок в `lesson_id`.
- Точки расширения: `examples` всегда `[]` (долг D-1, S5-4), кнопки
  «объясни» в ответе нет (долг D-2, S5-8, по `term_id` и
  `context_sentence`); `lesson_id` пока только проверяется — кандидатов
  урока пишет S3-7; статус слова (`user_terms`) добавит S3-5.
- Тесты: `internal/httpapi/terms_test.go` (фейк LibreTranslate на
  httptest: первый клик — один вызов и запись кэша, повторы в любой
  форме — ноль вызовов; сервис лежит/недоступен/не настроен — 200 и
  `translation: null`; 401/400/403/404), `internal/lemma/lemma_test.go`.

### S3-3 LibreTranslate в compose (◐)

- Клиент `backend/internal/translate` (`POST /translate`, JSON q/source/
  target/format, таймаут 3 с, тесты на httptest); api читает
  `LIBRETRANSLATE_URL`, пусто — перевод выключен (строка в логе на старте).
- `deploy/docker-compose.yml`: сервис `libretranslate`
  (`libretranslate/libretranslate:v1.9.6@sha256:1de2d705…bb32`, профиль
  `translate`, `LT_LOAD_ONLY=en,ru`, `LT_THREADS=1`, без веб-интерфейса
  и перевода файлов, `cpus: 1.5`, `mem_limit: 2g`, healthcheck
  `scripts/healthcheck.py`), разовый `libretranslate-models` (профиль
  `translate-setup`), том `lt_models`, сеть `translate` (`internal:
  true`) — в ней api и libretranslate, портов наружу нет (ADR-018).
- Новые ключи `.env`: `COMPOSE_PROFILES` (включить `translate`),
  `LIBRETRANSLATE_URL` (`http://libretranslate:5000`). Make-цель
  `translate-models`; запуск — [deploy/README.md](../../deploy/README.md).
- Образ вне блокирующего скана ADR-011 — долг D-10. Замер на одноразовом
  стенде на этом VPS: перевод незнакомого слова медиана 301 мс, максимум
  620 мс (20 слов), RAM ~376 MiB. **Осталось:** включить профиль на
  стенде (решение владельца, D-10) и повторить замер там.

### S3-4 Словарный датасет (◐)

- Таблицы `definitions` (word, lemma, pos, etymology, glosses[],
  translations_ru[], source_line, import_id; уникальность
  `definitions_entry_key (word, pos, etymology)`, индекс
  `definitions_lemma_idx`) и `definition_imports` (source,
  source_version, lines_done, entries, finished_at) — миграция 0009.
- Импорт — пакет `backend/internal/dictionary` и команда
  `backend/cmd/dictimport` (в образе worker'а, `/usr/local/bin/dictimport`;
  make-цель `dict-import`). Источник — JSONL Wiktextract с kaikki.org
  (по умолчанию `raw-wiktextract-data.jsonl.gz`), отбор и потолки —
  ADR-019; продолжение с контрольной точки, `-restart`, удаление
  устаревших статей только по успешному завершению.
- Тесты на фикстуре `internal/dictionary/testdata/wiktextract-sample.jsonl`:
  `internal/dictionary/entry_test.go`, `internal/httpapi/dictionary_import_test.go`
  (повтор — тот же словарь, gzip, продолжение после обрыва, смена версии
  источника, `-restart`, пустой и недокачанный источник, один импорт
  за раз, миграция up/down/up).
- Ожидаемый объём — 80–150 тыс. статей, 60–120 MB в PG (оценка
  по выборке первых 20 000 строк дампа, ADR-019). **Осталось:** полный
  импорт на стенде (`make dict-import`), после него — проверка DoD
  на частотных словах.

## Риски этапа

RAM LibreTranslate (лечится `--load-only`); качество лемматизации решает
слипание/дребезг терминов — выбрать библиотеку осознанно (ADR); парсер сцены
Excalidraw зависит от формата JSON — закрепить версией контейнера.

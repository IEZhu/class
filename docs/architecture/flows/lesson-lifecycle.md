# Поток: жизненный цикл урока

> Центральный сценарий платформы. Одна ссылка `/lesson/{id}` ведёт себя
> по-разному в зависимости от состояния (state machine по времени + статусу).

## State machine

```mermaid
stateDiagram-v2
    [*] --> scheduled: POST /lessons
    scheduled --> live: room_started/participant_joined в окне + запуск Egress
    live --> live: egress_ended до конца окна (закрыт сегмент)
    live --> processing: окно закрылось, запись есть
    live --> done: окно закрылось, записи нет
    processing --> done: транскрипт готов
    done --> [*]
```

Окно комнаты — с `starts_at` − 15 мин до `ends_at` + 15 мин: только в нём
выдаётся токен и запускается запись. live фиксируется только вместе
с успешным запуском Egress (ADR-012); без хранилища или при сорванном
запуске урок остаётся scheduled, а страница показывает фазу «идёт»
по времени (`web/lib/lesson-phase.ts`). Каждый запуск Egress — свой
сегмент записи; урок завершает `egress_ended` после окна или периодическая
уборка `recording_sweep`, запись урока — самый большой сегмент (ADR-031).

| Статус | Что показывает `/lesson/{id}` | Этап реализации |
|---|---|---|
| scheduled | материалы, домашка, время; событие уже в календарях | 0 (вид), 1 (календарь) |
| live | кнопка «Войти в класс» (LiveKit), вкладка доски, панель «словарь урока» | 1 (комната), 3 (доска, словарь) |
| processing | «запись обрабатывается» | 1–2 |
| done | плеер записи + транскрипт по спикерам + словарь урока + материалы | 1 (плеер), 2 (транскрипт), 3 (словарь) |

## Шаги

1. Преподаватель создаёт урок (группа, дата, время) → `POST /lessons`. *(этап 0)*
2. worker создаёт событие в Google Calendar: `events.insert`,
   `attendees` = email участников, `sendUpdates: all`,
   description = `https://platform/lesson/{id}`. Событие прилетает в личные
   календари студентов автоматически. Перенос → `events.update`,
   отмена → `events.delete`. *(этап 1)*
3. Преподаватель прикрепляет домашку/материалы; до старта страница показывает
   их. *(этап 0)*
4. В момент урока — кнопка «Войти в класс»: api выдаёт LiveKit access token,
   комната `lesson-{id}`. Рядом — вкладка доски: редактор Excalidraw,
   комната ретранслятора со случайным именем, имя и ключ комнаты — от api
   только участникам (ADR-020), — и панель «словарь урока»: клики по словам
   из материалов летят в `lesson_term_candidates`. *(этапы 1, 3)*
5. При старте комнаты api запускает LiveKit Egress: room composite →
   сегмент `s3://…/recordings/{id}/<время>-<случайное>.mp4`; экономный
   вариант — audio-only. Комната опустела посреди урока — сегмент закрыт,
   урок идёт дальше, следующий вход пишет новый сегмент. *(этап 1)*
6. Окно урока закрылось (`egress_ended` после него или уборка
   `recording_sweep`, которая сначала останавливает Egress, ещё пишущий
   после окна) → самый большой сегмент в `recording_s3_key`,
   `status=processing` + джоба `recording_ready` (записи нет — сразу
   `done`) → worker: строка `transcripts` и джоба `asr_submit` →
   presigned GET на запись → AssemblyAI (`speaker_labels: true`,
   `language_code: en`, webhook). *(этапы 1–2; без `ASSEMBLYAI_API_KEY`
   `recording_ready` сразу ставит `done`, ADR-013, ADR-015)*
7. Webhook AssemblyAI (потерян — через 2 ч сверка `asr_poll` спросит итог
   сама, ADR-016) → джоба `asr_result` сохраняет `utterances` → джоба
   `vtt_render` рендерит WebVTT (`<v A>…`) в S3 → `status=done`. Сбой
   транскрибации — урок всё равно `done`, транскрипт `error` (ADR-016).
   *(этап 2)*
8. **Post-lesson flow (выбор слов).** Преподавателю — чеклист кандидатов:
   клики за урок + текст-элементы из JSON-сцены Excalidraw (`type: "text"`) +
   ручной ввод. Отмеченные → `lesson_terms` → назначение всем/выборочно:
   `user_terms(status=1)` + `srs_cards` (FSRS init; появляется на этапе 4,
   до этого — только user_terms, см. долг D-3). Доску разбирает джоба
   `whiteboard_terms`, поставленная переходом урока из live, или кнопка
   преподавателя; назначение не понижает статус слова, которое студент
   уже учит (ADR-033, ADR-034). *(этапы 3–4)*
9. Та же ссылка `/lesson/{id}` показывает: плеер записи + скользящий
   транскрипт по спикерам (клик по реплике = seek) + словарь урока +
   материалы. Спикеров A/B/C преподаватель одним селектом маппит на имена
   (`utterances.user_id`), ошибки ASR правит в тексте реплики — правка
   перерендеривает VTT (ADR-025). *(этапы 1–3)*

Связано: [../04-api.md](../04-api.md) ·
[../integrations/livekit.md](../integrations/livekit.md) ·
[../integrations/assemblyai.md](../integrations/assemblyai.md) ·
планы [stage-1](../../plans/stage-1-calendar-livekit.md),
[stage-2](../../plans/stage-2-transcripts.md),
[stage-3](../../plans/stage-3-dictionary-whiteboard.md)

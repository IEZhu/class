# Планы этапов

Начни с правил конвейера: **[00-flow.md](00-flow.md)** — как этапы передают
артефакты друг другу. Текущие статусы задач — в
[../tasks/README.md](../tasks/README.md).

Карточки этого уровня:

| Этап | План | Оценка |
|---|---|---|
| — | [00-flow.md](00-flow.md) — конвейер артефактов, правила работы, шаблон плана | — |
| 0 | [stage-0-skeleton.md](stage-0-skeleton.md) — compose, auth, CRUD, `/lesson/{id}` state machine | 1–2 нед |
| 1 | [stage-1-calendar-livekit.md](stage-1-calendar-livekit.md) — календарь, комнаты, записи в S3, плеер | 1–2 нед |
| 2 | [stage-2-transcripts.md](stage-2-transcripts.md) — AssemblyAI, utterances, VTT, вьюер | 1 нед |
| 3 | [stage-3-dictionary-whiteboard.md](stage-3-dictionary-whiteboard.md) — ClickableText, словарь, доска | 2 нед |
| 4 | [stage-4-srs.md](stage-4-srs.md) — FSRS, карточки, cloze | 1–2 нед |
| 5 | [stage-5-content.md](stage-5-content.md) — сборщик субтитров, Tatoeba, LLM, практика | 2–3 нед |

Каждый план: Входные артефакты → Задачи с DoD → Выходные артефакты →
DoD этапа → «Факт» (заполняется по ходу) → Риски.

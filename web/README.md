# web — Next.js (SSR/React)

Код появляется на этапе 0 (S0-5). UI платформы: уроки, плеер, словарь,
практика, обёртка доски.

## Планируемые страницы и компоненты

```text
страницы:
  /login                вход (этап 0)
  /lessons              список уроков по роли (этап 0)
  /lesson/[id]          state machine: scheduled | live | processing | done (этап 0)
                        + LiveKit embed (1), плеер Plyr (1), транскрипт (2),
                        + доска (3), словарь урока и post-lesson picker (3)
  /dictionary           личный словарь студента (этап 3)
  /practice             очередь карточек (4), cloze (4), видео (5), тексты (5)

ключевые компоненты:
  <ClickableText>       ЕДИНСТВЕННЫЙ рендер любого текста: токенизация,
                        раскраска по user_terms.status, клик-попап (этап 3)
  <Player>              Plyr по presigned GET, Range-перемотка (этап 1)
  <TranscriptView>      реплики по спикерам, клик=seek (этап 2)
  <SubtitlePanel>       панель субтитров видео-практики с подсветкой (этап 5)
```

Состояния `/lesson/[id]`: `docs/architecture/flows/lesson-lifecycle.md`.
Инвариант ClickableText: `docs/architecture/03-data-model.md`.

# Интеграция: корпус Tatoeba

> Этап 5 (S5-4, [ADR-028](../decisions.md)). Примеры употребления в попапе
> клика по слову ([../flows/word-click.md](../flows/word-click.md)), дальше —
> тексто-практика (S5-7) и cloze.

## Источник

- Еженедельные выгрузки [tatoeba.org/downloads](https://tatoeba.org/downloads)
  (обновляются по субботам в 6:30 UTC), поязыковые файлы:
  - `https://downloads.tatoeba.org/exports/per_language/eng/eng-rus_links.tsv.bz2` —
    пары переводов: `id английского [tab] id русского`;
  - `https://downloads.tatoeba.org/exports/per_language/eng/eng_sentences_detailed.tsv.bz2`
    и `…/rus/rus_sentences_detailed.tsv.bz2` — предложения:
    `id [tab] язык (eng|rus) [tab] текст [tab] автор [tab] добавлено [tab] изменено`;
    `\N` вместо автора — у предложения нет владельца.
- Объём выгрузки 2026-10-24: 5 + 35 + 24 MB в bzip2; 832 тыс. пар,
  546 тыс. английских предложений с русским переводом.
- Ключей и учёток нет; сеть нужна только команде импорта, клик ходит
  в локальную таблицу.

## Лицензия и подпись

Тексты — **CC BY 2.0 FR** (<https://creativecommons.org/licenses/by/2.0/fr/>),
часть ещё и CC0. По условиям использования Tatoeba предложение можно
использовать, только назвав автора. Поэтому импорт берёт подробные
выгрузки (в них имя автора), а `POST /terms/click` отдаёт у каждого
примера `attribution`: автор и страница `https://tatoeba.org/sentences/show/{id}`
у английского предложения и у перевода, `license`, `license_url`.
Попап (S3-1) подписывает пример по этим полям.

## Реализация (S5-4)

- Миграция `0016_tatoeba`: `tatoeba_sentences` (английские — с леммами
  под GIN), `tatoeba_links` (пары en → ru), `tatoeba_imports` (прогоны).
- Импорт — пакет `backend/internal/tatoeba`, команда
  `backend/cmd/tatoebaimport` в образе worker'а, make-цель
  `tatoeba-import`; отбор, потолки, продолжение после обрыва и уборка —
  ADR-028, запуск — [deploy/README.md](../../../deploy/README.md#примеры-tatoeba-импорт-корпуса-s5-4).
- Примеры клика — `store.Examples` (`backend/internal/store/tatoeba.go`):
  до двух предложений с леммой термина, кратчайшие из хотя бы трёх слов,
  с переводом с младшим id.

Связано: [translation.md](translation.md) ·
план [stage-5](../../plans/stage-5-content.md)

# Поток: фоновый сборщик субтитров

> Этап 5 (S5-1…S5-3). Offline-процесс worker'а: собирает готовые английские
> субтитры для видео-практики. Ничего не транскрибирует и не скачивает видео.

## Шаги

1. **Seed.** Кураторский список каналов/плейлистов в `subtitle_sources`
   (TED, TED-Ed, BBC Learning English, VOA Learning English, …).
2. **Список видео** канала — бесплатный RSS-фид канала
   (без квоты YouTube Data API).
3. **Субтитры**:
   `yt-dlp --skip-download --write-subs --write-auto-subs --sub-langs en`
   с низким рейтом: 1 видео / 15–30 с, jitter, backoff.
   Manual-субтитры приоритетнее auto.
4. **Парсинг VTT** → `subtitle_segments` (start_ms, end_ms, text);
   tsvector-колонка генерируется автоматически.
5. **TED** — разовый импорт готовых дампов транскриптов (S5-3).

## Митигация IP-блокировок YouTube

- Низкий рейт + jitter + экспоненциальный backoff, ретраи.
- Cookies при необходимости.
- Fallback: запуск сборщика с домашней машины — **тот же worker-бинарь**,
  пишет в ту же PG через VPN/Tailscale (архитектурно предусмотрено:
  сборщик не завязан на VPS-окружение).

Связано: [practice-engine.md](practice-engine.md) ·
[../03-data-model.md](../03-data-model.md) ·
план [stage-5](../../plans/stage-5-content.md)

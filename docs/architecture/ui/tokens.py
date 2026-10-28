"""Цвета интерфейса Lingua Class (ADR-040) и их контраст по WCAG 2.x.

Один источник для блока токенов --c-* в web/app/globals.css и для таблиц
контраста в docs/architecture/08-ui.md §2.2. Каждая пара текст/фон из
спецификации считается по формуле относительной яркости WCAG: текст —
не меньше 4.5:1, границы полей, фокус и прочие элементы интерфейса — 3:1.

Запуск (только стандартная библиотека Python 3):
  python3 docs/architecture/ui/tokens.py        # таблицы для 08-ui.md §2.2;
                                                # код выхода 1, если пара ниже порога
  python3 docs/architecture/ui/tokens.py css    # блок токенов для globals.css (раздел 1)

Цвет меняется только здесь и вместе с пересчётом: новое значение — в LIGHT
или DARK, прогон без FAIL, затем таблицы — в 08-ui.md, токены — в globals.css.
"""
import json
import sys

LIGHT = {
    "bg": "#f5f6fa", "surface": "#ffffff", "surface-2": "#eef1f6", "surface-3": "#e3e7ef",
    "border": "#dde2eb", "border-strong": "#c4cbd8", "border-input": "#858ea1",
    "text": "#1a2030", "text-muted": "#576074", "text-disabled": "#8a92a3",
    "primary": "#4b45d6", "primary-hover": "#3d37bd", "primary-soft": "#ecebfd", "primary-strong": "#3a33b5",
    "on-primary": "#ffffff", "on-danger": "#ffffff", "focus": "#4b45d6",
    "success": "#17803d", "success-soft": "#e6f5ec", "success-text": "#14692f",
    "danger": "#c23030", "danger-soft": "#fdecec", "danger-text": "#a82424",
    "warning-soft": "#fff4dc", "warning-text": "#8a4b00",
    "info-soft": "#e8f1fd", "info-text": "#1d56a8",
    "live": "#17803d", "live-soft": "#e6f5ec",
    "hl-1": "#ffd43b", "hl-2": "#ffe168", "hl-3": "#ffec9e", "hl-4": "#fff6cf", "hl-text": "#1a2030",
    "kbd-bg": "#ffffff", "kbd-border": "#c4cbd8", "kbd-text": "#3b4356",
    "sidebar": "#ffffff", "nav-active": "#ecebfd", "nav-active-text": "#3a33b5",
    "overlay": "rgba(16,20,32,.45)", "media-bg": "#0f172a", "media-fg": "#ffffff", "media-scrim": "rgba(0,0,0,.6)",
}
DARK = {
    "bg": "#0e1220", "surface": "#161b2b", "surface-2": "#1e2538", "surface-3": "#283047",
    "border": "#2b3349", "border-strong": "#3a4560", "border-input": "#7a849c",
    "text": "#e8ebf3", "text-muted": "#a6aec0", "text-disabled": "#6f788d",
    "primary": "#a5a8ff", "primary-hover": "#bfc1ff", "primary-soft": "#2a2c5a", "primary-strong": "#c9cbff",
    "on-primary": "#12142e", "on-danger": "#2a0a0a", "focus": "#a5a8ff",
    "success": "#4ad08a", "success-soft": "#143524", "success-text": "#7fe3ad",
    "danger": "#ff8a80", "danger-soft": "#3d1a1d", "danger-text": "#ffb4ab",
    "warning-soft": "#3a2a0d", "warning-text": "#ffcf7a",
    "info-soft": "#152a48", "info-text": "#9cc4ff",
    "live": "#4ad08a", "live-soft": "#143524",
    "hl-1": "#ffd43b", "hl-2": "#ffe168", "hl-3": "#ffec9e", "hl-4": "#fff6cf", "hl-text": "#1a2030",
    "kbd-bg": "#1e2538", "kbd-border": "#3a4560", "kbd-text": "#c9cfdc",
    "sidebar": "#121726", "nav-active": "#2a2c5a", "nav-active-text": "#c9cbff",
    "overlay": "rgba(0,0,0,.6)", "media-bg": "#05070d", "media-fg": "#ffffff", "media-scrim": "rgba(0,0,0,.6)",
}

# (цвет текста или линии, фон, вид, где встречается); вид: text — 4.5:1, ui — 3:1
PAIRS = [
    ("text", "bg", "text", "основной текст на фоне страницы"),
    ("text", "surface", "text", "основной текст на карточках, в попапе и листах"),
    ("text", "surface-2", "text", "текст на светлой заливке (шапка таблицы, строка под курсором, дорожка сегментов)"),
    ("text-muted", "bg", "text", "второстепенный текст, строки сведений на фоне страницы"),
    ("text-muted", "surface", "text", "второстепенный текст, время, подсказки на карточках"),
    ("text-muted", "surface-2", "text", "второстепенный текст на светлой заливке"),
    ("primary", "surface", "text", "ссылки и прозрачные кнопки на карточках"),
    ("primary", "bg", "text", "ссылки на фоне страницы"),
    ("on-primary", "primary", "text", "надпись основной кнопки"),
    ("on-primary", "primary-hover", "text", "надпись основной кнопки под курсором и при нажатии"),
    ("primary-strong", "primary-soft", "text", "выбранный чип, сегмент, фильтр"),
    ("nav-active-text", "nav-active", "text", "текущий пункт навигации"),
    ("text", "sidebar", "text", "пункт навигации"),
    ("text-muted", "sidebar", "text", "подпись пункта нижней навигации (12px)"),
    ("primary", "sidebar", "text", "подпись текущего пункта нижней навигации"),
    ("success-text", "success-soft", "text", "сообщение об успехе, «Верно», статус «Идёт сейчас»"),
    ("success-text", "surface", "text", "итог формы «сохранено»"),
    ("danger-text", "danger-soft", "text", "сообщение об ошибке, неверный ответ"),
    ("danger-text", "surface", "text", "ошибка поля"),
    ("on-danger", "danger", "text", "надпись опасной кнопки"),
    ("warning-text", "warning-soft", "text", "предупреждение (о записи урока, «не загружено»)"),
    ("info-text", "info-soft", "text", "сведения, статус «Запланирован»"),
    ("text", "surface-3", "text", "нейтральный статус («Прошёл», «Завершён»)"),
    ("hl-text", "hl-1", "text", "подсветка слова со статусом 1 «новое»"),
    ("hl-text", "hl-2", "text", "подсветка слова со статусом 2 «узнаю»"),
    ("hl-text", "hl-3", "text", "подсветка слова со статусом 3 «учу»"),
    ("hl-text", "hl-4", "text", "подсветка слова со статусом 4 «почти знаю»"),
    ("kbd-text", "kbd-bg", "text", "подсказка клавиши"),
    ("border-input", "surface", "ui", "граница поля, списка, флажка (WCAG 1.4.11)"),
    ("border-input", "bg", "ui", "граница поля на фоне страницы"),
    ("focus", "surface", "ui", "кольцо фокуса на карточках"),
    ("focus", "bg", "ui", "кольцо фокуса на фоне страницы"),
    ("primary", "surface-3", "ui", "заполнение полосы прогресса против дорожки"),
    ("live", "surface", "ui", "точка «Идёт сейчас», полоса звучащей реплики"),
    ("media-fg", "media-bg", "text", "время и кнопки плеера на фоне видео"),
]


def lum(hexc):
    h = hexc.lstrip("#")
    r, g, b = (int(h[i:i + 2], 16) / 255 for i in (0, 2, 4))
    f = lambda v: v / 12.92 if v <= 0.03928 else ((v + 0.055) / 1.055) ** 2.4
    return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)


def ratio(a, b):
    l1, l2 = sorted((lum(a), lum(b)), reverse=True)
    return (l1 + 0.05) / (l2 + 0.05)


def table(theme, name):
    rows, bad = [], 0
    for fg, bg, kind, where in PAIRS:
        fgv = theme[fg]
        r = ratio(fgv, theme[bg])
        need = 4.5 if kind == "text" else 3.0
        ok = r >= need
        bad += not ok
        rows.append((fg, fgv, bg, theme[bg], round(r, 2), need, "да" if ok else "НЕТ", where))
    return rows, bad


def css():
    out = ["/* Цвета Lingua Class (docs/architecture/ui/tokens.py css) */", ":root,", ".light-island,", ".excalidraw {", "  color-scheme: light;"]
    out += [f"  --c-{k}: {v};" for k, v in LIGHT.items()]
    out.append("}")
    dark = [f"    --c-{k}: {v};" for k, v in DARK.items()]
    out += ["@media (prefers-color-scheme: dark) {", "  :root:not([data-theme=\"light\"]) {", "    color-scheme: dark;", *dark, "  }", "}",
            ":root[data-theme=\"dark\"] {", "  color-scheme: dark;", *[d[2:] for d in dark], "}"]
    return "\n".join(out) + "\n"


if __name__ == "__main__":
    mode = sys.argv[1] if len(sys.argv) > 1 else "table"
    if mode == "css":
        sys.stdout.write(css())
    else:
        allbad = 0
        for name, theme in (("light", LIGHT), ("dark", DARK)):
            rows, bad = table(theme, name)
            allbad += bad
            title = "Светлая" if name == "light" else "Тёмная"
            print(f"#### {title} тема ({len(rows)} пар, ниже порога — {bad})")
            print("| Текст / линия | Фон | Контраст | Нужно | Итог | Где |")
            print("|---|---|---|---|---|---|")
            for fg, fgv, bg, bgv, r, need, res, where in rows:
                print(f"| `{fg}` {fgv} | `{bg}` {bgv} | {r}:1 | {need}:1 | {res} | {where} |")
            print()
        print(f"<!-- пар ниже порога: {allbad} -->")
        sys.exit(1 if allbad else 0)

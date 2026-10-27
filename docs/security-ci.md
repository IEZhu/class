# Security CI

Workflow [`.github/workflows/security.yml`](../.github/workflows/security.yml) —
исполняемая форма [ADR-011](architecture/decisions.md#adr-011--2026-10-24--accepted--security-validation-in-ci).
Запускается на pull request, push в `main`, ежедневно в 05:23 UTC и вручную.
Права у него только `contents: read`: проверки не выкатывают стек, не ходят
в боевую базу, LiveKit и хранилище и не получают их ключей.

## Что блокирует

- **Любая известная уязвимость любой серьёзности** — `UNKNOWN` … `CRITICAL`,
  в том числе без выпущенного исправления (`--ignore-unfixed=false`):
  в исходниках (все залоченные npm-пакеты, включая dev- и build-зависимости,
  и все Go-модули) и в рантайм-образах compose.
- **Секреты в исходниках**: секрет-сканер Trivy со всеми встроенными
  правилами; правило, разрешающее Markdown, отключено, и
  `scripts/check_secret_scanner.py` перед сканом требует, чтобы
  сгенерированный токен в Markdown действительно ловился.
- **Неполный результат**: ошибка сканера (в том числе залогированная как
  `WARN`, `scripts/security/check_scanner_errors.py`), пустой или неполный
  инвентарь пакетов (`scripts/check_scan_inventory.py`): пустой список
  уязвимостей не считается доказательством, что пакеты проверены.

Файлов исключений нет: Trivy запускается с `--config ''`, `--ignorefile ''`
и `--ignore-policy ''`, переменные `TRIVY_*` перед ним сбрасываются,
поэтому `.trivyignore*` не читается вовсе. Снизить порог или занести
находку в игнор нельзя. Единственный путь пропустить пакетную
Go-уязвимость — свежее доказательство, что затронутого кода нет
(`vulnerable_code_not_present` на уровне пакета), привязанное к точному
снимку исходников и lock-файлов или ко всем Go-бинарям точного OCI-образа.
Устаревшее, неполное или чужое доказательство блокирует.

## Джобы

1. **`dependencies` — зависимости и секреты.** Сначала положительный
   контроль: `scripts/security/go_positive_control.sh` собирает программу,
   в которой затронутый код OpenPGP действительно есть, и требует, чтобы
   пакетный гейт govulncheck её отверг. Затем
   `python3 -m unittest discover -s scripts -p 'test_*.py'` — регрессии
   самих гейтов; `trivy fs` по копии файлов из `git ls-files`
   (`--scanners vuln,secret`, `--include-dev-deps`, `--list-all-pkgs`);
   `scripts/security/source_package_proof.py` — доказательства для
   Go-модулей backend.
2. **`images` — рантайм-образы**, матрица `api`, `worker`, `web`, `caddy`,
   `postgres`. Образ строится ровно так, как его строит compose (параметры
   `build` берутся из `docker compose config`; новая опция `build` без
   явной поддержки роняет джобу), проходит smoke-проверки
   `scripts/security/smoke_runtime.sh`, публикуется в одноразовый
   локальный registry ради настоящего OCI-дайджеста и сканируется
   `scripts/security/scan_runtime.sh`: сырой скан, доказательства
   `go_package_proof.sh` по каждому Go-бинарю образа (govulncheck, режим
   binary), повторный скан только с доказанными VEX-утверждениями, сверка,
   что ничего, кроме доказанного, не исчезло, и проверка инвентаря.
3. **`harvester`** — образ сборщика субтитров вне блокирующего скана
   ([ADR-022](architecture/decisions.md), долг D-12): только сборка и smoke.

Вне матрицы образов также `migrate` (профиль `tools`), `libretranslate`
(профиль `translate`, долг D-10) и `whiteboard` (профиль `whiteboard`,
долг D-11): эти сервисы включаются только явно.

Версии закреплены: actions — по SHA коммита, Trivy — v0.76.1 через
`aquasecurity/setup-trivy`, govulncheck — v1.9.0.

## Smoke-проверки рантайма

`scripts/security/smoke_runtime.sh <сервис> <образ>`, всё в контейнерах
без сети:

- `api`, `worker`, `harvester` — процесс под uid 10001; worker, `dictimport`
  и harvester без `DATABASE_URL` не стартуют;
- `web` — есть `/usr/bin/node`, нет npm и Yarn, мажорная версия Node — 22,
  standalone-сервер отдаёт `/login`; полный ICU (`icu-data-full`):
  `ru-RU` поддержан и дата по-русски собирается правильно при UTC;
- `caddy` — `caddy validate` нашего `deploy/Caddyfile`;
- `postgres` — `gosu` переключается не на root, сервер работает не от root,
  неверный пароль при `scram-sha-256` отклоняется.

## Улики

Артефакты `source-security-evidence` и `runtime-security-evidence-<образ>`
хранятся 7 дней и выгружаются и при упавшей проверке: полные инвентари,
сырые и итоговые отчёты, доказательства. Значения найденных секретов в них
вымараны (`scripts/security/redact_report.py`), сами находки остаются
и блокируют.

## Как менять

- Новый сервис compose со своим образом — в матрицу `images` и в
  `smoke_runtime.sh`; иначе — профиль compose и ADR, почему он вне
  блокирующего скана.
- Смена формата манифестов, правил замены Go-модулей, пакетного менеджера
  образа или раскладки Dockerfile — вместе с проверками инвентаря и их
  регрессионными тестами в `scripts/`.
- Локально: `python3 -m unittest discover -s scripts -p 'test_*.py'`.
  Проверка образа требует Docker, Trivy и govulncheck.

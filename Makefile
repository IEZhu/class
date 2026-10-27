# Эксплуатация стенда (deploy/docker-compose.yml). Окружение — deploy/.env
# (шаблон deploy/.env.example). Команды: up / down / build / logs / ps /
# migrate / psql / seed.

COMPOSE := docker compose -f deploy/docker-compose.yml

.PHONY: up down build logs ps migrate psql seed check-env

# Стартовые цели требуют заполненного deploy/.env: compose подхватывает его
# автоматически (project directory = deploy/), но шаблонный файл или пустые
# секреты должны останавливать запуск, а не давать стенд с ними.
# HTTP_PORT и SEED_PASSWORD не в списке: у HTTP_PORT есть default (8090),
# SEED_PASSWORD опционален (без него seed не задаёт пароли)
REQUIRED_ENV := DOMAIN POSTGRES_DB POSTGRES_USER POSTGRES_PASSWORD

check-env:
	@test -f deploy/.env || { echo "deploy/.env отсутствует — скопируй deploy/.env.example и заполни"; exit 1; }
	@! grep -q "change-me" deploy/.env || { echo "deploy/.env содержит значения change-me — заполни реальными"; exit 1; }
	@for v in $(REQUIRED_ENV); do \
		grep -Eq "^$$v=[^\"'[:space:]]" deploy/.env || { echo "deploy/.env: ключ $$v отсутствует, пуст или начинается с пробела/кавычки"; exit 1; }; \
	done

up: check-env
	$(COMPOSE) up -d --build

down:
	$(COMPOSE) down

build:
	$(COMPOSE) build

logs:
	$(COMPOSE) logs -f --tail=200

ps:
	$(COMPOSE) ps

# Миграции golang-migrate из backend/migrations (файлы появятся в S0-2).
# if/else, а не `&& … || echo`: ошибка самого migrate должна ронять цель.
# Проба по *.up.sql (формат golang-migrate) — seed.sql миграцией не считается.
migrate: check-env
	@if ls backend/migrations/*.up.sql >/dev/null 2>&1; then \
		$(COMPOSE) run --rm migrate up; \
	else \
		echo "migrate: файлов *.up.sql в backend/migrations нет"; \
	fi

psql:
	$(COMPOSE) exec postgres sh -c 'psql -U $$POSTGRES_USER -d $$POSTGRES_DB'

# Наполнение стенда тестовыми данными. seed_password — из SEED_PASSWORD
# окружения контейнера postgres (deploy/.env); пусто — пароли не задаются.
# Зависимость от migrate: seed на несмигрированной базе бессмыслен.
seed: migrate
	@if test -f backend/migrations/seed.sql; then \
		$(COMPOSE) exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -v seed_password="$$SEED_PASSWORD" -U $$POSTGRES_USER -d $$POSTGRES_DB' < backend/migrations/seed.sql; \
	else \
		echo "seed: файла backend/migrations/seed.sql нет"; \
	fi

# S3-3/S3-4: перевод и словарь (deploy/README.md, ADR-018, ADR-019).
# translate-models — разовая установка моделей en↔ru для профиля translate.
# dict-import — импорт дефиниций из образа worker'а; повторный запуск
# продолжает оборванный прогон. Локальный файл: DICT_DIR — каталог
# на хосте (монтируется в /data), DICT_SOURCE=/data/<файл>.
DICT_SOURCE ?= https://kaikki.org/dictionary/raw-wiktextract-data.jsonl.gz

.PHONY: translate-models dict-import

translate-models: check-env
	$(COMPOSE) --profile translate-setup run --rm libretranslate-models

dict-import: check-env
	$(COMPOSE) run --rm --entrypoint dictimport $(if $(DICT_DIR),-v $(abspath $(DICT_DIR)):/data:ro) worker -source "$(DICT_SOURCE)"

# S5-4: корпус Tatoeba (deploy/README.md, ADR-028). tatoeba-import — импорт
# из образа worker'а; без TATOEBA_DIR качает официальные выгрузки, с ним —
# читает три файла с именами как на tatoeba.org из каталога хоста
# (монтируется в /data). Повторный запуск продолжает оборванный прогон.
TATOEBA_FILES = -links /data/eng-rus_links.tsv.bz2 \
	-english /data/eng_sentences_detailed.tsv.bz2 -russian /data/rus_sentences_detailed.tsv.bz2

.PHONY: tatoeba-import

tatoeba-import: check-env
	$(COMPOSE) run --rm --entrypoint tatoebaimport $(if $(TATOEBA_DIR),-v $(abspath $(TATOEBA_DIR)):/data:ro) worker $(if $(TATOEBA_DIR),$(TATOEBA_FILES))

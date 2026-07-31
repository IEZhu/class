# Эксплуатация стенда (deploy/docker-compose.yml). Окружение — deploy/.env
# (шаблон deploy/.env.example). Команды: up / down / build / logs / ps /
# migrate / psql / seed.

COMPOSE := docker compose -f deploy/docker-compose.yml

.PHONY: up down build logs ps migrate psql seed check-env

# Стартовые цели требуют заполненного deploy/.env: compose подхватывает его
# автоматически (project directory = deploy/), но пустой или шаблонный файл
# должен останавливать запуск, а не давать стенд с change-me-секретами.
check-env:
	@test -f deploy/.env || { echo "deploy/.env отсутствует — скопируй deploy/.env.example и заполни"; exit 1; }
	@! grep -q "change-me" deploy/.env || { echo "deploy/.env содержит значения change-me — заполни реальными"; exit 1; }

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
migrate:
	@if ls backend/migrations/*.sql >/dev/null 2>&1; then \
		$(COMPOSE) run --rm migrate up; \
	else \
		echo "migrate: миграций пока нет (появятся в S0-2)"; \
	fi

psql:
	$(COMPOSE) exec postgres sh -c 'psql -U $$POSTGRES_USER -d $$POSTGRES_DB'

# Наполнение стенда тестовыми данными (seed.sql появится в S0-2)
seed:
	@if test -f backend/migrations/seed.sql; then \
		$(COMPOSE) exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -U $$POSTGRES_USER -d $$POSTGRES_DB' < backend/migrations/seed.sql; \
	else \
		echo "seed: backend/migrations/seed.sql пока нет (появится в S0-2)"; \
	fi

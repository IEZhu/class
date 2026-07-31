# Эксплуатация стенда (deploy/docker-compose.yml). Окружение — deploy/.env
# (шаблон deploy/.env.example). Команды: up / down / build / logs / ps /
# migrate / psql / seed.

COMPOSE := docker compose -f deploy/docker-compose.yml

.PHONY: up down build logs ps migrate psql seed

up:
	$(COMPOSE) up -d --build

down:
	$(COMPOSE) down

build:
	$(COMPOSE) build

logs:
	$(COMPOSE) logs -f --tail=200

ps:
	$(COMPOSE) ps

# Миграции golang-migrate из backend/migrations (файлы появятся в S0-2)
migrate:
	@ls backend/migrations/*.sql >/dev/null 2>&1 \
		&& $(COMPOSE) run --rm migrate up \
		|| echo "migrate: миграций пока нет (появятся в S0-2)"

psql:
	$(COMPOSE) exec postgres sh -c 'psql -U $$POSTGRES_USER -d $$POSTGRES_DB'

# Наполнение стенда тестовыми данными (seed.sql появится в S0-2)
seed:
	@test -f backend/migrations/seed.sql \
		&& $(COMPOSE) exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -U $$POSTGRES_USER -d $$POSTGRES_DB' < backend/migrations/seed.sql \
		|| echo "seed: backend/migrations/seed.sql пока нет (появится в S0-2)"

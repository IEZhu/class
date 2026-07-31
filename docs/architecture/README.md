# Архитектура

Карточки этого уровня:

| Документ | Что там |
|---|---|
| [00-source-variant-c.md](00-source-variant-c.md) | Снапшот исходного документа «Вариант C» (не редактируется) |
| [01-topology.md](01-topology.md) | Топология: что где живёт, что не живёт на VPS |
| [02-services.md](02-services.md) | Сервисы docker-compose, RAM/CPU, требования к VPS |
| [03-data-model.md](03-data-model.md) | Модель данных + этап появления каждой таблицы |
| [04-api.md](04-api.md) | Эскиз API + привязка эндпоинтов к этапам |
| [05-storage-s3.md](05-storage-s3.md) | S3: layout, presigned, lifecycle, бэкапы |
| [06-budget.md](06-budget.md) | Смета и пороги апгрейда |
| [07-risks.md](07-risks.md) | Риски и митигации |
| [decisions.md](decisions.md) | Журнал решений (ADR) |
| [flows/](flows/README.md) | Потоки: жизненный цикл урока, клик по слову, практика, сборщик субтитров |
| [integrations/](integrations/README.md) | Интеграции: Google Calendar, LiveKit, AssemblyAI, перевод, LLM |

Порядок чтения при первом знакомстве: 01 → 02 → 03 → 04 →
[flows/](flows/README.md) → [integrations/](integrations/README.md) → 05–07.
Реализация по этой архитектуре — [../plans/](../plans/README.md).

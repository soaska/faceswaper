# PocketBase

Образ фиксирует совместимую ветку PocketBase 0.22 и применяет миграции из `collections` вместе с hooks из `pb_hooks`.

Порядок схемы:

1. `collections-snapshot.js` создаёт исходные `users`, `circle_jobs` и `face_jobs`.
2. `z_1786400000_atomic_jobs.js` добавляет lease очереди и журнал операций пользователя.
3. последующие миграции добавляют idempotency key, сессию бота и ограничения схемы.

Hooks предоставляют admin-only маршруты:

- `POST /api/faceswaper/jobs/claim`
- `POST /api/faceswaper/jobs/heartbeat`
- `POST /api/faceswaper/jobs/status`
- `POST /api/faceswaper/users/apply-operation`

Не импортируйте отдельный schema JSON через UI: он не включает исполняемые миграции и hooks. Перед применением к старой production-базе проверьте отсутствие дубликатов `users.tgid`; уникальный индекс намеренно останавливает миграцию при неоднозначных данных.

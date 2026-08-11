# faceswaper

Telegram-бот создаёт видеокружки и выполняет замену лиц на фото и видео. Компоненты разделены намеренно: бот принимает файлы и создаёт задачи, PocketBase хранит очередь и баланс, job-manager арендует и выполняет задачи, face-swap-component запускает InsightFace/ONNX Runtime.

## Состав

- `telegram-bot` — команды, меню, загрузка файлов и постановка задач.
- `job-manager` — lease-based очередь, биллинг, обработка и отправка результата.
- `face-swap-component` — последовательная покадровая обработка с ограниченной памятью.
- `pocketbase` — данные, миграции и атомарные hooks очереди/баланса.

Обычный жизненный цикл задачи: `queued → processing → sending → completed`. Воркер продлевает lease; просроченная задача возвращается в очередь, а после трёх неудачных попыток получает статус ошибки. Списание и возврат монет имеют уникальные ключи операций и не применяются повторно после рестарта.

## Запуск полного стека

Требуются Docker Compose или совместимый Podman Compose и NVIDIA Container Runtime для GPU-варианта.

```shell
cp example.env .env
# заполнить секреты в .env
docker-compose up -d --build
```

Compose сам задаёт внутренние адреса сервисов. Порты администрирования привязаны только к `127.0.0.1`:

- PocketBase: `http://127.0.0.1:8080/_/`
- Telegram Bot API: `http://127.0.0.1:8081`
- статистика Telegram Bot API: `http://127.0.0.1:8082`
- Face Swap health: `http://127.0.0.1:7860/health`

При первом запуске создайте администратора PocketBase через UI либо CLI контейнера. Значения `POCKETBASE_LOGIN` и `POCKETBASE_PASSWORD` в `.env` должны совпадать с ним. Схема создаётся только миграциями из `pocketbase/collections`; ручной импорт JSON не нужен и не поддерживается.

Для изолированной проверки на GPU-сервере используйте `scripts/deploy-gpu-test.sh <ssh-host>`. Скрипт разворачивает отдельный compose-проект в `/opt/faceswaper-codex-test`, использует порты `18080`, `18081`, `18082` и `17860`, создаёт администратора только для новой базы и ждёт полной загрузки CUDA-модели. `telegram-bot` намеренно не запускается: профиль `telegram-e2e` можно включать только с отдельным тестовым токеном либо на время согласованного отключения production poller.

Для диагностики:

```shell
docker-compose ps
docker-compose logs --tail=200 pocketbase telegram-bot-api telegram-bot job-manager face-swap-component
```

Сервисы используют `restart: always`. Это особенно важно для локального Telegram Bot API: без него бот не может ни получать updates, ни отправлять пользователям результаты.

## Face Swap отдельно

```shell
docker-compose -f face-swap-component/compose.cpu.yaml up --build
docker-compose -f face-swap-component/compose.nvidia.yaml up --build
```

Оба варианта читают `FACE_SWAP_API_KEY` из корневого `.env`. Методы `POST /swap` и `POST /swap-photo` требуют заголовок `X-API-Key`. Результат возвращается непосредственно как поток `video/mp4` или `image/jpeg`; метрики находятся в заголовках `X-Session-ID`, `X-Processing-Duration`, `X-Workers-Used` и `X-Device-Type`.

Пайплайн держит в памяти только текущий кадр. Один процесс API обслуживает одну тяжёлую задачу одновременно; дополнительные запросы ожидают semaphore. Ограничения размера файлов, длительности и разрешения задаются переменными из `example.env`.

## Биллинг

- видеокружок — 1 монета;
- замена лица на фото — 1 монета;
- замена лица на видео — 2 монеты плюс округлённые worker-seconds (`duration × workers / 20`), максимум 30 монет.

Баланс не может уйти ниже нуля. При ошибке обработки или отправки применённые списания возвращаются идемпотентно.

## Локальные проверки

```shell
(cd telegram-bot && go test -race ./... && go vet ./...)
(cd job-manager && go test -race ./... && go vet ./...)
(cd face-swap-component && PYTHONPATH=. python3 -m pytest tests -q)
docker-compose config -q
```

## Данные и временные файлы

- `data/pocketbase` — постоянная база и загруженные файлы.
- `data/telegram-bot-api` — данные локального Telegram Bot API; эта же директория доступна боту для local-mode файлов.
- `data/face-model-cache` — кэш детектора InsightFace.
- `temp/face-media` и `temp/job-manager` — удаляемые рабочие файлы.

Не монтируйте каталог поверх `/temp` целиком: swap-модель встроена в образ в `/temp/models`, и такой mount скроет её.

## Лицензии моделей

Код проекта распространяется по [MPL-2.0](license). Код InsightFace имеет собственную лицензию, а предоставляемые авторами pretrained-модели, включая автоматически загружаемый `buffalo_l`, разрешены только для некоммерческих исследовательских целей. Для коммерческого использования нужна отдельно лицензированная модель и проверка условий `inswapper_128.onnx`.

Бот: [@NoKnAbThBo_Bot](https://t.me/NoKnAbThBo_Bot).

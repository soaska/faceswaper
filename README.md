# faceswaper (наконец-то с заменой лиц?)
Бот телеграм с конструкцией, позволяющей выполнять распределенные вычисления на нескольких устройствах.
Отдельно запускаются телеграм бот, который отвечает на запросы пользователя, работает с базой данных и
создает задачи, база данных pocketbase и обработчик задач, который может быть запущен в нескольких
экземплярах на разных устройствах для ускорения вычислений. В данном примере представлена обработка видео
с помощью ffmpeg для создания кружочков в телеграме. При этом не требуется большая мощность, поэтому
возможость горизонтального масштабирования не играет роли. Играет роль улучшеная модель контроля задач,
отслеживания ошибок, которые сохраняются в базе данных с графическим интерфейсом. Задачи можно перезапускать,
и они не сбрасываются при перезапуске бота.

Ссылки: 
- Бот: [@NoKnAbThBo_Bot](https://t.me/NoKnAbThBo_Bot)
- Канал: [@tobta_channel](https://t.me/tobta_channel)

### 1. Telegram Bot (`telegram-bot/`)
- Обработка команд и сообщений от пользователей
- Создание задач на обработку видео
- Отправка результатов пользователям
- Управление балансом монет
- Отслеживание статуса задач

### 2. Job Manager (`job-manager/`)
- Обработка очереди задач
- Управление балансом монет
- Взаимодействие с FaceSwap API
- Отправка результатов пользователям

### 3. Face Swap Component (`face-swap-component/`)
- Обработка видео для замены лиц на основе InsightFace
- Поддержка CPU и NVIDIA GPU (CUDA)  
- Многопоточная обработка с автоматическим расчетом нагрузки
- Временные файлы в `/temp` контейнера
- Автоматическая очистка при запуске
- JSON API для интеграции с job-manager

### 4. PocketBase (`pocketbase/`)
- База данных и API для хранения:
  - Информации о пользователях
  - Баланса монет
  - Статистики операций
  - Очереди задач
  - Статусов обработки
- Аутентификация и авторизация
- Управление файлами (видео, фото)
- REST API для взаимодействия компонентов
- Веб-интерфейс для администрирования

---

# Запуск в контейнере
Скопируем код
```shell
git clone https://github.com/soaska/faceswaper.git
cd faceswaper
```

Заполним окружение
```shell
cp example.env .env
vim .env
```

Запустим pocketbase, перейдем по её [url](http://0.0.0.0:8080/_/), создадим пользователя.
```shell
podman compose up pocketbase
```

Теперь можем запускать бота и воркер.
```shell
podman compose up -d --build
```

# Запуск (без функций замены лиц)
Запустим pocketbase по [этой](https://pocketbase.io/docs/) инструкции. Удалим collection `users`.
Зайдем во вкладку *settings / import* collections. Далее в меню *load from json* выбираем [файл](https://github.com/soaska/faceswaper/blob/main/pocketbase/collections/PB%20Schema.json)
`pocketbase/collections/PB Schema.json`

Скопируем код
```shell
git clone https://github.com/soaska/faceswaper.git
cd faceswaper
```

Перейдем в *telegram-bot* и заполним окружение
```shell
cd telegram-bot
cp example.env .env
vim .env
```

Скопируем `.env` в *job-manager*
```shell
cp .env ../job-manager/
```

Скачаем зависимости и запустим
```shell
go mod download
go run .
```

Перейдем в *job-manager*, запустим его
```shell
cd ../job-manager
go mod downloadl
go run .
```

---

Бот отвечает на сообщения с помощью компонента *telegram-bot*, задачи выполняются *job-manager*.
Компоненты связаны базой данных pocketbase, все операции выполняются через нее, ее наличие
обязательно. Папки `telegram-bot/data` и `job-manager/cache` содержат только временные файлы и
могут быть удалены в период неактивности программы. job-manager требует ffmpeg.


# Face Swap Service

Сервис для замены лиц в видео с использованием нейронных сетей. Поддерживает CPU, CUDA.

## Возможности

- Замена лиц в видео с сохранением качества
- Поддержка различных платформ (CPU, CUDA)
- Оптимизированная обработка видео
- Поддержка потоковой передачи видео
- Автоматическое определение лиц
- Сохранение звука из исходного видео
- Многопоточность с разбивкой на чанки

## Установка

1. Клонируйте репозиторий:
```bash
git clone https://github.com/your-username/face-swap.git
cd face-swap
```

2. Выберите версию для вашей платформы:

### CPU версия (не тестировалась)
```bash
docker compose -f face-swap-component/compose.cpu.yaml up --build
```

### NVIDIA версия
```bash
docker compose -f face-swap-component/compose.nvidia.yaml up --build
```


### API Endpoints

#### POST /swap
Замена лиц в видео

Параметры:
- `source_image`: Изображение с лицом для замены
- `target_video`: Видео, в котором нужно заменить лица

Ответ:
```json
{
    "video_path": "/temp/media/output_abc123.mp4",
    "duration_seconds": 42,
    "filename": "output.mp4", 
    "media_type": "video/mp4",
    "session_id": "abc123ef",
    "processing_time": 42,
    "workers_used": 2,
    "device_type": "nvidia"
}
```

#### GET /health
Проверка состояния сервиса

Ответ:
```json
{
    "status": "healthy",
    "device_type": "cpu|nvidia|apple",
    "providers": ["CPUExecutionProvider", ...],
    "onnxruntime_version": "1.16.3",
    "torch_version": "2.1.0"
}
```

## Оптимизации

- Использование ONNX Runtime для оптимизации инференса
- Поддержка CUDA для NVIDIA GPU
- Оптимизированная обработка видео с сохранением качества

## Ограничения

- Требуется хорошее освещение для корректного определения лиц
- Качество замены зависит от угла поворота лица
- Рекомендуется использовать видео с разрешением не более 1080p

По вопросам пишите в [issues](https://github.com/soaska/faceswaper/issues) или на почту soaska@cornspace.su.

[License](license): MPL-2.0
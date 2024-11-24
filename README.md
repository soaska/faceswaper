# faceswapper
Бот телеграм с конструкцией, позволяющей выполнять распределенные вычисления на нескольких устройствах.
Отдельно запускаются телеграм бот, который отвечает на запросы пользователя, работает с базой данных и
создает задачи, база данных pocketbase и обработчик задач, который может быть запущен в нескольких
экземплярах на разных устройствах для ускорения вычислений. В данном примере представлена обработка видео
с помощью ffmpeg для создания кружочков в телеграме. При этом не требудется большая мощность, поэтому
возможость горизонтального масштабирования не играет роли. Играет роль улучшеная модель контроля задач,
отслеживания ошибок, которые сохраняются в базе данных с графическим интерфейсом. Задачи можно перезапускать,
и они не сбрасываются при перезапуске бота.

# Запуск
Запустим pocketbase по [этой](https://pocketbase.io/docs/) инструкции.
Зайдем во вкладку settings / import collections. Далее в меню load from json выбираем [файл](https://github.com/soaska/faceswaper/blob/main/pocketbase/collections/PB%20Schema.json)
pocketbase/collections/PB Schema.json

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
могут быть удалены в период неактивности программы.

По вопросам пишите в [issues](https://github.com/soaska/faceswaper/issues) или на почту soaska@cornspace.su.

# e6akl4k music downloader bot

Telegram-бот на Go для поиска и скачивания музыки через `yt-dlp`.

## Возможности

- YouTube / YouTube Music, SoundCloud, Bandcamp, VK, Mixcloud, Audiomack и другие поддерживаемые `yt-dlp` источники.
- MP3 128/320/VBR, FLAC, M4A и OGG Vorbis с метаданными и обложками.
- Поиск по названию прямо в личном чате и inline-поиск `@bot название трека` в любом чате.
- Предпросмотр названия, исполнителя, длительности, числа треков и примерного размера.
- Spotify, Apple Music, Deezer, Tidal и Яндекс Музыка используются как ссылки на метаданные: бот предлагает подходящие версии с YouTube и просит подтвердить совпадение.
- Плейлисты целиком, первыми 10/25/75 треками или диапазонами по десять.
- Отправка аудио альбомами до десяти файлов либо ZIP с автоматическим разбиением примерно по 45 МБ.
- Русский и английский интерфейс, прогресс, ETA и отмена загрузки.
- Постоянный SQLite-кэш Telegram `file_id`: повторный запрос не запускает `yt-dlp` и `ffmpeg`.
- Объединение одинаковых одновременных запросов в одну загрузку.
- Раздельные ограниченные очереди поиска и загрузки, rate limit и одна тяжёлая задача на пользователя.
- Healthcheck, Prometheus-метрики, JSON-логи, контроль диска и админские `/stats` и `/status`.

Музыкальные сервисы вроде Spotify не используются как источник аудиофайла. Бот читает их публичные метаданные, ищет варианты на YouTube и показывает выбор пользователю. Это не маскирует приблизительное совпадение под оригинал.

## Состав

```text
runtime.go          startup, polling и graceful shutdown
config.go           типизированная конфигурация окружения
main.go             Telegram-обработчики и отправка файлов
workflows.go        предпросмотр, поиск, диапазоны и общий кэш
resolver.go         безопасное чтение oEmbed/Open Graph ссылок
store.go            SQLite: языки, кэш, счётчики и история
scheduler.go        очереди, rate limit и дедупликация
app_state.go        одноразовые действия и активные задачи
bot_ui.go           клавиатуры, URL-валидация и форматирование
inline.go           inline-состояния и placeholder
inline_handlers.go  inline-поиск, загрузка и замена аудио
downloader.go       yt-dlp, метаданные, диапазоны и файлы
archive.go          ZIP-архивы
health.go           /healthz, /metrics и контроль диска
locales.go          русские и английские тексты
deploy.sh           первоначальный деплой Debian/Ubuntu
Dockerfile          multi-stage образ Go + yt-dlp + ffmpeg + Deno
docker-compose.yml  постоянный ограниченный контейнер
```

`yt-dlp`, `ffmpeg` и Deno остаются внешними инструментами. Telegram-flow, очереди, постоянное состояние, кэш и архивы реализованы на Go.

## Inline-режим и cache-канал

1. Создай закрытый Telegram-канал.
2. Добавь бота администратором с правом публиковать сообщения.
3. Запиши числовой ID канала:

```env
CACHE_CHAT_ID=-1001234567890
```

Старое имя `INLINE_CACHE_CHAT_ID` тоже поддерживается.

4. В `@BotFather` выполни `/setinline`, выбери бота и задай placeholder, например `Найти музыку`.
5. Выполни `/setinlinefeedback` и установи `100`, чтобы бот получал каждый выбранный результат.
6. Перезапусти бота и напиши `@username название песни`.

На первом запуске бот создаст секундный беззвучный MP3 и загрузит его в cache-канал. Полученный `file_id` можно явно задать через `INLINE_PLACEHOLDER_FILE_ID`.

Готовые треки хранятся в SQLite (`$XDG_DATA_HOME/musicbot.db`, в Docker — `/app/cache/musicbot.db`). Старый `inline-audio-cache.json` автоматически импортируется и переименовывается в `.migrated`. Inline скачивает MP3 320 kbps; плейлисты обрабатываются в личном чате.

Cache-канал позволяет получить `file_id` до ответа первому пользователю. Без него общий кэш всё равно запомнит `file_id`, полученный при первой обычной отправке.

## Очереди и кэш

По умолчанию одновременно работают две загрузки и два быстрых lookup-запроса. В очередь принимается ещё 20 загрузок и 40 поисков. Ожидающая задача показывает позицию. Частота новых ссылок и поисков ограничена 12 запросами в минуту.

```env
DATABASE_PATH=/app/cache/musicbot.db
CACHE_TTL=4320h
DOWNLOAD_WORKERS=2
DOWNLOAD_QUEUE_SIZE=20
LOOKUP_WORKERS=2
LOOKUP_QUEUE_SIZE=40
UPDATE_WORKERS=32
UPDATE_QUEUE_SIZE=256
RATE_LIMIT=12
INLINE_RATE_LIMIT=60
RATE_WINDOW=1m
MAX_PLAYLIST_TRACKS=75
```

При наличии изменяемого `cookies.txt` процессы `yt-dlp` по умолчанию сериализуются (`YTDLP_COOKIE_CONCURRENCY=1`), чтобы не повредить файл конкурентной записью. Увеличивать значение стоит только для независимых или неизменяемых cookies.

## Быстрый деплой

На новом Debian/Ubuntu-сервере:

```bash
chmod +x deploy.sh
./deploy.sh
```

Скрипт установит Docker/Compose, запросит `BOT_TOKEN`, предложит путь к cookies, создаст каталоги, соберёт контейнер и дождётся успешного healthcheck.

Для автоматизированного запуска заранее создай `.env` через менеджер секретов:

```bash
COOKIES_FILE='/tmp/cookies.txt' ./deploy.sh
```

Повторный запуск сохраняет токен. `cookies.txt` заменяется только при явно заданном `COOKIES_FILE`.

```bash
sudo docker compose logs -f
sudo docker compose restart
sudo docker compose down
```

Dockerfile фиксирует версии `yt-dlp` и Deno build args, проверяет опубликованные SHA-256 и запускает `--version` как smoke-test. Для осознанного обновления измени `YTDLP_VERSION`/`DENO_VERSION`, затем:

```bash
sudo docker compose build --no-cache --pull
sudo docker compose up -d
```

## Cookies YouTube

На VPS YouTube часто отвечает `Sign in to confirm you're not a bot`. Экспортируй cookies залогиненного аккаунта в формате Netscape:

```bash
COOKIES_FILE="$HOME/cookies.txt" ./deploy.sh
```

Файл монтируется на запись: `yt-dlp` может обновлять cookies. Права устанавливаются в `0600`. При повторном bot-check экспортируй свежий файл и снова запусти deploy.

## Ручной запуск

Требования: Go 1.26+, свежие `yt-dlp`, `ffmpeg` и Deno в `PATH`.

```bash
cp .env.example .env
# укажи BOT_TOKEN и выставь chmod 600 .env
go build -o musicbot .
./musicbot
```

Бот читает `.env` из рабочей директории; переменные процесса имеют приоритет. `cookies.txt` по умолчанию ищется рядом, другой путь задаётся `YTDLP_COOKIES_FILE`. Полный перечень настроек находится в `.env.example`.

## Наблюдаемость и администрирование

HTTP-сервер слушает `127.0.0.1:8080` при нативном запуске и `0.0.0.0:8080` внутри контейнера:

```text
GET /healthz   состояние SQLite, yt-dlp, диска и очередей в JSON
GET /metrics   метрики Prometheus
```

Compose не публикует порт наружу. Для внешнего Prometheus добавь защищённый reverse proxy или локальную привязку порта.

```env
ADMIN_IDS=123456789,987654321
DISK_WARNING_BYTES=536870912
DISK_CHECK_INTERVAL=10m
LOG_FORMAT=json
```

Администраторам доступны `/stats` и `/status`. При нехватке диска предупреждение повторяется только после восстановления места и нового падения ниже порога. Повторяющийся YouTube bot-check/cookies error также считается отдельно и присылается администратору не чаще раза в час.

## Ограничения

- Официальный Telegram Bot API принимает загружаемые ботом аудиофайлы до 50 МБ.
- Порог длительности зависит от формата и заранее отбрасывает заведомо слишком большие треки.
- ZIP автоматически разбивается на части; одиночный файл всё равно должен помещаться в лимит Telegram.
- За один запрос скачивается не более настраиваемых 75 треков.
- Язык, кэш, статистика и 90-дневная история загрузок хранятся в SQLite. Одноразовые кнопки и временные файлы намеренно не восстанавливаются после рестарта.
- Telegram `file_id` принадлежит конкретному боту и не переносится при смене токена.

## Проверка

```bash
go test ./...
go test -race ./...
go vet ./...
docker compose config --quiet
```

CI также собирает бинарник и Docker-образ. Тесты покрывают SQLite, TTL кэша, очереди, rate limit, дедупликацию, диапазоны плейлистов, ZIP-разбиение, resolver и повторную отправку через Telegram `file_id`.

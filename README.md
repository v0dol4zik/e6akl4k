# e6akl4k music downloader bot

Telegram-бот на Go для скачивания треков и плейлистов через `yt-dlp`.

## Возможности

- YouTube / YouTube Music, SoundCloud, Bandcamp, VK, Mixcloud, Audiomack и остальные площадки из списка поддерживаемых ссылок.
- MP3 128/320/VBR, FLAC, M4A и OGG Vorbis.
- Скачивание плейлистов с выбором: отдельные аудиофайлы или один ZIP.
- Русский и английский интерфейс.
- Inline-поиск пяти результатов YouTube с отправкой выбранного трека в личный чат.
- Ограничение двух параллельных загрузок и проверка размера до и после скачивания.
- Метаданные и обложки в готовых аудиофайлах.

Spotify, Apple Music, Deezer и Tidal не отдают аудио через открытый API. Их поддержка зависит от текущих возможностей соответствующего экстрактора `yt-dlp`.

## Состав

```text
main.go             Telegram Bot API и пользовательские сценарии
downloader.go       запуск yt-dlp, обработка метаданных и файлов
locales.go          русские и английские тексты
deploy.sh           первоначальный деплой на Debian/Ubuntu
Dockerfile          multi-stage образ Go + yt-dlp + ffmpeg + deno
docker-compose.yml  постоянный запуск контейнера
```

`yt-dlp`, `ffmpeg` и `deno` остаются внешними инструментами. Сам бот, его состояния, Telegram-обработчики, ZIP и управление загрузками реализованы на Go.

## Быстрый деплой

Скопируй проект на новый Debian/Ubuntu-сервер и запусти:

```bash
chmod +x deploy.sh
./deploy.sh
```

Скрипт:

1. Установит Docker и Docker Compose, если их нет.
2. Запросит `BOT_TOKEN`, если рядом нет настроенного `.env`.
3. Предложит путь к `cookies.txt`.
4. Создаст каталоги, настроит права, соберёт и запустит контейнер.

Для автоматизированного запуска сначала создай `.env` через менеджер секретов, затем передай только путь к cookies:

```bash
COOKIES_FILE='/tmp/cookies.txt' ./deploy.sh
```

Повторный запуск сохраняет токен из существующего `.env`. `cookies.txt` заменяется только при явно заданном `COOKIES_FILE`.

Логи и управление из каталога проекта:

```bash
sudo docker compose logs -f
sudo docker compose restart
sudo docker compose down
```

Обновление `yt-dlp` до последнего релиза:

```bash
sudo docker compose build --no-cache --pull
sudo docker compose up -d
```

## Cookies YouTube

На VPS YouTube часто отвечает `Sign in to confirm you're not a bot`. Экспортируй cookies залогиненного аккаунта в формате Netscape и передай путь в `deploy.sh`.

```bash
COOKIES_FILE="$HOME/cookies.txt" ./deploy.sh
```

Файл монтируется на запись: `yt-dlp` может обновлять cookies. Права автоматически устанавливаются в `0600`. Если bot-check вернулся, экспортируй свежий файл и снова запусти deploy.

## Ручной запуск

Требования: Go 1.26+, свежий `yt-dlp`, `ffmpeg` и `deno` в `PATH`.

```bash
cp .env.example .env
# укажи BOT_TOKEN в .env
go build -o musicbot .
./musicbot
```

Бот автоматически читает `.env` из рабочей директории. Переменные окружения имеют приоритет. `cookies.txt` по умолчанию ищется там же; другой путь задаётся через `YTDLP_COOKIES_FILE`.

## Настройка Telegram

Создай бота через [@BotFather](https://t.me/BotFather), запиши токен в `.env` и включи inline mode командой `/setinline`. Чтобы Telegram присылал событие выбора inline-результата, включи inline feedback через `/setinlinefeedback` со значением `100%`.

## Ограничения

- Обычный Telegram Bot API принимает файлы до 50 МБ.
- Порог длительности зависит от формата и отбрасывает заведомо слишком большие треки до загрузки.
- Плейлисты больше десяти успешных треков предлагают упаковку в ZIP; сам ZIP также должен быть меньше 50 МБ.
- Язык пользователя и кнопки ожидающих действий хранятся в памяти и сбрасываются при перезапуске.
- Inline-результат отправляется в личный чат, поэтому пользователь должен сначала открыть бота и нажать Start.

## Проверка

```bash
go test ./...
go vet ./...
```

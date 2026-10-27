# e6akl4k
~~~
            ░██████             ░██       ░██    ░████   ░██
           ░██   ░██            ░██       ░██   ░██ ██   ░██
 ░███████  ░██        ░██████   ░██    ░██░██  ░██  ██   ░██    ░██
░██    ░██ ░███████        ░██  ░██   ░██ ░██ ░██   ██   ░██   ░██
░█████████ ░██   ░██  ░███████  ░███████  ░██ ░█████████ ░███████
░██        ░██   ░██ ░██   ░██  ░██   ░██ ░██      ░██   ░██   ░██
 ░███████   ░██████   ░█████░██ ░██    ░██░██      ░██   ░██    ░██
 ~~~
## A self-hosted Telegram bot for downloading music from streaming services
[![AI Slop Inside](https://sladge.net/badge.svg)](https://sladge.net)

[English](#english) | [Русский](#русский)

## English

`e6akl4k` is a self-hosted Telegram bot for finding and downloading music. It can search for tracks by name: for example, send `Lunar Abyss - Lchavasse` and the bot will show the results it found on YouTube — pick the right one, choose the download quality, and receive the finished track file.

Supported audio links, including Newgrounds tracks, are downloaded directly from their source. The bot also handles albums, playlists, inline search, MP3, FLAC, M4A, OGG, download history, Telegram file caching, tracklist and cover export, and last.fm lists. Spotify, Apple Music, Deezer, Tidal, and Yandex Music links are used as search hints rather than direct audio sources.

Send a voice message with 5–20 seconds of music playing in the private chat: the bot will recognize the song through Shazam and send the full track as MP3 320. Recordings of 3 to 60 seconds and up to 2 MiB are accepted. If the YouTube match is uncertain, the bot will offer versions to choose from. Docker includes recognition support; [native setup](docs/configuration.md#music-recognition) uses Python and `requirements-shazam.txt`, with no Shazam API key.

Try an existing instance: [@e6akl4k_bot](https://t.me/e6akl4k_bot)

### Host your own instance

You will need a Debian or Ubuntu server with root or `sudo` access:

1. Create a Telegram bot with [@BotFather](https://t.me/BotFather) and copy its token.
2. Clone and deploy the project:

```bash
git clone https://github.com/v0dol4zik/e6akl4k.git
cd e6akl4k
chmod +x bootstrap.sh deploy.sh rollback.sh
BOT_TOKEN='your-bot-token' ./bootstrap.sh
./deploy.sh
```

`bootstrap.sh` installs Docker and prepares persistent storage. YouTube may require a Netscape-format cookies file on VPS hosts; pass it during setup with `COOKIES_FILE=/path/to/cookies.txt`.

To update the bot later:

```bash
git pull --ff-only origin main
./deploy.sh
```

## Русский

`e6akl4k` — self-hosted Telegram-бот для поиска и скачивания музыки. Бот умеет искать треки по запросу, например: `Lunar Abyss - Lchavasse` бот выдаст результаты которые он нашел на Youtube и из них можно выбрать один - выбрать качество скачивания - и затем получить файл трека

Поддерживаемые аудиоссылки, включая треки Newgrounds, скачиваются напрямую с исходного сервиса. Он также поддерживает альбомы, плейлисты, inline-поиск, MP3, FLAC, M4A, OGG, историю загрузок, Telegram-кэш, экспорт треклистов и обложек и списки last.fm. Ссылки Spotify, Apple Music, Deezer, Tidal и Яндекс Музыки используются как подсказки для поиска, а не как прямые источники аудио.

Отправь в личный чат голосовое с 5–20 секундами звучащей музыки: бот распознает песню через Shazam и пришлёт полный трек в MP3 320. Принимаются записи от 3 до 60 секунд размером до 2 МиБ. Если совпадение на YouTube сомнительное, бот предложит выбрать версию. В Docker всё уже установлено; для [нативного запуска](docs/configuration.md#music-recognition) нужны Python и `requirements-shazam.txt`, ключ Shazam не требуется.

Попробуй существующий инстанс: [@e6akl4k_bot](https://t.me/e6akl4k_bot)

### Как захостить свой инстанс

Тебе понадобится сервер с Debian или Ubuntu и доступом root или `sudo`:

1. Создай Telegram-бота через [@BotFather](https://t.me/BotFather) и скопируй его токен.
2. Склонируй и разверни проект:

```bash
git clone https://github.com/v0dol4zik/e6akl4k.git
cd e6akl4k
chmod +x bootstrap.sh deploy.sh rollback.sh
BOT_TOKEN='токен-твоего-бота' ./bootstrap.sh
./deploy.sh
```

`bootstrap.sh` установит Docker и подготовит постоянное хранилище. На VPS YouTube может потребовать cookies в формате Netscape; их можно передать при установке через `COOKIES_FILE=/путь/к/cookies.txt`.

Для последующего обновления:

```bash
git pull --ff-only origin main
./deploy.sh
```

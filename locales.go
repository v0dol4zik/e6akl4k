package main

import "strings"

const (
	defaultLang        = "ru"
	chooseLanguageText = "Выберите язык интерфейса:\nPlease choose your interface language:"
)

var languages = map[string]string{
	"ru": "🇷🇺 русский",
	"en": "🇬🇧 english",
}

var languageOrder = []string{"ru", "en"}

var sizeUnits = map[string][]string{
	"ru": {"Б", "КБ", "МБ", "ГБ", "ТБ"},
	"en": {"B", "KB", "MB", "GB", "TB"},
}

var texts = map[string]map[string]string{
	"language_changed": {
		"ru": "Язык изменён на {lang_name}.",
		"en": "Language changed to {lang_name}.",
	},
	"welcome": {
		"ru": "👋 <b>Привет! Я музыкальный бот e6akl4k (ебаклак).</b>\n\n🔎 Теперь музыку можно искать <b>без ссылки</b>. Просто отправь мне в личный чат исполнителя и название, например:\n<code>Daft Punk — Get Lucky</code>\n\n🔗 Или пришли ссылку на трек или плейлист — я покажу информацию и предложу формат.\n\n🌐 Поиск в любом чате: <code>@{username} название песни</code>\n\nВсе возможности и примеры: /help",
		"en": "👋 <b>Hi! I'm the e6akl4k music bot.</b>\n\n🔎 You can now search for music <b>without a link</b>. Send an artist and title in our private chat, for example:\n<code>Daft Punk — Get Lucky</code>\n\n🔗 Or send a track or playlist link and I'll show its details and available formats.\n\n🌐 Search from any chat: <code>@{username} song name</code>\n\nSee every feature and example: /help",
	},
	"help": {
		"ru": "🎧 <b>Что умеет бот</b>\n\n🔎 <b>Поиск по названию</b>\nОтправь в личный чат исполнителя и название:\n<code>Daft Punk — Get Lucky</code>\nЯ покажу несколько вариантов. Выбери нужный, затем формат и качество.\n\n🔗 <b>Загрузка по ссылке</b>\nПришли ссылку YouTube, YouTube Music, SoundCloud, Bandcamp, VK, Mixcloud или Audiomack. До загрузки я покажу название, длительность и примерный размер.\n\n🎶 <b>Ссылки музыкальных сервисов</b>\nSpotify, Apple Music, Deezer, Tidal и Яндекс Музыка используются для поиска по метаданным. Я покажу похожие версии с YouTube — проверь и подтверди совпадение.\n\n📚 <b>Плейлисты</b>\nМожно выбрать весь плейлист в пределах лимита, первые 10/25 треков или показанный диапазон. Для большого набора способ отправки выбирается до загрузки: отдельные файлы приходят частями, ZIP автоматически делится.\n\n🌐 <b>Inline-поиск в любом чате</b>\nНапиши:\n<code>@{username} название песни</code>\nВыбери результат — аудио появится прямо в этом чате.\n\n⚙️ <b>Форматы</b>\nMP3 128/320/VBR и M4A отправляются как музыка. FLAC и OGG отправляются файлами; FLAC не улучшает качество исходника. Если загрузка попала в очередь, я покажу её позицию. Активную загрузку можно отменить кнопкой. Повторные запросы обслуживаются из кэша автоматически.\n\n/language — сменить язык",
		"en": "🎧 <b>What the bot can do</b>\n\n🔎 <b>Search by title</b>\nSend an artist and title in our private chat:\n<code>Daft Punk — Get Lucky</code>\nI'll show several results. Choose the right match, then its format and quality.\n\n🔗 <b>Download from a link</b>\nSend a YouTube, YouTube Music, SoundCloud, Bandcamp, VK, Mixcloud, or Audiomack link. Before downloading, I'll show the title, duration, and estimated size.\n\n🎶 <b>Music-service links</b>\nSpotify, Apple Music, Deezer, Tidal, and Yandex Music links are used to search by metadata. I'll show matching YouTube versions so you can verify and confirm the right one.\n\n📚 <b>Playlists</b>\nChoose the entire playlist within the configured limit, the first 10/25 tracks, or one of the displayed ranges. For large sets, choose delivery before downloading: individual files arrive in batches and ZIP archives are split automatically.\n\n🌐 <b>Inline search from any chat</b>\nType:\n<code>@{username} song name</code>\nChoose a result and the audio will appear directly in that chat.\n\n⚙️ <b>Formats</b>\nMP3 128/320/VBR and M4A are sent as music. FLAC and OGG are sent as files; converting to FLAC cannot improve the source. If a download is queued, I'll show its position. An active download can be cancelled with its button. Repeated requests are served from cache automatically.\n\n/language — change language",
	},
	"invalid_link": {
		"ru": "В группе отправь мне ссылку на трек или плейлист. Для поиска по названию используй <code>@{username} название песни</code> или открой личный чат с ботом.",
		"en": "In a group, send me a track or playlist link. To search by title, use <code>@{username} song name</code> or open a private chat with the bot.",
	},
	"analyzing":            {"ru": "🔎 <b>анализирую ссылку…</b>", "en": "🔎 <b>analyzing the link…</b>"},
	"searching":            {"ru": "🔎 <b>Ищу подходящие треки на YouTube…</b>", "en": "🔎 <b>Searching YouTube for matching tracks…</b>"},
	"search_too_short":     {"ru": "введи хотя бы три символа для поиска.", "en": "enter at least three characters to search."},
	"nothing_found":        {"ru": "ничего не найдено.", "en": "nothing found."},
	"search_results":       {"ru": "🎵 <b>Результаты поиска</b>\nВыбери нужный трек. После этого можно будет выбрать формат и качество:", "en": "🎵 <b>Search results</b>\nChoose the right track. You can select its format and quality next:"},
	"resolved_results":     {"ru": "🔗 <b>Нашёл похожие версии на YouTube</b>\nАудио будет загружено из выбранного варианта, поэтому проверь название, исполнителя и длительность:", "en": "🔗 <b>I found similar versions on YouTube</b>\nAudio will be downloaded from the option you choose, so check its title, artist, and duration:"},
	"command_start":        {"ru": "запустить бота", "en": "start the bot"},
	"command_help":         {"ru": "возможности и примеры", "en": "features and examples"},
	"command_language":     {"ru": "сменить язык", "en": "change language"},
	"command_stats":        {"ru": "статистика бота", "en": "bot statistics"},
	"command_status":       {"ru": "состояние очередей", "en": "queue status"},
	"internal_id_error":    {"ru": "не удалось создать идентификатор загрузки", "en": "failed to create a download identifier"},
	"preview_error":        {"ru": "❌ не удалось прочитать ссылку: {error}", "en": "❌ failed to inspect the link: {error}"},
	"preview_tracks":       {"ru": "🎶 треков: <b>{count}</b>", "en": "🎶 tracks: <b>{count}</b>"},
	"preview_sizes":        {"ru": "📦 примерно: MP3 128 — {size128}, MP3 320 — {size320}", "en": "📦 estimate: MP3 128 — {size128}, MP3 320 — {size320}"},
	"choose_range":         {"ru": "\nвыбери диапазон:", "en": "\nchoose a range:"},
	"choose_format":        {"ru": "\nвыбери формат и качество:", "en": "\nchoose format and quality:"},
	"selected_range":       {"ru": "✅ выбраны треки <b>{start}–{end}</b>. Теперь выбери формат:", "en": "✅ tracks <b>{start}–{end}</b> selected. Now choose a format:"},
	"btn_range_all":        {"ru": "🎶 весь плейлист", "en": "🎶 entire playlist"},
	"btn_range_10":         {"ru": "🔟 первые 10", "en": "🔟 first 10"},
	"btn_range_25":         {"ru": "2️⃣5️⃣ первые 25", "en": "2️⃣5️⃣ first 25"},
	"btn_range_limit":      {"ru": "📚 первые {limit}", "en": "📚 first {limit}"},
	"btn_range_custom":     {"ru": "🎵 {start}–{end}", "en": "🎵 {start}–{end}"},
	"queue_full":           {"ru": "⚠️ очередь сейчас заполнена. Попробуй немного позже.", "en": "⚠️ the queue is full. Please try again later."},
	"queued":               {"ru": "🕒 <b>загрузка в очереди</b>\nпозиция: {position}", "en": "🕒 <b>download queued</b>\nposition: {position}"},
	"archive_queued":       {"ru": "🕒 <b>архивация в очереди</b>\nпозиция: {position}", "en": "🕒 <b>archiving queued</b>\nposition: {position}"},
	"rate_limited":         {"ru": "⏳ слишком много запросов. Попробуй через {seconds} сек.", "en": "⏳ too many requests. Try again in {seconds} sec."},
	"user_download_active": {"ru": "⏳ у тебя уже есть активная или ожидающая загрузка.", "en": "⏳ you already have an active or queued download."},
	"looks_like_playlist": {
		"ru": "<b>Похоже, это плейлист.</b>\n\n",
		"en": "<b>This looks like a playlist.</b>\n\n",
	},
	"link_received": {
		"ru": "ссылка получена!\n{info}\nвыбери формат и качество:",
		"en": "link received!\n{info}\nchoose a format and quality:",
	},
	"btn_mp3_best": {"ru": "🎵 MP3 (лучшее качество)", "en": "🎵 MP3 (best quality)"},
	"btn_mp3_128":  {"ru": "🎵 MP3 (128 kbps)", "en": "🎵 MP3 (128 kbps)"},
	"btn_mp3_320":  {"ru": "🎵 MP3 (320 kbps)", "en": "🎵 MP3 (320 kbps)"},
	"btn_flac":     {"ru": "🎼 FLAC (конвертация)", "en": "🎼 FLAC (converted)"},
	"btn_m4a":      {"ru": "🎤 M4A (AAC)", "en": "🎤 M4A (AAC)"},
	"btn_ogg":      {"ru": "🎧 OGG Vorbis", "en": "🎧 OGG Vorbis"},
	"btn_cancel":   {"ru": "❌ отмена", "en": "❌ cancel"},
	"btn_zip_yes":  {"ru": "📁 ZIP-архивом (авторазбиение)", "en": "📁 ZIP archive (auto-split)"},
	"btn_zip_no":   {"ru": "🎵 отправить по одному", "en": "🎵 send one by one"},
	"cancelled":    {"ru": "❌ отменено.", "en": "❌ cancelled."},
	"data_expired": {"ru": "⚠️ данные устарели.", "en": "⚠️ this data has expired."},
	"link_expired": {"ru": "⚠️ ссылка устарела.", "en": "⚠️ this link has expired."},
	"action_unavailable": {
		"ru": "⚠️ кнопка устарела или принадлежит другому пользователю.",
		"en": "⚠️ this button has expired or belongs to another user.",
	},
	"playlist_too_large": {
		"ru": "⚠️ в плейлисте {count} треков. за один раз можно скачать не более {limit}.",
		"en": "⚠️ this playlist has {count} tracks. The limit per download is {limit}.",
	},
	"inline_loading": {
		"ru": "⏳ <b>скачиваю и обрабатываю…</b>",
		"en": "⏳ <b>downloading and processing…</b>",
	},
	"inline_cancel": {
		"ru": "❌ отменить загрузку",
		"en": "❌ cancel download",
	},
	"inline_cancelled": {
		"ru": "❌ загрузка отменена.",
		"en": "❌ download cancelled.",
	},
	"inline_already_active": {
		"ru": "⚠️ у тебя уже есть активная inline-загрузка.",
		"en": "⚠️ you already have an active inline download.",
	},
	"inline_error": {
		"ru": "❌ <b>ошибка inline-загрузки:</b> {error}",
		"en": "❌ <b>inline download error:</b> {error}",
	},
	"inline_too_big": {
		"ru": "файл слишком большой ({size})",
		"en": "the file is too large ({size})",
	},
	"inline_switch_pm": {
		"ru": "ℹ️ открыть бота",
		"en": "ℹ️ open the bot",
	},
	"download_starting": {
		"ru": "⏳ <b>начинаю загрузку…</b>",
		"en": "⏳ <b>starting download…</b>",
	},
	"download_finished": {"ru": "✅ загрузка завершена, отправляю файлы…", "en": "✅ download finished, sending files…"},
	"download_progress": {
		"ru": "⏳ <b>скачиваю плейлист…</b>\nтекущий трек: <b>{current}/{total}</b>\nосталось: {eta}",
		"en": "⏳ <b>downloading playlist…</b>\ncurrent track: <b>{current}/{total}</b>\nremaining: {eta}",
	},
	"download_batch": {
		"ru": "⏳ <b>скачиваю и сразу отправляю плейлист…</b>\nтреки: <b>{start}–{end}</b> из {total}",
		"en": "⏳ <b>downloading and sending the playlist in batches…</b>\ntracks: <b>{start}–{end}</b> of {total}",
	},
	"eta_calculating":           {"ru": "рассчитываю…", "en": "calculating…"},
	"eta_less_minute":           {"ru": "меньше минуты", "en": "less than a minute"},
	"eta_minutes":               {"ru": "около {count} мин.", "en": "about {count} min."},
	"eta_hours_minutes":         {"ru": "около {hours} ч. {minutes} мин.", "en": "about {hours} h. {minutes} min."},
	"download_already_finished": {"ru": "загрузка уже завершена или отменена.", "en": "the download has already finished or was cancelled."},
	"download_error": {
		"ru": "❌ <b>ошибка загрузки:</b> {error}",
		"en": "❌ <b>download error:</b> {error}",
	},
	"nothing_downloaded": {
		"ru": "❌ Ничего не скачано.\nОшибка: {error}\n\nЕсли она повторяется, отправь текст ошибки администратору: @dol6oe6xd",
		"en": "❌ Nothing was downloaded.\nError: {error}\n\nIf this keeps happening, send the error text to the administrator: @dol6oe6xd",
	},
	"unknown_error": {"ru": "неизвестная ошибка", "en": "unknown error"},
	"downloaded_count": {
		"ru": "📦 скачано треков: <b>{count}</b>. как отправить?",
		"en": "📦 downloaded tracks: <b>{count}</b>. how should I send them?",
	},
	"choose_delivery": {
		"ru": "📦 <b>Как отправить выбранный плейлист?</b>\nСпособ нужно выбрать до загрузки, чтобы бот не держал готовые файлы в ожидании.",
		"en": "📦 <b>How should I send the selected playlist?</b>\nChoose before downloading so the bot does not keep completed files waiting on disk.",
	},
	"send_error": {
		"ru": "⚠️ [{idx}/{total}] <b>ошибка:</b> <code>{error}</code>",
		"en": "⚠️ [{idx}/{total}] <b>error:</b> <code>{error}</code>",
	},
	"file_not_found": {
		"ru": "⚠️ [{idx}/{total}] файл не найден: {name}",
		"en": "⚠️ [{idx}/{total}] file not found: {name}",
	},
	"file_too_big": {
		"ru": "⚠️ [{idx}/{total}] <b>{title}</b>\nфайл слишком большой ({size}) — лимит telegram 50 МБ.",
		"en": "⚠️ [{idx}/{total}] <b>{title}</b>\nthe file is too large ({size}) — telegram's limit is 50 MB.",
	},
	"send_failed": {
		"ru": "⚠️ [{idx}/{total}] не удалось отправить: <code>{error}</code>",
		"en": "⚠️ [{idx}/{total}] failed to send: <code>{error}</code>",
	},
	"all_sent_summary": {
		"ru": "✅ готово! отправлено: {sent}/{total} треков.",
		"en": "✅ done! sent: {sent}/{total} tracks.",
	},
	"some_failed_suffix": {
		"ru": " некоторые треки не удалось отправить.",
		"en": " some tracks could not be sent.",
	},
	"no_files_for_zip": {
		"ru": "❌ нет файлов для упаковки в архив.",
		"en": "❌ no files available to pack into an archive.",
	},
	"zipping": {
		"ru": "🗜 упаковываю {count} треков в ZIP…",
		"en": "🗜 packing {count} tracks into a ZIP…",
	},
	"zip_too_big": {
		"ru": "⚠️ ZIP слишком большой ({size}) — превышает лимит 50 МБ.",
		"en": "⚠️ the ZIP is too large ({size}) — it exceeds the 50 MB limit.",
	},
	"zip_caption": {
		"ru": "<b>📁 плейлист — {count} треков</b>\n📦 {size} | {fmt}{skipped}",
		"en": "<b>📁 playlist — {count} tracks</b>\n📦 {size} | {fmt}{skipped}",
	},
	"zip_caption_skipped": {
		"ru": "\n⚠️ пропущено с ошибкой: {skipped}",
		"en": "\n⚠️ skipped due to errors: {skipped}",
	},
	"zip_part": {"ru": "\n📚 часть {part}/{total}", "en": "\n📚 part {part}/{total}"},
	"zip_sent": {"ru": "✅ готово! архив отправлен.", "en": "✅ done! archive sent."},
	"zip_error": {
		"ru": "❌ ошибка при создании архива: <code>{error}</code>",
		"en": "❌ error while creating the archive: <code>{error}</code>",
	},
	"admin_stats": {
		"ru": "📊 <b>Статистика</b>\nПользователи: {users}\nТреки в кэше: {cached}\nУспешно доставлено: {ok}\nЧастично: {partial}\nОшибки: {failed}\nОтменено: {cancelled}\nОшибки cookies: {cookies}\nПопадания в кэш: {hits}\nПоиски: {searches}\nОграничено rate limit: {limited}\nОтклонено очередью: {rejected}",
		"en": "📊 <b>Statistics</b>\nUsers: {users}\nDelivered successfully: {ok}\nPartially delivered: {partial}\nCached tracks: {cached}\nFailed: {failed}\nCancelled: {cancelled}\nCookie errors: {cookies}\nCache hits: {hits}\nSearches: {searches}\nRate limited: {limited}\nRejected by queue: {rejected}",
	},
	"admin_status": {
		"ru": "🟢 <b>Бот работает</b>\nЗагрузки: {downloads_active}/{downloads_capacity}, в очереди {downloads_waiting}\nПоиск: {lookups_active}/{lookups_capacity}, в очереди {lookups_waiting}\nАрхивация: {archives_active}/{archives_capacity}, в очереди {archives_waiting}\nАктивных пользователей: {active_users}",
		"en": "🟢 <b>Bot is running</b>\nDownloads: {downloads_active}/{downloads_capacity}, queued {downloads_waiting}\nSearches: {lookups_active}/{lookups_capacity}, queued {lookups_waiting}\nArchives: {archives_active}/{archives_capacity}, queued {archives_waiting}\nActive users: {active_users}",
	},
	"admin_stats_error": {
		"ru": "❌ Не удалось получить статистику: {error}",
		"en": "❌ Failed to load statistics: {error}",
	},
	"admin_disk_warning": {
		"ru": "⚠️ Мало места на диске: свободно {free}.",
		"en": "⚠️ Disk space is running low: {free} available.",
	},
	"admin_cookie_warning": {
		"ru": "⚠️ YouTube отклоняет cookies или требует bot-check. Обнови cookies.txt и проверь /status.",
		"en": "⚠️ YouTube is rejecting the cookies or requiring a bot check. Refresh cookies.txt and check /status.",
	},
}

func tr(key, lang string, values ...string) string {
	translations := texts[key]
	template := translations[lang]
	if template == "" {
		template = translations[defaultLang]
	}
	if len(values) == 0 {
		return template
	}
	replacements := make([]string, 0, len(values)*2)
	for i := 0; i+1 < len(values); i += 2 {
		replacements = append(replacements, "{"+values[i]+"}", values[i+1])
	}
	return strings.NewReplacer(replacements...).Replace(template)
}

package main

import "strings"

const (
	defaultLang        = "ru"
	chooseLanguageText = "выберите язык интерфейса:\nplease choose your interface language:"
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
		"ru": "язык изменён на {lang_name}.",
		"en": "language changed to {lang_name}.",
	},
	"welcome": {
		"ru": "👋 <b>привет! я музыкальный бот e6akl4k (ебаклак).</b>\n\n🔎 теперь музыку можно искать <b>без ссылки</b>. просто отправь мне в личный чат исполнителя и название, например:\n<code>Daft Punk — Get Lucky</code>\n\n🔗 или пришли ссылку на трек или плейлист — я покажу информацию и предложу формат.\n\n🌐 поиск в любом чате: <code>@{username} название песни</code>\n\nвсе возможности и примеры: /help",
		"en": "👋 <b>hi! i'm the e6akl4k music bot.</b>\n\n🔎 you can now search for music <b>without a link</b>. send an artist and title in our private chat, for example:\n<code>Daft Punk — Get Lucky</code>\n\n🔗 or send a track or playlist link and i'll show its details and available formats.\n\n🌐 search from any chat: <code>@{username} song name</code>\n\nsee every feature and example: /help",
	},
	"help": {
		"ru": "🎧 <b>что умеет бот</b>\n\n🔎 <b>поиск по названию</b>\nотправь в личный чат исполнителя и название:\n<code>Daft Punk — Get Lucky</code>\nя найду подходящие варианты на YouTube. выбери нужный трек, затем формат и качество.\n\n🔗 <b>загрузка по ссылке</b>\nпришли ссылку Octave Streaming, YouTube, YouTube Music, SoundCloud, Bandcamp, VK, Mixcloud или Audiomack. до загрузки я покажу название, длительность и примерный размер.\n\n🎶 <b>ссылки музыкальных сервисов</b>\nSpotify, Apple Music, Deezer, Tidal и Яндекс Музыка используются для поиска по метаданным. я покажу похожие версии с YouTube — проверь и подтверди совпадение.\n\n📚 <b>плейлисты и альбомы</b>\nможно выбрать весь плейлист или альбом в пределах лимита, первые 10/25 треков или показанный диапазон. для большого набора способ отправки выбирается до загрузки: отдельные файлы приходят частями, ZIP автоматически делится.\n\n🌐 <b>inline-поиск в любом чате</b>\nнапиши:\n<code>@{username} название песни</code>\nвыбери результат — аудио появится прямо в этом чате.\n\n⚙️ <b>форматы</b>\nMP3 128/320/VBR и M4A отправляются как музыка. FLAC и OGG отправляются файлами; FLAC не улучшает качество исходника. если загрузка попала в очередь, я покажу её позицию. активную загрузку можно отменить кнопкой. повторные запросы обслуживаются из кэша автоматически.\n\n/language — сменить язык",
		"en": "🎧 <b>what the bot can do</b>\n\n🔎 <b>search by title</b>\nsend an artist and title in our private chat:\n<code>Daft Punk — Get Lucky</code>\ni'll find matching options on YouTube. choose the right track, then its format and quality.\n\n🔗 <b>download from a link</b>\nsend an Octave Streaming, YouTube, YouTube Music, SoundCloud, Bandcamp, VK, Mixcloud, or Audiomack link. before downloading, i'll show its title, duration, and estimated size.\n\n🎶 <b>music-service links</b>\nSpotify, Apple Music, Deezer, Tidal, and Yandex Music links are used to search by metadata. i'll show matching YouTube versions so you can verify and confirm the right one.\n\n📚 <b>playlists and albums</b>\nchoose the entire playlist or album within the configured limit, the first 10/25 tracks, or one of the displayed ranges. for large sets, choose delivery before downloading: individual files arrive in batches and ZIP archives are split automatically.\n\n🌐 <b>inline search from any chat</b>\ntype:\n<code>@{username} song name</code>\nchoose a result and the audio will appear directly in that chat.\n\n⚙️ <b>formats</b>\nMP3 128/320/VBR and M4A are sent as music. FLAC and OGG are sent as files; converting to FLAC cannot improve the source. if a download is queued, i'll show its position. an active download can be cancelled with its button. repeated requests are served from cache automatically.\n\n/language — change language",
	},
	"invalid_link": {
		"ru": "в группе отправь мне ссылку на трек или плейлист. для поиска по названию используй <code>@{username} название песни</code> или открой личный чат с ботом.",
		"en": "in a group, send me a track or playlist link. to search by title, use <code>@{username} song name</code> or open a private chat with the bot.",
	},
	"analyzing":             {"ru": "🔎 <b>анализирую ссылку…</b>", "en": "🔎 <b>analyzing the link…</b>"},
	"searching":             {"ru": "🔎 <b>ищу подходящие треки на YouTube…</b>", "en": "🔎 <b>searching YouTube for matching tracks…</b>"},
	"search_too_short":      {"ru": "введи хотя бы три символа для поиска.", "en": "enter at least three characters to search."},
	"nothing_found":         {"ru": "ничего не найдено.", "en": "nothing found."},
	"search_results":        {"ru": "🎵 <b>результаты поиска</b>\nвыбери нужный трек. после этого можно будет выбрать формат и качество:", "en": "🎵 <b>search results</b>\nchoose the right track. you can select its format and quality next:"},
	"resolved_results":      {"ru": "🔗 <b>нашёл похожие версии</b>\nаудио будет загружено из выбранного варианта YouTube, поэтому проверь название, исполнителя и длительность:", "en": "🔗 <b>i found similar versions</b>\naudio will be downloaded from the selected YouTube result, so check its title, artist, and duration:"},
	"command_start":         {"ru": "запустить бота", "en": "start the bot"},
	"command_help":          {"ru": "возможности и примеры", "en": "features and examples"},
	"command_language":      {"ru": "сменить язык", "en": "change language"},
	"command_stats":         {"ru": "статистика бота", "en": "bot statistics"},
	"command_status":        {"ru": "состояние очередей", "en": "queue status"},
	"command_perf":          {"ru": "скорость этапов", "en": "stage performance"},
	"command_log":           {"ru": "диагностика загрузки", "en": "trace a download"},
	"command_ban":           {"ru": "заблокировать пользователя", "en": "ban a user"},
	"command_pardon":        {"ru": "разблокировать пользователя", "en": "unban a user"},
	"command_addadmin":      {"ru": "добавить администратора", "en": "add an administrator"},
	"command_deladmin":      {"ru": "удалить администратора", "en": "remove an administrator"},
	"command_admins":        {"ru": "список администраторов", "en": "list administrators"},
	"command_banlist":       {"ru": "список блокировок", "en": "list bans"},
	"command_userinfo":      {"ru": "информация о пользователе", "en": "user information"},
	"command_jobs":          {"ru": "активные задачи", "en": "active jobs"},
	"command_adminlog":      {"ru": "журнал администраторов", "en": "administrator audit"},
	"command_id":            {"ru": "узнать Telegram ID", "en": "look up a Telegram ID"},
	"banned_notice":         {"ru": "⛔ доступ к боту заблокирован. причина: {reason}", "en": "⛔ access to this bot is blocked. reason: {reason}"},
	"admin_forbidden":       {"ru": "⛔ недостаточно прав.", "en": "⛔ insufficient permissions."},
	"admin_usage":           {"ru": "использование: <code>{usage}</code>", "en": "usage: <code>{usage}</code>"},
	"admin_invalid_id":      {"ru": "нужен положительный числовой Telegram ID.", "en": "a positive numeric Telegram ID is required."},
	"admin_done":            {"ru": "✅ выполнено: {action} <code>{id}</code>", "en": "✅ completed: {action} <code>{id}</code>"},
	"admin_no_change":       {"ru": "ℹ️ ничего не изменилось для <code>{id}</code>.", "en": "ℹ️ nothing changed for <code>{id}</code>."},
	"admin_protected":       {"ru": "⛔ владельца или администратора нельзя заблокировать/удалить этой командой.", "en": "⛔ an owner or administrator cannot be banned/removed by this command."},
	"admin_db_error":        {"ru": "❌ ошибка SQLite: <code>{error}</code>", "en": "❌ database error: <code>{error}</code>"},
	"admin_empty":           {"ru": "список пуст.", "en": "the list is empty."},
	"admin_id_reply":        {"ru": "твой Telegram ID: <code>{id}</code>", "en": "your Telegram ID: <code>{id}</code>"},
	"id_lookup_reply":       {"ru": "Telegram ID {username}: <code>{id}</code>", "en": "Telegram ID for {username}: <code>{id}</code>"},
	"id_lookup_unknown":     {"ru": "не знаю ID {username}. Telegram не разрешает ботам искать произвольных людей по username — пользователь должен сначала написать боту или появиться в доступном ему чате.", "en": "i don't know the ID for {username}. Telegram does not let bots look up arbitrary people by username; the user must first message the bot or appear in a chat visible to it."},
	"id_lookup_error":       {"ru": "не удалось проверить ID. попробуй ещё раз позже.", "en": "couldn't look up the ID. please try again later."},
	"id_help":               {"ru": "/id [@username] — узнать свой или известный боту Telegram ID", "en": "/id [@username] — show your ID or one already known to the bot"},
	"support_notice":        {"ru": "нравится бот? пожалуйста, помогите ему стать популярнее\n\nя очень хотел бы чтобы e6akl4k продвинулся в массы и им часто пользовались, это мотивирует меня обновлять и поддерживать бота, вы можете поделиться им с друзьями, знакомыми и близкими\n\nспасибо! <3", "en": "like the bot? please help it become more popular\n\ni'd really love to see e6akl4k reach more people and get used often. it motivates me to keep updating and supporting the bot. you can share it with friends, acquaintances, and loved ones\n\nthank you! <3"},
	"btn_hide_support":      {"ru": "❌ скрыть это сообщение.", "en": "❌ hide this message."},
	"admin_list_title":      {"ru": "👮 <b>администраторы</b>", "en": "👮 <b>administrators</b>"},
	"admin_bans_title":      {"ru": "⛔ <b>блокировки · страница {page}</b>", "en": "⛔ <b>bans · page {page}</b>"},
	"admin_audit_title":     {"ru": "📜 <b>журнал администраторов</b>", "en": "📜 <b>administrator audit</b>"},
	"admin_perf_title":      {"ru": "📈 <b>производительность · {window}</b>", "en": "📈 <b>performance · {window}</b>"},
	"admin_circuit":         {"ru": "Octave remote circuit: <code>{state}</code>", "en": "Octave remote circuit: <code>{state}</code>"},
	"admin_more_stages":     {"ru": "… ещё этапов: {count}", "en": "… {count} more stages"},
	"admin_dropped_samples": {"ru": "потеряно samples: {count}", "en": "dropped samples: {count}"},
	"admin_perf_summary":    {"ru": "cache hits: {hits} ({ratio}%) · remote URL: {remote_ok}/{remote_total}", "en": "cache hits: {hits} ({ratio}%) · remote URL: {remote_ok}/{remote_total}"},
	"admin_role_owner":      {"ru": "владелец", "en": "owner"},
	"admin_role_admin":      {"ru": "администратор", "en": "administrator"},
	"admin_role_user":       {"ru": "пользователь", "en": "user"},
	"admin_user_line":       {"ru": "роль: {role}\nизвестен боту: {known}\nязык: {lang}\nзагрузок: {downloads}\nзаблокирован: {banned}", "en": "role: {role}\nknown to bot: {known}\nlanguage: {lang}\ndownloads: {downloads}\nbanned: {banned}"},
	"admin_reason_line":     {"ru": "причина: {reason}", "en": "reason: {reason}"},
	"internal_id_error":     {"ru": "не удалось создать идентификатор загрузки", "en": "failed to create a download identifier"},
	"preview_error":         {"ru": "❌ не удалось прочитать ссылку: {error}", "en": "❌ failed to inspect the link: {error}"},
	"preview_tracks":        {"ru": "🎶 треков: <b>{count}</b>", "en": "🎶 tracks: <b>{count}</b>"},
	"preview_sizes":         {"ru": "📦 примерно: MP3 128 — {size128}, MP3 320 — {size320}", "en": "📦 estimate: MP3 128 — {size128}, MP3 320 — {size320}"},
	"choose_range":          {"ru": "\nвыбери диапазон:", "en": "\nchoose a range:"},
	"choose_format":         {"ru": "\nвыбери формат и качество:", "en": "\nchoose format and quality:"},
	"selected_range":        {"ru": "✅ выбраны треки <b>{start}–{end}</b>. теперь выбери формат:", "en": "✅ tracks <b>{start}–{end}</b> selected. now choose a format:"},
	"btn_range_all":         {"ru": "🎶 весь плейлист", "en": "🎶 entire playlist"},
	"btn_range_10":          {"ru": "🔟 первые 10", "en": "🔟 first 10"},
	"btn_range_25":          {"ru": "2️⃣5️⃣ первые 25", "en": "2️⃣5️⃣ first 25"},
	"btn_range_limit":       {"ru": "📚 первые {limit}", "en": "📚 first {limit}"},
	"btn_range_custom":      {"ru": "🎵 {start}–{end}", "en": "🎵 {start}–{end}"},
	"queue_full":            {"ru": "⚠️ бот сейчас занят. попробуй немного позже.", "en": "⚠️ the bot is busy. please try again later."},
	"queued":                {"ru": "🕒 <b>загрузка в очереди</b>\nпозиция: {position}", "en": "🕒 <b>download queued</b>\nposition: {position}"},
	"stage_cache":           {"ru": "🔎 <b>проверяю кэш и быстрый путь…</b>", "en": "🔎 <b>checking cache and fast path…</b>"},
	"stage_source":          {"ru": "⬇️ <b>скачиваю источник…</b>", "en": "⬇️ <b>downloading source…</b>"},
	"stage_retry":           {"ru": "🔄 <b>повторяю загрузку · попытка {attempt}</b>\nсохранено: {bytes}", "en": "🔄 <b>retrying download · attempt {attempt}</b>\nsaved: {bytes}"},
	"stage_prepare":         {"ru": "⚙️ <b>подготавливаю аудио…</b>", "en": "⚙️ <b>preparing audio…</b>"},
	"stage_telegram_remote": {"ru": "☁️ <b>Telegram забирает файл по прямой ссылке…</b>", "en": "☁️ <b>Telegram is fetching the remote file…</b>"},
	"upload_progress":       {"ru": "⬆️ <b>отправляю в Telegram: {percent}%</b>\n{done} / {total}", "en": "⬆️ <b>uploading to Telegram: {percent}%</b>\n{done} / {total}"},
	"archive_queued":        {"ru": "🕒 <b>архивация в очереди</b>\nпозиция: {position}", "en": "🕒 <b>archiving queued</b>\nposition: {position}"},
	"rate_limited":          {"ru": "⏳ слишком много запросов. попробуй через {seconds} сек.", "en": "⏳ too many requests. try again in {seconds} sec."},
	"user_download_active":  {"ru": "⏳ у тебя уже есть активная или ожидающая загрузка.", "en": "⏳ you already have an active or queued download."},
	"looks_like_playlist": {
		"ru": "<b>похоже, это плейлист.</b>\n\n",
		"en": "<b>this looks like a playlist.</b>\n\n",
	},
	"link_received": {
		"ru": "ссылка получена!\n{info}\nвыбери формат и качество:",
		"en": "link received!\n{info}\nchoose a format and quality:",
	},
	"btn_mp3_best": {"ru": "🎵 MP3 (лучшее качество)", "en": "🎵 MP3 (best quality)"},
	"btn_mp3_128":  {"ru": "🎵 MP3 (128 kbps)", "en": "🎵 MP3 (128 kbps)"},
	"btn_mp3_320":  {"ru": "🎵 MP3 (320 kbps)", "en": "🎵 MP3 (320 kbps)"},
	"btn_flac":     {"ru": "🎼 FLAC", "en": "🎼 FLAC"},
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
		"en": "⚠️ this playlist has {count} tracks. the limit per download is {limit}.",
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
		"ru": "❌ ничего не скачано.\nошибка: {error}\n\nесли она повторяется, отправь текст ошибки администратору: @dol6oe6xd",
		"en": "❌ nothing was downloaded.\nerror: {error}\n\nif this keeps happening, send the error text to the administrator: @dol6oe6xd",
	},
	"unknown_error": {"ru": "неизвестная ошибка", "en": "unknown error"},
	"downloaded_count": {
		"ru": "📦 скачано треков: <b>{count}</b>. как отправить?",
		"en": "📦 downloaded tracks: <b>{count}</b>. how should i send them?",
	},
	"choose_delivery": {
		"ru": "📦 <b>как отправить выбранный плейлист?</b>\nспособ нужно выбрать до загрузки, чтобы бот не держал готовые файлы в ожидании.",
		"en": "📦 <b>how should i send the selected playlist?</b>\nchoose before downloading so the bot does not keep completed files waiting on disk.",
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
		"ru": "📊 <b>статистика</b>\nпользователи: {users}\nтреки в кэше: {cached}\nуспешно доставлено: {ok}\nчастично: {partial}\nошибки: {failed}\nотменено: {cancelled}\nошибки cookies: {cookies}\nпопадания в кэш: {hits}\nпоиски: {searches}\nограничено rate limit: {limited}\nотклонено очередью: {rejected}",
		"en": "📊 <b>statistics</b>\nusers: {users}\ndelivered successfully: {ok}\npartially delivered: {partial}\ncached tracks: {cached}\nfailed: {failed}\ncancelled: {cancelled}\ncookie errors: {cookies}\ncache hits: {hits}\nsearches: {searches}\nrate limited: {limited}\nrejected by queue: {rejected}",
	},
	"admin_status": {
		"ru": "🟢 <b>бот работает</b>\nзагрузки: {downloads_active}/{downloads_capacity}, в очереди {downloads_waiting}\nпоиск: {lookups_active}/{lookups_capacity}, в очереди {lookups_waiting}\nархивация: {archives_active}/{archives_capacity}, в очереди {archives_waiting}\nактивных пользователей: {active_users}",
		"en": "🟢 <b>bot is running</b>\ndownloads: {downloads_active}/{downloads_capacity}, queued {downloads_waiting}\nsearches: {lookups_active}/{lookups_capacity}, queued {lookups_waiting}\narchives: {archives_active}/{archives_capacity}, queued {archives_waiting}\nactive users: {active_users}",
	},
	"admin_stats_error": {
		"ru": "❌ не удалось получить статистику: {error}",
		"en": "❌ failed to load statistics: {error}",
	},
	"admin_disk_warning": {
		"ru": "⚠️ мало места на диске: свободно {free}.",
		"en": "⚠️ disk space is running low: {free} available.",
	},
	"admin_cookie_warning": {
		"ru": "⚠️ YouTube отклоняет cookies или требует bot-check. обнови cookies.txt и проверь /status.",
		"en": "⚠️ YouTube is rejecting the cookies or requiring a bot check. refresh cookies.txt and check /status.",
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

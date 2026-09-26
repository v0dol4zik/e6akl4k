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
		"ru": "🎧 <b>что умеет бот</b>\n\n🔎 <b>поиск по названию</b>\nотправь в личный чат исполнителя и название:\n<code>Daft Punk — Get Lucky</code>\nя поищу трек на YouTube и покажу подходящие варианты. выбери нужный трек, затем формат и качество.\nможно также переслать мне аудиофайл — я поищу его по исполнителю и названию из тегов.\n\n🔗 <b>загрузка по ссылке</b>\nпришли ссылку YouTube, YouTube Music, SoundCloud, Bandcamp, VK, Mixcloud или Audiomack. до загрузки я покажу название, длительность и примерный размер. несколько ссылок на треки в одном сообщении (до 5) скачиваются вместе с одним форматом.\n\n🎶 <b>ссылки музыкальных сервисов</b>\nSpotify, Apple Music, Deezer, Tidal и Яндекс Музыка используются для поиска по метаданным. я покажу похожие версии с YouTube — проверь и подтверди совпадение.\n\n📚 <b>плейлисты и альбомы</b>\nможно выбрать весь плейлист или альбом в пределах лимита, первые 10/25 треков или показанный диапазон. для большого набора способ отправки выбирается до загрузки: отдельные файлы приходят частями, ZIP автоматически делится.\n\n🌐 <b>inline-поиск в любом чате</b>\nнапиши:\n<code>@{username} название песни</code>\nвыбери результат — аудио появится прямо в этом чате.\n\n📝 <b>треклист и обложка</b>\n/export и /cover со ссылкой (или ответом на сообщение со ссылкой) присылают список «исполнитель - название» и обложку релиза. те же кнопки есть под превью ссылки. плейлисты YouTube, Deezer и Яндекс Музыки выгружаются целиком, до 1000 треков. идея экспорта — @yamusic_export_bot\n\n⚙️ <b>форматы</b>\nMP3 128/320/VBR и M4A отправляются как музыка. FLAC и OGG отправляются файлами; FLAC не улучшает качество исходника. если загрузка попала в очередь, я покажу её позицию. активную загрузку можно отменить кнопкой. повторные запросы обслуживаются из кэша автоматически.\n\n/settings — формат и качество по умолчанию, чтобы не выбирать каждый раз\n/history — последние загрузки: получить трек из кэша повторно\n/language — сменить язык",
		"en": "🎧 <b>what the bot can do</b>\n\n🔎 <b>search by title</b>\nsend an artist and title in our private chat:\n<code>Daft Punk — Get Lucky</code>\ni search YouTube and show the matching versions. choose the right track, then its format and quality.\nyou can also forward me an audio file and i'll search by the performer and title from its tags.\n\n🔗 <b>download from a link</b>\nsend a YouTube, YouTube Music, SoundCloud, Bandcamp, VK, Mixcloud, or Audiomack link. before downloading, i'll show its title, duration, and estimated size. several track links in one message (up to 5) are downloaded together with one format.\n\n🎶 <b>music-service links</b>\nSpotify, Apple Music, Deezer, Tidal, and Yandex Music links are used to search by metadata. i'll show matching YouTube versions so you can verify and confirm the right one.\n\n📚 <b>playlists and albums</b>\nchoose the entire playlist or album within the configured limit, the first 10/25 tracks, or one of the displayed ranges. for large sets, choose delivery before downloading: individual files arrive in batches and ZIP archives are split automatically.\n\n🌐 <b>inline search from any chat</b>\ntype:\n<code>@{username} song name</code>\nchoose a result and the audio will appear directly in that chat.\n\n📝 <b>tracklist and cover</b>\n/export and /cover with a link (or as a reply to a message with a link) send «artist - title» lines and the release cover. the same buttons appear under a link preview. YouTube, Deezer, and Yandex Music playlists are exported in full, up to 1000 tracks. export idea by @yamusic_export_bot\n\n⚙️ <b>formats</b>\nMP3 128/320/VBR and M4A are sent as music. FLAC and OGG are sent as files; converting to FLAC cannot improve the source. if a download is queued, i'll show its position. an active download can be cancelled with its button. repeated requests are served from cache automatically.\n\n/settings — default format and quality so you don't have to choose every time\n/history — recent downloads: get a track from cache again\n/language — change language",
	},
	"invalid_link": {
		"ru": "в группе отправь мне ссылку на трек или плейлист. для поиска по названию используй <code>@{username} название песни</code> или открой личный чат с ботом.",
		"en": "in a group, send me a track or playlist link. to search by title, use <code>@{username} song name</code> or open a private chat with the bot.",
	},
	"analyzing":          {"ru": "🔎 <b>анализирую ссылку…</b>", "en": "🔎 <b>analyzing the link…</b>"},
	"searching":          {"ru": "🔎 <b>ищу подходящие треки…</b>", "en": "🔎 <b>searching for matching tracks…</b>"},
	"search_too_short":   {"ru": "введи хотя бы три символа для поиска.", "en": "enter at least three characters to search."},
	"searching_by_audio": {"ru": "🎧 <b>ищу этот трек по тегам аудио…</b>", "en": "🎧 <b>searching for this track by its audio tags…</b>"},
	"audio_no_metadata":  {"ru": "у этого аудио нет исполнителя, названия и имени файла — не по чему искать. отправь исполнителя и название текстом.", "en": "this audio has no performer, title, or file name, so there is nothing to search by. send the artist and title as text instead."},
	"nothing_found":      {"ru": "ничего не найдено.", "en": "nothing found."},
	"search_results":     {"ru": "🎵 <b>результаты поиска</b>\nвыбери нужный трек. после этого можно будет выбрать формат и качество:", "en": "🎵 <b>search results</b>\nchoose the right track. you can select its format and quality next:"},
	"resolved_results":   {"ru": "🔗 <b>нашёл похожие версии</b>\nаудио будет загружено из выбранного варианта YouTube, поэтому проверь название, исполнителя и длительность:", "en": "🔗 <b>i found similar versions</b>\naudio will be downloaded from the selected YouTube result, so check its title, artist, and duration:"},
	"command_start":      {"ru": "запустить бота", "en": "start the bot"},
	"command_help":       {"ru": "возможности и примеры", "en": "features and examples"},
	"command_language":   {"ru": "сменить язык", "en": "change language"},
	"command_settings":   {"ru": "формат и качество по умолчанию", "en": "default format and quality"},
	"command_history":    {"ru": "последние загрузки", "en": "recent downloads"},
	"command_export":     {"ru": "треклист по ссылке", "en": "tracklist from a link"},
	"command_cover":      {"ru": "обложка по ссылке", "en": "cover from a link"},
	"history_title":      {"ru": "🕘 <b>последние загрузки</b>\nнажми на трек, чтобы получить его из кэша ещё раз:", "en": "🕘 <b>recent downloads</b>\ntap a track to get it from cache again:"},
	"history_empty":      {"ru": "история пуста. пришли ссылку или название трека — загрузка появится здесь.", "en": "history is empty. send a link or a track title and the download will show up here."},
	"history_expired":    {"ru": "этот трек уже удалён из кэша. пришли ссылку ещё раз, и я загружу его заново.", "en": "this track is no longer cached. send the link again and i'll download it anew."},
	"history_cleared":    {"ru": "🧹 история очищена.", "en": "🧹 history cleared."},
	"btn_history_clear":  {"ru": "🧹 очистить историю", "en": "🧹 clear history"},
	"btn_cookie_check":   {"ru": "🔎 проверить", "en": "🔎 check"},
	"settings_title":     {"ru": "⚙️ <b>настройки</b>", "en": "⚙️ <b>settings</b>"},
	"settings_current": {
		"ru": "формат по умолчанию: <b>{value}</b>\nвыбери новый вариант — он будет применяться к трекам и альбомам без вопроса о формате:",
		"en": "default format: <b>{value}</b>\nchoose a new option — it will apply to tracks and albums without asking for a format:",
	},
	"settings_ask_each_time": {
		"ru": "❓ спрашивать каждый раз",
		"en": "❓ ask each time",
	},
	"settings_saved": {"ru": "✅ сохранено: <b>{value}</b>", "en": "✅ saved: <b>{value}</b>"},
	"settings_applied_hint": {
		"ru": "формат: {value} · изменить: /settings",
		"en": "format: {value} · change: /settings",
	},
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
	"btn_mp3_best":        {"ru": "🎵 MP3 (лучшее качество)", "en": "🎵 MP3 (best quality)"},
	"btn_mp3_128":         {"ru": "🎵 MP3 (128 kbps)", "en": "🎵 MP3 (128 kbps)"},
	"btn_mp3_320":         {"ru": "🎵 MP3 (320 kbps)", "en": "🎵 MP3 (320 kbps)"},
	"btn_flac":            {"ru": "🎼 FLAC", "en": "🎼 FLAC"},
	"btn_m4a":             {"ru": "🎤 M4A (AAC)", "en": "🎤 M4A (AAC)"},
	"btn_ogg":             {"ru": "🎧 OGG Vorbis", "en": "🎧 OGG Vorbis"},
	"btn_cancel":          {"ru": "❌ отмена", "en": "❌ cancel"},
	"btn_export":          {"ru": "📝 треклист текстом", "en": "📝 tracklist as text"},
	"btn_cover":           {"ru": "🖼 обложка", "en": "🖼 cover"},
	"export_single":       {"ru": "📝 <code>{line}</code>\n\n💡 идея экспорта — @yamusic_export_bot", "en": "📝 <code>{line}</code>\n\n💡 export idea by @yamusic_export_bot"},
	"export_caption":      {"ru": "📝 <b>{name}</b>\nтреков: {count}\n\n💡 идея экспорта — @yamusic_export_bot", "en": "📝 <b>{name}</b>\ntracks: {count}\n\n💡 export idea by @yamusic_export_bot"},
	"cover_caption":       {"ru": "🖼 <b>{name}</b>\n\n💡 идея экспорта — @yamusic_export_bot", "en": "🖼 <b>{name}</b>\n\n💡 export idea by @yamusic_export_bot"},
	"export_count_capped": {"ru": "{count} из {total}", "en": "{count} of {total}"},
	"export_untitled":     {"ru": "без названия", "en": "untitled"},
	"export_working":      {"ru": "📝 <b>собираю треклист…</b>", "en": "📝 <b>collecting the tracklist…</b>"},
	"cover_working":       {"ru": "🖼 <b>ищу обложку…</b>", "en": "🖼 <b>looking for the cover…</b>"},
	"export_usage": {
		"ru": "📝 пришли ссылку после команды: <code>/export ссылка</code>\nили ответь командой /export на сообщение со ссылкой. треклист придёт строками «исполнитель - название», плейлисты и альбомы — файлом .txt.\nподдерживаются YouTube, YouTube Music, SoundCloud, Bandcamp, Deezer и Яндекс Музыка; у Spotify, Apple Music и Tidal — только отдельные треки.",
		"en": "📝 send a link after the command: <code>/export link</code>\nor reply with /export to a message that contains a link. the tracklist arrives as «artist - title» lines; playlists and albums come as a .txt file.\nsupported: YouTube, YouTube Music, SoundCloud, Bandcamp, Deezer, and Yandex Music; for Spotify, Apple Music, and Tidal only single tracks.",
	},
	"cover_usage": {
		"ru": "🖼 пришли ссылку после команды: <code>/cover ссылка</code>\nили ответь командой /cover на сообщение со ссылкой. обложка придёт файлом в исходном качестве.\nподдерживаются YouTube, YouTube Music, SoundCloud, Bandcamp, Deezer и Яндекс Музыка.",
		"en": "🖼 send a link after the command: <code>/cover link</code>\nor reply with /cover to a message that contains a link. the cover arrives as a file in its original quality.\nsupported: YouTube, YouTube Music, SoundCloud, Bandcamp, Deezer, and Yandex Music.",
	},
	"export_unsupported": {
		"ru": "⚠️ у Spotify, Apple Music и Tidal нельзя получить состав плейлиста или альбома — доступны только отдельные треки. пришли ссылку YouTube Music, Deezer или Яндекс Музыки.",
		"en": "⚠️ Spotify, Apple Music, and Tidal don't expose playlist or album contents, only single tracks. send a YouTube Music, Deezer, or Yandex Music link instead.",
	},
	"cover_unsupported": {
		"ru": "⚠️ обложки Spotify, Apple Music и Tidal боту недоступны. пришли ссылку YouTube Music, Deezer или Яндекс Музыки на тот же релиз.",
		"en": "⚠️ covers from Spotify, Apple Music, and Tidal aren't available to the bot. send a YouTube Music, Deezer, or Yandex Music link to the same release.",
	},
	"export_empty":             {"ru": "⚠️ по этой ссылке не нашлось треков.", "en": "⚠️ no tracks were found at this link."},
	"collection_kind_playlist": {"ru": "плейлист", "en": "a playlist"},
	"collection_kind_album":    {"ru": "альбом", "en": "an album"},
	"collection_kind_artist":   {"ru": "страница исполнителя", "en": "an artist page"},
	"music_collection": {
		"ru": "⚠️ это {kind} {service}, а не отдельный трек. целиком такие ссылки не скачиваются: бот ищет треки музыкальных сервисов на YouTube по одному.",
		"en": "⚠️ this is {kind} from {service}, not a single track. such links aren't downloaded as a whole: the bot finds music-service tracks on YouTube one at a time.",
	},
	"music_collection_hint_export": {
		"ru": "📝 кнопка «треклист текстом» пришлёт весь список, из него можно присылать нужные песни по одному.",
		"en": "📝 the \"tracklist as text\" button sends the whole list, so you can send the songs you need one by one.",
	},
	"music_collection_hint": {
		"ru": "пришли ссылку на отдельный трек или название песни. плейлист целиком можно скачать по ссылке YouTube Music.",
		"en": "send a link to a single track or a song title. a whole playlist can be downloaded from a YouTube Music link.",
	},
	"music_collection_read_error": {"ru": "❌ не удалось прочитать состав: <code>{error}</code>", "en": "❌ couldn't read the contents: <code>{error}</code>"},
	"music_service_blocked": {
		"ru": "⚠️ ссылки {service} сейчас не открываются: сервис не пускает запросы из страны, где работает бот. пришли название песни или ссылку YouTube.",
		"en": "⚠️ {service} links can't be opened right now: the service blocks requests from the country where the bot runs. send the song title or a YouTube link instead.",
	},
	"link_generic_page": {
		"ru": "⚠️ по ссылке открылась общая страница {service}, а не трек. скопируй ссылку на сам трек через «поделиться» или пришли его название.",
		"en": "⚠️ the link opened the {service} home page, not a track. copy the link of the track itself with \"share\" or send its title.",
	},
	"cover_not_found":        {"ru": "⚠️ у этой ссылки нет обложки.", "en": "⚠️ this link has no cover."},
	"export_timeout":         {"ru": "⌛ сервис отвечает слишком долго. попробуй ещё раз позже.", "en": "⌛ the service is taking too long to respond. please try again later."},
	"export_error":           {"ru": "❌ не удалось собрать треклист: <code>{error}</code>", "en": "❌ couldn't collect the tracklist: <code>{error}</code>"},
	"cover_error":            {"ru": "❌ не удалось получить обложку: <code>{error}</code>", "en": "❌ couldn't get the cover: <code>{error}</code>"},
	"command_lastfm":         {"ru": "скробблы и топ с last.fm", "en": "scrobbles and top tracks from last.fm"},
	"lastfm_help":            {"ru": "🎧 <code>/lastfm ник</code> — последние скробблы, любимые треки и топ с last.fm; любой трек из списка найду на YouTube", "en": "🎧 <code>/lastfm name</code> — recent scrobbles, loved and top tracks from last.fm; any track from a list is found on YouTube"},
	"lastfm_usage":           {"ru": "🎧 <b>last.fm</b>\nпривяжи профиль: <code>/lastfm ник</code> или ссылка last.fm/user/ник — и смотри последние скробблы, любимые треки и топ, а любой трек из списка найду на YouTube.\nотвязать профиль: <code>/lastfm off</code>", "en": "🎧 <b>last.fm</b>\nlink your profile: <code>/lastfm name</code> or a last.fm/user/name link — then browse recent scrobbles, loved and top tracks, and any track from a list is found on YouTube.\nunlink: <code>/lastfm off</code>"},
	"lastfm_disabled":        {"ru": "интеграция с last.fm не настроена на этом боте.", "en": "last.fm integration isn't configured on this bot."},
	"lastfm_bad_name":        {"ru": "это не похоже на ник last.fm. пример: <code>/lastfm rj</code> или <code>/lastfm last.fm/user/rj</code>", "en": "that doesn't look like a last.fm username. example: <code>/lastfm rj</code> or <code>/lastfm last.fm/user/rj</code>"},
	"lastfm_checking":        {"ru": "🔎 проверяю профиль last.fm…", "en": "🔎 checking the last.fm profile…"},
	"lastfm_linked":          {"ru": "✅ профиль last.fm <b>{user}</b> привязан.", "en": "✅ last.fm profile <b>{user}</b> linked."},
	"lastfm_unlinked":        {"ru": "🔌 профиль last.fm отвязан. привязать снова: <code>/lastfm ник</code>", "en": "🔌 last.fm profile unlinked. link again: <code>/lastfm name</code>"},
	"lastfm_menu":            {"ru": "🎧 last.fm · <b>{user}</b>\nвыбери список, а потом номер трека — найду его на YouTube.", "en": "🎧 last.fm · <b>{user}</b>\npick a list, then a track number, and i'll find it on YouTube."},
	"lastfm_loading":         {"ru": "⏳ загружаю список с last.fm…", "en": "⏳ loading the list from last.fm…"},
	"lastfm_pick_hint":       {"ru": "нажми номер — найду трек на YouTube.", "en": "tap a number to find the track on YouTube."},
	"lastfm_name_recent":     {"ru": "последние скробблы", "en": "recent scrobbles"},
	"lastfm_name_loved":      {"ru": "любимые треки", "en": "loved tracks"},
	"lastfm_name_top_7day":   {"ru": "топ за неделю", "en": "top of the week"},
	"lastfm_name_top_1month": {"ru": "топ за месяц", "en": "top of the month"},
	"lastfm_empty":           {"ru": "тут пока пусто: last.fm не вернул ни одного трека.", "en": "nothing here yet: last.fm returned no tracks."},
	"lastfm_not_found":       {"ru": "пользователь last.fm <b>{user}</b> не найден. проверь ник.", "en": "last.fm user <b>{user}</b> not found. check the name."},
	"lastfm_private":         {"ru": "история прослушиваний <b>{user}</b> скрыта настройками приватности last.fm.", "en": "<b>{user}</b> hides their listening history in last.fm privacy settings."},
	"lastfm_rate_limited":    {"ru": "last.fm просит подождать. попробуй через минуту.", "en": "last.fm asks to slow down. try again in a minute."},
	"lastfm_timeout":         {"ru": "last.fm не ответил вовремя. попробуй ещё раз чуть позже.", "en": "last.fm didn't answer in time. try again a bit later."},
	"lastfm_error":           {"ru": "❌ не получилось получить данные last.fm: <code>{error}</code>", "en": "❌ couldn't get data from last.fm: <code>{error}</code>"},
	"btn_lastfm_recent":      {"ru": "🕘 последние {count}", "en": "🕘 recent {count}"},
	"btn_lastfm_loved":       {"ru": "❤️ любимые", "en": "❤️ loved"},
	"btn_lastfm_top_7day":    {"ru": "🔥 топ недели", "en": "🔥 top of the week"},
	"btn_lastfm_top_1month":  {"ru": "🏆 топ месяца", "en": "🏆 top of the month"},
	"btn_lastfm_unlink":      {"ru": "🔌 отвязать профиль", "en": "🔌 unlink profile"},
	"btn_lastfm_txt":         {"ru": "📄 списком .txt", "en": "📄 as .txt"},
	"btn_lastfm_menu":        {"ru": "↩️ меню", "en": "↩️ menu"},
	"command_notify":         {"ru": "оповещения от бота: вкл/выкл", "en": "bot notifications on/off"},
	"command_msgall":         {"ru": "оповещение всем пользователям", "en": "notify all users"},
	"command_msg":            {"ru": "сообщение пользователю", "en": "message a user"},
	"notify_help":            {"ru": "🔔 /notify — включить или выключить оповещения от бота", "en": "🔔 /notify — turn bot notifications on or off"},
	"notify_status_on":       {"ru": "🔔 <b>оповещения включены</b>\nиногда бот присылает новости и важные сообщения. выключить: /notify off или кнопка ниже.", "en": "🔔 <b>notifications are on</b>\nthe bot occasionally sends news and important messages. turn them off with /notify off or the button below."},
	"notify_status_off":      {"ru": "🔕 <b>оповещения выключены</b>\nрассылки от бота приходить не будут. включить: /notify on или кнопка ниже.", "en": "🔕 <b>notifications are off</b>\nthe bot won't send you announcements. turn them on with /notify on or the button below."},
	"btn_notify_off":         {"ru": "🔕 отключить оповещения", "en": "🔕 turn off notifications"},
	"btn_notify_on":          {"ru": "🔔 включить оповещения", "en": "🔔 turn on notifications"},
	"notice_preview":         {"ru": "👀 так рассылку увидят пользователи:", "en": "👀 this is how users will see the notice:"},
	"notice_confirm":         {"ru": "📢 <b>разослать сообщение выше?</b>\nполучателей: {count}\nотключили оповещения: {muted}", "en": "📢 <b>send the message above to everyone?</b>\nrecipients: {count}\nmuted notifications: {muted}"},
	"btn_notice_send":        {"ru": "✅ отправить", "en": "✅ send"},
	"btn_notice_cancel":      {"ru": "✖️ отмена", "en": "✖️ cancel"},
	"notice_cancelled":       {"ru": "✖️ рассылка отменена.", "en": "✖️ notice cancelled."},
	"notice_no_recipients":   {"ru": "📭 некому отправлять: остальные пользователи отключили оповещения или ещё не выбрали язык в боте.", "en": "📭 no one to send to: other users muted notifications or haven't picked a language in the bot yet."},
	"notice_busy":            {"ru": "⏳ другая рассылка ещё идёт. дождись её отчёта и нажми «отправить» снова.", "en": "⏳ another notice is still being sent. wait for its report and press send again."},
	"notice_progress":        {"ru": "📤 рассылаю… {done} из {total}", "en": "📤 sending… {done} of {total}"},
	"notice_done_title":      {"ru": "✅ <b>рассылка завершена</b>", "en": "✅ <b>notice sent</b>"},
	"notice_stopped_title":   {"ru": "⏹ <b>рассылка прервана перезапуском бота</b>", "en": "⏹ <b>the notice was interrupted by a bot restart</b>"},
	"notice_report":          {"ru": "доставлено: {delivered} из {total}\nзаблокировали бота или недоступны: {unreachable}\nошибок: {failed}", "en": "delivered: {delivered} of {total}\nblocked the bot or unreachable: {unreachable}\nerrors: {failed}"},
	"notice_sent_one":        {"ru": "✅ сообщение доставлено пользователю <code>{id}</code>.", "en": "✅ message delivered to <code>{id}</code>."},
	"notice_muted_one":       {"ru": "🔕 пользователь <code>{id}</code> отключил оповещения — сообщение не отправлено.", "en": "🔕 user <code>{id}</code> muted notifications, so the message wasn't sent."},
	"notice_unreachable_one": {"ru": "⚠️ не доставлено: пользователь <code>{id}</code> заблокировал бота или ещё не запускал его.", "en": "⚠️ not delivered: user <code>{id}</code> blocked the bot or hasn't started it yet."},
	"notice_failed":          {"ru": "❌ не удалось отправить сообщение: <code>{error}</code>", "en": "❌ couldn't send the message: <code>{error}</code>"},
	"btn_zip_yes":            {"ru": "📁 ZIP-архивом (авторазбиение)", "en": "📁 ZIP archive (auto-split)"},
	"btn_zip_no":             {"ru": "🎵 отправить по одному", "en": "🎵 send one by one"},
	"cancelled":              {"ru": "❌ отменено.", "en": "❌ cancelled."},
	"data_expired":           {"ru": "⚠️ данные устарели.", "en": "⚠️ this data has expired."},
	"link_expired":           {"ru": "⚠️ ссылка устарела.", "en": "⚠️ this link has expired."},
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
	"choose_delivery_batch": {
		"ru": "📦 <b>как отправить {count} треков из сообщения?</b>\nспособ нужно выбрать до загрузки, чтобы бот не держал готовые файлы в ожидании.",
		"en": "📦 <b>how should i send the {count} tracks from the message?</b>\nchoose before downloading so the bot does not keep completed files waiting on disk.",
	},
	"batch_preview": {
		"ru": "🔗 <b>ссылок в сообщении: {count}</b>\nскачаю их вместе, формат один на все:",
		"en": "🔗 <b>links in the message: {count}</b>\ni'll download them together with one format for all:",
	},
	"batch_title":        {"ru": "{count} треков", "en": "{count} tracks"},
	"batch_no_playlists": {"ru": "⚠️ в одном сообщении можно скачать только отдельные треки. плейлист или альбом пришли отдельной ссылкой.", "en": "⚠️ several links in one message must all be single tracks. send a playlist or album as a separate link."},
	"batch_limit":        {"ru": "⚠️ за один раз обрабатываю не более {max} ссылок — беру первые {max}.", "en": "⚠️ i handle at most {max} links at once — taking the first {max}."},
	"batch_link_failed":  {"ru": "⚠️ <code>{url}</code> — пропускаю: {error}", "en": "⚠️ <code>{url}</code> — skipped: {error}"},
	"batch_search_only":  {"ru": "ссылки музыкальных сервисов ищутся по названию, пришли такую ссылку отдельно", "en": "music-service links are searched by title, send this link separately"},
	"batch_progress": {
		"ru": "⬇️ <b>скачиваю ссылку {current}/{total}…</b>\n{title}",
		"en": "⬇️ <b>downloading link {current}/{total}…</b>\n{title}",
	},
	"batch_partial": {
		"ru": "⚠️ готово частично: отправлено {ok}, с ошибкой {failed}.",
		"en": "⚠️ partially done: sent {ok}, failed {failed}.",
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
		"ru": "⚠️ cookies YouTube больше не работают: YouTube отклонил загрузку, и проверка входа с этими cookies не прошла. обнови cookies.txt и проверь /status.",
		"en": "⚠️ the YouTube cookies no longer work: YouTube rejected a download and a login check with these cookies failed. refresh cookies.txt and check /status.",
	},
	"admin_cookie_degraded": {
		"ru": "⚠️ YouTube отдаёт 403 с текущими cookies, и проверка входа с ними не прошла. загрузки идут без cookies, треки 18+ и приватные недоступны — обнови cookies.txt.",
		"en": "⚠️ YouTube returns 403 with the current cookies and a login check with them failed. downloads continue without cookies, so age-restricted and private tracks are unavailable — refresh cookies.txt.",
	},
	"error_report_title":   {"ru": "🐞 <b>ошибка у пользователя</b> · <code>{stage}</code>", "en": "🐞 <b>user-facing error</b> · <code>{stage}</code>"},
	"error_report_user":    {"ru": "пользователь: {user}", "en": "user: {user}"},
	"error_report_chat":    {"ru": "чат: {chat}", "en": "chat: {chat}"},
	"error_report_url":     {"ru": "ссылка: {url}", "en": "link: {url}"},
	"error_report_query":   {"ru": "запрос: {query}", "en": "query: {query}"},
	"error_report_format":  {"ru": "формат: {format}", "en": "format: {format}"},
	"error_report_error":   {"ru": "ошибка: <code>{error}</code>", "en": "error: <code>{error}</code>"},
	"error_report_repeats": {"ru": "ещё таких же за прошлые {minutes} мин: {count}", "en": "same error {count} more times in the previous {minutes} min"},
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

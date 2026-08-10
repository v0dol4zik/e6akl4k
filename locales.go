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
		"ru": "👋 <b>привет! я музыкальный бот e6akl4k - (ебаклак)</b>\n\nпришли ссылку — выберу формат и скачаю.",
		"en": "👋 <b>hi! I'm a music bot.</b>\n\nsend me a link and I'll let you pick a format to download.",
	},
	"help": {
		"ru": "отправь мне ссылку на трек или плейлист, и я пришлю тебе аудиофайл. в любом чате можно написать `@username название песни` для inline-поиска.",
		"en": "send me a track or playlist link and I'll return the audio. In any chat, type `@username song name` to search in inline mode.",
	},
	"invalid_link": {
		"ru": "пришли мне корректную ссылку на трек или плейлист!",
		"en": "please send me a valid link to a track or playlist!",
	},
	"looks_like_playlist": {
		"ru": "<b>видется мне, что это плейлист.</b>\n\n",
		"en": "<b>looks like this is a playlist.</b>\n\n",
	},
	"link_received": {
		"ru": "ссылка получена!\n{info}\nвыбери формат и качество:",
		"en": "link received!\n{info}\nchoose a format and quality:",
	},
	"btn_mp3_best": {"ru": "🎵 MP3 (лучшее качество)", "en": "🎵 MP3 (best quality)"},
	"btn_mp3_128":  {"ru": "🎵 MP3 (128 kbps)", "en": "🎵 MP3 (128 kbps)"},
	"btn_mp3_320":  {"ru": "🎵 MP3 (320 kbps)", "en": "🎵 MP3 (320 kbps)"},
	"btn_flac":     {"ru": "🎼 FLAC (без потерь)", "en": "🎼 FLAC (lossless)"},
	"btn_m4a":      {"ru": "🎤 M4A (AAC)", "en": "🎤 M4A (AAC)"},
	"btn_ogg":      {"ru": "🎧 OGG Vorbis", "en": "🎧 OGG Vorbis"},
	"btn_cancel":   {"ru": "❌ отмена", "en": "❌ cancel"},
	"btn_zip_yes":  {"ru": "📁 одним ZIP-архивом", "en": "📁 as a single ZIP archive"},
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
		"ru": "❌ ничего не скачано. ошибка: {error}\nпожалуйста! пришли эту ошибку админу бота чтобы он мог ее исправить - @dol6oe6xd",
		"en": "❌ nothing was downloaded. Error: {error}",
	},
	"unknown_error": {"ru": "неизвестная ошибка", "en": "unknown error"},
	"downloaded_count": {
		"ru": "📦 скачано треков: <b>{count}</b>. как отправить?",
		"en": "📦 downloaded tracks: <b>{count}</b>. how should I send them?",
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
	"zip_sent": {"ru": "✅ готово! архив отправлен.", "en": "✅ done! archive sent."},
	"zip_error": {
		"ru": "❌ ошибка при создании архива: <code>{error}</code>",
		"en": "❌ error while creating the archive: <code>{error}</code>",
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

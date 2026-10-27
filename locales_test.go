package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestTranslationsCoverEveryLanguage(t *testing.T) {
	for key, translations := range texts {
		for _, lang := range languageOrder {
			if strings.TrimSpace(translations[lang]) == "" {
				t.Errorf("translation %q is missing language %q", key, lang)
			}
		}
	}
}

func TestTranslationsUseLowercaseStyle(t *testing.T) {
	protected := []string{
		"AAC", "Apple", "Audiomack", "Bandcamp", "Daft", "Deezer", "FLAC", "M4A",
		"Mixcloud", "MP3", "Newgrounds", "OGG", "SoundCloud", "Spotify", "Tidal", "VK", "Yandex",
		"Telegram", "YouTube", "ZIP",
	}
	check := func(key, lang, value string) {
		t.Helper()
		plain := stripHTMLTags(value)
		segments := strings.FieldsFunc(plain, func(r rune) bool {
			return r == '\n' || r == '.' || r == '!' || r == '?' || r == '…' || r == ':'
		})
		for _, segment := range segments {
			segment = strings.TrimSpace(segment)
			for i, r := range segment {
				if !unicode.IsLetter(r) {
					continue
				}
				rest := segment[i:]
				allowed := false
				for _, prefix := range protected {
					if strings.HasPrefix(rest, prefix) {
						allowed = true
						break
					}
				}
				if !unicode.IsLower(r) && !allowed {
					t.Errorf("%s/%s starts a text segment with uppercase: %q", key, lang, segment)
				}
				break
			}
		}
	}

	for key, translations := range texts {
		for _, lang := range languageOrder {
			check(key, lang, translations[lang])
		}
	}
	check("choose_language", "ru+en", chooseLanguageText)
}

func stripHTMLTags(value string) string {
	var result strings.Builder
	inTag := false
	for _, r := range value {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				result.WriteRune(r)
			}
		}
	}
	return result.String()
}

func TestUserGuidesExplainNewFeatures(t *testing.T) {
	bot := &tgbotapi.BotAPI{Self: tgbotapi.User{UserName: "music_test_bot"}}
	app := &app{bot: bot}
	for _, lang := range languageOrder {
		welcome := app.guideText("welcome", lang)
		help := aHelpSectionsText(t, app, lang)
		if !strings.Contains(welcome, "@music_test_bot") || !strings.Contains(help, "@music_test_bot") {
			t.Errorf("%s guide does not contain the actual bot username", lang)
		}
		for _, feature := range []string{"Daft Punk", "YouTube", "Newgrounds", "Spotify", "ZIP", "MP3", "/settings", "Shazam"} {
			if !strings.Contains(help, feature) {
				t.Errorf("%s help does not explain %q", lang, feature)
			}
		}
		if strings.Contains(welcome, "{username}") || strings.Contains(help, "{username}") {
			t.Errorf("%s guide contains an unresolved placeholder", lang)
		}
	}
}

// aHelpSectionsText joins the help table of contents and every section message the way /help renders them.
func aHelpSectionsText(t *testing.T, app *app, lang string) string {
	t.Helper()
	text := app.guideText("help", lang)
	for _, section := range helpSectionIDs {
		sectionText, ok := app.helpSectionText(section, lang)
		if !ok {
			t.Fatalf("help section %q is not defined", section)
		}
		if strings.Contains(sectionText, "{username}") {
			t.Fatalf("%s help section %q contains an unresolved placeholder", lang, section)
		}
		if len([]rune(sectionText)) > 4096 {
			t.Errorf("%s help section %q is too long for a Telegram message: %d runes", lang, section, len([]rune(sectionText)))
		}
		text += "\n\n" + sectionText
	}
	return text
}

func TestBotCommandsAreLocalized(t *testing.T) {
	ru := botCommands("ru", false)
	en := botCommands("en", false)
	if len(ru) != 9 || len(en) != 9 || ru[1].Description == en[1].Description || ru[3].Command != "settings" || ru[4].Command != "history" || ru[5].Command != "export" || ru[6].Command != "cover" || ru[7].Command != "notify" || ru[8].Command != "id" {
		t.Fatalf("unexpected localized commands: ru=%#v en=%#v", ru, en)
	}
	lastfm := botCommands("en", true)
	if len(lastfm) != 10 || lastfm[7].Command != "lastfm" || !reflect.DeepEqual(lastfm[:7], en[:7]) || !reflect.DeepEqual(lastfm[8:], en[7:]) {
		t.Fatalf("/lastfm must be listed before /notify only when configured: %#v", lastfm)
	}
	for _, commands := range [][]tgbotapi.BotCommand{ru, en, lastfm, botCommands("ru", true)} {
		for _, command := range commands {
			if command.Description == "" || len([]rune(command.Description)) > 256 {
				t.Errorf("invalid command description: %#v", command)
			}
		}
	}
}

func TestHelpCommandSendsLocalizedHTMLGuide(t *testing.T) {
	var sentText, parseMode, sentMarkup string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"guide_bot"}}`)
		case "sendMessage":
			_ = r.ParseForm()
			sentText, parseMode, sentMarkup = r.FormValue("text"), r.FormValue("parse_mode"), r.FormValue("reply_markup")
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":10,"type":"private"}}}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	})
	bot, err := tgbotapi.NewBotAPIWithClient("token", "https://telegram.test/bot%s/%s", handlerClient{handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	app := newApp(context.Background(), bot, nil)
	app.setLang(10, "ru")
	app.handleMessage(&tgbotapi.Message{
		Text:     "/help",
		Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: 5}},
		From:     &tgbotapi.User{ID: 10},
		Chat:     &tgbotapi.Chat{ID: 10, Type: "private"},
	})
	if parseMode != "HTML" || !strings.Contains(sentText, "справка e6akl4k") {
		t.Fatalf("unexpected help response: parse_mode=%q text=%q", parseMode, sentText)
	}
	if !strings.Contains(sentMarkup, `"help:search"`) || !strings.Contains(sentMarkup, `"help:formats"`) {
		t.Fatalf("help response must show section buttons: markup=%q", sentMarkup)
	}
	for _, section := range helpSectionIDs {
		if _, ok := app.helpSectionText(section, "ru"); !ok {
			t.Fatalf("help section %q must render in the guide", section)
		}
	}
}

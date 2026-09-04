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
		"Mixcloud", "MP3", "Octave", "OGG", "SoundCloud", "Spotify", "Tidal", "VK", "Yandex",
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
		help := app.guideText("help", lang)
		if !strings.Contains(welcome, "@music_test_bot") || !strings.Contains(help, "@music_test_bot") {
			t.Errorf("%s guide does not contain the actual bot username", lang)
		}
		for _, feature := range []string{"Daft Punk", "Octave", "Spotify", "ZIP", "MP3", "/settings"} {
			if !strings.Contains(help, feature) {
				t.Errorf("%s help does not explain %q", lang, feature)
			}
		}
		if strings.Contains(welcome, "{username}") || strings.Contains(help, "{username}") {
			t.Errorf("%s guide contains an unresolved placeholder", lang)
		}
		if len([]rune(help)) > 4096 {
			t.Errorf("%s help is too long for a Telegram message: %d runes", lang, len([]rune(help)))
		}
	}
}

func TestBotCommandsAreLocalized(t *testing.T) {
	ru := botCommands("ru", false)
	en := botCommands("en", false)
	if len(ru) != 6 || len(en) != 6 || ru[1].Description == en[1].Description || ru[3].Command != "settings" || ru[4].Command != "history" || ru[5].Command != "id" {
		t.Fatalf("unexpected localized commands: ru=%#v en=%#v", ru, en)
	}
	admin := botCommands("en", true)
	if !reflect.DeepEqual(admin, en) {
		t.Fatalf("admin commands must stay hidden from the Telegram menu: public=%#v admin=%#v", en, admin)
	}
	for _, commands := range [][]tgbotapi.BotCommand{ru, en, admin} {
		for _, command := range commands {
			if command.Description == "" || len([]rune(command.Description)) > 256 {
				t.Errorf("invalid command description: %#v", command)
			}
		}
	}
}

func TestHelpCommandSendsLocalizedHTMLGuide(t *testing.T) {
	var sentText, parseMode string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"bot","username":"guide_bot"}}`)
		case "sendMessage":
			_ = r.ParseForm()
			sentText, parseMode = r.FormValue("text"), r.FormValue("parse_mode")
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
	if parseMode != "HTML" || !strings.Contains(sentText, "@guide_bot") || !strings.Contains(sentText, "поиск по названию") {
		t.Fatalf("unexpected help response: parse_mode=%q text=%q", parseMode, sentText)
	}
}

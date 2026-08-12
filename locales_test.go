package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

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

func TestUserGuidesExplainNewFeatures(t *testing.T) {
	bot := &tgbotapi.BotAPI{Self: tgbotapi.User{UserName: "music_test_bot"}}
	app := &app{bot: bot}
	for _, lang := range languageOrder {
		welcome := app.guideText("welcome", lang)
		help := app.guideText("help", lang)
		if !strings.Contains(welcome, "@music_test_bot") || !strings.Contains(help, "@music_test_bot") {
			t.Errorf("%s guide does not contain the actual bot username", lang)
		}
		for _, feature := range []string{"Daft Punk", "Spotify", "ZIP", "MP3"} {
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
	if len(ru) != 3 || len(en) != 3 || ru[1].Description == en[1].Description {
		t.Fatalf("unexpected localized commands: ru=%#v en=%#v", ru, en)
	}
	admin := botCommands("en", true)
	if len(admin) != 5 || admin[3].Command != "stats" || admin[4].Command != "status" {
		t.Fatalf("unexpected admin commands: %#v", admin)
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
	if parseMode != "HTML" || !strings.Contains(sentText, "@guide_bot") || !strings.Contains(sentText, "Поиск по названию") {
		t.Fatalf("unexpected help response: parse_mode=%q text=%q", parseMode, sentText)
	}
}

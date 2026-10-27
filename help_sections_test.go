package main

import (
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// TestHelpKeyboards checks the section buttons and the single back button.
func TestHelpKeyboards(t *testing.T) {
	for _, lang := range languageOrder {
		keyboard := helpKeyboard(lang)
		if len(keyboard.InlineKeyboard) != 2 {
			t.Fatalf("%s: expected two rows of section buttons, got %d", lang, len(keyboard.InlineKeyboard))
		}
		var buttons []string
		for _, row := range keyboard.InlineKeyboard {
			if len(row) != 2 {
				t.Fatalf("%s: expected two buttons per row, got %d", lang, len(row))
			}
			for _, button := range row {
				if button.CallbackData == nil {
					t.Fatalf("%s: section button has no callback data", lang)
				}
				buttons = append(buttons, *button.CallbackData)
			}
		}
		if len(buttons) != len(helpSectionIDs) {
			t.Fatalf("%s: expected %d buttons, got %d", lang, len(helpSectionIDs), len(buttons))
		}
		for i, section := range helpSectionIDs {
			if buttons[i] != "help:"+section {
				t.Errorf("%s: button %d callback = %q, want help:%s", lang, i, buttons[i], section)
			}
		}

		back := helpBackKeyboard(lang)
		if len(back.InlineKeyboard) != 1 || len(back.InlineKeyboard[0]) != 1 || back.InlineKeyboard[0][0].CallbackData == nil || *back.InlineKeyboard[0][0].CallbackData != "help:main" {
			t.Fatalf("%s: back keyboard must be a single help:main button", lang)
		}
		if back.InlineKeyboard[0][0].Text == "" {
			t.Errorf("%s: back button has no label", lang)
		}
	}
}

// TestHelpSectionTextRejectsUnknownSections guards the callback data namespace.
func TestHelpSectionTextRejectsUnknownSections(t *testing.T) {
	application := &app{}
	if text, ok := application.helpSectionText("nope", "ru"); ok || text != "" {
		t.Fatal("unknown help sections must not render")
	}
	for _, section := range []string{"search", "links", "collections", "formats"} {
		for _, lang := range languageOrder {
			text, ok := application.helpSectionText(section, lang)
			if !ok || text == "" {
				t.Fatalf("%s help section %q must render", lang, section)
			}
			if strings.Contains(text, "{username}") {
				t.Fatalf("%s help section %q has an unresolved placeholder", lang, section)
			}
		}
	}
}

// TestHandleHelpSection checks that section and back callbacks edit the same message.
func TestHandleHelpSection(t *testing.T) {
	telegram := &exportTelegram{}
	application := newExportTestApp(t, telegram)
	for _, lang := range languageOrder {
		application.setLang(10, lang)
	}
	callback := &tgbotapi.CallbackQuery{
		ID:      "1",
		Data:    "help:search",
		From:    &tgbotapi.User{ID: 10, UserName: "u"},
		Message: &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 10, Type: "private"}, MessageID: 5},
	}

	application.handleHelpSection(callback)
	callback.Data = "help:main"
	application.handleHelpSection(callback)
	calls := telegram.snapshot()
	if len(calls) != 2 {
		t.Fatalf("help callbacks must edit the same message twice: %#v", calls)
	}
	for _, call := range calls {
		if call.method != "editMessageText" || call.chatID != "10" {
			t.Fatalf("help callbacks must edit the chat message: %#v", call)
		}
	}
	sectionCalls := calls[:1]
	if !strings.Contains(sectionCalls[0].text, "Daft Punk") {
		t.Error("search section must explain title search")
	}
	if !strings.Contains(sectionCalls[0].markup, `"help:main"`) {
		t.Error("section screen must show only the back button")
	}
	if strings.Contains(sectionCalls[0].markup, "help:search") {
		t.Error("section screen must not keep the section button")
	}

	for _, section := range helpSectionIDs {
		if !strings.Contains(calls[1].markup, `"help:`+section+`"`) {
			t.Errorf("main screen must keep the %q section button", section)
		}
	}
	if strings.Contains(calls[1].markup, "help:main") {
		t.Error("main screen must not show the back button")
	}
}

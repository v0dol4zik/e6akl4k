package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func newTestInlineService() *inlineService {
	return newInlineService(nil, time.Hour)
}

func TestLatestInlineQueryCancelsPreviousOne(t *testing.T) {
	service := newTestInlineService()
	firstCtx, firstID := service.beginQuery(context.Background(), 10)
	secondCtx, secondID := service.beginQuery(context.Background(), 10)
	select {
	case <-firstCtx.Done():
	default:
		t.Fatal("previous inline query was not cancelled")
	}
	service.finishQuery(10, firstID)
	select {
	case <-secondCtx.Done():
		t.Fatal("finishing an old query cancelled the current query")
	default:
	}
	service.finishQuery(10, secondID)
	select {
	case <-secondCtx.Done():
	default:
		t.Fatal("current inline query context was not released")
	}
}

func TestYouTubeLinkTarget(t *testing.T) {
	cases := []struct {
		url      string
		id       string
		playlist bool
	}{
		{url: "https://youtu.be/dQw4w9WgXcQ?si=abc", id: "dQw4w9WgXcQ"},
		{url: "https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PL123", id: "dQw4w9WgXcQ"},
		{url: "https://music.youtube.com/watch?v=dQw4w9WgXcQ", id: "dQw4w9WgXcQ"},
		{url: "https://m.youtube.com/shorts/dQw4w9WgXcQ", id: "dQw4w9WgXcQ"},
		{url: "https://youtube.com/live/dQw4w9WgXcQ", id: "dQw4w9WgXcQ"},
		{url: "https://youtube.com/samples/dQw4w9WgXcQ", id: "dQw4w9WgXcQ"},
		{url: "https://www.youtube.com/playlist?list=PL123", playlist: true},
		{url: "https://www.youtube.com/@channel"},
		{url: "https://www.youtube.com/watch?v=short"},
		{url: "https://soundcloud.com/artist/track"},
	}
	for _, tc := range cases {
		id, playlist := youtubeLinkTarget(tc.url)
		if id != tc.id || playlist != tc.playlist {
			t.Errorf("youtubeLinkTarget(%q) = %q, %v; want %q, %v", tc.url, id, playlist, tc.id, tc.playlist)
		}
	}
}

func TestInlineCandidateIsBoundToUser(t *testing.T) {
	service := newTestInlineService()
	id, err := service.storeCandidate(inlineCandidate{URL: "https://youtu.be/test", UserID: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := service.takeCandidate(id, 11); ok {
		t.Fatal("another user consumed an inline result")
	}
	if candidate, ok := service.takeCandidate(id, 10); !ok || candidate.URL == "" {
		t.Fatalf("owner could not consume inline result: %#v, %v", candidate, ok)
	}
	if _, ok := service.takeCandidate(id, 10); ok {
		t.Fatal("inline result was consumed twice")
	}
}

func TestExpiredInlineCandidateIsRejected(t *testing.T) {
	service := newTestInlineService()
	service.candidates["expired"] = inlineCandidate{UserID: 10, ExpiresAt: time.Now().Add(-time.Second)}
	if _, ok := service.takeCandidate("expired", 10); ok {
		t.Fatal("expired inline result was accepted")
	}
}

func TestInlineCancelIsBoundToUserAndMessage(t *testing.T) {
	service := newTestInlineService()
	cancelled := make(chan struct{}, 1)
	service.setActive("result", inlineActiveDownload{
		cancel:          func() { cancelled <- struct{}{} },
		userID:          10,
		inlineMessageID: "message",
	})
	if service.cancelDownload("result", 11, "message") {
		t.Fatal("another user cancelled the inline download")
	}
	if service.cancelDownload("result", 10, "other-message") {
		t.Fatal("download was cancelled from another inline message")
	}
	if !service.cancelDownload("result", 10, "message") {
		t.Fatal("owner could not cancel inline download")
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("cancel function was not called")
	}
	service.clearActive("result")
	if service.cancelDownload("result", 10, "message") {
		t.Fatal("a finished download was cancelled")
	}
}

func TestLegacyInlineCacheIsMigratedOnce(t *testing.T) {
	state, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	if err := state.putCachedAudio(ctx, cachedAudio{Key: "youtube:kept:mp3:320", FileID: "newer-file", Format: "mp3", Quality: "320"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "inline-audio-cache.json")
	if err := os.WriteFile(path, []byte(`{"youtube:old:mp3:320":"old-file","youtube:kept:mp3:320":"stale-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyInlineCache(ctx, state, path, time.Hour); err != nil {
		t.Fatal(err)
	}
	service := newInlineService(state, time.Hour)
	if got := service.cachedFileID("youtube:old:mp3:320"); got != "old-file" {
		t.Fatalf("migrated file ID = %q", got)
	}
	if got := service.cachedFileID("youtube:kept:mp3:320"); got != "newer-file" {
		t.Fatalf("the migration replaced a newer entry: %q", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("legacy cache was not renamed: %v", err)
	}
	if err := migrateLegacyInlineCache(ctx, state, path, time.Hour); err != nil {
		t.Fatalf("a second start failed: %v", err)
	}
}

func TestInlineHelpers(t *testing.T) {
	if got := inlineDurationSeconds("1:02:03"); got != 3723 {
		t.Fatalf("duration = %d", got)
	}
	if got := inlineDurationSeconds("invalid"); got != 0 {
		t.Fatalf("invalid duration = %d", got)
	}
	first := inlineCacheKey("https://example.com/a", "", "")
	second := inlineCacheKey("https://example.com/b", "", "")
	if first == second || !strings.HasPrefix(first, "url:") {
		t.Fatalf("unexpected URL cache keys: %q %q", first, second)
	}
	if got := inlineCacheKey("ignored", "youtube", "video-id"); got != "youtube:video-id:mp3:320" {
		t.Fatalf("unexpected YouTube cache key: %q", got)
	}
	if got := inlineCacheKey("ignored", "soundcloud", "123"); got != "soundcloud:123:mp3:320" {
		t.Fatalf("unexpected SoundCloud cache key: %q", got)
	}
	if got := youtubeThumbnail("dQw4w9WgXcQ"); got != "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg" {
		t.Fatalf("thumbnail = %q", got)
	}
	if got := youtubeThumbnail("../../x"); got != "" {
		t.Fatalf("thumbnail of an invalid ID = %q", got)
	}
	text := inlineMessageText("⏳ status", inlineCandidate{Title: "A < B", Artist: "one & two", Duration: "3:25"})
	if text != "⏳ status\n<b>A &lt; B</b>\none &amp; two · 3:25" {
		t.Fatalf("inline message text = %q", text)
	}
	if got := inlineMessageText("status", inlineCandidate{}); got != "status\n<b>Unknown</b>" {
		t.Fatalf("text without metadata = %q", got)
	}
	markupJSON, err := json.Marshal(emptyInlineKeyboard())
	if err != nil {
		t.Fatal(err)
	}
	if string(markupJSON) != `{"inline_keyboard":[]}` {
		t.Fatalf("empty inline keyboard = %s", markupJSON)
	}
}

func TestInlineResultShowsTrackInsteadOfPlaceholder(t *testing.T) {
	candidate := inlineCandidate{Title: "Sploinky Dub", Artist: "Subtronics", Duration: "3:25", Thumbnail: youtubeThumbnail("dQw4w9WgXcQ")}
	encoded, err := json.Marshal(inlineResult("result-id", candidate, "", "en"))
	if err != nil {
		t.Fatal(err)
	}
	var article struct {
		Type                string `json:"type"`
		ID                  string `json:"id"`
		Title               string `json:"title"`
		Description         string `json:"description"`
		ThumbnailURL        string `json:"thumbnail_url"`
		InputMessageContent struct {
			Text      string `json:"message_text"`
			ParseMode string `json:"parse_mode"`
		} `json:"input_message_content"`
		ReplyMarkup tgbotapi.InlineKeyboardMarkup `json:"reply_markup"`
	}
	if err := json.Unmarshal(encoded, &article); err != nil {
		t.Fatal(err)
	}
	if article.Type != "article" || article.ID != "result-id" || article.Title != "Sploinky Dub" || article.Description != "Subtronics · 3:25" {
		t.Fatalf("article = %s", encoded)
	}
	if article.ThumbnailURL != "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg" {
		t.Fatalf("thumbnail = %q", article.ThumbnailURL)
	}
	if article.InputMessageContent.ParseMode != "HTML" || !strings.Contains(article.InputMessageContent.Text, "<b>Sploinky Dub</b>") {
		t.Fatalf("message = %+v", article.InputMessageContent)
	}
	rows := article.ReplyMarkup.InlineKeyboard
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0].CallbackData == nil || *rows[0][0].CallbackData != "inline_cancel:result-id" {
		t.Fatalf("a loading message needs the cancel button, which also makes Telegram report its inline_message_id: %s", encoded)
	}

	encoded, err = json.Marshal(inlineResult("result-id", candidate, "cached-file", "en"))
	if err != nil {
		t.Fatal(err)
	}
	var cached struct {
		Type   string `json:"type"`
		FileID string `json:"audio_file_id"`
	}
	if err := json.Unmarshal(encoded, &cached); err != nil {
		t.Fatal(err)
	}
	if cached.Type != "audio" || cached.FileID != "cached-file" {
		t.Fatalf("cached result = %s", encoded)
	}
}

func TestSearchLookupBuildsCandidatesWithThumbnails(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "fake-yt-dlp")
	script := `#!/bin/sh
printf '%s' '{"entries":[{"id":"dQw4w9WgXcQ","title":"Track","uploader":"Artist","duration":125,"url":"dQw4w9WgXcQ"}]}'
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	d := downloader{bin: bin}
	candidates, err := d.searchLookup(context.Background(), "track name", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidate count = %d", len(candidates))
	}
	got := candidates[0]
	if got.URL != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" || got.Title != "Track" || got.Artist != "Artist" || got.Duration != "2:05" {
		t.Fatalf("unexpected candidate: %#v", got)
	}
	if got.CacheKey != "youtube:dQw4w9WgXcQ:mp3:320" || got.Thumbnail != "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg" {
		t.Fatalf("unexpected cache key or thumbnail: %#v", got)
	}
}

func TestSearchRetriesWithoutTrailingWord(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-yt-dlp")
	queries := filepath.Join(dir, "queries")
	script := `#!/bin/sh
for a in "$@"; do query="$a"; done
printf '%s\n' "$query" >> "` + queries + `"
case "$query" in
  *vshj*|*never*) printf '%s' '{"entries":[]}' ;;
  *) printf '%s' '{"entries":[{"id":"dQw4w9WgXcQ","title":"Subtronics - Sploinky Dub","uploader":"Subtronics","duration":200,"url":"dQw4w9WgXcQ"}]}' ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	d := downloader{bin: bin}
	candidates, err := d.searchLookup(context.Background(), "subtronics — sploinky dub vshj", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].SourceID != "dQw4w9WgXcQ" || candidates[0].Match != "exact" {
		t.Fatalf("candidates = %#v", candidates)
	}
	logged, err := os.ReadFile(queries)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(logged); got != "ytsearch5:subtronics — sploinky dub vshj\nytsearch5:subtronics — sploinky dub\n" {
		t.Fatalf("queries = %q", got)
	}

	if err := os.Remove(queries); err != nil {
		t.Fatal(err)
	}
	_, err = d.searchLookup(context.Background(), "never found at all here", 0)
	if !errors.Is(err, errNothingFound) {
		t.Fatalf("err = %v, want errNothingFound", err)
	}
	logged, err = os.ReadFile(queries)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(logged), "\n"); lines != 1+searchRelaxRetries {
		t.Fatalf("a query that finds nothing ran %d searches: %q", lines, logged)
	}
}

func TestDropLastSearchWord(t *testing.T) {
	cases := []struct {
		query string
		want  string
		ok    bool
	}{
		{query: "subtronics — sploinky dub vshj", want: "subtronics — sploinky dub", ok: true},
		{query: "one two three —", want: "one two", ok: true},
		{query: "artist — track", ok: false},
		{query: "two words", ok: false},
		{query: "— —", ok: false},
	}
	for _, tc := range cases {
		got, ok := dropLastSearchWord(tc.query)
		if got != tc.want || ok != tc.ok {
			t.Errorf("dropLastSearchWord(%q) = %q, %v; want %q, %v", tc.query, got, ok, tc.want, tc.ok)
		}
	}
}

func newInlineHarness(t *testing.T) *batchHarness {
	t.Helper()
	h := newBatchHarness(t)
	h.app.cfg.CacheChatID = -100
	h.app.inlineLimiter = newRateLimiter(20, time.Minute)
	h.app.inline = newInlineService(h.state, time.Hour)
	return h
}

func inlineQuery(text string) *tgbotapi.InlineQuery {
	return &tgbotapi.InlineQuery{ID: "query", From: &tgbotapi.User{ID: 10}, Query: text}
}

// inlineAnswer returns the only answerInlineQuery call: its results and its button text.
func inlineAnswer(t *testing.T, h *batchHarness) ([]map[string]interface{}, string) {
	t.Helper()
	var answers []telegramCall
	for _, call := range h.snapshot() {
		if call.method == "answerInlineQuery" {
			answers = append(answers, call)
		}
	}
	if len(answers) != 1 {
		t.Fatalf("answers = %#v", answers)
	}
	var results []map[string]interface{}
	if err := json.Unmarshal([]byte(answers[0].form.Get("results")), &results); err != nil {
		t.Fatal(err)
	}
	return results, answers[0].form.Get("switch_pm_text")
}

func TestInlineQueryAnswersWithTrackTitles(t *testing.T) {
	h := newInlineHarness(t)
	h.app.handleInlineQuery(inlineQuery("first artist"))
	results, button := inlineAnswer(t, h)
	if len(results) != 1 || results[0]["type"] != "article" || results[0]["title"] != "First" || results[0]["description"] != "Artist · 3:00" {
		t.Fatalf("results = %#v", results)
	}
	if button != tr("inline_switch_pm", "en") {
		t.Fatalf("button = %q", button)
	}

	cacheTrack(t, h.state, "11", "First")
	h.app.handleInlineQuery(inlineQuery("first artist"))
	results, _ = inlineAnswer(t, h)
	if len(results) != 1 || results[0]["type"] != "audio" || results[0]["audio_file_id"] != "cached-file" {
		t.Fatalf("a cached track must be sent as audio: %#v", results)
	}
}

func TestInlineQueryExplainsEmptyResults(t *testing.T) {
	cases := []struct {
		query string
		hint  string
	}{
		{query: "", hint: "inline_hint_empty"},
		{query: "ab", hint: "inline_hint_short"},
		{query: "nothing matches this", hint: "inline_hint_nothing"},
		{query: "https://www.youtube.com/playlist?list=PL3", hint: "inline_hint_playlist"},
		{query: "https://open.spotify.com/album/abc", hint: "inline_hint_playlist"},
	}
	for _, tc := range cases {
		t.Run(tc.hint, func(t *testing.T) {
			h := newInlineHarness(t)
			h.app.handleInlineQuery(inlineQuery(tc.query))
			results, button := inlineAnswer(t, h)
			if len(results) != 0 || button != tr(tc.hint, "en") {
				t.Fatalf("results = %#v, button = %q, want %q", results, button, tr(tc.hint, "en"))
			}
		})
	}
}

func TestInlineQueriesOfOneUserDoNotWaitForEachOther(t *testing.T) {
	first := tgbotapi.Update{UpdateID: 1, InlineQuery: inlineQuery("first")}
	second := tgbotapi.Update{UpdateID: 2, InlineQuery: inlineQuery("second")}
	if updateOwnerKey(first) == updateOwnerKey(second) || updateOwnerKey(first) == 10 {
		t.Fatal("an inline query would wait behind the user's other updates")
	}
	chosen := tgbotapi.Update{UpdateID: 3, ChosenInlineResult: &tgbotapi.ChosenInlineResult{From: &tgbotapi.User{ID: 10}}}
	if updateOwnerKey(chosen) != 10 {
		t.Fatal("a chosen inline result must queue with the user's other downloads")
	}
}

// chooseInline stores a result for user 10 and reports it chosen from an inline message.
func chooseInline(t *testing.T, h *batchHarness, candidate inlineCandidate) {
	t.Helper()
	candidate.UserID = 10
	resultID, err := h.app.inline.storeCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	h.app.handleChosenInlineResult(&tgbotapi.ChosenInlineResult{ResultID: resultID, From: &tgbotapi.User{ID: 10}, InlineMessageID: "inline-message"})
}

func TestChosenInlineResultBecomesAudio(t *testing.T) {
	h := newInlineHarness(t)
	chooseInline(t, h, inlineCandidate{URL: "https://youtu.be/12", CacheKey: inlineCacheKey("https://youtu.be/12", "youtube", "12"), Title: "Second", SourceID: "12", Extractor: "youtube"})
	calls := h.snapshot()
	var edits []telegramCall
	for _, call := range calls {
		switch call.method {
		case "editMessageMedia":
			edits = append(edits, call)
		case "editMessageText":
			t.Fatalf("the loading message was replaced with text: %q", call.text)
		}
	}
	if h.count("sendAudio", calls) != 1 || len(edits) != 1 {
		t.Fatalf("calls = %#v", calls)
	}
	if edits[0].form.Get("inline_message_id") != "inline-message" || !strings.Contains(edits[0].form.Get("media"), `"media":"cached-file"`) {
		t.Fatalf("edit = %#v", edits[0].form)
	}
	if got := h.app.inline.cachedFileID(sourceCacheKey("youtube", "12", "mp3", "320")); got != "cached-file" {
		t.Fatalf("the track was not cached for the next inline query: %q", got)
	}
}

func TestChosenInlineResultReportsFailureInMessage(t *testing.T) {
	h := newInlineHarness(t)
	chooseInline(t, h, inlineCandidate{URL: "https://youtu.be/13", CacheKey: inlineCacheKey("https://youtu.be/13", "youtube", "13"), Title: "Third", SourceID: "13", Extractor: "youtube"})
	var texts []string
	for _, call := range h.snapshot() {
		if call.method == "editMessageText" && call.form.Get("inline_message_id") == "inline-message" {
			texts = append(texts, call.text)
		}
	}
	if len(texts) != 1 || !strings.HasPrefix(texts[0], "❌ <b>couldn't download:</b>") || !strings.Contains(texts[0], "<b>Third</b>") {
		t.Fatalf("texts = %q", texts)
	}
}

func TestExpiredChosenInlineResultClearsLoadingText(t *testing.T) {
	h := newInlineHarness(t)
	h.app.handleChosenInlineResult(&tgbotapi.ChosenInlineResult{ResultID: "gone", From: &tgbotapi.User{ID: 10}, InlineMessageID: "inline-message"})
	calls := h.snapshot()
	if len(calls) != 1 || calls[0].method != "editMessageText" || calls[0].text != tr("inline_expired", "en") {
		t.Fatalf("calls = %#v", calls)
	}
}

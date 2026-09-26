package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var lighterKeyPattern = regexp.MustCompile(`"dl:mp3:128:([^"]+)"`)

// lighterOffer returns the too-large notice and the pending key of its lighter-format buttons.
func (h *batchHarness) lighterOffer(t *testing.T, calls []telegramCall) (telegramCall, string) {
	t.Helper()
	for _, call := range calls {
		if call.method != "sendMessage" || !strings.Contains(call.text, "over the Telegram limit") {
			continue
		}
		match := lighterKeyPattern.FindStringSubmatch(call.markup)
		if match == nil {
			t.Fatalf("the notice has no lighter-format buttons: %#v", call)
		}
		return call, match[1]
	}
	t.Fatalf("the too-large notice was not sent: %#v", calls)
	return telegramCall{}, ""
}

func (h *batchHarness) counter(t *testing.T, name string) int64 {
	t.Helper()
	counters, err := h.state.counters(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return counters[name]
}

func TestTooLongTrackOffersLighterFormats(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cacheChatID int64
	}{
		{"direct download", 0},
		{"through the cache channel", -100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newBatchHarness(t)
			h.app.cfg.CacheChatID = tc.cacheChatID
			h.app.errorReports = newErrorReporter(777)

			h.app.handleMessage(batchMessage("https://youtu.be/77"))
			h.snapshot()
			h.app.handleCallback(batchCallback("dl:flac:best:" + h.pendingKey(t)))
			calls := h.snapshot()
			if got := h.count("sendAudio", calls) + h.count("sendDocument", calls); got != 0 {
				t.Fatalf("nothing must be uploaded: %#v", calls)
			}
			offer, key := h.lighterOffer(t, calls)
			if !strings.Contains(offer.text, "<b>Long</b> · 20:00") || !strings.Contains(offer.text, "in FLAC it is ≈ ") || !strings.Contains(offer.text, tr("too_large_choose", "en")) {
				t.Fatalf("unexpected notice: %q", offer.text)
			}
			for _, want := range []string{`"dl:mp3:best:`, `"dl:mp3:128:`, `"dl:ogg:best:`, `"cancel:`} {
				if !strings.Contains(offer.markup, want) {
					t.Fatalf("markup lacks %s: %s", want, offer.markup)
				}
			}
			for _, unwanted := range []string{`"dl:flac:`, `"dl:m4a:`, `"dl:mp3:320:`} {
				if strings.Contains(offer.markup, unwanted) {
					t.Fatalf("markup offers %s, which does not fit: %s", unwanted, offer.markup)
				}
			}
			for _, call := range calls {
				if strings.Contains(call.text, "error") {
					t.Fatalf("a size-limit hit is not an error: %#v", call)
				}
			}
			if queued := len(h.app.errorReports.queue); queued != 0 {
				t.Fatalf("error reports queued=%d, want none", queued)
			}
			if got := h.counter(t, "downloads_too_large"); got != 1 {
				t.Fatalf("downloads_too_large=%d", got)
			}
			if got := h.counter(t, "downloads_too_large_flac"); got != 1 {
				t.Fatalf("downloads_too_large_flac=%d", got)
			}
			stats, err := h.state.stats(context.Background())
			if err != nil || stats.TooLarge != 1 {
				t.Fatalf("stats=%+v err=%v", stats, err)
			}
			var status string
			if err := h.state.db.QueryRow(`SELECT status FROM download_history ORDER BY id DESC LIMIT 1`).Scan(&status); err != nil || status != "too_large" {
				t.Fatalf("history status=%q err=%v", status, err)
			}
			retry, ok := h.app.getURL(key, 10, 10)
			if !ok || retry.URL != "https://youtu.be/77" || len(retry.Batch) != 0 || retry.Preview.IsPlaylist {
				t.Fatalf("unexpected retry request: %#v", retry)
			}

			h.app.handleCallback(batchCallback("dl:mp3:128:" + key))
			// Through the cache channel the track is uploaded there and then re-sent by file_id.
			want := 1
			if tc.cacheChatID != 0 {
				want = 2
			}
			if got := h.count("sendAudio", h.snapshot()); got != want {
				t.Fatalf("the lighter format must be delivered: sendAudio=%d want %d", got, want)
			}
		})
	}
}

func TestBatchOffersLighterFormatForTooLongLink(t *testing.T) {
	h := newBatchHarness(t)
	h.app.errorReports = newErrorReporter(777)

	h.app.handleMessage(batchMessage("https://youtu.be/11 https://youtu.be/77"))
	h.snapshot()
	key := h.pendingKey(t)
	h.app.handleCallback(batchCallback("dl:flac:best:" + key))
	h.deliveryPrompt(t, h.snapshot())
	h.app.handleCallback(batchCallback("delivery:individual:flac:best:" + key))
	calls := h.snapshot()
	if got := h.count("sendDocument", calls); got != 1 {
		t.Fatalf("the track that fits must be sent: sendDocument=%d calls=%#v", got, calls)
	}
	offer, retryKey := h.lighterOffer(t, calls)
	if !strings.Contains(offer.text, "<b>Long</b>") {
		t.Fatalf("unexpected notice: %q", offer.text)
	}
	if queued := len(h.app.errorReports.queue); queued != 0 {
		t.Fatalf("error reports queued=%d, want none", queued)
	}
	if got := h.counter(t, "downloads_too_large"); got != 1 {
		t.Fatalf("downloads_too_large=%d", got)
	}
	retry, ok := h.app.getURL(retryKey, 10, 10)
	if !ok || retry.URL != "https://youtu.be/77" || len(retry.Batch) != 0 || retry.Delivery != "" {
		t.Fatalf("one remaining link must become a plain track request: %#v", retry)
	}
}

func TestLighterOptions(t *testing.T) {
	limit := int64(maxFileSize)
	names := func(options []downloadOption) string {
		parts := make([]string, 0, len(options))
		for _, option := range options {
			parts = append(parts, option.format+":"+option.quality)
		}
		return strings.Join(parts, " ")
	}
	cases := []struct {
		name            string
		seconds         int
		format, quality string
		want            string
	}{
		{"20 minutes of FLAC", 1200, "flac", "best", "mp3:best mp3:128 ogg:best"},
		{"10 minutes of FLAC", 600, "flac", "best", "mp3:best mp3:128 mp3:320 m4a:best ogg:best"},
		{"an hour of MP3 320", 3600, "mp3", "320", ""},
		{"45 minutes of MP3 320", 2700, "mp3", "320", "mp3:128"},
		{"unknown length", 0, "m4a", "best", "mp3:best mp3:128 mp3:320 ogg:best"},
		{"unknown length of OGG", 0, "ogg", "best", "mp3:128"},
		{"nothing is lighter than MP3 128", 0, "mp3", "128", ""},
	}
	for _, tc := range cases {
		if got := names(lighterOptions(tc.seconds, limit, tc.format, tc.quality)); got != tc.want {
			t.Errorf("%s: lighterOptions=%q want %q", tc.name, got, tc.want)
		}
	}
}

func TestLighterFormatRequest(t *testing.T) {
	a := &app{}
	single := pendingURL{URL: "https://youtu.be/77", ChatID: 1, UserID: 1, Preview: mediaPreview{Title: "Long", SourceID: "77", Extractor: "youtube"}}
	request, ok := a.lighterFormatRequest(10, 20, []downloadResult{{Title: "Long"}}, "", &single)
	if !ok || request.URL != single.URL || request.ChatID != 10 || request.UserID != 20 || request.Preview.SourceID != "77" {
		t.Fatalf("single request=%#v ok=%v", request, ok)
	}

	tracks := []downloadResult{
		{Title: "A", URL: "https://www.youtube.com/watch?v=a", DurationSeconds: 700},
		{Title: "no link"},
		{Title: "B", URL: "https://www.youtube.com/watch?v=b", DurationSeconds: 800},
		{Title: "foreign", URL: "https://example.com/c"},
	}
	request, ok = a.lighterFormatRequest(10, 20, tracks, "zip", nil)
	if !ok || len(request.Batch) != 2 || len(request.BatchPreviews) != 2 || request.Delivery != "zip" || request.URL != tracks[0].URL {
		t.Fatalf("batch request=%#v ok=%v", request, ok)
	}
	if request.Preview.Extractor != "batch" || request.Preview.TrackCount != 2 || request.Preview.DurationSeconds != 1500 {
		t.Fatalf("batch preview=%#v", request.Preview)
	}
	request, ok = a.lighterFormatRequest(10, 20, tracks, "", nil)
	if !ok || request.Delivery != "individual" {
		t.Fatalf("batch delivery=%q ok=%v", request.Delivery, ok)
	}

	request, ok = a.lighterFormatRequest(10, 20, tracks[2:], "zip", nil)
	if !ok || request.URL != tracks[2].URL || len(request.Batch) != 0 || request.Delivery != "" || request.Preview.Title != "B" {
		t.Fatalf("one link must collapse to a track request: %#v ok=%v", request, ok)
	}

	if _, ok := a.lighterFormatRequest(10, 20, tracks[1:2], "", nil); ok {
		t.Fatal("tracks without links must not produce a request")
	}
}

func TestDeliveryStatusTooLarge(t *testing.T) {
	cases := []struct {
		report deliveryReport
		want   string
	}{
		{deliveryReport{Failed: 2, TooLarge: make([]downloadResult, 2)}, "too_large"},
		{deliveryReport{Failed: 2, TooLarge: make([]downloadResult, 1)}, "delivery_failed"},
		{deliveryReport{Delivered: 1, Failed: 1, TooLarge: make([]downloadResult, 1)}, "partial"},
	}
	for _, tc := range cases {
		if got := deliveryStatus(tc.report); got != tc.want {
			t.Errorf("deliveryStatus(%+v)=%q want %q", tc.report, got, tc.want)
		}
	}
}

func TestIncrementByAddsDelta(t *testing.T) {
	h := newBatchHarness(t)
	h.state.incrementBy(context.Background(), "downloads_too_large", 3)
	h.state.increment(context.Background(), "downloads_too_large")
	if got := h.counter(t, "downloads_too_large"); got != 4 {
		t.Fatalf("downloads_too_large=%d want 4", got)
	}
}

func TestMediaGroupEnd(t *testing.T) {
	const mb = 1024 * 1024
	groups := func(sizes []int64) []int {
		var ends []int
		for start := 0; start < len(sizes); start = mediaGroupEnd(sizes, start) {
			ends = append(ends, mediaGroupEnd(sizes, start))
		}
		return ends
	}
	cloud := make([]int64, 12)
	for i := range cloud {
		cloud[i] = 49 * mb
	}
	if got := fmt.Sprint(groups(cloud)); got != "[10 12]" {
		t.Fatalf("cloud-sized files keep groups of 10: %s", got)
	}
	local := []int64{300 * mb, 150 * mb, 60 * mb, 900 * mb, 10 * mb}
	if got := fmt.Sprint(groups(local)); got != "[2 3 4 5]" {
		t.Fatalf("large files split by bytes: %s", got)
	}
}

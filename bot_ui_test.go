package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestDetectURLs(t *testing.T) {
	tests := map[string]struct {
		text string
		max  int
		want []string
	}{
		"two links in order": {
			text: "first https://youtu.be/one then https://soundcloud.com/artist/track",
			max:  5,
			want: []string{"https://youtu.be/one", "https://soundcloud.com/artist/track"},
		},
		"duplicates are removed": {
			text: "https://youtu.be/one https://youtu.be/one youtu.be/one https://youtu.be/two",
			max:  5,
			want: []string{"https://youtu.be/one", "https://youtu.be/two"},
		},
		"cap keeps the first links": {
			text: "https://youtu.be/a https://youtu.be/b https://youtu.be/c",
			max:  2,
			want: []string{"https://youtu.be/a", "https://youtu.be/b"},
		},
		"no cap": {
			text: "https://youtu.be/a https://youtu.be/b https://youtu.be/c",
			max:  0,
			want: []string{"https://youtu.be/a", "https://youtu.be/b", "https://youtu.be/c"},
		},
		"samples link becomes a video": {
			text: "https://youtube.com/samples/dQw4w9WgXcQ?si=x www.youtube.com/samples/dQw4w9WgXcQ https://youtube.com/samples",
			max:  5,
			want: []string{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "https://youtube.com/samples"},
		},
		"invalid host dropped": {
			text: "http://youtube.com@127.0.0.1/private https://youtu.be/ok",
			max:  5,
			want: []string{"https://youtu.be/ok"},
		},
		"no links": {
			text: "просто текст",
			max:  5,
			want: nil,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := detectURLs(tc.text, tc.max); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("detectURLs(%q, %d) = %q, want %q", tc.text, tc.max, got, tc.want)
			}
		})
	}
	if got := detectURL("see https://youtu.be/a and https://youtu.be/b"); got != "https://youtu.be/a" {
		t.Fatalf("detectURL must return the first link, got %q", got)
	}
}

// rangeChoices lists the range callbacks of a playlist keyboard in order.
func rangeChoices(key string, count, limit int) []string {
	var choices []string
	for _, row := range rangeKeyboard(key, count, limit, "en").InlineKeyboard {
		for _, button := range row {
			if button.CallbackData == nil {
				continue
			}
			choice, ok := strings.CutPrefix(*button.CallbackData, "range:")
			if ok {
				choices = append(choices, strings.TrimSuffix(choice, ":"+key))
			}
		}
	}
	return choices
}

func TestRangeKeyboardReachesEveryTrack(t *testing.T) {
	tests := map[string]struct {
		count, limit int
		want         []string
	}{
		"short playlist has no ranges":  {count: 7, limit: 1000, want: []string{"all"}},
		"album keeps ten-track ranges":  {count: 30, limit: 1000, want: []string{"all", "10", "25", "11-20", "21-30"}},
		"old limit no longer hides all": {count: 80, limit: 1000, want: []string{"all", "10", "25", "11-20", "21-30", "31-40", "41-50", "51-60", "61-70", "71-80"}},
		"long playlist gets wider ranges": {count: 300, limit: 1000, want: []string{
			"all", "10", "25", "26-50", "51-75", "76-100", "101-125", "126-150", "151-175", "176-200", "201-225", "226-250", "251-275", "276-300",
		}},
		"over the limit ranges cover the rest": {count: 1500, limit: 1000, want: []string{
			"limit", "10", "25", "1-250", "251-500", "501-750", "751-1000", "1001-1250", "1251-1500",
		}},
		"ranges stay within a small limit": {count: 200, limit: 20, want: []string{
			"limit", "10", "1-20", "21-40", "41-60", "61-80", "81-100", "101-120", "121-140", "141-160", "161-180", "181-200",
		}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := rangeChoices("k", test.count, test.limit)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("choices = %v, want %v", got, test.want)
			}
			// Every choice must be accepted by the range handler and select no more than the limit.
			covered := make([]bool, test.count+1)
			for _, choice := range got {
				start, end, ok := playlistRange(choice, test.count, test.limit)
				if !ok || end-start+1 > test.limit {
					t.Fatalf("choice %q: start=%d end=%d ok=%v", choice, start, end, ok)
				}
				for track := start; track <= end; track++ {
					covered[track] = true
				}
			}
			for track := 1; track <= test.count; track++ {
				if !covered[track] {
					t.Fatalf("track %d is not reachable", track)
				}
			}
		})
	}
}

func TestPlaylistRange(t *testing.T) {
	tests := []struct {
		choice       string
		count, limit int
		start, end   int
		ok           bool
	}{
		{choice: "all", count: 300, limit: 1000, start: 1, end: 300, ok: true},
		{choice: "all", count: 1500, limit: 1000, start: 1, end: 1000, ok: true},
		{choice: "limit", count: 1500, limit: 1000, start: 1, end: 1000, ok: true},
		{choice: "10", count: 7, limit: 1000, start: 1, end: 7, ok: true},
		{choice: "25", count: 300, limit: 20, start: 1, end: 20, ok: true},
		{choice: "1251-1500", count: 1500, limit: 1000, start: 1251, end: 1500, ok: true},
		{choice: "1-1000", count: 1500, limit: 1000, start: 1, end: 1000, ok: true},
		{choice: "1-1001", count: 1500, limit: 1000},
		{choice: "1401-1501", count: 1500, limit: 1000},
		{choice: "0-5", count: 30, limit: 1000},
		{choice: "20-10", count: 30, limit: 1000},
		{choice: "abc", count: 30, limit: 1000},
	}
	for _, test := range tests {
		start, end, ok := playlistRange(test.choice, test.count, test.limit)
		if start != test.start || end != test.end || ok != test.ok {
			t.Errorf("playlistRange(%q, %d, %d) = %d, %d, %v; want %d, %d, %v", test.choice, test.count, test.limit, start, end, ok, test.start, test.end, test.ok)
		}
	}
}

func TestRangeKeyboardStaysSmall(t *testing.T) {
	for _, count := range []int{1, 10, 75, 120, 121, 300, 301, 999, 1000, 5000, 20000} {
		rows := len(rangeKeyboard("k", count, maxPlaylistTracks, "en").InlineKeyboard)
		// all/limit, first 10, first 25, six rows of ranges, export, cancel.
		if rows > 11 {
			t.Errorf("count %d: %d keyboard rows", count, rows)
		}
	}
}

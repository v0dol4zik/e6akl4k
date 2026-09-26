package main

import (
	"reflect"
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

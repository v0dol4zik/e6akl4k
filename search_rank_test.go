package main

import "testing"

func TestRankCandidatesPenalizesUnrequestedVersions(t *testing.T) {
	candidates := []inlineCandidate{
		{Title: "Get Lucky (Live Remix)", Artist: "Daft Punk", Duration: "04:10", Extractor: "youtube"},
		{Title: "Get Lucky", Artist: "Daft Punk", Duration: "04:08"},
		{Title: "Lucky", Artist: "Unknown", Duration: "03:00", Extractor: "youtube"},
	}
	ranked := rankCandidates("Daft Punk — Get Lucky", 248, candidates)
	if ranked[0].Title != "Get Lucky" || ranked[0].Match != "exact" || ranked[1].Match != "variant" {
		t.Fatalf("ranked=%#v", ranked)
	}
}

func TestNormalizeSearchTextHandlesFeatAndDashes(t *testing.T) {
	first := normalizeSearchText("Ёлка — Песня feat. Артист")
	second := normalizeSearchText("елка - песня FT. артист")
	if first != second {
		t.Fatalf("normalized values differ: %q != %q", first, second)
	}
}

package main

import (
	"sort"
	"strings"
	"unicode"
)

var versionMarkers = []string{"live", "remix", "nightcore", "karaoke", "cover", "sped up", "slowed", "instrumental", "remaster", "remastered"}

func normalizeSearchText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("ё", "е", "–", " ", "—", " ", "-", " ", "&", " and ", "feat.", " feat ", "ft.", " feat ").Replace(value)
	var builder strings.Builder
	space := false
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
			space = false
		} else if !space {
			builder.WriteByte(' ')
			space = true
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

func rankCandidates(query string, expectedDuration int, candidates []inlineCandidate) []inlineCandidate {
	normalizedQuery := normalizeSearchText(query)
	queryTokens := strings.Fields(normalizedQuery)
	for index := range candidates {
		candidate := &candidates[index]
		title := normalizeSearchText(candidate.Title)
		artist := normalizeSearchText(candidate.Artist)
		haystack := strings.TrimSpace(artist + " " + title)
		hayTokens := make(map[string]bool)
		for _, token := range strings.Fields(haystack) {
			hayTokens[token] = true
		}
		matched := 0
		for _, token := range queryTokens {
			if hayTokens[token] {
				matched++
			}
		}
		score := 0
		if len(queryTokens) > 0 {
			score = matched * 70 / len(queryTokens)
		}
		if normalizedQuery == title || normalizedQuery == haystack {
			score += 22
		} else if strings.Contains(haystack, normalizedQuery) {
			score += 12
		}
		variant := false
		for _, marker := range versionMarkers {
			if strings.Contains(haystack, marker) && !strings.Contains(normalizedQuery, marker) {
				score -= 28
				variant = true
			}
		}
		if expectedDuration > 0 {
			duration := inlineDurationSeconds(candidate.Duration)
			delta := duration - expectedDuration
			if delta < 0 {
				delta = -delta
			}
			switch {
			case duration <= 0:
			case delta <= 3:
				score += 15
			case delta <= 10:
				score += 6
			case delta > 30:
				score -= 25
			}
		}
		if candidate.Extractor == "octave" {
			score += 3
		}
		if score < 0 {
			score = 0
		}
		if score > 100 {
			score = 100
		}
		candidate.Confidence = score
		switch {
		case variant:
			candidate.Match = "variant"
		case score >= 85:
			candidate.Match = "exact"
		default:
			candidate.Match = "similar"
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Confidence != candidates[j].Confidence {
			return candidates[i].Confidence > candidates[j].Confidence
		}
		if candidates[i].Extractor != candidates[j].Extractor {
			return candidates[i].Extractor == "octave"
		}
		return candidates[i].Title < candidates[j].Title
	})
	if len(candidates) > inlineResultLimit {
		candidates = candidates[:inlineResultLimit]
	}
	return candidates
}

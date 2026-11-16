package main

import (
	"regexp"
	"strconv"
	"strings"

	"crypto/sha256"
	"encoding/hex"
)

// fingerprintNoise matches promotional bracket groups that repeat the same track
// under different titles: "(Official Audio)", "[Remastered 2011]", "(lyric video)".
// Content-bearing suffixes such as (Live), (Remix), (feat. ...) are kept: they name
// different recordings of the same song and must not fold into one cache entry.
var fingerprintNoise = regexp.MustCompile(`\s*[(\[]\s*(?:official\s+|lyrics?\s+|lyric\s+)?(?:audio|video|lyrics?|mv|hd|4k|hq|visualizer|remaster(?:ed)?(?:\s+\d{2,4})?|visualiser)\s*[)\]]\s*`)

// unicodeSpace covers NBSP and friends that make "несколько  символов" different strings.
var unicodeSpace = regexp.MustCompile(`[\x{00A0}\x{2000}-\x{200A}\x{202F}\x{205F}\x{3000}]`)

var dashVariants = regexp.MustCompile(`[\x{2010}-\x{2015}\x{2212}\x{FE58}\x{FE63}\x{FF0D}]+`)

// normalizeTrackText flattens a title or artist into a canonical form so that
// "Daft Punk — Get Lucky (Official Audio)" and "daft punk - get lucky" collide.
func normalizeTrackText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = strings.ToLower(text)
	text = unicodeSpace.ReplaceAllString(text, " ")
	text = dashVariants.ReplaceAllString(text, "-")
	text = strings.ReplaceAll(text, "ё", "е")
	text = fingerprintNoise.ReplaceAllString(text, " ")
	text = strings.Join(strings.Fields(text), " ")
	return strings.TrimSpace(text)
}

// trackFingerprint hashes the normalized "artist\x1ftitle" pair. It is empty when
// either side is missing: no artist means search flows may match unrelated covers.
func trackFingerprint(artist, title string) string {
	a, t := normalizeTrackText(artist), normalizeTrackText(title)
	if a == "" || t == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(a + "\x1f" + t))
	return "v1:" + hex.EncodeToString(sum[:16])
}

// parseDurationSeconds turns "3:14", "1:02:03" or "75" into seconds, 0 when absent.
func parseDurationSeconds(duration string) int {
	duration = strings.TrimSpace(duration)
	if duration == "" {
		return 0
	}
	parts := strings.Split(duration, ":")
	if len(parts) > 3 {
		return 0
	}
	total := 0
	for _, part := range parts {
		number, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || number < 0 {
			return 0
		}
		total = total*60 + number
	}
	return total
}

// fingerprintDurationTolerance keeps near-misses (VBR drift, rounding) together while
// separating a radio edit from the album cut.
const fingerprintDurationTolerance = 7

// durationsCompatible reports whether two durations may belong to the same recording.
// An unknown (zero) duration accepts everything: the artist+title pair already
// carries most of the match.
func durationsCompatible(want, have int) bool {
	if want <= 0 || have <= 0 {
		return true
	}
	diff := want - have
	if diff < 0 {
		diff = -diff
	}
	return diff <= fingerprintDurationTolerance
}

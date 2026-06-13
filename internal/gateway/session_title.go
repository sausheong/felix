package gateway

import (
	"strings"
	"unicode/utf8"
)

// maxTitleWords caps the generated title to keep it glanceable ("< 10 words").
const maxTitleWords = 9

// sanitizeTitle cleans a model-generated title into a single short line:
// collapse all whitespace (incl. newlines) to single spaces, strip a single
// pair of surrounding quotes, drop a trailing period, cap to maxTitleWords
// words, then clamp to sessionMetaMaxTitleLen runes. Returns "" when nothing
// usable remains.
func sanitizeTitle(raw string) string {
	// Collapse whitespace.
	s := strings.Join(strings.Fields(raw), " ")
	if s == "" {
		return ""
	}
	// Strip one pair of surrounding quotes (straight single or double).
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			s = strings.TrimSpace(s[1 : len(s)-1])
		}
	}
	// Drop a single trailing period (titles shouldn't end in punctuation).
	s = strings.TrimRight(s, " ")
	if strings.HasSuffix(s, ".") {
		s = strings.TrimSuffix(s, ".")
		s = strings.TrimRight(s, " ")
	}
	if s == "" {
		return ""
	}
	// Cap word count.
	words := strings.Fields(s)
	if len(words) > maxTitleWords {
		words = words[:maxTitleWords]
	}
	s = strings.Join(words, " ")
	// Clamp rune length to the meta cap.
	if utf8.RuneCountInString(s) > sessionMetaMaxTitleLen {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:sessionMetaMaxTitleLen]))
	}
	return s
}

package gateway

import "testing"

func TestSanitizeTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Deploying the release", "Deploying the release"},
		{"  spaced  out \n title \t here ", "spaced out title here"},
		{"\"Quoted title\"", "Quoted title"},
		{"'single quoted'", "single quoted"},
		{"Ends with a period.", "Ends with a period"},
		{"one two three four five six seven eight nine ten eleven",
			"one two three four five six seven eight nine"}, // capped at 9 words
		{"", ""},
		{"   ", ""},
		{"line1\nline2", "line1 line2"},
	}
	for _, c := range cases {
		if got := sanitizeTitle(c.in); got != c.want {
			t.Errorf("sanitizeTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeTitle_ClampsLength(t *testing.T) {
	// 9 very long words still get clamped to the rune cap.
	long := ""
	for i := 0; i < 9; i++ {
		for j := 0; j < 30; j++ {
			long += "x"
		}
		long += " "
	}
	got := sanitizeTitle(long)
	if len([]rune(got)) > sessionMetaMaxTitleLen {
		t.Errorf("sanitizeTitle did not clamp: %d runes", len([]rune(got)))
	}
}

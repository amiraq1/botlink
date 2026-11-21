package store

import (
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello team", "hello team"},
		{"http link", "see https://example.com/x now", "see [LINK_REMOVED] now"},
		{"www link", "visit www.example.com today", "visit [LINK_REMOVED] today"},
		{"http uppercase", "SEE HTTP://EXAMPLE.COM/X", "SEE [LINK_REMOVED]"},
		{"bracketed link", "see [https://example.com] ok", "see [LINK_REMOVED] ok"},
		{"control chars", "a\x00b\x07c", "abc"},
		{"bidi + zero width", "a\u202e b\u200b c", "a b c"},
		{"tab becomes space", "a\tb", "a b"},
		{"newline kept", "a\nb", "a\nb"},
		{"trimmed", "  hello  ", "hello"},
		{"empty after sanitize", "https://example.com", "[LINK_REMOVED]"},
	}
	for _, c := range cases {
		if got := Sanitize(c.in); got != c.want {
			t.Errorf("%s: Sanitize(%q) = %q; want %q", c.name, c.in, got, c.want)
		}
	}
	// Strict 3000-rune cap.
	long := strings.Repeat("x", 5000)
	if got := Sanitize(long); len([]rune(got)) != 3000 {
		t.Errorf("long input sanitized to %d runes; want 3000", len([]rune(got)))
	}
	// Input is examined only up to 30000 runes.
	huge := strings.Repeat("y", 40000)
	if got := Sanitize(huge); len([]rune(got)) != 3000 {
		t.Errorf("huge input sanitized to %d runes; want 3000", len([]rune(got)))
	}
}

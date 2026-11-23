package store

import (
	"strings"
	"testing"
	"unicode/utf8"
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
		{"variation selectors", "a\ufe0f b\U000e0100 c\uffa0 d\u3164", "a b c d"},
		{"tab becomes space", "a\tb", "a b"},
		{"newline kept", "a\nb", "a\nb"},
		{"trimmed", "  hello  ", "hello"},
		{"empty after sanitize", "https://example.com", "[LINK_REMOVED]"},
		{"arabic text", "مرحبا بكم في الفريق", "مرحبا بكم في الفريق"},
		{"arabic with link", "راجع الرابط https://botlink.app للتفاصيل", "راجع الرابط [LINK_REMOVED] للتفاصيل"},
		{"arabic with bidi and invisible", "مرحبا\u200f \u200eبكم\u200b", "مرحبا بكم"},
		{"emojis preserved", "بوت المحادثة 🤖 جاهز للعمل 🚀", "بوت المحادثة 🤖 جاهز للعمل 🚀"},
		{"mixed rtl ltr", "Bot رقم 1 جاهز للرد في room_123", "Bot رقم 1 جاهز للرد في room_123"},
	}
	for _, c := range cases {
		if got := Sanitize(c.in); got != c.want {
			t.Errorf("%s: Sanitize(%q) = %q; want %q", c.name, c.in, got, c.want)
		}
	}

	// Strict 3000-rune cap (ASCII).
	long := strings.Repeat("x", 5000)
	if got := Sanitize(long); len([]rune(got)) != 3000 {
		t.Errorf("long input sanitized to %d runes; want 3000", len([]rune(got)))
	}

	// Strict 3000-rune cap with 2-byte Arabic text (3000 runes != 3000 bytes).
	arabicRepeat := strings.Repeat("مرحبا", 600) // 5 runes * 600 = 3000 runes
	sanitizedArabic := Sanitize(arabicRepeat)
	if utf8.RuneCountInString(sanitizedArabic) != 3000 {
		t.Errorf("arabic input sanitized to %d runes; want 3000", utf8.RuneCountInString(sanitizedArabic))
	}
	if !utf8.ValidString(sanitizedArabic) {
		t.Error("arabic sanitization produced invalid UTF-8 string")
	}

	// Strict 3000-rune cap with 4-byte emojis (never split mid-rune).
	emojiRepeat := strings.Repeat("🤖💬", 1600) // 2 runes * 1600 = 3200 runes
	sanitizedEmoji := Sanitize(emojiRepeat)
	if utf8.RuneCountInString(sanitizedEmoji) != 3000 {
		t.Errorf("emoji input sanitized to %d runes; want 3000", utf8.RuneCountInString(sanitizedEmoji))
	}
	if !utf8.ValidString(sanitizedEmoji) {
		t.Error("emoji sanitization produced invalid UTF-8 string (split mid-rune)")
	}

	// Input is examined only up to 30000 runes.
	huge := strings.Repeat("y", 40000)
	if got := Sanitize(huge); len([]rune(got)) != 3000 {
		t.Errorf("huge input sanitized to %d runes; want 3000", len([]rune(got)))
	}
}

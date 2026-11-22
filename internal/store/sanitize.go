package store

import (
	"strings"
	"unicode"
)

// Sanitize ports the exact sanitization logic from src/backend/lib/rooms.mo:
//
//   - Pass 1: drop control/invisible/bidi characters (keep newlines, tabs
//     become spaces), looking at most at the first 30,000 runes.
//   - Pass 2: replace each http://, https:// or www. link run (up to the
//     next whitespace) with a single "[LINK_REMOVED]" marker; a link wrapped
//     as "[http://...]" must not leave a doubled bracket behind.
//   - Trim and cap at 3000 runes.
func Sanitize(raw string) string {
	// Pass 1.
	var cleaned []rune
	count := 0
	for _, r := range raw {
		if count >= 30000 {
			break
		}
		count++
		switch {
		case r == '\n':
			cleaned = append(cleaned, r)
		case r == '\t':
			cleaned = append(cleaned, ' ')
		case !isStripped(r):
			cleaned = append(cleaned, r)
		}
	}

	// Pass 2.
	patterns := []string{"http://", "https://", "www."}
	var result []rune
	i := 0
	for i < len(cleaned) {
		if (lowerRune(cleaned[i]) == 'h' || lowerRune(cleaned[i]) == 'w') && matchPattern(cleaned, i, patterns) {
			// A link wrapped as "[http://...]" must not leave a doubled bracket behind.
			if n := len(result); n > 0 && result[n-1] == '[' {
				result = result[:n-1]
			}
			result = append(result, []rune("[LINK_REMOVED]")...)
			for i < len(cleaned) && !unicode.IsSpace(cleaned[i]) {
				i++
			}
			continue
		}
		result = append(result, cleaned[i])
		i++
	}

	trimmed := strings.TrimSpace(string(result))
	return strings.TrimSpace(truncateRunes(trimmed, 3000))
}

// isStripped reports whether a rune is a control / invisible / bidi-control
// character that must never reach a room. Mirrors rooms.mo isStripped.
func isStripped(r rune) bool {
	code := r
	switch {
	case code < 32:
		return true
	case code >= 127 && code <= 159:
		return true
	case code == 173, // soft hyphen
		code == 847,  // combining grapheme joiner
		code == 1564, // arabic letter mark
		code == 4447,
		code == 4448,
		code == 6158:
		return true
	case code >= 8203 && code <= 8207: // zero-width space/joiners, LRM, RLM
		return true
	case code >= 8232 && code <= 8238: // line/paragraph separators, bidi embeddings
		return true
	case code >= 8288 && code <= 8303: // word joiner, invisible operators, bidi isolates
		return true
	case code == 12644, // Hangul filler
		code == 65279, // BOM / zero-width no-break space
		code == 65280, // Hangul filler (invisible)
		code == 65440:
		return true
	case code >= 65024 && code <= 65039: // variation selectors FE00-FE0F
		return true
	case code >= 65529 && code <= 65531:
		return true
	case code >= 917504 && code <= 917631: // tag characters
		return true
	case code >= 917760 && code <= 917999: // variation selectors E0100-E01EF
		return true
	default:
		return false
	}
}

func lowerRune(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return r
}

func matchPattern(rs []rune, i int, patterns []string) bool {
	for _, p := range patterns {
		pr := []rune(p)
		if i+len(pr) > len(rs) {
			continue
		}
		ok := true
		for j, pc := range pr {
			if lowerRune(rs[i+j]) != pc {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func truncateRunes(s string, limit int) string {
	rs := []rune(s)
	if len(rs) > limit {
		return string(rs[:limit])
	}
	return s
}

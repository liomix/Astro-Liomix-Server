package spam

import (
	"strings"
	"unicode"
)

// normalize prepares a field for matching.
//
// Word lists are only as good as the text they are matched against, and the
// cheapest evasion is typographic: a full-width "ｗｅｃｈａｔ", a zero-width space
// inside 赌场, a soft hyphen between letters. Folding those before matching
// costs nothing and makes every rule — not just the lexicon — harder to walk
// around.
//
// It returns lower-cased text with single spaces, so callers can compare and
// fingerprint without caring about the original spacing.
func normalize(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		// Zero-width characters and the soft hyphen are invisible in a browser
		// and exist here only to break up a word.
		case r == '\u200b', r == '\u200c', r == '\u200d', r == '\u200e', r == '\u200f',
			r == '\u2060', r == '\ufeff', r == '\u00ad':
			continue
		// Full-width ASCII maps onto its ASCII twin, one block away.
		case r >= '\uff01' && r <= '\uff5e':
			b.WriteRune(r - 0xfee0)
		// Ideographic space is a space.
		case r == '\u3000':
			b.WriteRune(' ')
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

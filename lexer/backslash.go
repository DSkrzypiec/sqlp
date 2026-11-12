package lexer

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Strings, bytes and quoted names with backslash escapes, as BigQuery writes
// them (Config.BackslashEscapes, PrefixedStrings, AdjacentStrings). Value holds
// the decoded content. Every rule here was checked against BigQuery.

// stringStartAt reports whether a string literal starts at i: a quote, after
// up to two prefix letters (r for raw, b for bytes, in either order and case)
// when PrefixedStrings is set. n is the number of prefix letters.
func (l *Lexer) stringStartAt(i int) (raw, bytes bool, n int, ok bool) {
	for ; n < 2 && l.cfg.PrefixedStrings && i+n < len(l.src); n++ {
		switch l.src[i+n] | 0x20 { // lower case for letters
		case 'r':
			if raw {
				return false, false, 0, false
			}
			raw = true
			continue
		case 'b':
			if bytes {
				return false, false, 0, false
			}
			bytes = true
			continue
		}
		break
	}
	if i+n >= len(l.src) {
		return false, false, 0, false
	}
	switch l.src[i+n] {
	case '\'':
		return raw, bytes, n, true
	case '"':
		return raw, bytes, n, l.cfg.DoubleQuotedStrings
	}
	return false, false, 0, false
}

// backslashString scans the literal whose quote is at l.pos. With
// AdjacentStrings, literals that follow it after whitespace or comments are
// joined to it; strings and bytes cannot be mixed.
func (l *Lexer) backslashString(start int, raw, bytes bool) Token {
	var sb strings.Builder
	for {
		if msg := l.backslashLiteral(&sb, raw, bytes); msg != "" {
			return l.illegal(start, "%s", msg)
		}
		end := l.pos
		gap := end
		if l.cfg.AdjacentStrings {
			gap = l.skipGap(end)
		}
		nextRaw, nextBytes, n, ok := l.stringStartAt(gap)
		switch {
		case !ok || !l.cfg.AdjacentStrings:
			return Token{Kind: String, Text: l.src[start:end], Value: sb.String(), Pos: start, Bytes: bytes}
		case nextBytes != bytes:
			return l.illegal(gap, "string and bytes literals cannot be concatenated")
		case gap == end:
			return l.illegal(gap, "concatenated string literals must be separated by whitespace or comments")
		}
		l.pos = gap + n
		raw = nextRaw
	}
}

// skipGap returns the offset after the whitespace and comments at i.
func (l *Lexer) skipGap(i int) int {
	for i < len(l.src) {
		switch c := l.src[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			i++
		case c == '-' && strings.HasPrefix(l.src[i:], "--"), c == '#' && l.cfg.HashComments:
			for i < len(l.src) && l.src[i] != '\n' && !(l.src[i] == '\r' && l.cfg.CommentEndsAtCR) {
				i++
			}
		case c == '/' && strings.HasPrefix(l.src[i:], "/*"):
			end := strings.Index(l.src[i+2:], "*/")
			if end < 0 {
				return i // left for the scanner to report
			}
			i += end + 4
		default:
			return i
		}
	}
	return i
}

// backslashLiteral scans one literal from its opening quote at l.pos, adding
// its content to sb, and returns an error message or "".
func (l *Lexer) backslashLiteral(sb *strings.Builder, raw, bytes bool) string {
	q := l.src[l.pos]
	closing := string(q)
	if l.cfg.PrefixedStrings && strings.HasPrefix(l.src[l.pos:], strings.Repeat(closing, 3)) {
		closing = strings.Repeat(closing, 3) // may span lines
	}
	l.pos += len(closing)
	for l.pos < len(l.src) {
		switch c := l.src[l.pos]; {
		case strings.HasPrefix(l.src[l.pos:], closing):
			l.pos += len(closing)
			return ""
		case (c == '\n' || c == '\r') && len(closing) == 1:
			return "unterminated string literal"
		case c == '\\' && raw:
			// The backslash stays, and the character after it cannot end the
			// literal.
			if l.pos+1 >= len(l.src) {
				return "unterminated raw string literal"
			}
			sb.WriteString(l.src[l.pos : l.pos+2])
			l.pos += 2
		case c == '\\':
			if msg := l.backslashEscape(sb, bytes, false); msg != "" {
				return msg
			}
		default:
			sb.WriteByte(c)
			l.pos++
		}
	}
	if raw {
		return "unterminated raw string literal"
	}
	return "unterminated string literal"
}

var simpleEscapes = map[byte]byte{
	'a': '\a', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', 'v': '\v',
	'\\': '\\', '?': '?', '"': '"', '\'': '\'', '`': '`',
}

// backslashEscape decodes the escape at l.pos into sb.
func (l *Lexer) backslashEscape(sb *strings.Builder, bytes, ident bool) string {
	if l.pos+1 >= len(l.src) {
		if ident {
			return "unterminated quoted identifier"
		}
		return "unterminated string literal"
	}
	c := l.src[l.pos+1]
	if v, ok := simpleEscapes[c]; ok {
		sb.WriteByte(v)
		l.pos += 2
		return ""
	}
	digits := func(n int, valid func(byte) bool) (uint32, bool) {
		if l.pos+2+n > len(l.src) {
			return 0, false
		}
		var v uint32
		for _, d := range []byte(l.src[l.pos+2 : l.pos+2+n]) {
			if !valid(d) {
				return 0, false
			}
			v = v*16 + uint32(hexDigit(d))
		}
		return v, true
	}
	switch {
	case c >= '0' && c <= '3':
		s := l.src[l.pos+1 : min(l.pos+4, len(l.src))]
		if len(s) < 3 || !isOctal(s[1]) || !isOctal(s[2]) {
			return "illegal escape sequence: an octal escape needs 3 octal digits"
		}
		sb.WriteByte((c-'0')<<6 | (s[1]-'0')<<3 | (s[2] - '0'))
		l.pos += 4
	case c == 'x' || c == 'X':
		v, ok := digits(2, isHexDigit)
		if !ok {
			return "illegal escape sequence: a hex escape needs 2 hex digits"
		}
		sb.WriteByte(byte(v))
		l.pos += 4
	case c == 'u' || c == 'U':
		if bytes {
			return `illegal escape sequence: \u cannot be used in bytes literals`
		}
		n := map[byte]int{'u': 4, 'U': 8}[c]
		v, ok := digits(n, isHexDigit)
		switch {
		case !ok:
			return fmt.Sprintf(`illegal escape sequence: \%c needs %d hex digits`, c, n)
		case v >= 0xD800 && v <= 0xDFFF || v > utf8.MaxRune:
			return fmt.Sprintf(`illegal escape sequence: \%c%0*X is not a valid character`, c, n, v)
		}
		sb.WriteRune(rune(v))
		l.pos += 2 + n
	case c == '\n' || c == '\r':
		return "illegal escaped newline"
	default:
		return fmt.Sprintf(`illegal escape sequence: \%c`, c)
	}
	return ""
}

// backslashIdent scans a quoted name with backslash escapes. A doubled quote
// is not an escape: `a“b` is two names.
func (l *Lexer) backslashIdent(start int, q byte) Token {
	l.pos++
	var sb strings.Builder
	for l.pos < len(l.src) {
		switch c := l.src[l.pos]; {
		case c == q:
			l.pos++
			if sb.Len() == 0 && l.cfg.NoEmptyQuotedIdent {
				return l.illegal(start, "zero-length delimited identifier")
			}
			return Token{Kind: QuotedIdent, Text: l.src[start:l.pos], Value: sb.String(), Pos: start}
		case c == '\n' || c == '\r':
			return l.illegal(start, "unterminated quoted identifier")
		case c == '\\':
			if msg := l.backslashEscape(&sb, false, true); msg != "" {
				return l.illegal(start, "%s", msg)
			}
		default:
			sb.WriteByte(c)
			l.pos++
		}
	}
	return l.illegal(start, "unterminated quoted identifier")
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

func hexDigit(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}

// BackslashQuote writes s between quotes q for a dialect with backslash
// escapes. Control characters, and in bytes every byte that is not printable
// ASCII, are written as escapes; so is any byte of a string that is not part of
// valid UTF-8.
func BackslashQuote(s string, q byte, bytes bool) string {
	var b strings.Builder
	b.WriteByte(q)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		c := s[i]
		switch {
		case c == '\\' || c == q:
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20 || c == 0x7f || c >= utf8.RuneSelf && (bytes || r == utf8.RuneError && size == 1):
			fmt.Fprintf(&b, `\x%02x`, c)
			size = 1
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte(q)
	return b.String()
}

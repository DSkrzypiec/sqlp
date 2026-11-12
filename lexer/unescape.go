package lexer

import (
	"strings"
	"unicode/utf8"
)

// Unescape resolves the escapes of an E'...' string body for dialects that
// have no escape strings, following PostgreSQL's documented set. An octal or
// hexadecimal escape gives one byte, so a body that passed CheckEscapeString
// gives valid UTF-8.
func Unescape(s string) string {
	var sb strings.Builder
	var high rune // a UTF-16 high surrogate waiting for its low half
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' && i+1 < len(s) && s[i+1] == '\'' {
			sb.WriteByte('\'')
			i++
			continue
		}
		if c != '\\' || i+1 >= len(s) {
			sb.WriteByte(c)
			continue
		}
		i++
		switch e := s[i]; {
		case e == 'n':
			sb.WriteByte('\n')
		case e == 't':
			sb.WriteByte('\t')
		case e == 'r':
			sb.WriteByte('\r')
		case e == 'b':
			sb.WriteByte('\b')
		case e == 'f':
			sb.WriteByte('\f')
		case e >= '0' && e <= '7':
			v, n := radixRun(s[i:], 8, 3)
			sb.WriteByte(byte(v))
			i += n - 1
		case e == 'x':
			if v, n := radixRun(s[i+1:], 16, 2); n > 0 {
				sb.WriteByte(byte(v))
				i += n
			} else {
				sb.WriteByte(e)
			}
		case e == 'u' || e == 'U':
			want := 4
			if e == 'U' {
				want = 8
			}
			v, n := radixRun(s[i+1:], 16, want)
			if n != want {
				sb.WriteByte(e)
				break
			}
			i += n
			switch r := rune(v); {
			case r >= 0xD800 && r <= 0xDBFF:
				high = r
			case r >= 0xDC00 && r <= 0xDFFF && high != 0:
				sb.WriteRune((high-0xD800)<<10 + (r - 0xDC00) + 0x10000)
				high = 0
			default:
				sb.WriteRune(r)
			}
		default:
			sb.WriteByte(e)
		}
	}
	return sb.String()
}

// radixRun reads up to max digits in the given base from the start of s,
// returning the value and how many digits it used.
func radixRun(s string, base, max int) (value, n int) {
	for n < max && n < len(s) {
		d := -1
		switch c := s[n]; {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		}
		if d < 0 || d >= base {
			break
		}
		value = value*base + d
		n++
	}
	return value, n
}

const (
	badUnicodeEscape = "invalid Unicode escape"
	badUnicodeValue  = "invalid Unicode escape value"
	badSurrogatePair = "invalid Unicode surrogate pair"
	badByteSequence  = "invalid byte sequence for encoding \"UTF8\""
)

// surrogates tracks a UTF-16 high surrogate waiting for its low half.
type surrogates struct{ pending bool }

func (u *surrogates) add(c uint32) string {
	if c == 0 || c > 0x10FFFF {
		return badUnicodeValue
	}
	high, low := c >= 0xD800 && c <= 0xDBFF, c >= 0xDC00 && c <= 0xDFFF
	if u.pending {
		u.pending = false
		if !low {
			return badSurrogatePair
		}
		return ""
	}
	if low {
		return badSurrogatePair
	}
	u.pending = high
	return ""
}

// hexValue parses exactly the first n bytes of s as hexadecimal.
func hexValue(s string, n int) (uint32, bool) {
	if len(s) < n {
		return 0, false
	}
	var v uint32
	for i := range n {
		d := strings.IndexByte("0123456789abcdef", s[i]|0x20)
		if d < 0 || s[i] < '0' {
			return 0, false
		}
		v = v<<4 | uint32(d)
	}
	return v, true
}

// CheckEscapeString returns what is wrong with the escapes in the body of an
// E'...' string, or "". PostgreSQL checks the \u and \U escapes one by one.
// Octal and hexadecimal escapes give single bytes, and once one of them is
// zero or above 0x7F the whole string must be valid UTF-8 without a zero byte.
func CheckEscapeString(s string) string {
	var u surrogates
	rawByte := false
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			if u.pending {
				return badSurrogatePair
			}
			continue
		}
		i++
		digits := map[byte]int{'u': 4, 'U': 8}[s[i]]
		if digits == 0 {
			if u.pending {
				return badSurrogatePair
			}
			v, n := -1, 0
			if s[i] >= '0' && s[i] <= '7' {
				v, n = radixRun(s[i:], 8, 3)
			} else if s[i] == 'x' {
				if v, n = radixRun(s[i+1:], 16, 2); n == 0 {
					v = -1
				}
			}
			if b := byte(v); v >= 0 && (b == 0 || b >= utf8.RuneSelf) {
				rawByte = true
			}
			continue
		}
		c, ok := hexValue(s[i+1:], digits)
		if !ok {
			return badUnicodeEscape
		}
		if msg := u.add(c); msg != "" {
			return msg
		}
		i += digits
	}
	if u.pending {
		return badSurrogatePair
	}
	if rawByte {
		if text := Unescape(s); !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
			return badByteSequence
		}
	}
	return ""
}

// CheckUnicodeString returns what is wrong with the escapes in the body of a
// U&'...' string or U&"..." identifier using escape character esc, or "".
func CheckUnicodeString(s string, esc byte) string {
	var u surrogates
	for i := 0; i < len(s); i++ {
		if s[i] != esc {
			if u.pending {
				return badSurrogatePair
			}
			continue
		}
		if i+1 < len(s) && s[i+1] == esc {
			if u.pending {
				return badSurrogatePair
			}
			i++
			continue
		}
		start, digits := i+1, 4
		if i+1 < len(s) && s[i+1] == '+' {
			start, digits = i+2, 6
		}
		c, ok := hexValue(s[start:], digits)
		if !ok {
			return badUnicodeEscape
		}
		if msg := u.add(c); msg != "" {
			return msg
		}
		i = start + digits - 1
	}
	if u.pending {
		return badSurrogatePair
	}
	return ""
}

// DecodeUnicode resolves the escapes in the body of a U&'...' string or
// U&"..." identifier that uses escape character esc. It assumes the body
// passed CheckUnicodeString; an escape that does not parse is kept as written.
func DecodeUnicode(s string, esc byte) string {
	var sb strings.Builder
	var high rune // a UTF-16 high surrogate waiting for its low half
	for i := 0; i < len(s); i++ {
		if s[i] != esc {
			sb.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == esc {
			sb.WriteByte(esc)
			i++
			continue
		}
		start, digits := i+1, 4
		if i+1 < len(s) && s[i+1] == '+' {
			start, digits = i+2, 6
		}
		c, ok := hexValue(s[start:], digits)
		if !ok {
			sb.WriteByte(s[i])
			continue
		}
		i = start + digits - 1
		switch r := rune(c); {
		case r >= 0xD800 && r <= 0xDBFF:
			high = r
		case r >= 0xDC00 && r <= 0xDFFF && high != 0:
			sb.WriteRune((high-0xD800)<<10 + (r - 0xDC00) + 0x10000)
			high = 0
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// ValidUEscape reports whether s can follow UESCAPE.
func ValidUEscape(s string) bool {
	if len(s) != 1 {
		return false
	}
	c := s[0]
	_, hex := hexValue(s, 1)
	return !hex && !strings.ContainsRune("+'\" \t\n\r\f", rune(c)) // not \v: no whitespace in PostgreSQL 16
}

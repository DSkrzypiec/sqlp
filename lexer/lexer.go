// Package lexer turns SQL text into tokens. It is dialect-agnostic: every
// difference between dialects is expressed through Config, which is pure data.
//
// Keywords are not decided here. Every bare word is emitted as an Ident token
// whose Value is the upper-cased text; the parser decides, based on position
// and the dialect's reserved-word table, whether a word is a keyword or a name.
package lexer

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Kind classifies a token.
type Kind uint8

const (
	EOF         Kind = iota
	Illegal          // lexing error; Value holds the message
	Ident            // bare word; Value is the upper-cased text
	QuotedIdent      // "x", `x`, [x]; Value is the unquoted name
	String           // 'x', E'x', $$x$$; Value is the logical content (see Token.Escape)
	Number           // 1, 1.5, .5, 1e10, 0x1F
	Blob             // X'0F' / B'01'; Value is the raw text
	Param            // $1, ?, ?1, :name, @name, $name; Value is the raw text
	Op               // operators and punctuation; Value == Text
	Comment          // -- or /* */; Value is the raw text
)

var kindNames = [...]string{
	EOF: "EOF", Illegal: "Illegal", Ident: "Ident", QuotedIdent: "QuotedIdent",
	String: "String", Number: "Number", Blob: "Blob", Param: "Param", Op: "Op", Comment: "Comment",
}

func (k Kind) String() string { return kindNames[k] }

// Token is a single lexeme.
type Token struct {
	Kind  Kind
	Text  string // raw source text
	Value string // normalized value, meaning depends on Kind
	Pos   int    // byte offset of the first character
	// Escape is set on String tokens written as E'...' (PostgreSQL). For such
	// tokens Value holds the raw inner text with backslash escapes untouched.
	Escape bool
	// Unicode is set on String tokens written as U&'...' (PostgreSQL).
	Unicode bool
	// Bytes is set on String tokens written as b'...' (BigQuery).
	Bytes bool
}

func (t Token) String() string {
	if t.Kind == EOF {
		return "EOF"
	}
	return fmt.Sprintf("%s(%q)", t.Kind, t.Text)
}

// Config describes the dialect-specific lexical rules.
type Config struct {
	// IdentQuotes lists the opening characters that start a quoted identifier.
	// `"` closes with `"`, "`" with "`", "[" with "]".
	IdentQuotes string
	// DollarQuotes enables PostgreSQL $tag$...$tag$ strings.
	DollarQuotes bool
	// EscapeStrings enables PostgreSQL E'...' strings with backslash escapes.
	EscapeStrings bool
	// BlobLiterals enables X'..' literals.
	BlobLiterals bool
	// BitStrings enables B'..' literals (PostgreSQL). In SQLite `b'x'` is a
	// column followed by a string alias.
	BitStrings bool
	// UnicodeStrings enables U&'...' strings (PostgreSQL).
	UnicodeStrings bool
	// NationalStrings reads N'...' as the word NCHAR followed by a string
	// (PostgreSQL). The Ident token keeps Text "N" and has Value "NCHAR".
	NationalStrings bool
	// StringContinuation joins 'a' <newline> 'b' into one literal (PostgreSQL).
	StringContinuation bool
	// RadixLiterals enables 0o17 and 0b101 (PostgreSQL 16). 0x1F is always on.
	RadixLiterals bool
	// DigitSeparators enables 1_000_000. A separator sits between two digits.
	DigitSeparators bool
	// RadixSeparator also allows a separator right after a radix prefix, as in
	// 0x_1F (PostgreSQL).
	RadixSeparator bool
	// LaxBlockComments lets an unterminated /* run to end of input (SQLite).
	LaxBlockComments bool
	// StrictBlobs requires X'..' to hold an even number of hex digits (SQLite).
	StrictBlobs bool
	// NoEmptyQuotedIdent rejects "" as an identifier (PostgreSQL).
	NoEmptyQuotedIdent bool
	// ParamPrefixes lists the characters that start a bind parameter.
	// PostgreSQL: "$" ($1). SQLite: "?:@$#" (?, ?1, :a, @a, $a, #a).
	ParamPrefixes string
	// TclParams lets a named parameter contain "::" and end with a
	// parenthesized suffix, as in $ns::arr(key) (SQLite).
	TclParams bool
	// CustomOperators enables PostgreSQL's rule that any run of operator
	// characters is an operator (with the trailing +/- restriction).
	// When false, only the operators in Operators are recognized.
	CustomOperators bool
	// Operators is the fixed operator set, longest first, used when
	// CustomOperators is false. Single-char punctuation is always recognized.
	Operators []string
	// NestedComments enables nested /* /* */ */ comments (PostgreSQL).
	NestedComments bool
	// CommentEndsAtCR ends a -- comment at a carriage return as well as at a
	// line feed (PostgreSQL). SQLite ends it only at a line feed.
	CommentEndsAtCR bool
	// BackslashEscapes makes a backslash escape the next character in strings
	// and quoted identifiers, where a doubled quote is not an escape (BigQuery).
	// The escapes are decoded into Value and invalid ones are errors.
	BackslashEscapes bool
	// DoubleQuotedStrings makes "..." a string, not a name (BigQuery).
	DoubleQuotedStrings bool
	// PrefixedStrings enables raw r'...' and bytes b'...' literals, in either
	// order and case, and tripled quotes ''' or """ that may span lines
	// (BigQuery). It goes with BackslashEscapes.
	PrefixedStrings bool
	// AdjacentStrings joins literals separated by whitespace or comments into
	// one; literals with nothing between them are an error (BigQuery).
	AdjacentStrings bool
	// HashComments makes # start a line comment (BigQuery).
	HashComments bool
	// PlainQuestionMarks makes ? a parameter that takes no number (BigQuery):
	// ?1 is ? followed by 1.
	PlainQuestionMarks bool
	// DashedPaths: in my-project-123.dataset, a number right after word- ends
	// before a dot that leads into a name, for the parser to join the parts
	// (BigQuery's project names).
	DashedPaths bool
	// ASCIIIdents allows only ASCII letters, digits and _ in bare names
	// (BigQuery). Otherwise any byte >= 0x80 and $ count too.
	ASCIIIdents bool
	// BOMIsSpace treats a UTF-8 byte-order mark between tokens as whitespace
	// (SQLite). Elsewhere its bytes are identifier characters.
	BOMIsSpace bool
}

// Lexer is a single-pass scanner over a SQL string.
type Lexer struct {
	cfg Config
	src string
	pos int
}

// New returns a lexer over src.
func New(src string, cfg Config) *Lexer {
	return &Lexer{cfg: cfg, src: src}
}

// Tokenize lexes the whole input, dropping comments. It stops at the first
// Illegal token and returns it as an error.
func Tokenize(src string, cfg Config) ([]Token, error) {
	lx := New(src, cfg)
	var toks []Token
	for {
		t := lx.Next()
		switch t.Kind {
		case Comment:
			continue
		case Illegal:
			return toks, fmt.Errorf("at offset %d: %s", t.Pos, t.Value)
		}
		toks = append(toks, t)
		if t.Kind == EOF {
			return toks, nil
		}
	}
}

// Next returns the next token, including comments.
func (l *Lexer) Next() Token {
	l.skipSpace()
	start := l.pos
	if l.pos >= len(l.src) {
		return Token{Kind: EOF, Pos: start}
	}
	return l.scan(start)
}

func (l *Lexer) scan(start int) Token {
	c := l.src[l.pos]
	switch {
	case c == '-' && l.peekAt(1) == '-', c == '#' && l.cfg.HashComments:
		return l.lineComment(start)
	case l.cfg.BackslashEscapes && (c == '\'' || c == '"' && l.cfg.DoubleQuotedStrings):
		return l.backslashString(start, false, false)
	case c == '/' && l.peekAt(1) == '*' && !(l.cfg.LaxBlockComments && l.pos+2 >= len(l.src)):
		return l.blockComment(start) // SQLite reads a final "/*" as two operators
	case c == '\'':
		return l.quotedString(start, false)
	case c == '"' || strings.IndexByte(l.cfg.IdentQuotes, c) >= 0:
		if c == '"' && strings.IndexByte(l.cfg.IdentQuotes, '"') < 0 {
			return l.unexpectedChar(start)
		}
		if l.cfg.BackslashEscapes {
			return l.backslashIdent(start, c)
		}
		return l.quotedIdent(start, c)
	case isDigit(c), c == '.' && isDigit(l.peekAt(1)):
		return l.number(start)
	case (c == 'E' || c == 'e') && l.cfg.EscapeStrings && l.peekAt(1) == '\'':
		l.pos++
		return l.quotedString(start, true)
	case (c == 'U' || c == 'u') && l.cfg.UnicodeStrings && l.peekAt(1) == '&' && l.peekAt(2) == '\'':
		l.pos += 2
		t := l.quotedString(start, false)
		t.Unicode = t.Kind == String
		return t
	case (c == 'U' || c == 'u') && l.cfg.UnicodeStrings && l.peekAt(1) == '&' && l.peekAt(2) == '"':
		l.pos += 2
		t := l.quotedIdent(start, '"')
		t.Unicode = t.Kind == QuotedIdent
		return t
	case (c == 'N' || c == 'n') && l.cfg.NationalStrings && l.peekAt(1) == '\'':
		l.pos++
		t := l.tok(Ident, start)
		t.Value = "NCHAR"
		return t
	case (c == 'X' || c == 'x') && l.cfg.BlobLiterals && l.peekAt(1) == '\'':
		return l.blob(start)
	case (c == 'B' || c == 'b') && l.cfg.BitStrings && l.peekAt(1) == '\'':
		return l.blob(start)
	case c == '$':
		return l.dollar(start)
	case strings.IndexByte(l.cfg.ParamPrefixes, c) >= 0 && l.paramFollows(c):
		return l.param(start)
	case l.identStart(c):
		if raw, bytes, n, ok := l.stringStartAt(l.pos); ok && n > 0 {
			l.pos += n
			return l.backslashString(start, raw, bytes)
		}
		return l.ident(start)
	case strings.IndexByte("(),;[]", c) >= 0:
		l.pos++
		return l.tok(Op, start)
	case c == '.':
		l.pos++
		return l.tok(Op, start)
	case c == ':' && l.peekAt(1) == ':':
		l.pos += 2
		return l.tok(Op, start)
	case c == ':' && l.peekAt(1) == '=':
		l.pos += 2
		return l.tok(Op, start)
	case c == ':' && !l.cfg.CustomOperators:
		l.pos++
		return l.tok(Op, start)
	case isOpChar(c) || c == ':':
		return l.operator(start)
	}
	return l.unexpectedChar(start)
}

// --- helpers -------------------------------------------------------------

func (l *Lexer) tok(k Kind, start int) Token {
	text := l.src[start:l.pos]
	return Token{Kind: k, Text: text, Value: text, Pos: start}
}

// unexpectedChar reports a character that starts no token. Unlike other
// lexing errors it does not end the input: the lexer resumes after it, so a
// script can go on with its next statement.
func (l *Lexer) unexpectedChar(start int) Token {
	r, size := utf8.DecodeRuneInString(l.src[start:])
	l.pos = start + size
	return Token{Kind: Illegal, Text: l.src[start:l.pos], Pos: start, Value: fmt.Sprintf("unexpected character %q", r)}
}

// AtEnd reports whether the lexer has consumed all of its input.
func (l *Lexer) AtEnd() bool { return l.pos >= len(l.src) }

func (l *Lexer) illegal(start int, format string, args ...any) Token {
	l.pos = len(l.src)
	return Token{Kind: Illegal, Pos: start, Value: fmt.Sprintf(format, args...)}
}

func (l *Lexer) peekAt(n int) byte {
	if l.pos+n < len(l.src) {
		return l.src[l.pos+n]
	}
	return 0
}

// skipSpace skips whitespace. A vertical tab is not whitespace in PostgreSQL 16
// or in SQLite.
func (l *Lexer) skipSpace() {
	for l.pos < len(l.src) {
		switch c := l.src[l.pos]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			l.pos++
		case l.cfg.BOMIsSpace && strings.HasPrefix(l.src[l.pos:], bom):
			l.pos += len(bom)
		default:
			return
		}
	}
}

const bom = "\xEF\xBB\xBF"

func (l *Lexer) lineComment(start int) Token {
	for l.pos < len(l.src) && l.src[l.pos] != '\n' && !(l.src[l.pos] == '\r' && l.cfg.CommentEndsAtCR) {
		l.pos++
	}
	return l.tok(Comment, start)
}

func (l *Lexer) blockComment(start int) Token {
	depth := 0
	for l.pos < len(l.src) {
		switch {
		case l.src[l.pos] == '/' && l.peekAt(1) == '*':
			depth++
			l.pos += 2
			if depth > 1 && !l.cfg.NestedComments {
				depth = 1
			}
		case l.src[l.pos] == '*' && l.peekAt(1) == '/':
			depth--
			l.pos += 2
			if depth == 0 {
				return l.tok(Comment, start)
			}
		default:
			l.pos++
		}
	}
	if l.cfg.LaxBlockComments {
		return l.tok(Comment, start)
	}
	return l.illegal(start, "unterminated block comment")
}

// quotedString scans '...' with ” doubling. For escape strings, backslash
// escapes are skipped over but kept verbatim in Value.
func (l *Lexer) quotedString(start int, escape bool) Token {
	l.pos++ // opening quote
	var sb strings.Builder
	inner := l.pos
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\\' && escape && l.pos+1 < len(l.src):
			l.pos += 2
		case c == '\'' && l.peekAt(1) == '\'':
			if !escape {
				sb.WriteString(l.src[inner:l.pos])
				sb.WriteByte('\'')
				inner = l.pos + 2
			}
			l.pos += 2
		case c == '\'':
			sb.WriteString(l.src[inner:l.pos])
			l.pos++
			if next := l.continuation(); next >= 0 {
				l.pos = next + 1
				inner = l.pos
				continue
			}
			return Token{Kind: String, Text: l.src[start:l.pos], Value: sb.String(), Pos: start, Escape: escape}
		default:
			l.pos++
		}
	}
	return l.illegal(start, "unterminated string literal")
}

// continuation returns the offset of the opening quote of a string that
// continues the one just closed, or -1. PostgreSQL requires the two parts to
// be separated by whitespace that contains at least one newline.
func (l *Lexer) continuation() int {
	if !l.cfg.StringContinuation {
		return -1
	}
	newline := false
	for i := l.pos; i < len(l.src); i++ {
		switch l.src[i] {
		case '\n', '\r':
			newline = true
		case ' ', '\t', '\f':
		case '\'':
			if newline {
				return i
			}
			return -1
		default:
			return -1
		}
	}
	return -1
}

func (l *Lexer) quotedIdent(start int, open byte) Token {
	close := open
	if open == '[' {
		close = ']'
	}
	l.pos++
	var sb strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == close {
			if open != '[' && l.peekAt(1) == close { // doubled quote
				sb.WriteByte(c)
				l.pos += 2
				continue
			}
			l.pos++
			if sb.Len() == 0 && l.cfg.NoEmptyQuotedIdent {
				return l.illegal(start, "zero-length delimited identifier")
			}
			return Token{Kind: QuotedIdent, Text: l.src[start:l.pos], Value: sb.String(), Pos: start}
		}
		sb.WriteByte(c)
		l.pos++
	}
	return l.illegal(start, "unterminated quoted identifier")
}

func (l *Lexer) blob(start int) Token {
	l.pos += 2 // X'
	for l.pos < len(l.src) && l.src[l.pos] != '\'' {
		l.pos++
	}
	if l.pos >= len(l.src) {
		return l.illegal(start, "unterminated bit/blob literal")
	}
	l.pos++
	t := l.tok(Blob, start)
	t.Value = strings.ToUpper(t.Text[:1]) + t.Text[1:]
	if l.cfg.StrictBlobs && !validHex(t.Text[2:len(t.Text)-1]) {
		return l.illegal(start, "malformed blob literal")
	}
	return t
}

// digits skips a run of digits. A separator needs a digit on both sides;
// sepFirst lets one come before the first digit too.
func (l *Lexer) digits(valid func(byte) bool, sepFirst bool) {
	first := l.pos
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case valid(c):
			l.pos++
		case c == '_' && l.cfg.DigitSeparators && valid(l.peekAt(1)) && (l.pos > first || sepFirst):
			l.pos += 2
		default:
			return
		}
	}
}

func (l *Lexer) number(start int) Token {
	if l.src[l.pos] == '0' {
		var valid func(byte) bool
		switch l.peekAt(1) {
		case 'x', 'X':
			valid = isHexDigit
		case 'o', 'O':
			if l.cfg.RadixLiterals {
				valid = func(c byte) bool { return c >= '0' && c <= '7' }
			}
		case 'b', 'B':
			if l.cfg.RadixLiterals {
				valid = func(c byte) bool { return c == '0' || c == '1' }
			}
		}
		if valid != nil {
			l.pos += 2
			first := l.pos
			l.digits(valid, l.cfg.RadixSeparator)
			if l.pos == first {
				return l.illegal(start, "malformed numeric literal")
			}
			return l.endNumber(start)
		}
	}
	l.digits(isDigit, false)
	if l.peekAt(0) == '.' && l.peekAt(1) != '.' && !l.dashedPathDot(start) {
		l.pos++
		l.digits(isDigit, false)
	}
	if c := l.peekAt(0); c == 'e' || c == 'E' {
		n := 1
		if s := l.peekAt(1); s == '+' || s == '-' {
			n = 2
		}
		if isDigit(l.peekAt(n)) {
			l.pos += n
			l.digits(isDigit, false)
		}
	}
	return l.endNumber(start)
}

// dashedPathDot reports whether the dot after the number at start separates
// the parts of a dashed path (Config.DashedPaths): the number follows word-,
// and a name, not an exponent, follows the dot.
func (l *Lexer) dashedPathDot(start int) bool {
	if !l.cfg.DashedPaths || start < 2 || l.src[start-1] != '-' || !l.identPart(l.src[start-2]) {
		return false
	}
	c := l.peekAt(1)
	if (c == 'e' || c == 'E') && (isDigit(l.peekAt(2)) || l.peekAt(2) == '+' || l.peekAt(2) == '-') {
		return false
	}
	return l.identStart(c) || c == '`'
}

// endNumber rejects a literal running straight into a word (`1a`), which both
// PostgreSQL (>= 15) and SQLite treat as a lexical error.
func (l *Lexer) endNumber(start int) Token {
	if l.pos < len(l.src) && l.identStart(l.src[l.pos]) {
		return l.illegal(start, "trailing junk after numeric literal")
	}
	return l.tok(Number, start)
}

func (l *Lexer) ident(start int) Token {
	for l.pos < len(l.src) && l.identPart(l.src[l.pos]) {
		l.pos++
	}
	t := l.tok(Ident, start)
	t.Value = strings.ToUpper(t.Text)
	return t
}

// dollar handles $tag$...$tag$ strings, $1 / $name parameters, or falls back
// to treating '$' as an identifier character error.
func (l *Lexer) dollar(start int) Token {
	if l.cfg.DollarQuotes {
		// $tag$ where tag is empty or an identifier not starting with a digit
		i := l.pos + 1
		if i < len(l.src) && isIdentStart(l.src[i]) {
			for i < len(l.src) && isIdentPart(l.src[i]) && l.src[i] != '$' {
				i++
			}
		}
		if i < len(l.src) && l.src[i] == '$' {
			tag := l.src[l.pos : i+1]
			body := i + 1
			end := strings.Index(l.src[body:], tag)
			if end < 0 {
				return l.illegal(start, "unterminated dollar-quoted string")
			}
			l.pos = body + end + len(tag)
			return Token{Kind: String, Text: l.src[start:l.pos], Value: l.src[body : body+end], Pos: start}
		}
	}
	if strings.IndexByte(l.cfg.ParamPrefixes, '$') >= 0 && l.paramFollows('$') {
		return l.param(start)
	}
	return l.unexpectedChar(start)
}

func (l *Lexer) paramFollows(prefix byte) bool {
	c := l.peekAt(1)
	switch prefix {
	case '?':
		return true // bare ? or ?NNN
	case '$':
		if l.cfg.DollarQuotes { // PostgreSQL: only $1, $2, ...
			return isDigit(c)
		}
	}
	if l.cfg.TclParams && c == ':' && l.peekAt(2) == ':' {
		// SQLite: $::a, and even $:: followed by more of the same, as long as
		// a name character comes before the end.
		i := l.pos + 1
		for strings.HasPrefix(l.src[i:], "::") {
			i += 2
		}
		return i < len(l.src) && isIdentPart(l.src[i])
	}
	return isIdentPart(c) // SQLite: $a, :a, @a, also :1 and $$x
}

func (l *Lexer) param(start int) Token {
	prefix := l.src[l.pos]
	l.pos++
	if prefix == '?' || prefix == '$' && l.cfg.DollarQuotes {
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) && !(prefix == '?' && l.cfg.PlainQuestionMarks) {
			l.pos++
		}
		return l.tok(Param, start)
	}
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case isIdentPart(c):
			l.pos++
			continue
		case l.cfg.TclParams && c == ':' && l.peekAt(1) == ':':
			l.pos += 2
			continue
		case l.cfg.TclParams && c == '(':
			// The suffix runs to ")" and may not hold whitespace.
			end := strings.IndexAny(l.src[l.pos:], ") \t\n\v\f\r")
			if end < 0 || l.src[l.pos+end] != ')' {
				return l.illegal(start, "unterminated parameter name")
			}
			l.pos += end + 1
		}
		break
	}
	return l.tok(Param, start)
}

func (l *Lexer) operator(start int) Token {
	if !l.cfg.CustomOperators {
		for _, op := range l.cfg.Operators {
			if strings.HasPrefix(l.src[l.pos:], op) {
				l.pos += len(op)
				return l.tok(Op, start)
			}
		}
		l.pos++
		return l.tok(Op, start)
	}
	// PostgreSQL rule (scan.l): take the longest run of operator characters,
	// stop before a comment start, and trim trailing + or - unless the
	// operator contains at least one of ~ ! @ # % ^ & | ` ?
	end := l.pos
	for end < len(l.src) && isOpChar(l.src[end]) {
		if l.src[end] == '-' && end+1 < len(l.src) && l.src[end+1] == '-' {
			break
		}
		if l.src[end] == '/' && end+1 < len(l.src) && l.src[end+1] == '*' {
			break
		}
		end++
	}
	if end == l.pos { // lone ':'
		end++
	}
	op := l.src[l.pos:end]
	if len(op) > 1 && !strings.ContainsAny(op, "~!@#%^&|`?") {
		for len(op) > 1 && (op[len(op)-1] == '+' || op[len(op)-1] == '-') {
			op = op[:len(op)-1]
		}
	}
	l.pos += len(op)
	return l.tok(Op, start)
}

func validHex(s string) bool {
	if len(s)%2 != 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isHexDigit(s[i]) {
			return false
		}
	}
	return true
}

func isDigit(c byte) bool    { return c >= '0' && c <= '9' }
func isHexDigit(c byte) bool { return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// isIdentStart follows PostgreSQL's scan.l and SQLite's tokenizer: every byte
// >= 0x80 counts as an identifier character, whatever the Unicode category.
func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= utf8.RuneSelf
}
func isIdentPart(c byte) bool { return isIdentStart(c) || isDigit(c) || c == '$' }

func (l *Lexer) identStart(c byte) bool {
	return isIdentStart(c) && !(l.cfg.ASCIIIdents && c >= utf8.RuneSelf)
}

func (l *Lexer) identPart(c byte) bool {
	return l.identStart(c) || isDigit(c) || c == '$' && !l.cfg.ASCIIIdents
}
func isOpChar(c byte) bool { return strings.IndexByte("+-*/<>=~!@#%^&|`?", c) >= 0 }

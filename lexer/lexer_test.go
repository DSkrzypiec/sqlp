package lexer

import (
	"strings"
	"testing"

	"sqlp/internal/testkit"
)

// dialectConfig pairs a lexer configuration with the dialect -dialect selects it by.
type dialectConfig struct {
	Config
	name string
}

var pgCfg = dialectConfig{name: "postgres", Config: Config{
	IdentQuotes: `"`, DollarQuotes: true, EscapeStrings: true, BlobLiterals: true, BitStrings: true,
	UnicodeStrings: true, NationalStrings: true, StringContinuation: true, RadixLiterals: true, DigitSeparators: true,
	RadixSeparator: true, ParamPrefixes: "$", CustomOperators: true, NestedComments: true, CommentEndsAtCR: true,
}}

var sqliteCfg = dialectConfig{name: "sqlite", Config: Config{
	IdentQuotes: "\"`[", BlobLiterals: true, DigitSeparators: true, LaxBlockComments: true, ParamPrefixes: "?:@$#",
	TclParams: true, BOMIsSpace: true,
	Operators: []string{"->>", "->", "||", "<<", ">>", "<=", ">=", "<>", "!=", "==", "=", "<", ">", "+", "-", "*", "/", "%", "&", "|", "~"},
}}

var bqCfg = dialectConfig{name: "bigquery", Config: Config{
	IdentQuotes: "`", NoEmptyQuotedIdent: true, BackslashEscapes: true, DoubleQuotedStrings: true,
	PrefixedStrings: true, AdjacentStrings: true, HashComments: true, ASCIIIdents: true, CommentEndsAtCR: true,
	ParamPrefixes: "?",
	Operators:     []string{"|>", "<<", ">>", "<=", ">=", "<>", "!=", "||", "=>", "@@", "=", "<", ">", "+", "-", "*", "/", "&", "|", "^", "~", "@"},
}}

// render is a compact token description used in test tables: "Kind:Value".
func render(toks []Token) string {
	var parts []string
	for _, t := range toks {
		if t.Kind == EOF {
			break
		}
		s := t.Kind.String() + ":" + t.Value
		if t.Escape {
			s += "(E)"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func TestTokenize(t *testing.T) {
	cases := []struct {
		cfg  dialectConfig
		sql  string
		want string
	}{
		// words and punctuation
		{pgCfg, "select a, b from t", "Ident:SELECT Ident:A Op:, Ident:B Ident:FROM Ident:T"},
		{pgCfg, "t.col", "Ident:T Op:. Ident:COL"},
		{pgCfg, "a.*", "Ident:A Op:. Op:*"},
		{pgCfg, "f(x);", "Ident:F Op:( Ident:X Op:) Op:;"},
		{pgCfg, "_id$2", "Ident:_ID$2"},
		{pgCfg, "zażółć", "Ident:ZAŻÓŁĆ"},
		{pgCfg, "§", "Ident:§"}, // every byte >= 0x80 is an identifier character
		// quoted identifiers
		{pgCfg, `"My Col"`, "QuotedIdent:My Col"},
		{pgCfg, `"a""b"`, `QuotedIdent:a"b`},
		{sqliteCfg, "`col`", "QuotedIdent:col"},
		{sqliteCfg, "[col name]", "QuotedIdent:col name"},
		{pgCfg, "a[1]", "Ident:A Op:[ Number:1 Op:]"},
		// strings
		{pgCfg, "'abc'", "String:abc"},
		{pgCfg, "'it''s'", "String:it's"},
		{pgCfg, "'a'\n  'b'", "String:ab"},
		{pgCfg, "'a' 'b'", "String:a String:b"},
		{sqliteCfg, "'a'\n'b'", "String:a String:b"},
		{pgCfg, `E'a\nb'`, `String:a\nb(E)`},
		{pgCfg, "N'a' n'b'", "Ident:NCHAR String:a Ident:NCHAR String:b"},
		{sqliteCfg, "N'a'", "Ident:N String:a"},
		{pgCfg, `e'it\'s'`, `String:it\'s(E)`},
		{pgCfg, "$$dollar 'quoted'$$", "String:dollar 'quoted'"},
		{pgCfg, "$tag$ a $x$ b $tag$", "String: a $x$ b "},
		{sqliteCfg, "'abc'", "String:abc"},
		// numbers
		{pgCfg, "1 1.5 .5 1e10 2.5E-3 0x1F", "Number:1 Number:1.5 Number:.5 Number:1e10 Number:2.5E-3 Number:0x1F"},
		{pgCfg, "t.5", "Ident:T Number:.5"},
		{pgCfg, "SELECT .5", "Ident:SELECT Number:.5"},
		{pgCfg, "0o17 0b101 1_000", "Number:0o17 Number:0b101 Number:1_000"},
		{sqliteCfg, "1_000", "Number:1_000"},
		{pgCfg, "0x_1F 0o_17 0b_1 1_0.5_5e1_0 1. 1.e5", "Number:0x_1F Number:0o_17 Number:0b_1 Number:1_0.5_5e1_0 Number:1. Number:1.e5"},
		{sqliteCfg, "0x1_F 1_0.5_5e1_0 1.", "Number:0x1_F Number:1_0.5_5e1_0 Number:1."},
		{pgCfg, "1-1", "Number:1 Op:- Number:1"},
		// blobs
		{pgCfg, "X'0F' b'01'", "Blob:X'0F' Blob:B'01'"},
		{sqliteCfg, "b'01'", "Ident:B String:01"}, // a column and a string alias
		{sqliteCfg, "x'0f'", "Blob:X'0f'"},
		// parameters
		{pgCfg, "$1 $23", "Param:$1 Param:$23"},
		{sqliteCfg, "? ?1 :name @name $name", "Param:? Param:?1 Param::name Param:@name Param:$name"},
		{sqliteCfg, "$$x$$ :1", "Param:$$x$$ Param::1"},
		{sqliteCfg, "?1a ?a $1a #1", "Param:?1 Ident:A Param:? Ident:A Param:$1a Param:#1"}, // the parser rejects #1
		// SQLite's Tcl-style names
		{sqliteCfg, "$a::b $::a $a:: :a::b::c @a::b #a", "Param:$a::b Param:$::a Param:$a:: Param::a::b::c Param:@a::b Param:#a"},
		{sqliteCfg, "$a(b) $a() $a::b(c)d :a(b,'c') #a(b)", "Param:$a(b) Param:$a() Param:$a::b(c) Ident:D Param::a(b,'c') Param:#a(b)"},
		// operators: PostgreSQL custom operator rule
		{pgCfg, "a::int", "Ident:A Op::: Ident:INT"},
		{pgCfg, "a -> 'b' ->> 'c' #> '{x}' @> b <@ c ?| d", "Ident:A Op:-> String:b Op:->> String:c Op:#> String:{x} Op:@> Ident:B Op:<@ Ident:C Op:?| Ident:D"},
		{pgCfg, "a*-1", "Ident:A Op:* Op:- Number:1"},
		{pgCfg, "a<-1", "Ident:A Op:< Op:- Number:1"},
		{pgCfg, "a<=-1", "Ident:A Op:<= Op:- Number:1"},
		{pgCfg, "a||b", "Ident:A Op:|| Ident:B"},
		{pgCfg, "f(a => 1, b := 2)", "Ident:F Op:( Ident:A Op:=> Number:1 Op:, Ident:B Op::= Number:2 Op:)"},
		{pgCfg, "a[1:2]", "Ident:A Op:[ Number:1 Op:: Number:2 Op:]"},
		{pgCfg, "a<>b a!=b", "Ident:A Op:<> Ident:B Ident:A Op:!= Ident:B"},
		// operators: SQLite fixed set
		{sqliteCfg, "a->>'b' a->'c' a||b a<<1 a>>1 a==b", "Ident:A Op:->> String:b Ident:A Op:-> String:c Ident:A Op:|| Ident:B Ident:A Op:<< Number:1 Ident:A Op:>> Number:1 Ident:A Op:== Ident:B"},
		{sqliteCfg, "a<>b", "Ident:A Op:<> Ident:B"},
		// comments are dropped by Tokenize
		{pgCfg, "a -- comment\n b", "Ident:A Ident:B"},
		{pgCfg, "a /* c */ b", "Ident:A Ident:B"},
		{pgCfg, "a /* outer /* inner */ still */ b", "Ident:A Ident:B"},
		{sqliteCfg, "a /* outer /* inner */ b", "Ident:A Ident:B"},
		{sqliteCfg, "a /* unterminated", "Ident:A"},
		{sqliteCfg, "a /*", "Ident:A Op:/ Op:*"},
		{pgCfg, "a--b", "Ident:A"},
		{pgCfg, "a --x\rb", "Ident:A Ident:B"}, // PostgreSQL ends the comment at \r too
		{sqliteCfg, "a --x\rb", "Ident:A"},
		// whitespace
		{pgCfg, "a\fb\r\nc", "Ident:A Ident:B Ident:C"},
		{sqliteCfg, "a\fb\r\nc", "Ident:A Ident:B Ident:C"},
		{pgCfg, "'a'\f\r'b'", "String:ab"},
		{sqliteCfg, bom + "a " + bom + bom + "b a" + bom, "Ident:A Ident:B Ident:A" + bom},
		{pgCfg, bom + "a", "Ident:" + bom + "A"},
		// BigQuery, each checked with a dry run
		{bqCfg, "\"\"\"a\nb\"\"\"", "String:a\nb"}, // a tripled quote may span lines
		{bqCfg, `'''a'b'''`, "String:a'b"},
		{bqCfg, `'a' "b"`, "String:ab"},
		{bqCfg, "'a' /* c */ 'b'", "String:ab"},
		{bqCfg, "'a' -- c\n'b'", "String:ab"},
		{bqCfg, "'a'#c\n'b'", "String:ab"},
		{bqCfg, `'\x41\101\u00e9'`, "String:AAé"},
		{bqCfg, "'\\a\\b\\f\\n\\r\\t\\v\\\\\\?\\\"\\'\\`'", "String:\a\b\f\n\r\t\v\\?\"'`"},
		{bqCfg, `r'\d'`, `String:\d`},
		{bqCfg, `r'\''`, `String:\'`},
		{bqCfg, `b'\xff' BR'\x'`, "String:\xff\\x"},
		{bqCfg, "`a\\`b`", "QuotedIdent:a`b"},
		{bqCfg, "`a``b`", "QuotedIdent:a QuotedIdent:b"}, // a doubled quote is not an escape
		{bqCfg, "1 # c\n2", "Number:1 Number:2"},
		{bqCfg, "a --x\rb", "Ident:A Ident:B"},
		{bqCfg, "@@x @ a", "Op:@@ Ident:X Op:@ Ident:A"},
	}
	for _, c := range cases {
		if !testkit.Enabled(c.cfg.name) {
			continue
		}
		t.Run(c.cfg.name+"/"+c.sql, func(t *testing.T) {
			toks, err := Tokenize(c.sql, c.cfg.Config)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := render(toks); got != c.want {
				t.Fatalf("\n want: %s\n  got: %s", c.want, got)
			}
		})
	}
}

func TestTokenizeErrors(t *testing.T) {
	cases := []struct {
		cfg dialectConfig
		sql string
		msg string
	}{
		{pgCfg, "'abc", "unterminated string"},
		{pgCfg, `"abc`, "unterminated quoted identifier"},
		{pgCfg, "/* abc", "unterminated block comment"},
		{pgCfg, "$$abc", "unterminated dollar-quoted string"},
		{pgCfg, "X'0F", "unterminated bit/blob literal"},
		{sqliteCfg, "[abc", "unterminated quoted identifier"},
		{pgCfg, "1a", "trailing junk"},
		{sqliteCfg, "1a", "trailing junk"},
		{pgCfg, "0x", "malformed numeric"},
		{pgCfg, "$name", "unexpected character '$'"},
		{pgCfg, "1._5", "trailing junk"},
		{sqliteCfg, "1._5", "trailing junk"},
		{pgCfg, "1__0", "trailing junk"},
		{pgCfg, "0x1F_", "trailing junk"},
		{pgCfg, "0x_", "malformed numeric"},
		{sqliteCfg, "0x_1F", "malformed numeric"},
		{pgCfg, "a\vb", "unexpected character"}, // a vertical tab is not whitespace
		{sqliteCfg, "a\vb", "unexpected character"},
		{pgCfg, "'a'\v\n'b'", "unexpected character"},
		{sqliteCfg, "$a(b c)", "unterminated parameter name"},
		{sqliteCfg, "$a(b", "unterminated parameter name"},
		{sqliteCfg, "$(a)", "unexpected character '$'"},
		{sqliteCfg, "$::", "unexpected character '$'"},
		// BigQuery, each checked with a dry run
		{bqCfg, "'a\nb'", "unterminated string"},
		{bqCfg, "`a\nb`", "unterminated quoted identifier"},
		{bqCfg, "``", "zero-length"},
		{bqCfg, "'a''b'", "concatenated string literals must be separated"},
		{bqCfg, "'a' b'x'", "string and bytes literals cannot be concatenated"},
		{bqCfg, "'a'b'x'", "string and bytes literals cannot be concatenated"},
		{bqCfg, `'\q'`, `illegal escape sequence: \q`},
		{bqCfg, `'\10'`, "octal escape needs 3"},
		{bqCfg, `'\777'`, `illegal escape sequence: \7`}, // an octal escape is at most \377
		{bqCfg, `'\x4'`, "hex escape needs 2"},
		{bqCfg, `'\ud800'`, "not a valid character"},
		{bqCfg, `'\U00110000'`, "not a valid character"},
		{bqCfg, `b'\u00e9'`, "cannot be used in bytes"},
		{bqCfg, `r'\'`, "unterminated raw string"},
		{bqCfg, "'abc\\\nx'", "illegal escaped newline"},
		{bqCfg, "é", "unexpected character"},
		{bqCfg, "a$b", "unexpected character '$'"},
		{bqCfg, "1a", "trailing junk"},
		{bqCfg, "1 /* x", "unterminated block comment"},
	}
	for _, c := range cases {
		if !testkit.Enabled(c.cfg.name) {
			continue
		}
		t.Run(c.cfg.name+"/"+c.sql, func(t *testing.T) {
			_, err := Tokenize(c.sql, c.cfg.Config)
			if err == nil || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("want error containing %q, got %v", c.msg, err)
			}
		})
	}
}

func TestTokenPositions(t *testing.T) {
	testkit.Require(t, pgCfg.name)
	toks, err := Tokenize("SELECT  'x'\n, 42", pgCfg.Config)
	if err != nil {
		t.Fatal(err)
	}
	wantPos := []int{0, 8, 12, 14, 16}
	for i, p := range wantPos {
		if toks[i].Pos != p {
			t.Errorf("token %d (%s): pos %d, want %d", i, toks[i], toks[i].Pos, p)
		}
	}
	if toks[1].Text != "'x'" || toks[1].Value != "x" {
		t.Errorf("string token text/value: %q / %q", toks[1].Text, toks[1].Value)
	}
}

func TestLexerKeepsComments(t *testing.T) {
	testkit.Require(t, pgCfg.name)
	lx := New("a -- hi\nb", pgCfg.Config)
	var kinds []Kind
	for {
		tk := lx.Next()
		kinds = append(kinds, tk.Kind)
		if tk.Kind == EOF {
			break
		}
	}
	want := []Kind{Ident, Comment, Ident, EOF}
	if len(kinds) != len(want) {
		t.Fatalf("kinds %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds %v, want %v", kinds, want)
		}
	}
}

func TestEscapeStrings(t *testing.T) {
	testkit.Require(t, "postgres")
	for _, tc := range []struct{ in, want, problem string }{
		{in: `a\nb\t\\ \' ''`, want: "a\nb\t\\ ' '"},
		{in: `\101\x41\u0041\U00000041`, want: "AAAA"},
		{in: `\8 \xg \q \x7f`, want: "8 xg q \x7f"},
		{in: `\303\251 \xc3\xa9 \u00e9`, want: "é é é"}, // bytes that form valid UTF-8
		{in: `\ud83d\ude00 \U0001F600`, want: "\U0001F600 \U0001F600"},
		{in: `\0`, problem: badByteSequence},
		{in: `\400`, problem: badByteSequence}, // 0o400 wraps to a zero byte
		{in: `\x0`, problem: badByteSequence},
		{in: `\351`, problem: badByteSequence},
		{in: `\xe9`, problem: badByteSequence},
		{in: `é\351`, problem: badByteSequence},
		{in: `\xc3a`, problem: badByteSequence},
		{in: `\ud83d`, problem: badSurrogatePair},
		{in: `\u00`, problem: badUnicodeEscape},
	} {
		problem := CheckEscapeString(tc.in)
		if problem != tc.problem {
			t.Errorf("CheckEscapeString(%q) = %q, want %q", tc.in, problem, tc.problem)
		}
		if got := Unescape(tc.in); problem == "" && got != tc.want {
			t.Errorf("Unescape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDecodeUnicode(t *testing.T) {
	testkit.Require(t, "postgres")
	for _, tc := range []struct {
		in   string
		esc  byte
		want string
	}{
		{`d\0061t\+000061`, '\\', "data"},
		{`d!0061ta`, '!', "data"},
		{`a\\b`, '\\', `a\b`},
		{`\D83D\DE00 \+01F600`, '\\', "\U0001F600 \U0001F600"},
		{`no escapes`, '\\', "no escapes"},
		{`bad \zz`, '\\', `bad \zz`},
	} {
		if got := DecodeUnicode(tc.in, tc.esc); got != tc.want {
			t.Errorf("DecodeUnicode(%q, %q) = %q, want %q", tc.in, tc.esc, got, tc.want)
		}
	}
}

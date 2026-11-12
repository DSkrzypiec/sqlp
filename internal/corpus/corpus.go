// Package corpus holds the frozen expected results replayed by plain go test,
// and the code shared by the test and by tools/corpusgen, which writes them.
package corpus

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"slices"
	"strings"

	"sqlp/ast"
	"sqlp/dialect"
	"sqlp/gen"
	"sqlp/lexer"
	"sqlp/parser"
)

// Record is one statement and what the real engine said about it.
type Record struct {
	SQL    string `json:"sql"`
	Reject bool   `json:"reject,omitempty"`
	// Shape is the fully parenthesized form of the statement, frozen only
	// after the engine confirmed that it has the same tree as SQL.
	Shape string `json:"shape,omitempty"`
}

// Known is a recorded disagreement between the parser and the engine.
type Known struct {
	SQL  string `json:"sql"`
	Kind string `json:"kind"` // AcceptsInvalid or RejectsValid
}

const (
	AcceptsInvalid = "accepts-invalid"
	RejectsValid   = "rejects-valid"
)

// Read loads a JSON Lines file.
func Read[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<26)
	for sc.Scan() {
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

// Write stores rows as JSON Lines, one per line, so changes diff cleanly.
func Write[T any](path string, rows []T) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return w.Flush()
}

// Parsed is the parser's view of one statement.
type Parsed struct {
	Stmts  []ast.Statement
	Err    error
	Opaque bool // kept partly as raw text, so acceptance means little
}

// Parse parses sql in dialect d and classifies the result.
func Parse(sql string, d *dialect.Dialect) Parsed {
	stmts, err := parser.Parse(sql, d)
	return Parsed{Stmts: stmts, Err: err, Opaque: err == nil && hasOpaque(stmts)}
}

func hasOpaque(stmts []ast.Statement) bool {
	return slices.ContainsFunc(stmts, func(s ast.Statement) bool {
		o, ok := s.(*ast.Opaque)
		return ast.IsOpaque(s) && !(ok && o.Checked)
	})
}

// Shape returns the fully parenthesized form of stmts.
func Shape(stmts []ast.Statement, d *dialect.Dialect) string { return gen.Shape(stmts, d) }

// Exprs generates n deterministic SELECTs of unparenthesized constant
// expressions, mixing every precedence level of the named dialect. Literals are
// distinct and nonzero, so a different grouping almost always evaluates
// differently. A binary operator ending in ESCAPE takes an escape clause.
func Exprs(name string, n int) []string {
	binary := []string{"*", "/", "%", "+", "-", "<<", ">>", "&", "|", "<", "<=", ">", ">=", "=", "<>", "!=", "AND", "OR", "||", "LIKE", "NOT LIKE", "LIKE ESCAPE"}
	prefix := []string{"-", "+", "NOT "}
	postfix := []string{" IS NULL", " IS NOT NULL", " BETWEEN 2 AND 7", " NOT BETWEEN 3 AND 11", " IN (5, 13)", " NOT IN (17, 19)", " ISNULL", " NOTNULL"}
	var atoms []string
	switch name {
	case "postgres":
		binary = append(binary, "^", "ILIKE", "NOT ILIKE", "ILIKE ESCAPE", "SIMILAR TO", "NOT SIMILAR TO", "#", "<@", "IS DISTINCT FROM", "OPERATOR(pg_catalog.+)")
		prefix = append(prefix, "~", "@ ")
		postfix = append(postfix, "::int", " COLLATE \"C\"", " AT TIME ZONE 'UTC'")
		atoms = []string{"(1, 2) OVERLAPS (3, 4)", "ROW(5, 7) OVERLAPS ROW(11, 13)"}
	case "sqlite":
		binary = append(binary, "==", "IS", "IS NOT", "GLOB", "NOT GLOB", "REGEXP", "NOT REGEXP", "->", "->>")
		prefix = append(prefix, "~")
		postfix = append(postfix, " COLLATE NOCASE", " NOT NULL")
	}
	primes := []int{2, 3, 5, 7, 11, 13, 17, 19, 23, 29, 31, 37, 41, 43, 47, 53, 59, 61, 67, 71, 73, 79, 83, 89, 97}
	r := rand.New(rand.NewSource(1))
	term := func() string {
		if len(atoms) > 0 && r.Intn(12) == 0 {
			return atoms[r.Intn(len(atoms))]
		}
		s := fmt.Sprint(primes[r.Intn(len(primes))])
		if r.Intn(4) == 0 {
			s = prefix[r.Intn(len(prefix))] + s
		}
		for r.Intn(4) == 0 {
			s += postfix[r.Intn(len(postfix))]
		}
		return s
	}
	seen := map[string]bool{}
	var out []string
	for len(out) < n {
		s := term()
		for k := 1 + r.Intn(5); k > 0; k-- {
			op, escape := strings.CutSuffix(binary[r.Intn(len(binary))], " ESCAPE")
			s += " " + op + " " + term()
			if escape {
				s += " ESCAPE '#'"
			}
		}
		if s = "SELECT " + s; !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if name == "postgres" { // SQLite cannot parenthesize a compound operand
		out = append(out, setOperationChains(r)...)
	}
	return out
}

// setOperationChains mixes set operators, whose grouping differs by operator.
func setOperationChains(r *rand.Rand) []string {
	ops := []string{"UNION", "UNION ALL", "INTERSECT", "INTERSECT ALL", "EXCEPT", "EXCEPT ALL"}
	var out []string
	for range 100 {
		s := "SELECT 1"
		for k := 2 + r.Intn(3); k > 0; k-- {
			s += " " + ops[r.Intn(len(ops))] + " SELECT " + fmt.Sprint(2+r.Intn(50))
		}
		out = append(out, s)
	}
	return out
}

// splitStatements cuts a script at top-level semicolons using the lexer, so
// quotes and comments are respected. Trigger and BEGIN ATOMIC bodies, which
// contain semicolons of their own, stay in one piece.
func splitStatements(src string, d *dialect.Dialect) []string {
	var out []string
	lx := lexer.New(src, d.Lexer)
	start, depth, body := -1, 0, 0
	var words []string
	flush := func(end int) {
		if start >= 0 {
			out = append(out, strings.TrimSpace(src[start:end]))
		}
		start, depth, body, words = -1, 0, 0, nil
	}
	var prev lexer.Token
	for {
		t := lx.Next()
		if t.Kind == lexer.EOF || t.Kind == lexer.Illegal {
			if t.Kind == lexer.EOF {
				flush(len(src))
			}
			return out
		}
		if t.Kind == lexer.Comment {
			continue
		}
		if start < 0 {
			start = t.Pos
		}
		if t.Kind == lexer.Ident && len(words) < 5 {
			words = append(words, t.Value)
		}
		switch {
		case t.Kind == lexer.Op && (t.Text == "(" || t.Text == "["):
			depth++
		case t.Kind == lexer.Op && (t.Text == ")" || t.Text == "]"):
			depth--
		case t.Kind == lexer.Op && t.Text == ";" && depth <= 0 && body == 0:
			flush(t.Pos)
		case t.Kind == lexer.Ident:
			trigger := d.Features.VirtualTables && len(words) > 1 && words[0] == "CREATE" && strings.Contains(strings.Join(words, " "), "TRIGGER")
			switch {
			case t.Value == "BEGIN" && trigger && body == 0,
				t.Value == "ATOMIC" && prev.Value == "BEGIN" && body == 0:
				body = 1
			case t.Value == "CASE" && body > 0:
				body++
			case t.Value == "END" && body > 0:
				body--
			}
		}
		prev = t
	}
}

var psqlTail = regexp.MustCompile(`\\(gset|gexec|gdesc|g|crosstabview|watch)\b.*$`)

// PostgresStatements extracts statements from a psql regression script. A
// statement that looks like it uses a psql variable is kept only if keep says
// so, because a[1:x] and JSON_OBJECT('k': v) look the same; keep may be nil.
func PostgresStatements(script string, keep func(string) bool) []string {
	var b strings.Builder
	inCopy := false
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)
		switch {
		case inCopy:
			inCopy = trimmed != `\.`
			continue
		case strings.HasPrefix(trimmed, `\`):
			continue
		case strings.HasPrefix(upper, "COPY ") && strings.Contains(upper, "FROM STDIN"):
			inCopy = true
		}
		line = psqlTail.ReplaceAllString(line, ";")
		b.WriteString(strings.ReplaceAll(line, `\;`, ";"))
		b.WriteByte('\n')
	}
	var out []string
	for _, s := range splitStatements(b.String(), dialect.Postgres) {
		if !usesPsqlVariable(s) || keep != nil && keep(s) {
			out = append(out, s)
		}
	}
	return out
}

// usesPsqlVariable reports :name, :'name' or :"name" interpolation.
func usesPsqlVariable(s string) bool {
	toks, err := lexer.Tokenize(s, dialect.Postgres.Lexer)
	if err != nil {
		return true
	}
	for i := 0; i+1 < len(toks); i++ {
		next := toks[i+1].Kind
		if toks[i].Kind == lexer.Op && toks[i].Text == ":" &&
			(next == lexer.Ident || next == lexer.String || next == lexer.QuotedIdent) {
			return true
		}
	}
	return false
}

var tclSQL = regexp.MustCompile(`\b(do_execsql_test|do_catchsql_test|do_eqp_test|execsql|catchsql)\b[^\n{"]*\{`)

// SQLiteStatements extracts brace-quoted SQL from a Tcl test file.
func SQLiteStatements(script string) []string {
	var out []string
	for pos := 0; ; {
		loc := tclSQL.FindStringIndex(script[pos:])
		if loc == nil {
			return out
		}
		open := pos + loc[1] - 1
		depth, i := 0, open
		for ; i < len(script); i++ {
			if script[i] == '{' {
				depth++
			} else if script[i] == '}' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		if i >= len(script) {
			return out
		}
		body := script[open+1 : i]
		if !strings.ContainsAny(body, "$[") || !strings.Contains(body, "$") {
			out = append(out, splitStatements(body, dialect.SQLite)...)
		}
		pos = i + 1
	}
}

// Mutate damages a token list in one random way and returns the SQL text.
func Mutate(r *rand.Rand, toks []lexer.Token, pool []lexer.Token) string {
	toks = append([]lexer.Token{}, toks...)
	i := r.Intn(len(toks))
	switch r.Intn(4) {
	case 0: // delete
		toks = append(toks[:i], toks[i+1:]...)
	case 1: // duplicate
		toks = append(toks[:i+1], toks[i:]...)
	case 2: // swap neighbours
		if i+1 < len(toks) {
			toks[i], toks[i+1] = toks[i+1], toks[i]
		}
	case 3: // replace with a token from elsewhere in the corpus
		toks[i] = pool[r.Intn(len(pool))]
	}
	parts := make([]string, len(toks))
	for j, t := range toks {
		parts[j] = t.Text
	}
	return strings.Join(parts, " ")
}

// Mutations returns n deterministic damaged copies of each statement, keeping
// the number of statements the same. Tokens are borrowed across the whole list.
func Mutations(list []string, cfg lexer.Config, n int) []string {
	var bases [][]lexer.Token
	var pool []lexer.Token
	for _, s := range list {
		toks, err := lexer.Tokenize(s, cfg)
		if err == nil && len(toks) > 1 {
			toks = toks[:len(toks)-1] // drop EOF
			bases = append(bases, toks)
			pool = append(pool, toks...)
		}
	}
	r := rand.New(rand.NewSource(1))
	var out []string
	for _, base := range bases {
		for range n {
			if m := Mutate(r, base, pool); !strings.Contains(m, ";") {
				out = append(out, m)
			}
		}
	}
	return out
}

// SQLiteUsable reports whether sql can be sent to the sqlite3 CLI and yield a
// meaningful verdict.
func SQLiteUsable(sql string) bool {
	s := strings.TrimSpace(sql)
	if s == "" || s[0] == '.' || s[0] == '-' {
		return false // a dot-command or a CLI flag
	}
	// Errors raised mid-parse by these hide any later syntax error.
	upper := strings.ToUpper(s)
	for _, noisy := range []string{"CREATE TABLE S .", "ALTER TABLE T ADD", "ALTER TABLE MAIN", `"C"`,
		"DESC AUTOINCREMENT", "TEMP . ", "MAIN . TRG", "%WO%"} {
		if strings.Contains(upper, noisy) {
			return false
		}
	}
	return true
}

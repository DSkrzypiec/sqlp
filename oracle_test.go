//go:build oracle

// Differential tests against real engines. Run with:
//
//	PGHOST=127.0.0.1 PGPORT=54329 PGUSER=sqlp go test -tags oracle .
//
// PostgreSQL must be a THROWAWAY server: statements are really executed
// (a statement's syntax verdict comes from the error it produces).
package sqlp

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sqlp/ast"
	"sqlp/dialect"
	"sqlp/internal/corpus"
	"sqlp/internal/testkit"
	"sqlp/lexer"
)

// engine answers whether a real database rejects a statement's syntax.
type engine struct {
	d       *dialect.Dialect
	rejects func(sql string) bool
	// usable filters statements unsafe or meaningless to send to the engine.
	usable func(sql string) bool
}

const sqliteSchema = "CREATE TABLE t(a,b,c,d,e,f,g,h,id,x,y,name,s,j,v,rowid2);" +
	"CREATE TABLE u(a,b,id,x,y);CREATE TABLE a(id,kind,x,y);CREATE TABLE b(id,kind,x,y);" +
	"CREATE TABLE c(id);CREATE TABLE d(id);CREATE INDEX idx ON t(a);CREATE VIEW v AS SELECT 1 AS a;"

// runSQLite reports whether sqlite3 -safe, in a scratch directory, gives a
// syntax verdict of "rejected" for an untrusted statement.
func runSQLite(bin, sql string) bool {
	dir, err := os.MkdirTemp("", "sqlp-oracle")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-safe", "-batch", ":memory:", sql)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	s := string(out)
	return strings.Contains(s, "syntax error") || strings.Contains(s, "unrecognized token") ||
		strings.Contains(s, "incomplete input") || strings.Contains(s, "join type") ||
		strings.Contains(s, "unknown table option") || strings.Contains(s, "should come after")
}

func psqlPath() string {
	if p := os.Getenv("ORACLE_PSQL"); p != "" {
		return p
	}
	return "psql"
}

func sqliteEngine(t *testing.T) engine {
	dir := t.TempDir() // ATTACH and VACUUM INTO create files
	return engine{
		d: dialect.SQLite,
		rejects: func(sql string) bool {
			run := func(script string) bool {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "sqlite3", "-batch", ":memory:", script)
				cmd.Dir = dir
				out, _ := cmd.CombinedOutput()
				s := string(out)
				return strings.Contains(s, "syntax error") || strings.Contains(s, "unrecognized token") ||
					strings.Contains(s, "incomplete input") || strings.Contains(s, "join type") ||
					strings.Contains(s, "unknown table option") ||
					strings.Contains(s, "a JOIN clause is required") || strings.Contains(s, "error in generated column")
			}
			// SQLite runs semantic actions while parsing, so "no such table"
			// or "table already exists" can pre-empt a later syntax error.
			// Trying with and without tables exposes it either way.
			return run(sql) || run(sqliteSchema+sql)
		},
		// A leading '.' or '-' would be read as a dot-command or a CLI flag.
		usable: func(sql string) bool {
			s := strings.TrimSpace(sql)
			if s == "" || s[0] == '.' || s[0] == '-' {
				return false
			}
			// Errors raised mid-parse by these hide any later syntax error.
			upper := strings.ToUpper(s)
			for _, noisy := range []string{"CREATE TABLE S .", "ALTER TABLE T ADD", "ALTER TABLE MAIN", `"C"`,
				"DESC AUTOINCREMENT", "TEMP . ", "MAIN . TRG"} {
				if strings.Contains(upper, noisy) {
					return false
				}
			}
			return true
		},
	}
}

func postgresEngine(t *testing.T) engine {
	if os.Getenv("PGHOST") == "" {
		t.Skip("PGHOST not set")
	}
	return engine{
		d: dialect.Postgres,
		rejects: func(sql string) bool {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, psqlPath(), "-X", "-q", "-v", "VERBOSITY=verbose", "-c", sql)
			cmd.Env = append(os.Environ(), "PGOPTIONS=-c lock_timeout=200 -c statement_timeout=2000")
			out, _ := cmd.CombinedOutput()
			// LOCATION names the backend source file that raised the error:
			// only scan.l and gram.y are about syntax. SELECT * with no FROM,
			// for one, is 42601 too but comes from the analyzer.
			inError := false
			for _, line := range strings.Split(string(out), "\n") {
				switch {
				case strings.HasPrefix(line, "ERROR:"):
					inError = true
				case strings.HasPrefix(line, "LOCATION:") && inError:
					return strings.Contains(line, ", scan.l:") || strings.Contains(line, ", gram.y:")
				}
			}
			return false
		},
		// COPY ... PROGRAM runs a shell command on the server.
		usable: func(sql string) bool { return !strings.Contains(strings.ToUpper(sql), "PROGRAM") },
	}
}

func (e engine) verdicts(sqls []string) []bool {
	out := make([]bool, len(sqls))
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				out[i] = e.rejects(sqls[i])
			}
		}()
	}
	for i := range sqls {
		next <- i
	}
	close(next)
	wg.Wait()
	return out
}

// engines builds only the selected dialects, so -dialect=sqlite needs no
// PostgreSQL server.
func engines(t *testing.T) []engine {
	var out []engine
	if testkit.Enabled(dialect.SQLite.Name) {
		out = append(out, sqliteEngine(t))
	}
	if testkit.Enabled(dialect.Postgres.Name) {
		out = append(out, postgresEngine(t))
	}
	return out
}

func inputs(rs []rewrite) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.in
	}
	return out
}

func sqls(fs []failure) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.sql
	}
	return out
}

// Every statement our tests call valid must be valid in the real engine.
func TestOracleAccepted(t *testing.T) {
	corpus := map[string][]string{
		"sqlite":   append(append([]string{}, commonIdentity...), append(sqliteIdentity, inputs(sqliteRewrites)...)...),
		"postgres": append(append([]string{}, commonIdentity...), append(postgresIdentity, inputs(postgresRewrites)...)...),
	}
	for _, e := range engines(t) {
		t.Run(e.d.Name, func(t *testing.T) {
			var list []string
			for _, s := range corpus[e.d.Name] {
				if e.usable(s) {
					list = append(list, s)
				}
			}
			for i, rejected := range e.verdicts(list) {
				if rejected {
					t.Errorf("engine rejects: %s", list[i])
				}
			}
		})
	}
}

// Every statement our tests call invalid must be invalid in the real engine.
func TestOracleRejected(t *testing.T) {
	corpus := map[string][]string{
		"sqlite":   append(sqls(commonFailures), append(sqls(sqliteFailures), sqls(sqliteRejectsPostgres)...)...),
		"postgres": append(sqls(commonFailures), append(sqls(postgresFailures), sqls(postgresRejectsSQLite)...)...),
	}
	for _, e := range engines(t) {
		t.Run(e.d.Name, func(t *testing.T) {
			list := corpus[e.d.Name]
			for i, rejected := range e.verdicts(list) {
				if !rejected {
					t.Errorf("engine accepts: %s", list[i])
				}
			}
		})
	}
}

// parseVerdict reports whether our parser rejects sql, and whether it kept
// any part of it opaque (in which case its acceptance means little).
func parseVerdict(sql string, d *dialect.Dialect) (rejected, opaque bool) {
	stmts, err := Parse(sql, d)
	if err != nil {
		return true, false
	}
	for _, s := range stmts {
		switch st := s.(type) {
		case *ast.Opaque:
			return false, true
		case *ast.AlterTable:
			for _, a := range st.Actions {
				if _, ok := a.(*ast.AlterOpaque); ok {
					return false, true
				}
			}
		}
	}
	return false, false
}

func semicolons(toks []lexer.Token) int {
	n := 0
	for _, t := range toks {
		if t.Kind == lexer.Op && t.Text == ";" {
			n++
		}
	}
	return n
}

// TestOracleMutations compares syntax verdicts on randomly damaged statements.
// ORACLE_MUTATIONS sets how many are tried per dialect (default 3000).
func TestOracleMutations(t *testing.T) {
	n := 3000
	if v, err := strconv.Atoi(os.Getenv("ORACLE_MUTATIONS")); err == nil {
		n = v
	}
	tables := map[string][]string{
		"sqlite":   append(append([]string{}, commonIdentity...), sqliteIdentity...),
		"postgres": append(append([]string{}, commonIdentity...), postgresIdentity...),
	}
	for _, e := range engines(t) {
		t.Run(e.d.Name, func(t *testing.T) {
			r := rand.New(rand.NewSource(1))
			var bases [][]lexer.Token
			var pool []lexer.Token
			for _, s := range tables[e.d.Name] {
				toks, err := lexer.Tokenize(s, e.d.Lexer)
				if err != nil {
					t.Fatal(err)
				}
				toks = toks[:len(toks)-1] // drop EOF
				if len(toks) > 0 {
					bases = append(bases, toks)
					pool = append(pool, toks...)
				}
			}
			seen := map[string]bool{}
			var list []string
			for len(list) < n {
				base := bases[r.Intn(len(bases))]
				m := corpus.Mutate(r, base, pool)
				// A mutation that splits one statement in two is not comparable:
				// the engine stops at the first statement's semantic error.
				if seen[m] || !e.usable(m) || strings.Count(m, ";") != semicolons(base) {
					continue
				}
				seen[m] = true
				list = append(list, m)
			}
			verdicts := e.verdicts(list)
			over, under, opaque := 0, 0, 0
			for i, engineRejects := range verdicts {
				weRejects, isOpaque := parseVerdict(list[i], e.d)
				switch {
				case isOpaque:
					opaque++
				case weRejects && !engineRejects:
					under++
					t.Logf("UNDER-accepts: %s", list[i])
				case !weRejects && engineRejects:
					over++
					t.Logf("OVER-accepts: %s", list[i])
				}
			}
			t.Log(fmt.Sprintf("%s: %d mutations, %d under-accepted, %d over-accepted, %d opaque",
				e.d.Name, len(list), under, over, opaque))
			// Known gaps are mostly exotic damaged input; only a jump is a failure.
			if budget := len(list) / 200; under+over > budget {
				t.Errorf("%d disagreements, more than the budget of %d", under+over, budget)
			}
		})
	}
}

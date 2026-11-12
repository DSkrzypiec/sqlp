//go:build oracle

package sqlp

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"sqlp/dialect"
	"sqlp/internal/corpus"
	"sqlp/internal/testkit"
	"sqlp/lexer"
)

var bucketRe = regexp.MustCompile(`line \d+, column \d+|'[^']*'`)

// TestOracleCorpus parses the official test suites and compares every verdict
// with the engine. Point ORACLE_PG_CORPUS at src/test/regress/sql and
// ORACLE_SQLITE_CORPUS at test/. The text is untrusted: PostgreSQL runs it as
// ORACLE_CORPUS_PGUSER, which must be an unprivileged role on a throwaway
// server, and sqlite3 runs in -safe mode.
func TestOracleCorpus(t *testing.T) {
	type source struct {
		dir, glob string
		e         engine
		extract   func(string) []string
	}
	var sources []source
	if dir := os.Getenv("ORACLE_SQLITE_CORPUS"); dir != "" && testkit.Enabled(dialect.SQLite.Name) {
		sqlite := sqliteEngine(t)
		sqlite.rejects = func(sql string) bool { return sqliteSafeRejects(t, sql) }
		sources = append(sources, source{dir, "*.test", sqlite, corpus.SQLiteStatements})
	}
	if dir := os.Getenv("ORACLE_PG_CORPUS"); dir != "" && testkit.Enabled(dialect.Postgres.Name) {
		pg := postgresEngine(t)
		if u := os.Getenv("ORACLE_CORPUS_PGUSER"); u != "" {
			os.Setenv("PGUSER", u)
		} else {
			t.Fatal("ORACLE_CORPUS_PGUSER must name an unprivileged role")
		}
		extract := func(script string) []string { return corpus.PostgresStatements(script, nil) }
		sources = append(sources, source{dir, "*.sql", pg, extract})
	}
	for _, src := range sources {
		t.Run(src.e.d.Name, func(t *testing.T) {
			files, _ := filepath.Glob(filepath.Join(src.dir, src.glob))
			seen := map[string]bool{}
			var list []string
			for _, f := range files {
				raw, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				for _, s := range src.extract(string(raw)) {
					if s != "" && !seen[s] && src.e.usable(s) && strings.IndexByte(s, 0) < 0 {
						seen[s] = true
						list = append(list, s)
					}
				}
			}
			verdicts := src.e.verdicts(list)
			parsed, under, over, opaque, bothReject := 0, 0, 0, 0, 0
			buckets := map[string][]string{}
			var overs []string
			for i, s := range list {
				_, perr := Parse(s, src.e.d)
				rejected, isOpaque := parseVerdict(s, src.e.d)
				if !rejected {
					parsed++
				}
				switch {
				case isOpaque:
					opaque++
				case rejected && verdicts[i]:
					bothReject++
				case rejected:
					under++
					key := bucketRe.ReplaceAllString(perr.Error(), "_")
					buckets[key] = append(buckets[key], s)
				case verdicts[i]:
					over++
					overs = append(overs, s)
				}
			}
			t.Logf("%s: %d statements; parsed %d; both reject %d; UNDER %d; OVER %d; opaque %d",
				src.e.d.Name, len(list), parsed, bothReject, under, over, opaque)
			checkGenerator(t, src.e, list, verdicts)
			keys := make([]string, 0, len(buckets))
			for k := range buckets {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool { return len(buckets[keys[i]]) > len(buckets[keys[j]]) })
			for i, k := range keys {
				if i >= 70 {
					break
				}
				t.Logf("UNDER [%d] %s\n    e.g. %s", len(buckets[k]), k, oneLine(buckets[k][0]))
			}
			for i, s := range overs {
				if i >= 30 {
					break
				}
				t.Logf("OVER %s", oneLine(s))
			}
		})
	}
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 170 {
		return s[:170] + "..."
	}
	return s
}

func sqliteSafeRejects(t *testing.T, sql string) bool { return runSQLite("sqlite3", sql) }

// significant collects the tokens a faithful rewrite must keep: every number,
// string, parameter, blob and quoted identifier, with its count.
func significant(sql string, d *dialect.Dialect) map[string]int {
	toks, err := lexer.Tokenize(sql, d.Lexer)
	if err != nil {
		return nil
	}
	out := map[string]int{}
	for _, t := range toks {
		switch t.Kind {
		case lexer.Number, lexer.Param, lexer.Blob:
			out[strings.ToUpper(t.Text)]++
		case lexer.String, lexer.QuotedIdent:
			out[t.Value]++
		}
	}
	return out
}

// checkGenerator verifies that everything both sides accept survives a round
// trip: the output re-parses, is stable, keeps every literal, and the engine
// accepts it too.
func checkGenerator(t *testing.T, e engine, list []string, verdicts []bool) {
	var outputs, inputs []string
	unparsable, unstable, lost := 0, 0, 0
	for i, s := range list {
		if verdicts[i] {
			continue
		}
		if rejected, isOpaque := parseVerdict(s, e.d); rejected || isOpaque {
			continue
		}
		stmts, err := Parse(s, e.d)
		if err != nil {
			continue
		}
		out := Format(stmts, e.d)
		again, err := Parse(out, e.d)
		if err != nil {
			unparsable++
			if unparsable <= 25 {
				t.Logf("REPARSE %v\n    in:  %s\n    out: %s", err, oneLine(s), oneLine(out))
			}
			continue
		}
		if second := Format(again, e.d); second != out {
			unstable++
			if unstable <= 15 {
				t.Logf("UNSTABLE\n    1st: %s\n    2nd: %s", oneLine(out), oneLine(second))
			}
		}
		a, b := significant(s, e.d), significant(out, e.d)
		for k, n := range a {
			if b[k] < n && !strings.Contains(s, "$$") {
				lost++
				if lost <= 20 {
					t.Logf("LOST %q\n    in:  %s\n    out: %s", k, oneLine(s), oneLine(out))
				}
				break
			}
		}
		outputs = append(outputs, out)
		inputs = append(inputs, s)
	}
	broken := 0
	for i, rejected := range e.verdicts(outputs) {
		if rejected {
			broken++
			if broken <= 25 {
				t.Logf("ENGINE REJECTS OUTPUT\n    in:  %s\n    out: %s", oneLine(inputs[i]), oneLine(outputs[i]))
			}
		}
	}
	t.Logf("%s generator: %d round-tripped; %d unparsable, %d unstable, %d lost a literal, %d rejected by engine",
		e.d.Name, len(outputs), unparsable, unstable, lost, broken)
}

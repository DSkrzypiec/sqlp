package sqlp

import (
	"os"
	"strings"
	"testing"

	"sqlp/dialect"
	"sqlp/gen"
	"sqlp/internal/corpus"
	"sqlp/internal/testkit"
)

// TestANSI holds the parser to the engine verdicts in corpus.ANSI. With -v it
// logs a per-dialect conformance summary.
func TestANSI(t *testing.T) {
	known := standardFeatures(t)
	for _, c := range corpus.ANSI {
		if !known[c.Feature] {
			t.Errorf("unknown feature ID %s: %s", c.Feature, c.SQL)
		}
	}
	for _, d := range []*dialect.Dialect{dialect.Postgres, dialect.SQLite} {
		testkit.Run(t, d.Name, func(t *testing.T) {
			supported, opaque := map[string]bool{}, 0
			for _, c := range corpus.ANSI {
				t.Run(c.Feature+"/"+c.SQL, func(t *testing.T) {
					p := corpus.Parse(c.SQL, d)
					switch {
					case !c.Accepted(d.Name) && p.Err == nil:
						t.Fatalf("engine rejects, parsed as: %s", gen.Statements(p.Stmts, d))
					case c.Accepted(d.Name) && p.Err != nil:
						t.Fatalf("engine accepts, parser says: %v", p.Err)
					case p.Err != nil:
						return
					}
					supported[c.Feature] = true
					if p.Opaque {
						opaque++
						return
					}
					out := gen.Statements(p.Stmts, d)
					again := corpus.Parse(out, d)
					if again.Err != nil {
						t.Fatalf("output does not re-parse: %v\n  out: %s", again.Err, out)
					}
					if second := gen.Statements(again.Stmts, d); second != out {
						t.Fatalf("unstable round trip\n  1st: %s\n  2nd: %s", out, second)
					}
				})
			}
			t.Logf("%d features accepted, %d statements kept as raw text", len(supported), opaque)
		})
	}
}

// standardFeatures reads the feature IDs, such as E011 and E011-01, from the
// list PostgreSQL publishes for the SQL standard.
func standardFeatures(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("testdata/ansi/sql_features.txt")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		f := strings.Split(line, "\t")
		id := f[0]
		if f[2] != "" {
			id += "-" + f[2]
		}
		ids[id] = true
	}
	return ids
}

package sqlp

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"sqlp/analyze"
	"sqlp/ast"
	"sqlp/dialect"
	"sqlp/internal/corpus"
	"sqlp/internal/testkit"
)

// TestQStructOnCorpus runs QStruct and ListOutCols on every SELECT and
// CREATE VIEW in the frozen corpora. Nothing checks their results against an
// engine yet; this only requires that they never fail or panic on a
// statement the parser accepts, and that their output is well formed.
func TestQStructOnCorpus(t *testing.T) {
	for _, d := range []*dialect.Dialect{dialect.Postgres, dialect.SQLite} {
		testkit.Run(t, d.Name, func(t *testing.T) {
			files, _ := filepath.Glob(filepath.Join("testdata", "corpus", d.Name, "*.jsonl"))
			analysed := 0
			for _, f := range files {
				rows, err := corpus.Read[corpus.Record](f)
				if err != nil {
					t.Fatal(err)
				}
				for _, r := range rows {
					if p := corpus.Parse(r.SQL, d); p.Err == nil {
						analysed += checkQStruct(t, r.SQL, p.Stmts, d)
						checkOutCols(t, r.SQL, p.Stmts, d)
					}
				}
			}
			t.Logf("analysed %d statements", analysed)
		})
	}
}

// checkQStruct requires QStruct to succeed on each SELECT and CREATE VIEW in
// stmts, with unique node IDs and JSON that encodes. It returns how many
// statements it analysed.
func checkQStruct(t *testing.T, sql string, stmts []ast.Statement, d *dialect.Dialect) int {
	t.Helper()
	n := 0
	for _, s := range stmts {
		switch s.(type) {
		case *ast.Query, *ast.CreateView:
		default:
			continue
		}
		n++
		g, err := analyze.QStruct(s, d, analyze.Options{})
		if err != nil {
			t.Fatalf("%s: %v: %q", d.Name, err, sql)
		}
		ids := map[string]bool{}
		for _, node := range g.Nodes() {
			if ids[node.ID] {
				t.Fatalf("%s: node ID %q used twice: %q", d.Name, node.ID, sql)
			}
			ids[node.ID] = true
		}
		if _, err := json.Marshal(g); err != nil {
			t.Fatalf("%s: %v: %q", d.Name, err, sql)
		}
	}
	return n
}

// checkOutCols requires ListOutCols to succeed on each SELECT and CREATE
// VIEW in stmts, listing each column once, with JSON that encodes.
func checkOutCols(t *testing.T, sql string, stmts []ast.Statement, d *dialect.Dialect) {
	t.Helper()
	for _, s := range stmts {
		switch s.(type) {
		case *ast.Query, *ast.CreateView:
		default:
			continue
		}
		c, err := analyze.ListOutCols(s, d, analyze.Options{})
		if err != nil {
			t.Fatalf("%s: %v: %q", d.Name, err, sql)
		}
		seen := map[string]bool{}
		for _, col := range c.Columns {
			key := col.Name() + "\x00" + string(col.Status)
			if seen[key] {
				t.Fatalf("%s: column %s listed twice: %q", d.Name, col.Name(), sql)
			}
			seen[key] = true
			if len(col.Clauses) == 0 {
				t.Fatalf("%s: column %s has no clauses: %q", d.Name, col.Name(), sql)
			}
		}
		if _, err := json.Marshal(c); err != nil {
			t.Fatalf("%s: %v: %q", d.Name, err, sql)
		}
	}
}

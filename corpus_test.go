package sqlp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sqlp/dialect"
	"sqlp/gen"
	"sqlp/internal/corpus"
	"sqlp/internal/testkit"
)

// TestCorpus replays the verdicts and tree shapes frozen by tools/corpusgen.
// It needs no engine. Each disagreement must be listed in testdata/known, and
// a listed one that no longer disagrees fails too, so the list stays honest.
func TestCorpus(t *testing.T) {
	for _, d := range dialect.All {
		testkit.Run(t, d.Name, func(t *testing.T) {
			files, _ := filepath.Glob(filepath.Join("testdata", "corpus", d.Name, "*.jsonl"))
			if len(files) == 0 {
				t.Skip("no corpus frozen for this dialect yet")
			}
			known := map[string]string{}
			listed, err := corpus.Read[corpus.Known](filepath.Join("testdata", "known", d.Name+".jsonl"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			for _, k := range listed {
				known[k.SQL] = k.Kind
			}
			used := map[string]bool{}
			for _, f := range files {
				rows, err := corpus.Read[corpus.Record](f)
				if err != nil {
					t.Fatal(err)
				}
				t.Run(strings.TrimSuffix(filepath.Base(f), ".jsonl"), func(t *testing.T) {
					for _, r := range rows {
						checkRecord(t, d, r, known, used)
					}
				})
			}
			for sql := range known {
				if !used[sql] {
					t.Errorf("stale entry in testdata/known: %s", sql)
				}
			}
		})
	}
}

func checkRecord(t *testing.T, d *dialect.Dialect, r corpus.Record, known map[string]string, used map[string]bool) {
	t.Helper()
	p := corpus.Parse(r.SQL, d)
	kind := ""
	switch {
	case p.Opaque:
		return // only leading keywords were checked
	case r.Reject && p.Err == nil:
		kind = corpus.AcceptsInvalid
	case !r.Reject && p.Err != nil:
		kind = corpus.RejectsValid
	}
	if kind != "" {
		if known[r.SQL] != kind {
			t.Errorf("%s: %s", kind, r.SQL)
		}
		used[r.SQL] = true
		return
	}
	if r.Reject {
		return
	}
	out := gen.Statements(p.Stmts, d)
	again := corpus.Parse(out, d)
	if again.Err != nil {
		t.Errorf("output does not re-parse: %v\n  in:  %s\n  out: %s", again.Err, r.SQL, out)
		return
	}
	if second := gen.Statements(again.Stmts, d); second != out {
		t.Errorf("unstable round trip\n  1st: %s\n  2nd: %s", out, second)
	}
	if r.Shape == "" {
		return
	}
	if got := corpus.Shape(p.Stmts, d); got != r.Shape {
		t.Errorf("wrong tree for %s\n want: %s\n  got: %s", r.SQL, r.Shape, got)
	}
	if got := corpus.Shape(again.Stmts, d); got != r.Shape {
		t.Errorf("generator changed the tree of %s\n want: %s\n  got: %s", r.SQL, r.Shape, got)
	}
}

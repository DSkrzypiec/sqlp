package main

import (
	"strings"
	"testing"

	"sqlp/analyze"
	"sqlp/dialect"
	"sqlp/parser"
)

func graphOf(t *testing.T, sql string) *analyze.QStructGraph {
	t.Helper()
	stmts, err := parser.Parse(sql, dialect.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	g, err := analyze.QStruct(stmts[0], dialect.Postgres, analyze.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func boxByLabel(d drawing, label string) box {
	for _, b := range d.Boxes {
		if b.Label == label {
			return b
		}
	}
	return box{}
}

func TestLayout(t *testing.T) {
	d := layout(graphOf(t, `WITH a AS (SELECT * FROM t JOIN u ON true), b AS (SELECT * FROM a, t)
SELECT * FROM b, a, v, (SELECT * FROM w) s`), "arrow-1")

	// Columns from the sources on the left to the root on the right.
	x := func(label string) int { return boxByLabel(d, label).X }
	if !(x("t") < x("a") && x("a") < x("b") && x("b") < x("query")) {
		t.Errorf("columns out of order: t=%d a=%d b=%d query=%d", x("t"), x("a"), x("b"), x("query"))
	}
	// t is read at two depths; it sits at the deepest so every arrow points right.
	if x("t") >= x("u") && x("t") != x("u") {
		t.Errorf("t and u should share the leftmost column: t=%d u=%d", x("t"), x("u"))
	}
	for _, e := range d.Edges {
		if strings.Contains(e.Class, "back") {
			t.Errorf("unexpected backward edge %s", e.Path)
		}
	}
	if len(d.Edges) != 9 {
		t.Errorf("%d edges, want 9", len(d.Edges))
	}

	// No two boxes overlap.
	for i, p := range d.Boxes {
		for _, q := range d.Boxes[i+1:] {
			if p.X < q.X+q.W && q.X < p.X+p.W && p.Y < q.Y+q.H && q.Y < p.Y+p.H {
				t.Errorf("%s overlaps %s", p.Label, q.Label)
			}
		}
		if p.X < 0 || p.Y < 0 || p.X+p.W > d.Width || p.Y+p.H > d.Height {
			t.Errorf("%s is outside the drawing", p.Label)
		}
	}
}

func TestLayoutRecursion(t *testing.T) {
	d := layout(graphOf(t, "WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM r) SELECT * FROM r"), "a")
	loops := 0
	for _, e := range d.Edges {
		if e.Class == "edge loop" {
			loops++
		}
	}
	if loops != 1 {
		t.Errorf("%d loops, want 1: %+v", loops, d.Edges)
	}
}

func TestLayoutLabels(t *testing.T) {
	long := strings.Repeat("x", 60)
	d := layout(graphOf(t, "SELECT * FROM "+long+", missing.t"), "a")
	b := boxByLabel(d, strings.Repeat("x", maxLabel-1)+"…")
	if b.Label == "" {
		t.Fatalf("long label not cut: %+v", d.Boxes)
	}
	if !strings.Contains(b.Title, long) {
		t.Errorf("tooltip lacks the full name: %q", b.Title)
	}
	if got := boxByLabel(d, "missing.t"); got.Sub != "object · unchecked" || !strings.Contains(got.Class, "s-unchecked") {
		t.Errorf("missing.t: %+v", got)
	}
}

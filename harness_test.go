package sqlp

import (
	"slices"
	"strings"
	"testing"

	"sqlp/dialect"
	"sqlp/internal/testkit"
)

// identity checks that each SQL string parses in dialect d and that the
// generator reproduces it exactly (modulo the trailing ';'). It also checks
// that the generated text re-parses to the same text (round-trip stability).
func identity(t *testing.T, d *dialect.Dialect, sqls []string) {
	t.Helper()
	for _, sql := range sqls {
		t.Run(sql, func(t *testing.T) {
			stmts, err := Parse(sql, d)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			got := strings.TrimSuffix(Format(stmts, d), ";")
			if got != sql {
				t.Fatalf("not identity\n want: %s\n  got: %s", sql, got)
			}
			again, err := Parse(got, d)
			if err != nil {
				t.Fatalf("re-parse error: %v", err)
			}
			if got2 := strings.TrimSuffix(Format(again, d), ";"); got2 != got {
				t.Fatalf("unstable round trip\n first: %s\nsecond: %s", got, got2)
			}
		})
	}
}

// rewrite is a pair of input SQL and the canonical output expected from the
// generator in the same dialect.
type rewrite struct{ in, out string }

func rewrites(t *testing.T, d *dialect.Dialect, cases []rewrite) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			stmts, err := Parse(c.in, d)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			if got := strings.TrimSuffix(Format(stmts, d), ";"); got != c.out {
				t.Fatalf("\n want: %s\n  got: %s", c.out, got)
			}
		})
	}
}

// failure is an input that must not parse, and a fragment that must appear in
// the error message.
type failure struct{ sql, msg string }

func failures(t *testing.T, d *dialect.Dialect, cases []failure) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			stmts, err := Parse(c.sql, d)
			if err == nil {
				t.Fatalf("expected error, parsed as: %s", Format(stmts, d))
			}
			if !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("error %q does not contain %q", err.Error(), c.msg)
			}
		})
	}
}

// caseSet groups the table-driven cases of one dialect.
type caseSet struct {
	name     string
	identity []string
	rewrites []rewrite
	failures []failure
}

// conformance lists what each dialect must satisfy. To add a table, add it
// here: "common" holds SQL valid in every dialect, "dialect" this dialect's own
// syntax, and "foreign" other dialects' syntax it must reject.
var conformance = []struct {
	d    *dialect.Dialect
	sets []caseSet
}{
	{dialect.Postgres, []caseSet{
		{name: "common", identity: commonIdentity, failures: commonFailures},
		{name: "dialect", identity: postgresIdentity, rewrites: postgresRewrites, failures: postgresFailures},
		{name: "foreign", failures: postgresRejectsSQLite},
	}},
	{dialect.SQLite, []caseSet{
		{name: "common", identity: commonIdentity, failures: commonFailures},
		{name: "dialect", identity: sqliteIdentity, rewrites: sqliteRewrites, failures: sqliteFailures},
		{name: "foreign", failures: sqliteRejectsPostgres},
	}},
	// The common sets are PostgreSQL and SQLite syntax: in BigQuery "x" is a
	// string, and INSERT t VALUES (1) is valid.
	{dialect.BigQuery, []caseSet{
		{name: "dialect", identity: bigqueryIdentity, failures: bigqueryFailures},
	}},
}

// TestConformance runs as TestConformance/<dialect>/<set>/<kind>/<sql>.
func TestConformance(t *testing.T) {
	for _, c := range conformance {
		testkit.Run(t, c.d.Name, func(t *testing.T) {
			for _, s := range c.sets {
				t.Run(s.name, func(t *testing.T) {
					t.Run("identity", func(t *testing.T) { identity(t, c.d, s.identity) })
					t.Run("rewrites", func(t *testing.T) { rewrites(t, c.d, s.rewrites) })
					t.Run("failures", func(t *testing.T) { failures(t, c.d, s.failures) })
				})
			}
		})
	}
}

// A dialect missing from either list would silently go untested.
func TestEveryDialectHasConformanceCases(t *testing.T) {
	var names []string
	for _, c := range conformance {
		names = append(names, c.d.Name)
	}
	if !slices.Equal(names, testkit.Dialects) {
		t.Fatalf("conformance covers %v, testkit.Dialects is %v", names, testkit.Dialects)
	}
}

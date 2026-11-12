package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "sqlp/internal/testkit" // defines the -dialect flag go test ./... passes
)

func TestRun(t *testing.T) {
	dir := t.TempDir()
	catalogFile := filepath.Join(dir, "catalog.json")
	badCatalog := filepath.Join(dir, "bad.json")
	misspelled := filepath.Join(dir, "misspelled.json")
	for name, content := range map[string]string{
		catalogFile: `{"database": "shop", "objects": [{"schema": "public", "name": "t", "kind": "table", "columns": [{"name": "a"}]},
			{"schema": "public", "name": "v", "kind": "view", "definition": "SELECT * FROM t"}]}`,
		badCatalog: `{"objects": [{"name": "t", "kind": "View"}]}`,
		misspelled: `{"objects": [{"name": "v", "kind": "view", "defintion": "SELECT 1"}]}`,
	} {
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name   string
		args   []string
		code   int
		stdout []string
		stderr string
	}{
		{name: "tree", args: []string{"SELECT a + 1 FROM t"}, code: 0,
			stdout: []string{"Query\n", "Select\n", "Binary", `Op: "+"`, "Name a", "TableRef", "Name t"}},
		{name: "enum values are named", args: []string{"SELECT * FROM a LEFT JOIN b ON true"}, code: 0,
			stdout: []string{"Kind: LeftJoin"}},
		{name: "dialect flag", args: []string{"-dialect", "sqlite", "SELECT a FROM t LIMIT 1, 2"}, code: 0,
			stdout: []string{"Select\n"}},
		{name: "dialect matters", args: []string{"SELECT a FROM t LIMIT 1, 2"}, code: 1, stderr: "sqlp: "},
		{name: "syntax error", args: []string{"SELECT FROM WHERE"}, code: 1, stderr: "sqlp: "},
		{name: "unknown dialect", args: []string{"-dialect", "nope", "SELECT 1"}, code: 2, stderr: `unknown dialect "nope"`},
		{name: "no query", args: nil, code: 2, stderr: "usage: sqlp"},
		{name: "two queries", args: []string{"SELECT 1", "SELECT 2"}, code: 2, stderr: "usage: sqlp"},
		{name: "bad flag", args: []string{"-nope"}, code: 2, stderr: "usage: sqlp"},
		{name: "qstruct", args: []string{"-qstruct", "WITH x AS (SELECT * FROM t) SELECT * FROM x; SELECT 2"}, code: 0,
			stdout: []string{`"root": "query"`, `"id": "x"`, `"status": "unchecked"`, "}\n{\n"}},
		{name: "qstruct with catalog", args: []string{"-qstruct", "-catalog", catalogFile, "-expand-views", "SELECT * FROM v"},
			code: 0, stdout: []string{`"id": "shop.public.v"`, `"kind": "view"`, `"shop.public.t"`}},
		{name: "qstruct of an unsupported statement", args: []string{"-qstruct", "DELETE FROM t"}, code: 1,
			stderr: "sqlp: analyze: QStruct needs a SELECT or CREATE VIEW statement"},
		{name: "missing catalog", args: []string{"-qstruct", "-catalog", filepath.Join(dir, "nope.json"), "SELECT 1"},
			code: 1, stderr: "nope.json"},
		{name: "invalid catalog", args: []string{"-qstruct", "-catalog", badCatalog, "SELECT 1"}, code: 1,
			stderr: `unknown kind "View"`},
		{name: "misspelled catalog field", args: []string{"-qstruct", "-catalog", misspelled, "SELECT 1"}, code: 1,
			stderr: `unknown field "defintion"`},
		{name: "catalog without qstruct", args: []string{"-catalog", catalogFile, "SELECT 1"}, code: 2,
			stderr: "need -qstruct or -outcols"},
		{name: "outcols", args: []string{"-outcols", "SELECT a, t.* FROM t JOIN u ON u.id = t.id; SELECT 2"}, code: 0,
			stdout: []string{`"object": "t"`, `"column": "a"`, `"star": true`, `"join"`, "}\n{\n", `"columns": []`}},
		{name: "outcols with catalog", args: []string{"-outcols", "-catalog", catalogFile, "-expand-views", "SELECT * FROM v"},
			code: 0, stdout: []string{`"database": "shop"`, `"object": "t"`, `"column": "a"`}},
		{name: "outcols of an unsupported statement", args: []string{"-outcols", "DELETE FROM t"}, code: 1,
			stderr: "sqlp: analyze: ListOutCols needs a SELECT or CREATE VIEW statement"},
		{name: "qstruct and outcols", args: []string{"-qstruct", "-outcols", "SELECT 1"}, code: 2, stderr: "not both"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			if code := run(c.args, &stdout, &stderr); code != c.code {
				t.Fatalf("exit code %d, want %d\nstderr: %s", code, c.code, stderr.String())
			}
			for _, want := range c.stdout {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
				}
			}
			if !strings.Contains(stderr.String(), c.stderr) {
				t.Errorf("stderr lacks %q:\n%s", c.stderr, stderr.String())
			}
		})
	}
}

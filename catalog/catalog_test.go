package catalog_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"sqlp/catalog"
	_ "sqlp/internal/testkit" // defines the -dialect flag go test ./... passes
)

func TestJSON(t *testing.T) {
	const doc = `{"database":"shop","objects":[` +
		`{"schema":"public","name":"orders","kind":"table","columns":[{"name":"id","type":"integer"},{"name":"note"}]},` +
		`{"name":"recent","kind":"view","columns":[{"name":"id"}],"definition":"SELECT id FROM orders"},` +
		`{"database":"db","schema":"s","name":"t"}]}`
	want := catalog.Catalog{Database: "shop", Objects: []catalog.Object{
		{
			Name:    catalog.Name{Schema: "public", Object: "orders"},
			Kind:    catalog.Table,
			Columns: []catalog.Column{{Name: "id", Type: "integer"}, {Name: "note"}},
		},
		{
			Name: catalog.Name{Object: "recent"}, Kind: catalog.View, Columns: []catalog.Column{{Name: "id"}},
			Definition: "SELECT id FROM orders",
		},
		{Name: catalog.Name{Database: "db", Schema: "s", Object: "t"}},
	}}

	var got catalog.Catalog
	if err := json.Unmarshal([]byte(doc), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded\n%+v\nwant\n%+v", got, want)
	}
	out, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != doc {
		t.Fatalf("encoded\n%s\nwant\n%s", out, doc)
	}
}

func TestAdd(t *testing.T) {
	var c catalog.Catalog
	orders := catalog.Name{Schema: "public", Object: "orders"}
	users := catalog.Name{Schema: "public", Object: "users"}
	c.Add(orders, catalog.Table, catalog.Column{Name: "id", Type: "integer"})
	c.Add(orders, catalog.Table, catalog.Column{Name: "total"})
	c.Add(users, catalog.View, catalog.Column{Name: "id"}).Definition = "SELECT 1 AS id"
	// Out of order, and with a different kind: the object keeps its first kind.
	c.Add(orders, catalog.View, catalog.Column{Name: "user_id"})
	// An object with no columns, and the same name in another schema.
	c.Add(catalog.Name{Object: "empty"}, catalog.Table)
	c.Add(catalog.Name{Schema: "other", Object: "orders"}, "", catalog.Column{Name: "x"})

	want := []catalog.Object{
		{Name: orders, Kind: catalog.Table, Columns: []catalog.Column{
			{Name: "id", Type: "integer"}, {Name: "total"}, {Name: "user_id"},
		}},
		{Name: users, Kind: catalog.View, Columns: []catalog.Column{{Name: "id"}}, Definition: "SELECT 1 AS id"},
		{Name: catalog.Name{Object: "empty"}, Kind: catalog.Table},
		{Name: catalog.Name{Schema: "other", Object: "orders"}, Columns: []catalog.Column{{Name: "x"}}},
	}
	if !reflect.DeepEqual(c.Objects, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", c.Objects, want)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	col := func(names ...string) []catalog.Column {
		var cols []catalog.Column
		for _, n := range names {
			cols = append(cols, catalog.Column{Name: n})
		}
		return cols
	}
	obj := func(schema, name string, cols ...string) catalog.Object {
		return catalog.Object{Name: catalog.Name{Schema: schema, Object: name}, Columns: col(cols...)}
	}
	for _, tc := range []struct {
		name    string
		objects []catalog.Object
		want    []string // one line per expected error, in order
	}{
		{name: "empty catalog"},
		{
			name: "valid",
			objects: []catalog.Object{
				obj("", "t", "a", "b"),
				// Different names only by schema or case: the dialect decides later.
				obj("public", "t", "a"),
				obj("", "T", "a", "A"),
				{Name: catalog.Name{Database: "db", Schema: "s", Object: "t"}, Kind: catalog.ForeignTable},
				{Name: catalog.Name{Object: "m"}, Kind: catalog.MaterializedView},
			},
		},
		{
			name:    "empty object name",
			objects: []catalog.Object{obj("s", "")},
			want:    []string{"catalog: objects[0]: empty name"},
		},
		{
			name:    "database without schema",
			objects: []catalog.Object{{Name: catalog.Name{Database: "db", Object: "t"}}},
			want:    []string{`catalog: objects[0] (db.t): database "db" given without a schema`},
		},
		{
			name:    "unknown kind",
			objects: []catalog.Object{{Name: catalog.Name{Object: "t"}, Kind: "View"}},
			want:    []string{`catalog: objects[0] (t): unknown kind "View"`},
		},
		{
			name: "definitions",
			objects: []catalog.Object{
				{Name: catalog.Name{Object: "v"}, Kind: catalog.View, Definition: "SELECT 1"},
				{Name: catalog.Name{Object: "m"}, Kind: catalog.MaterializedView, Definition: "SELECT 1"},
				{Name: catalog.Name{Object: "x"}, Definition: "SELECT 1"},
				{Name: catalog.Name{Object: "t"}, Kind: catalog.Table, Definition: "SELECT 1"},
				{Name: catalog.Name{Object: "f"}, Kind: catalog.ForeignTable, Definition: "SELECT 1"},
			},
			want: []string{
				"catalog: objects[3] (t): a table cannot have a definition",
				"catalog: objects[4] (f): a foreign_table cannot have a definition",
			},
		},
		{
			name:    "duplicate object",
			objects: []catalog.Object{obj("s", "t"), obj("s", "u"), obj("s", "t")},
			want:    []string{"catalog: objects[2] (s.t): also listed as objects[0]"},
		},
		{
			name:    "column problems",
			objects: []catalog.Object{obj("", "t", "a", "", "a")},
			want: []string{
				"catalog: objects[0] (t): columns[1]: empty name",
				`catalog: objects[0] (t): duplicate column "a"`,
			},
		},
		{
			name:    "every problem is reported",
			objects: []catalog.Object{obj("", ""), obj("", ""), obj("", "100%", "x", "x")},
			want: []string{
				"catalog: objects[0]: empty name",
				"catalog: objects[1]: empty name",
				`catalog: objects[2] (100%): duplicate column "x"`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := catalog.Catalog{Objects: tc.objects}
			err := c.Validate()
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("no error, want:\n%s", strings.Join(tc.want, "\n"))
			}
			if got := err.Error(); got != strings.Join(tc.want, "\n") {
				t.Fatalf("got:\n%s\nwant:\n%s", got, strings.Join(tc.want, "\n"))
			}
		})
	}
}

func TestNameString(t *testing.T) {
	for _, tc := range []struct {
		name catalog.Name
		want string
	}{
		{catalog.Name{Object: "t"}, "t"},
		{catalog.Name{Schema: "s", Object: "t"}, "s.t"},
		{catalog.Name{Database: "d", Schema: "s", Object: "t"}, "d.s.t"},
		{catalog.Name{Schema: "My Schema", Object: `a"b`}, `My Schema.a"b`},
		{catalog.Name{}, ""},
	} {
		if got := tc.name.String(); got != tc.want {
			t.Errorf("%#v.String() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func ExampleCatalog_Add() {
	// Rows as returned by the queries in the package documentation.
	rows := [][5]string{
		{"public", "orders", "table", "id", "integer"},
		{"public", "orders", "table", "total", "numeric(10,2)"},
		{"public", "recent_orders", "view", "id", "integer"},
	}
	var c catalog.Catalog
	for _, r := range rows {
		c.Add(catalog.Name{Schema: r[0], Object: r[1]}, catalog.Kind(r[2]),
			catalog.Column{Name: r[3], Type: r[4]})
	}
	if err := c.Validate(); err != nil {
		panic(err)
	}
	for _, o := range c.Objects {
		fmt.Println(o.Kind, o.Name, o.Columns)
	}
	// Output:
	// table public.orders [{id integer} {total numeric(10,2)}]
	// view public.recent_orders [{id integer}]
}

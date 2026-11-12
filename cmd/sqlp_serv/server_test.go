package main

import (
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"sqlp"
	"sqlp/analyze"
	"sqlp/dialect"
	_ "sqlp/internal/testkit" // defines the -dialect flag go test ./... passes
	"sqlp/parser"
)

func newTestServer(t *testing.T) *httptest.Server {
	return newTestServerWithTimeout(t, requestTimeout)
}

func newTestServerWithTimeout(t *testing.T, timeout time.Duration) *httptest.Server {
	t.Helper()
	s, err := newServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.timeout = timeout
	ts := httptest.NewServer(s.routes())
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, ts *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func run(t *testing.T, ts *httptest.Server, form url.Values) string {
	t.Helper()
	resp, err := http.PostForm(ts.URL+"/run", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	return html.UnescapeString(string(body))
}

// labels lists the node labels of every graph in a result, in order.
func labels(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<text class="label"[^>]*>([^<]*)</text>`).FindAllStringSubmatch(body, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

func TestPage(t *testing.T) {
	ts := newTestServer(t)
	resp, body := get(t, ts, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{
		`hx-post="/run"`, `name="sql"`, `name="catalog"`, `name="dialect"`, `name="action"`,
		"WITH recent_orders AS", "&#34;database&#34;: &#34;shop&#34;", `/static/htmx.min.js?v=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP = %q", csp)
	}
	if resp, _ := get(t, ts, "/nope"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path: status %d", resp.StatusCode)
	}
	if resp, _ := get(t, ts, "/run"); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /run: status %d", resp.StatusCode)
	}
	if _, body := get(t, ts, "/healthz"); !strings.HasPrefix(body, "ok ") {
		t.Errorf("healthz = %q", body)
	}
}

func TestStatic(t *testing.T) {
	ts := newTestServer(t)
	for path, typ := range map[string]string{
		"/static/style.css":   "text/css",
		"/static/app.js":      "text/javascript",
		"/static/htmx.min.js": "text/javascript",
		"/static/favicon.svg": "image/svg+xml",
	} {
		resp, body := get(t, ts, path+"?v=1")
		if resp.StatusCode != http.StatusOK || len(body) == 0 {
			t.Errorf("%s: status %d, %d bytes", path, resp.StatusCode, len(body))
		}
		if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, typ) {
			t.Errorf("%s: Content-Type %q, want %s", path, got, typ)
		}
		if !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
			t.Errorf("%s: Cache-Control %q", path, resp.Header.Get("Cache-Control"))
		}
	}
}

func TestSamples(t *testing.T) {
	ts := newTestServer(t)
	_, sql := get(t, ts, "/sample/sql")
	_, cat := get(t, ts, "/sample/catalog")
	if !strings.Contains(sql, `id="sql"`) || !strings.Contains(cat, `id="catalog"`) {
		t.Fatalf("samples are not editors:\n%s\n%s", sql, cat)
	}
	if _, bq := get(t, ts, "/sample/sql?dialect=bigquery"); !strings.Contains(bq, "acme-shop.sales.orders") {
		t.Errorf("BigQuery's sample query is not served:\n%s", bq)
	}
	// The samples must work together: everything resolves, nothing is reported.
	for _, d := range dialect.All {
		sample := sampleFor(d.Name)
		stmts, err := parser.Parse(sample.SQL, d)
		if err != nil {
			t.Fatalf("%s: %v", d.Name, err)
		}
		cat, err := parseCatalog(sample.Catalog)
		if err != nil {
			t.Fatal(err)
		}
		g, err := analyze.QStruct(stmts[0], d, analyze.Options{Catalog: cat, ExpandViews: true})
		if err != nil {
			t.Fatalf("%s: %v", d.Name, err)
		}
		for _, n := range g.Objects() {
			if n.Status != analyze.Resolved {
				t.Errorf("%s: %s is %s", d.Name, n.ID, n.Status)
			}
		}
		if len(g.Diagnostics) > 0 {
			t.Errorf("%s: diagnostics %v", d.Name, g.Diagnostics)
		}
		c, err := analyze.ListOutCols(stmts[0], d, analyze.Options{Catalog: cat, ExpandViews: true})
		if err != nil {
			t.Fatalf("%s: %v", d.Name, err)
		}
		for _, col := range c.Columns {
			if col.Status != analyze.Resolved {
				t.Errorf("%s: column %s is %s", d.Name, col.Name(), col.Status)
			}
		}
		if len(c.Columns) == 0 || len(c.Diagnostics) > 0 {
			t.Errorf("%s: %d columns, diagnostics %v", d.Name, len(c.Columns), c.Diagnostics)
		}
	}
}

func TestRunQStruct(t *testing.T) {
	ts := newTestServer(t)
	body := run(t, ts, url.Values{
		"sql": {sampleSQL}, "catalog": {sampleCatalog}, "dialect": {"postgres"},
		"action": {"qstruct"}, "use_catalog": {"on"}, "expand": {"on"},
	})
	want := []string{
		"query", "shop.crm.active_customers", "shop.crm.customers", "customer_spend", "recent_orders",
		"shop.sales.orders", "order_totals", "shop.sales.order_items", "shop.public.products",
		"last_ticket", "shop.support.tickets",
	}
	if got := labels(body); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("nodes %v\nwant  %v", got, want)
	}
	for _, s := range []string{"<svg", `marker-end="url(#arrow-1)"`, "Objects read (6)", `"root": "query"`} {
		if !strings.Contains(body, s) {
			t.Errorf("result lacks %q", s)
		}
	}

	// With "Use" unticked, names stay as written and views are not expanded.
	body = run(t, ts, url.Values{
		"sql": {sampleSQL}, "catalog": {sampleCatalog}, "dialect": {"sqlite"}, "action": {"qstruct"}, "expand": {"on"},
	})
	want = []string{
		"query", "crm.active_customers", "customer_spend", "recent_orders", "sales.orders",
		"order_totals", "sales.order_items", "products", "last_ticket", "support.tickets",
	}
	if got := labels(body); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("without catalog: %v\nwant %v", got, want)
	}
	if !strings.Contains(body, "unchecked") {
		t.Error("without catalog, names should be unchecked")
	}
}

// rows lists the rows of every column table in a result, as
// "object column clauses status".
func rows(body string) []string {
	var out []string
	cell := regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`)
	tag := regexp.MustCompile(`<[^>]*>`)
	for _, row := range regexp.MustCompile(`(?s)<tr><td>.*?</tr>`).FindAllString(body, -1) {
		var cells []string
		for _, m := range cell.FindAllStringSubmatch(row, -1) {
			cells = append(cells, strings.Join(strings.Fields(tag.ReplaceAllString(m[1], " ")), " "))
		}
		out = append(out, strings.Join(cells, " | "))
	}
	return out
}

func TestRunOutCols(t *testing.T) {
	ts := newTestServer(t)
	body := run(t, ts, url.Values{
		"sql": {sampleSQL}, "catalog": {sampleCatalog}, "dialect": {"postgres"},
		"action": {"outcols"}, "use_catalog": {"on"}, "expand": {"on"},
	})
	want := []string{
		"shop.crm.customers | name | select | in catalog",
		"shop.crm.customers | country | select | in catalog",
		"shop.sales.order_items | quantity | select | in catalog",
		"shop.public.products | price | select | in catalog",
		"shop.support.tickets | opened_at | select | in catalog",
		"shop.sales.order_items | product_id | join | in catalog",
		"shop.public.products | id | join | in catalog",
		"shop.sales.orders | id | join | in catalog",
		"shop.sales.order_items | order_id | join | in catalog",
		"shop.crm.customers | id | join | in catalog",
		"shop.sales.orders | customer_id | join | in catalog",
		"shop.support.tickets | customer_id | join | in catalog",
	}
	if got := rows(body); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, s := range []string{"through CTEs and subqueries and views.", `"clauses": [`} {
		if !strings.Contains(body, s) {
			t.Errorf("result lacks %q", s)
		}
	}

	// Without the catalog: names as written, stars, and what only a catalog
	// could settle.
	body = run(t, ts, url.Values{
		"sql": {"SELECT t.*, x FROM t JOIN u USING (id); DELETE FROM t; SELECT 1"}, "catalog": {sampleCatalog},
		"dialect": {"sqlite"}, "action": {"outcols"}, "expand": {"on"},
	})
	want = []string{
		"t | * all columns | select | unchecked",
		"unknown | x | select | ambiguous",
		"t | id | join | unchecked",
		"u | id | join | unchecked",
	}
	if got := rows(body); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows without catalog:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, s := range []string{
		"column x could come from t or u", `class="warn">ambiguous`, "through CTEs and subqueries.",
		"Columns used covers SELECT and CREATE VIEW statements", "The query uses no table or view columns.",
	} {
		if !strings.Contains(body, s) {
			t.Errorf("result lacks %q", s)
		}
	}
}

func TestRunAST(t *testing.T) {
	ts := newTestServer(t)
	body := run(t, ts, url.Values{"sql": {"select a+1 from t"}, "dialect": {"postgres"}, "action": {"ast"}})
	for _, s := range []string{"SELECT a + 1 FROM t;", `<span class="t">Query</span>`, `<span class="t">Binary</span>`,
		`<span class="f">Op</span><span class="v">"+"</span>`} {
		if !strings.Contains(body, s) {
			t.Errorf("result lacks %q:\n%s", s, body)
		}
	}
}

func TestRunScripts(t *testing.T) {
	ts := newTestServer(t)
	body := run(t, ts, url.Values{
		"sql":     {"SELECT 1 FROM a;\nSELEC 2;\nDELETE FROM b;\n  SELECT * FROM c WHERE"},
		"dialect": {"postgres"}, "action": {"qstruct"},
	})
	for _, s := range []string{
		"Statement 1", "Statement 4", "4 statements in",
		"Syntax error at line 2, column 1", "SELEC 2;\n<span class=\"caret\">^</span>",
		"Syntax error at line 4, column 24", "  SELECT * FROM c WHERE\n<span class=\"caret\">" + strings.Repeat(" ", 23) + "^</span>",
		"Query structure covers SELECT and CREATE VIEW statements",
	} {
		if !strings.Contains(body, s) {
			t.Errorf("result lacks %q:\n%s", s, body)
		}
	}

	body = run(t, ts, url.Values{"sql": {"  -- nothing\n"}, "dialect": {"sqlite"}, "action": {"ast"}})
	if !strings.Contains(body, "Nothing to analyse") {
		t.Errorf("empty input:\n%s", body)
	}
	body = run(t, ts, url.Values{"sql": {strings.Repeat("SELECT 1;", maxStatements+5)}, "dialect": {"sqlite"}, "action": {"ast"}})
	if !strings.Contains(body, "Showing the first 50 of 55 statements.") || strings.Contains(body, "Statement 51") {
		t.Errorf("statement cap not applied")
	}
}

func TestRunBadInput(t *testing.T) {
	ts := newTestServer(t)
	for _, tc := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"sql": {"SELECT 1"}, "dialect": {"oracle"}, "action": {"ast"}}, "Unknown dialect."},
		{url.Values{"sql": {"SELECT 1"}, "dialect": {"sqlite"}, "action": {"drop"}}, "Unknown action."},
		{url.Values{"sql": {"SELECT 1"}, "dialect": {"sqlite"}, "action": {"qstruct"}, "use_catalog": {"on"},
			"catalog": {`{"objects": [`}}, "The catalog is not valid JSON"},
		{url.Values{"sql": {"SELECT 1"}, "dialect": {"sqlite"}, "action": {"qstruct"}, "use_catalog": {"on"},
			"catalog": {`{"objects": [], "tables": []}`}}, `unknown field "tables"`},
		{url.Values{"sql": {"SELECT 1"}, "dialect": {"sqlite"}, "action": {"qstruct"}, "use_catalog": {"on"},
			"catalog": {`{"objects": [{"name": "t", "kind": "View"}]}`}}, `unknown kind "View"`},
		{url.Values{"sql": {"SELECT 1"}, "dialect": {"sqlite"}, "action": {"outcols"}, "use_catalog": {"on"},
			"catalog": {`{"objects": [{"name": "t", "columns": [{"name": "a"}, {"name": "a"}]}]}`}}, `duplicate column "a"`},
		// The syntax tree does not use the catalog.
		{url.Values{"sql": {"SELECT 1"}, "dialect": {"sqlite"}, "action": {"ast"}, "use_catalog": {"on"},
			"catalog": {`{"objects": [`}}, "1 statement in"},
		// A broken catalog does not matter when it is not used.
		{url.Values{"sql": {"SELECT 1"}, "dialect": {"sqlite"}, "action": {"qstruct"},
			"catalog": {`{"objects": [`}}, "1 statement in"},
		{url.Values{"sql": {strings.Repeat("x", maxSQL+1)}, "dialect": {"sqlite"}, "action": {"ast"}},
			"The SQL is too long: the limit is 200 KB."},
		{url.Values{"sql": {strings.Repeat("x", maxBody+1)}, "dialect": {"sqlite"}, "action": {"ast"}},
			"The request is too large: the limit is 512 KB."},
	} {
		if body := run(t, ts, tc.form); !strings.Contains(body, tc.want) {
			t.Errorf("%v: result lacks %q:\n%.400s", tc.form.Get("dialect"), tc.want, body)
		}
	}
}

// TestRunWorstCases sends inputs made to cost as much as their size allows.
// Each is close to the size limits and must come back quickly and small:
// before the limits in server.go, the first took 45 seconds and "one long
// list" returned 17 MB.
func TestRunWorstCases(t *testing.T) {
	ts := newTestServer(t)
	// rep joins n copies of format, with # replaced by the copy's number
	// and $ by the next one.
	rep := func(n int, format string) string {
		var sb strings.Builder
		for i := range n {
			sb.WriteString(strings.NewReplacer("#", strconv.Itoa(i), "$", strconv.Itoa(i+1)).Replace(format))
		}
		return sb.String()
	}
	wide := `{"objects": [` + rep(200, `{"name": "v#", "kind": "view", "definition": "SELECT 1 FROM t`+strings.Repeat(",t", 450)+`"},`) +
		`{"name": "t"}]}`
	wideTable := `{"objects": [{"name": "t", "columns": [{"name": "c0"}` + rep(999, `, {"name": "c$"}`) + `]}]}`
	for _, tc := range []struct {
		name, action, sql, catalog string
		want                       string
		count                      int // how many times want appears
	}{
		{"one alias on many subqueries", "qstruct", "SELECT 1 FROM (SELECT 1) x" + rep(15000, ",(SELECT 1) x"), "",
			"The graph has 15002 nodes and 15001 arrows, too many to show", 1},
		{"many tables", "qstruct", "SELECT 1 FROM t" + rep(27000, ",t#"), "", "The graph has 27002 nodes", 1},
		{"many statements with many subqueries", "qstruct", rep(50, "SELECT 1"+strings.Repeat(",(SELECT 1)", 280)+";"), "",
			"The graph has 281 nodes and 280 arrows, too many to show", 47},
		{"a chain of CTEs", "qstruct", "WITH c0 AS (SELECT 1)" + rep(6000, ",c$ AS (SELECT 1 FROM c#)") + " SELECT 1 FROM c6000", "",
			"The graph has 6002 nodes", 1},
		{"many arrows between few nodes", "qstruct", "WITH c AS (SELECT 1)" + rep(190, ",c# AS (SELECT 1 FROM t"+rep(190, ",t#")+")") +
			" SELECT 1 FROM c" + rep(190, ",c#"), "", "The graph has 383 nodes and 36481 arrows, too many to show", 1},
		{"many unused CTEs", "qstruct", "WITH c AS (SELECT 1)" + rep(9000, ",c# AS (SELECT 1)") + " SELECT 1", "", "… and 8981 more", 1},
		{"long names", "qstruct", "SELECT 1 FROM " + strings.Repeat("a", 60000) + rep(3, ", (SELECT 1 FROM "+strings.Repeat("a", 40000)+")"), "",
			"<svg", 1},
		{"a name that grows when escaped", "qstruct", `SELECT 1 FROM "` + strings.Repeat("<", 160000) + `"`, "", "The result is over 2 MB", 1},
		{"many wide views to expand", "qstruct", rep(3, "SELECT 1 FROM v0"+rep(200, ",v#")+";"), wide, "<svg", 3},
		{"a deep stack of views to expand", "qstruct", rep(10, "SELECT 1 FROM v3000;"), deepViews(3000),
			"The graph has 3002 nodes and 3001 arrows", 10},
		{"many statements with long lists", "ast", rep(50, "SELECT 1"+strings.Repeat(",1", 1900)+";"), "",
			"too many to show: the page shows up to 10000 per run", 49},
		{"one long list", "ast", "SELECT 1" + strings.Repeat(",1", 95000), "", "too many to show: the page shows up to 10000 per run", 1},
		{"text that grows when escaped", "ast", "SELECT '" + strings.Repeat(`"`, 165000) + "'", "", "<summary>Formatted</summary>", 1},
		{"many errors on one line", "ast", rep(50, "SELECT 1"+strings.Repeat(",1", 1900)+" FROM;"), "", "Syntax error at line 1, column", 50},
		{"an error at a long token", "ast", "SELECT 1 a " + strings.Repeat("b", 190000), "", "Syntax error at line 1, column 12", 1},
		{"many statements with many columns", "outcols", rep(5, "SELECT a0"+rep(999, ",a$")+" FROM t;"), "",
			"The query uses 1000 columns, too many to show: the page lists up to 3000 per run.", 2},
		{"many columns that many tables could have", "outcols", "SELECT a0" + rep(1990, ",a$") + " FROM t0" + rep(9000, ",t$"), "",
			"… and 1971 more", 1},
		{"stars that double", "outcols", "WITH c0 AS (SELECT * FROM t)" + rep(4000, ",c$ AS (SELECT * FROM c# AS x, c# AS y)") +
			" SELECT * FROM c4000", wideTable, "<td><code>public.t</code></td>", 1000},
		{"a deep stack of views to list", "outcols", rep(10, "SELECT 1 FROM v3000;"), deepViews(3000),
			"The query uses no table or view columns.", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"sql": {tc.sql}, "dialect": {"postgres"}, "action": {tc.action}}
			if tc.catalog != "" {
				form.Set("catalog", tc.catalog)
				form.Set("use_catalog", "on")
				form.Set("expand", "on")
			}
			if len(tc.sql) > maxSQL || len(form.Encode()) > maxBody {
				t.Fatalf("the input is over the limits: %d bytes of SQL, %d in all", len(tc.sql), len(form.Encode()))
			}
			start := time.Now()
			resp, err := http.PostForm(ts.URL+"/run", form)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			took := time.Since(start)
			t.Logf("%d bytes sent, %d back in %v", len(form.Encode()), len(raw), took.Round(time.Millisecond))
			if got := strings.Count(html.UnescapeString(string(raw)), tc.want); got != tc.count {
				t.Errorf("%q appears %d times, want %d:\n%.600s", tc.want, got, tc.count, raw)
			}
			if len(raw) > maxReply {
				t.Errorf("%d bytes back, want at most %d", len(raw), maxReply)
			}
			if took > 2*time.Second {
				t.Errorf("took %v, want well under 2s", took)
			}
		})
	}
}

// deepViews is a catalog of n views, each reading the one before it twice.
func deepViews(n int) string {
	var sb strings.Builder
	sb.WriteString(`{"objects": [{"name": "v0"}`)
	for i := range n {
		fmt.Fprintf(&sb, `, {"name": "v%d", "kind": "view", "definition": "SELECT 1 FROM v%d a, v%d b"}`, i+1, i, i)
	}
	return sb.String() + "]}"
}

// TestRunDeadline checks that a request stops between statements once its
// time is up. Every statement here expands the whole catalog, so the work is
// the number of statements times the size of the catalog.
func TestRunDeadline(t *testing.T) {
	ts := newTestServerWithTimeout(t, 30*time.Millisecond)
	start := time.Now()
	body := run(t, ts, url.Values{"sql": {strings.Repeat("SELECT 1 FROM v3000;", 50)}, "dialect": {"postgres"}, "action": {"qstruct"},
		"catalog": {deepViews(3000)}, "use_catalog": {"on"}, "expand": {"on"}})
	if !strings.Contains(body, "took too long and was stopped") {
		t.Errorf("no note about the deadline:\n%.600s", body)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("took %v with a 30ms deadline", took)
	}
}

func TestCutText(t *testing.T) {
	for _, tc := range []struct {
		s           string
		n           int
		first, last string
	}{
		{"", 3, "", ""},
		{"abc", 3, "abc", "abc"},
		{"abcd", 3, "ab…", "…cd"},
		{"żółw", 4, "żółw", "żółw"},
		{"żółwie", 4, "żół…", "…wie"},
		{"a", 1, "a", "a"},
		{"ab", 1, "…", "…"},
	} {
		if got := firstRunes(tc.s, tc.n); got != tc.first {
			t.Errorf("firstRunes(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.first)
		}
		if got := lastRunes(tc.s, tc.n); got != tc.last {
			t.Errorf("lastRunes(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.last)
		}
	}
}

// TestSyntaxErrorExcerpt checks that the caret still points at the error
// when the line is cut on either side.
func TestSyntaxErrorExcerpt(t *testing.T) {
	long := strings.Repeat("1, ", 200)
	for _, tc := range []struct{ name, sql, line, caret string }{
		{"a short line", "SELECT 1 FROM", "SELECT 1 FROM", strings.Repeat(" ", 13) + "^"},
		{"tabs are kept", "SELECT\t1 FROM", "SELECT\t1 FROM", "      \t" + strings.Repeat(" ", 6) + "^"},
		{"cut after the error", "SELECT FROM " + long, "SELECT FROM " + strings.Repeat("1, ", 17) + "1…", strings.Repeat(" ", 12) + "^"},
		{"cut before the error", "SELECT " + long + "FROM", "…" + strings.Repeat("1, ", 35) + "FROM", strings.Repeat(" ", 106) + "^"},
		{"cut on both sides", "SELECT " + long + "FROM " + long, "…" + strings.Repeat("1, ", 35) + "FROM " + strings.Repeat("1, ", 15) + "1,…",
			strings.Repeat(" ", 106) + "^"},
		{"a later line", "SELECT 1;\nSELECT żółw FROM", "SELECT żółw FROM", strings.Repeat(" ", 16) + "^"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sqlp.Parse(tc.sql, dialect.Postgres)
			var perr *parser.Error
			if !errors.As(err, &perr) {
				t.Fatalf("Parse returned %v, want a syntax error", err)
			}
			v := describeSyntaxError(strings.Split(tc.sql, "\n"), perr)
			if v.SourceLine != tc.line || v.Caret != tc.caret {
				t.Errorf("got\n%s\n%s\nwant\n%s\n%s", v.SourceLine, v.Caret, tc.line, tc.caret)
			}
		})
	}
}

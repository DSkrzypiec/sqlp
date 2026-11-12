package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"

	"sqlp"
	"sqlp/analyze"
	"sqlp/ast"
	"sqlp/catalog"
	"sqlp/dialect"
	"sqlp/internal/astdump"
	"sqlp/parser"
)

//go:embed web
var webFS embed.FS

// Limits for a public, unauthenticated service. Parsing and analysis take
// time in proportion to the input, so the input limits bound the work; the
// others bound the HTML sent back, which would otherwise grow much faster
// than the input (TestRunWorstCases).
const (
	maxBody        = 512 << 10 // the whole form
	maxSQL         = 200 << 10
	maxStatements  = 50              // shown per request
	maxTreeEntries = 10000           // syntax-tree entries shown per request
	maxGraphItems  = 2000            // graph nodes and edges drawn per request
	maxColumnRows  = 3000            // used columns listed per request
	maxDiagnostics = 20              // shown per statement
	maxErrorLine   = 160             // characters of a source line shown with a syntax error
	maxMessage     = 300             // characters of an error message
	maxReply       = 2 << 20         // bytes of HTML; a larger result is replaced by a message
	requestTimeout = 2 * time.Second // parsing and every statement's analysis together
	maxConcurrent  = 8
	queueTimeout   = 5 * time.Second
)

type server struct {
	store   *store // nil when runs are not recorded
	tmpl    *template.Template
	static  fs.FS
	version string // changes whenever a static file does, to bust caches
	build   string
	busy    chan struct{}
	timeout time.Duration // requestTimeout, except in tests
	log     *slog.Logger
}

func newServer(log *slog.Logger) (*server, error) {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"isStruct": func(n *astdump.Node) bool { return n.Kind == astdump.Struct },
		"isList":   func(n *astdump.Node) bool { return n.Kind == astdump.List },
	}).ParseFS(webFS, "web/*.html")
	if err != nil {
		return nil, err
	}
	static, err := fs.Sub(webFS, "web/static")
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	err = fs.WalkDir(static, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(static, path)
		h.Write([]byte(path))
		h.Write(data)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &server{
		tmpl:    tmpl,
		static:  static,
		version: hex.EncodeToString(h.Sum(nil))[:12],
		build:   buildVersion(),
		busy:    make(chan struct{}, maxConcurrent),
		timeout: requestTimeout,
		log:     log,
	}, nil
}

// buildVersion is the commit the binary was built from, as stamped by go build.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if dirty {
		rev += "+dirty"
	}
	return rev
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handlePage)
	mux.HandleFunc("POST /run", s.handleRun)
	mux.HandleFunc("GET /sample/sql", s.handleSample("sqlbox", func(x sample) string { return x.SQL }))
	mux.HandleFunc("GET /sample/catalog", s.handleSample("catalogbox", func(x sample) string { return x.Catalog }))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "ok %s\n", s.build)
	})
	files := http.StripPrefix("/static/", http.FileServerFS(s.static))
	mux.HandleFunc("GET /static/", func(w http.ResponseWriter, r *http.Request) {
		// URLs carry ?v=<version>, so a new deploy never meets a stale copy.
		w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
		files.ServeHTTP(w, r)
	})
	return s.logRequests(secure(mux))
}

// secure adds headers that suit a page with no inline scripts or styles.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; "+
			"img-src 'self' data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status, bytes int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// logRequests logs one line per request. It never logs the SQL.
func (s *server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"bytes", sw.bytes, "ms", time.Since(start).Milliseconds(), "client", clientIP(r))
	})
}

type option struct {
	Value, Label string
	Selected     bool
}

type pageData struct {
	Version, Build string
	Recording      bool // runs are stored, which the page says
	SQL, Catalog   string
	Samples        map[string]sample // every dialect's, for the script
	Dialects       []option
	Actions        []option
}

// sampleSQL is what the page starts with: CTEs, joins and a subquery, over
// the objects in sampleCatalog.
const sampleSQL = `-- Top customers this year, with their latest support ticket.
WITH recent_orders AS (
  SELECT o.id, o.customer_id
  FROM sales.orders o
  WHERE o.created_at >= '2026-01-01'
),
order_totals AS (
  SELECT i.order_id, sum(i.quantity * p.price) AS total
  FROM sales.order_items i
  JOIN products p ON p.id = i.product_id
  GROUP BY i.order_id
),
customer_spend AS (
  SELECT r.customer_id, sum(t.total) AS spent
  FROM recent_orders r
  JOIN order_totals t ON t.order_id = r.id
  GROUP BY r.customer_id
)
SELECT c.name, c.country, s.spent, last_ticket.opened_at
FROM crm.active_customers c
JOIN customer_spend s ON s.customer_id = c.id
LEFT JOIN (
  SELECT customer_id, max(opened_at) AS opened_at
  FROM support.tickets
  GROUP BY customer_id
) last_ticket ON last_ticket.customer_id = c.id
ORDER BY s.spent DESC
LIMIT 20;
`

// sampleCatalog covers every object in sampleSQL. products has no schema, so
// it lands in the dialect's default one; active_customers is a view.
const sampleCatalog = `{
  "database": "shop",
  "objects": [
    {"schema": "sales", "name": "orders", "kind": "table", "columns": [
      {"name": "id", "type": "bigint"}, {"name": "customer_id", "type": "bigint"},
      {"name": "created_at", "type": "timestamp"}, {"name": "status", "type": "text"}]},
    {"schema": "sales", "name": "order_items", "kind": "table", "columns": [
      {"name": "order_id", "type": "bigint"}, {"name": "product_id", "type": "bigint"},
      {"name": "quantity", "type": "integer"}]},
    {"name": "products", "kind": "table", "columns": [
      {"name": "id", "type": "bigint"}, {"name": "name", "type": "text"},
      {"name": "price", "type": "numeric(10,2)"}]},
    {"schema": "crm", "name": "customers", "kind": "table", "columns": [
      {"name": "id", "type": "bigint"}, {"name": "name", "type": "text"},
      {"name": "country", "type": "text"}, {"name": "deleted_at", "type": "timestamp"}]},
    {"schema": "crm", "name": "active_customers", "kind": "view", "columns": [
      {"name": "id"}, {"name": "name"}, {"name": "country"}],
     "definition": "SELECT id, name, country FROM crm.customers WHERE deleted_at IS NULL"},
    {"schema": "support", "name": "tickets", "kind": "table", "columns": [
      {"name": "id", "type": "bigint"}, {"name": "customer_id", "type": "bigint"},
      {"name": "opened_at", "type": "timestamp"}]}
  ]
}
`

// bigquerySample is BigQuery's sample, kept in files because its names are
// written in backquotes: tables named by quoted project.dataset.table paths, a
// query parameter, # comments, QUALIFY and SAFE_DIVIDE. In its catalog the
// database is the project, the schema the dataset, and the types BigQuery's.
var bigquerySample = sample{SQL: mustRead("web/samples/bigquery.sql"), Catalog: mustRead("web/samples/bigquery.json")}

func mustRead(path string) string {
	data, err := webFS.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// sample is what the editors start with in one dialect.
type sample struct {
	SQL     string `json:"sql"`
	Catalog string `json:"catalog"`
}

// samples holds each dialect's sample. The page's script swaps an editor to
// the new dialect's sample when the dialect changes, unless it was edited.
var samples = map[string]sample{
	"postgres": {sampleSQL, sampleCatalog},
	"sqlite":   {sampleSQL, sampleCatalog},
	"bigquery": bigquerySample,
}

func sampleFor(dialect string) sample {
	if s, ok := samples[dialect]; ok {
		return s
	}
	return samples["postgres"]
}

func (s *server) handlePage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "page", pageData{
		Version:   s.version,
		Build:     s.build,
		Recording: s.store != nil,
		SQL:       sampleSQL,
		Catalog:   sampleCatalog,
		Samples:   samples,
		Dialects: []option{
			{Value: "postgres", Label: "PostgreSQL 16", Selected: true},
			{Value: "sqlite", Label: "SQLite 3.51"},
			{Value: "bigquery", Label: "BigQuery"},
		},
		Actions: []option{
			{Value: "qstruct", Label: "Query structure", Selected: true},
			{Value: "outcols", Label: "Columns used"},
			{Value: "ast", Label: "Syntax tree"},
		},
	})
}

// handleSample serves a fresh editor holding the sample of the dialect named
// in the query string, for the reset buttons and dialect changes.
func (s *server) handleSample(name string, text func(sample) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.render(w, name, text(sampleFor(r.URL.Query().Get("dialect"))))
	}
}

// render writes one template. A reply that would pass maxReply is replaced
// by a message, which render returns. The limits above keep most results far
// below that; this covers the rest, such as a long name that the result
// repeats several times and that grows when escaped.
func (s *server) render(w http.ResponseWriter, name string, data any) string {
	var buf replyBuffer
	replaced := ""
	err := s.tmpl.ExecuteTemplate(&buf, name, data)
	if errors.Is(err, errReplyTooLarge) {
		buf.Reset()
		replaced = fmt.Sprintf("The result is over %d MB, too large to show here.", maxReply>>20)
		err = s.tmpl.ExecuteTemplate(&buf, "result", resultData{Error: replaced})
	}
	if err != nil {
		s.log.Error("template", "name", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return "internal error"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, buf.String())
	return replaced
}

// replyBuffer refuses to grow past maxReply, which stops the template.
type replyBuffer struct{ strings.Builder }

var errReplyTooLarge = errors.New("the reply is too large")

func (b *replyBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxReply {
		return 0, errReplyTooLarge
	}
	return b.Builder.Write(p)
}

type resultData struct {
	Error      string // the request as a whole failed
	Notes      []string
	Statements []stmtResult
	Multi      bool
	Summary    string
}

type stmtResult struct {
	Index, Line int
	Preview     string
	Err         *errorView
	Message     string // why the action does not apply to this statement
	QStruct     *qstructView
	OutCols     *outColsView
	AST         *astView
}

type errorView struct {
	Message    string
	SourceLine string
	Caret      string
}

type qstructView struct {
	Drawing     drawing
	Objects     []objectRow
	Diagnostics []string
	JSON        string
}

// room is what is left of the per-request display limits.
type room struct{ treeEntries, graphItems, columnRows int }

type objectRow struct{ Name, Kind, Status string }

type outColsView struct {
	Columns     []columnRow
	Expanded    bool // views were followed through their definitions
	Diagnostics []string
	JSON        string
}

type columnRow struct {
	Object, Column, Status string
	Star                   bool
	Clauses                []string
	Warn                   bool // the status says something is wrong
}

type astView struct {
	Formatted string
	Tree      *astdump.Node // nil when the request has no room left for it
	Size      int
	Limit     int
}

// handleRun parses the SQL and runs the chosen action on each statement.
// Problems with the input are shown in the page, with status 200, because
// htmx does not swap error responses in. Every run, failed or not, is
// recorded when the server has a store, a crash included.
func (s *server) handleRun(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &record{At: start, IP: clientIP(r), Build: s.build}
	defer func() {
		if s.store == nil {
			return
		}
		p := recover()
		if p != nil {
			rec.Error = fmt.Sprintf("panic: %v\n%s", p, debug.Stack())
		}
		rec.Duration = time.Since(start)
		s.store.add(rec)
		if p != nil {
			panic(p) // net/http logs it and drops the connection, as without a store
		}
	}()
	fail := func(msg string) {
		rec.Error = msg
		s.render(w, "result", resultData{Error: msg})
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		fail(fmt.Sprintf("The request is too large: the limit is %d KB.", maxBody>>10))
		return
	}
	rec.Dialect, rec.Action, rec.SQL = r.PostForm.Get("dialect"), r.PostForm.Get("action"), r.PostForm.Get("sql")
	rec.Expand = r.PostForm.Get("expand") != ""
	d, ok := dialect.ByName(rec.Dialect)
	if !ok {
		fail("Unknown dialect.")
		return
	}
	action := rec.Action
	if action != "qstruct" && action != "outcols" && action != "ast" {
		fail("Unknown action.")
		return
	}
	opts := analyze.Options{ExpandViews: rec.Expand}
	useCatalog := r.PostForm.Get("use_catalog") != "" && action != "ast"
	if text := strings.TrimSpace(r.PostForm.Get("catalog")); text != "" && useCatalog {
		rec.UseCatalog, rec.Catalog = true, text
		cat, err := parseCatalog(text)
		if err != nil {
			fail(err.Error())
			return
		}
		opts.Catalog = cat
	}

	select {
	case s.busy <- struct{}{}:
		defer func() { <-s.busy }()
	case <-time.After(queueTimeout):
		fail("The server is busy. Please try again in a moment.")
		return
	case <-r.Context().Done():
		rec.Error = "The client went away while waiting for a free slot."
		return
	}

	sql := rec.SQL
	if len(sql) > maxSQL {
		fail(fmt.Sprintf("The SQL is too long: the limit is %d KB.", maxSQL>>10))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()
	script, err := sqlp.ParseScript(ctx, sql, d, sqlp.Options{Limits: sqlp.Limits{MaxBytes: maxSQL}})
	if err != nil {
		fail(describeScriptError(err))
		return
	}
	rec.Statements = len(script.Statements)
	res := resultData{Multi: len(script.Statements) > 1}
	if len(script.Statements) == 0 {
		res.Notes = append(res.Notes, "Nothing to analyse yet: enter a SQL statement above.")
	}
	stmts := script.Statements
	if len(stmts) > maxStatements {
		res.Notes = append(res.Notes, fmt.Sprintf("Showing the first %d of %d statements.", maxStatements, len(stmts)))
		stmts = stmts[:maxStatements]
	}
	left := &room{treeEntries: maxTreeEntries, graphItems: maxGraphItems, columnRows: maxColumnRows}
	var lines []string
	for i, st := range stmts {
		// The analysis of one statement cannot be interrupted, but it takes
		// time in proportion to the statement and the catalog.
		if ctx.Err() != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("This took too long and was stopped after %d of %d statements.", i, len(stmts)))
			break
		}
		out := stmtResult{Index: i + 1, Line: st.Span.StartLine, Preview: preview(st.Text)}
		switch {
		case st.Err != nil:
			if lines == nil {
				lines = strings.Split(sql, "\n")
			}
			out.Err = describeSyntaxError(lines, st.Err)
		case action == "ast":
			out.AST = astResult(st.Node, d, left)
		case action == "outcols":
			out.OutCols, out.Message = outColsResult(st.Node, d, opts, left)
		default:
			out.QStruct, out.Message = qstructResult(st.Node, d, opts, i+1, left)
		}
		res.Statements = append(res.Statements, out)
		rec.Stmts = append(rec.Stmts, stmtRecordOf(out, st.Text))
	}
	rec.Notes = res.Notes
	noun := "statements"
	if len(script.Statements) == 1 {
		noun = "statement"
	}
	res.Summary = fmt.Sprintf("%d %s in %s", len(script.Statements), noun, time.Since(start).Round(time.Microsecond))
	if msg := s.render(w, "result", res); msg != "" {
		rec.Error = msg
	}
}

// stmtRecordOf is what the store keeps of one statement's result.
func stmtRecordOf(out stmtResult, text string) stmtRecord {
	sr := stmtRecord{Index: out.Index, Line: out.Line, Text: text, Message: out.Message}
	if out.Err != nil {
		sr.SyntaxError = out.Err.Message
	}
	switch {
	case out.QStruct != nil:
		sr.Diagnostics, sr.Result = out.QStruct.Diagnostics, out.QStruct.JSON
	case out.OutCols != nil:
		sr.Diagnostics, sr.Result = out.OutCols.Diagnostics, out.OutCols.JSON
	case out.AST != nil:
		sr.Result = out.AST.Formatted
	}
	return sr
}

// clientIP is the address a request came from. Behind nginx it is the
// X-Real-IP header, which is trusted only from a loopback peer: anyone else
// could set it to anything.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
			return real
		}
	}
	return host
}

func parseCatalog(text string) (*catalog.Catalog, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	var cat catalog.Catalog
	if err := dec.Decode(&cat); err != nil {
		return nil, fmt.Errorf("The catalog is not valid JSON for a catalog: %v", err)
	}
	if err := cat.Validate(); err != nil {
		return nil, fmt.Errorf("The catalog has problems:\n%v", err)
	}
	return &cat, nil
}

func describeScriptError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "parse aborted") {
		return "Parsing took too long and was stopped."
	}
	return err.Error()
}

// describeSyntaxError shows the message with the line it points at. Both
// are cut to a fixed length: a script can be one long line with an error in
// every statement.
func describeSyntaxError(lines []string, e *parser.Error) *errorView {
	v := &errorView{Message: fmt.Sprintf("Syntax error at line %d, column %d: %s", e.Line, e.Column, firstRunes(e.Msg, maxMessage))}
	if e.Line < 1 || e.Line > len(lines) {
		return v
	}
	line := strings.TrimRight(lines[e.Line-1], "\r")
	col := len(line) // the byte offset of the error's character
	for i := range line {
		if e.Column--; e.Column <= 0 {
			col = i
			break
		}
	}
	before, after := lastRunes(line[:col], maxErrorLine*2/3), firstRunes(line[col:], maxErrorLine/3)
	// Keep tabs so the caret lines up under them.
	var pad strings.Builder
	for _, r := range before {
		if r == '\t' {
			pad.WriteByte('\t')
		} else {
			pad.WriteByte(' ')
		}
	}
	v.SourceLine, v.Caret = before+after, pad.String()+"^"
	return v
}

// firstRunes cuts s to at most n characters. When anything is cut, the last
// character kept is replaced by "…".
func firstRunes(s string, n int) string {
	count := 0
	for i := range s {
		if count++; count == n { // s[i:] starts with the nth character
			if _, size := utf8.DecodeRuneInString(s[i:]); i+size < len(s) {
				return s[:i] + "…"
			}
			return s
		}
	}
	return s
}

// lastRunes is firstRunes from the other end.
func lastRunes(s string, n int) string {
	count := 0
	for i := len(s); i > 0; {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		if count++; count == n { // s[:i] ends with the nth character from the end
			if i-size > 0 {
				return "…" + s[i:]
			}
			return s
		}
		i -= size
	}
	return s
}

func preview(text string) string {
	return firstRunes(strings.Join(strings.Fields(text), " "), 90)
}

// astResult shows the tree when the request still has room for it.
func astResult(stmt ast.Statement, d *dialect.Dialect, left *room) *astView {
	v := &astView{Formatted: sqlp.Format([]ast.Statement{stmt}, d), Limit: maxTreeEntries}
	tree := astdump.Tree(stmt)
	v.Size = tree.Size()
	if v.Size <= left.treeEntries {
		left.treeEntries -= v.Size
		v.Tree = tree
	}
	return v
}

// qstructResult draws the graph when the request still has room for it.
func qstructResult(stmt ast.Statement, d *dialect.Dialect, opts analyze.Options, n int, left *room) (*qstructView, string) {
	switch stmt.(type) {
	case *ast.Query, *ast.CreateView:
	default:
		return nil, "Query structure covers SELECT and CREATE VIEW statements, so this one is skipped."
	}
	g, err := analyze.QStruct(stmt, d, opts)
	if err != nil {
		return nil, err.Error()
	}
	nodes, edges := 0, 0
	for _, node := range g.Nodes() {
		nodes++
		edges += len(node.Children)
	}
	if nodes+edges > left.graphItems {
		return nil, fmt.Sprintf("The graph has %d nodes and %d arrows, too many to show: the page draws up to %d per run. "+
			"The sqlp command prints a graph of any size (sqlp -qstruct).", nodes, edges, maxGraphItems)
	}
	left.graphItems -= nodes + edges
	v := &qstructView{Drawing: layout(g, fmt.Sprintf("arrow-%d", n))}
	for _, o := range g.Objects() {
		row := objectRow{Name: o.Name.String(), Kind: strings.ReplaceAll(string(o.Kind), "_", " ")}
		switch o.Status {
		case analyze.Resolved:
			row.Status = "in catalog"
		case analyze.NotFound:
			row.Status = "not in catalog"
		case analyze.Unchecked:
			row.Status = "unchecked"
		}
		v.Objects = append(v.Objects, row)
	}
	v.Diagnostics = diagnostics(g.Diagnostics)
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return nil, err.Error()
	}
	v.JSON = string(data)
	return v, ""
}

// diagnostics shows at most maxDiagnostics, each cut to maxMessage.
func diagnostics(ds []analyze.Diagnostic) []string {
	var out []string
	for i, d := range ds {
		if i == maxDiagnostics {
			out = append(out, fmt.Sprintf("… and %d more", len(ds)-i))
			break
		}
		out = append(out, firstRunes(d.Message, maxMessage))
	}
	return out
}

// outColsResult lists the columns when the request still has room for them.
func outColsResult(stmt ast.Statement, d *dialect.Dialect, opts analyze.Options, left *room) (*outColsView, string) {
	switch stmt.(type) {
	case *ast.Query, *ast.CreateView:
	default:
		return nil, "Columns used covers SELECT and CREATE VIEW statements, so this one is skipped."
	}
	c, err := analyze.ListOutCols(stmt, d, opts)
	if err != nil {
		return nil, err.Error()
	}
	if len(c.Columns) > left.columnRows {
		return nil, fmt.Sprintf("The query uses %d columns, too many to show: the page lists up to %d per run. "+
			"The sqlp command lists any number (sqlp -outcols).", len(c.Columns), maxColumnRows)
	}
	left.columnRows -= len(c.Columns)
	v := &outColsView{Expanded: opts.ExpandViews && opts.Catalog != nil, Diagnostics: diagnostics(c.Diagnostics)}
	for _, col := range c.Columns {
		row := columnRow{Object: col.Object.String(), Column: col.Column, Star: col.Star}
		switch col.Status {
		case analyze.Resolved:
			row.Status = "in catalog"
		case analyze.NotFound:
			row.Status, row.Warn = "not in catalog", true
		case analyze.Unchecked:
			row.Status = "unchecked"
		case analyze.Ambiguous:
			row.Status, row.Warn = "ambiguous", true
		}
		for _, cl := range col.Clauses {
			row.Clauses = append(row.Clauses, string(cl))
		}
		v.Columns = append(v.Columns, row)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err.Error()
	}
	v.JSON = string(data)
	return v, ""
}

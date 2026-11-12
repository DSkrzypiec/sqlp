package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newRecordingServer is a test server whose runs go to a fresh database. The
// store is asynchronous: call closeStore before reading the database.
func newRecordingServer(t *testing.T) (ts *httptest.Server, s *server, path string) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := newServer(log)
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "runs.db")
	if s.store, err = openStore(path, 64<<20, log); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.store.close() })
	ts = httptest.NewServer(s.routes())
	t.Cleanup(ts.Close)
	return ts, s, path
}

// closeStore writes every queued run and closes the store.
func closeStore(t *testing.T, s *server) {
	t.Helper()
	if err := s.store.close(); err != nil {
		t.Fatal(err)
	}
}

func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRecordRuns(t *testing.T) {
	ts, s, path := newRecordingServer(t)
	if _, body := get(t, ts, "/"); !strings.Contains(body, "Each run is stored with your IP address") {
		t.Error("the page does not say runs are stored")
	}
	catalog := `{"objects": [{"name": "t", "columns": [{"name": "a"}, {"name": "b"}]}]}`
	runs := []url.Values{
		{"sql": {"SELECT a FROM t"}, "dialect": {"postgres"}, "action": {"qstruct"},
			"catalog": {catalog}, "use_catalog": {"on"}},
		{"sql": {"SELECT a, b FROM t;\nSELECT FROM WHERE;"}, "dialect": {"sqlite"}, "action": {"outcols"},
			"catalog": {catalog}, "use_catalog": {"on"}, "expand": {"on"}},
		{"sql": {"select 1"}, "dialect": {"oracle"}, "action": {"qstruct"}},
		{"sql": {"select a from t where b=1"}, "dialect": {"postgres"}, "action": {"ast"},
			"catalog": {catalog}, "use_catalog": {"on"}},
	}
	for _, form := range runs {
		run(t, ts, form)
	}
	closeStore(t, s)

	db := openDB(t, path)
	type request struct {
		IP, Dialect, Action, Error string
		UseCatalog, Expand         bool
		HasCatalog                 bool
		Statements, SyntaxErrors   int
	}
	var got []request
	rows, err := db.Query(`SELECT ip, dialect, action, coalesce(error, ''), use_catalog, expand_views,
		catalog_sha256 IS NOT NULL, statements, syntax_errors FROM requests ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var r request
		if err := rows.Scan(&r.IP, &r.Dialect, &r.Action, &r.Error, &r.UseCatalog, &r.Expand,
			&r.HasCatalog, &r.Statements, &r.SyntaxErrors); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []request{
		{IP: "127.0.0.1", Dialect: "postgres", Action: "qstruct", UseCatalog: true, HasCatalog: true, Statements: 1},
		{IP: "127.0.0.1", Dialect: "sqlite", Action: "outcols", UseCatalog: true, Expand: true, HasCatalog: true,
			Statements: 2, SyntaxErrors: 1},
		{IP: "127.0.0.1", Dialect: "oracle", Action: "qstruct", Error: "Unknown dialect."},
		// The syntax tree takes no catalog, so none is recorded.
		{IP: "127.0.0.1", Dialect: "postgres", Action: "ast", Statements: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("%d requests recorded, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d:\n got %+v\nwant %+v", i+1, got[i], want[i])
		}
	}

	var catalogs int
	if err := db.QueryRow(`SELECT count(*) FROM catalogs`).Scan(&catalogs); err != nil {
		t.Fatal(err)
	}
	if catalogs != 1 {
		t.Errorf("%d catalogs stored, want the one catalog once", catalogs)
	}

	type statement struct {
		Request, Index, Line          int
		Text, SyntaxError, ResultHead string
	}
	var stmts []statement
	rows, err = db.Query(`SELECT request_id, idx, line, text, coalesce(syntax_error, ''),
		substr(coalesce(result, ''), 1, 30) FROM statements ORDER BY request_id, idx`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var st statement
		if err := rows.Scan(&st.Request, &st.Index, &st.Line, &st.Text, &st.SyntaxError, &st.ResultHead); err != nil {
			t.Fatal(err)
		}
		stmts = append(stmts, st)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 4 {
		t.Fatalf("%d statements recorded, want 4: %+v", len(stmts), stmts)
	}
	if st := stmts[0]; st.Request != 1 || !strings.HasPrefix(st.ResultHead, "{") {
		t.Errorf("the query structure is not stored as JSON: %+v", st)
	}
	if st := stmts[1]; st.Request != 2 || st.Index != 1 || st.SyntaxError != "" || !strings.HasPrefix(st.ResultHead, "{") {
		t.Errorf("the columns used are not stored as JSON: %+v", st)
	}
	if st := stmts[2]; st.Request != 2 || st.Index != 2 || st.Line != 2 ||
		!strings.HasPrefix(st.SyntaxError, "Syntax error at line 2") || st.ResultHead != "" {
		t.Errorf("the syntax error is not stored: %+v", st)
	}
	if st := stmts[3]; st.Request != 4 || st.Text != "select a from t where b=1" || !strings.HasPrefix(st.ResultHead, "SELECT a FROM t WHERE b = 1") {
		t.Errorf("the syntax tree run does not store the formatted SQL: %+v", st)
	}

	// Times sort as text: fixed width, UTC.
	var at string
	if err := db.QueryRow(`SELECT at FROM requests WHERE id = 1`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(timeFormat, at); err != nil || len(at) != len("2026-11-11T10:02:15.286Z") {
		t.Errorf("at = %q, want the fixed-width UTC form", at)
	}
}

func TestPageWithoutStore(t *testing.T) {
	ts := newTestServer(t)
	if _, body := get(t, ts, "/"); strings.Contains(body, "Each run is stored") {
		t.Error("the page says runs are stored, but the server has no store")
	}
}

func TestClientIP(t *testing.T) {
	for _, tc := range []struct {
		remote, realIP, want string
	}{
		{"127.0.0.1:41000", "203.0.113.7", "203.0.113.7"}, // from nginx
		{"[::1]:41000", "2001:db8::1", "2001:db8::1"},
		{"127.0.0.1:41000", "", "127.0.0.1"},
		{"198.51.100.2:443", "203.0.113.7", "198.51.100.2"}, // anyone else could set the header
		{"198.51.100.2:443", "", "198.51.100.2"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.remote
		if tc.realIP != "" {
			r.Header.Set("X-Real-IP", tc.realIP)
		}
		if got := clientIP(r); got != tc.want {
			t.Errorf("clientIP(%s, X-Real-IP %q) = %q, want %q", tc.remote, tc.realIP, got, tc.want)
		}
	}
}

func TestRecordClientIP(t *testing.T) {
	ts, s, path := newRecordingServer(t)
	req, err := http.NewRequest("POST", ts.URL+"/run",
		strings.NewReader(url.Values{"sql": {"select 1"}, "dialect": {"sqlite"}, "action": {"ast"}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Real-IP", "203.0.113.7")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	closeStore(t, s)
	var ip string
	if err := openDB(t, path).QueryRow(`SELECT ip FROM requests`).Scan(&ip); err != nil {
		t.Fatal(err)
	}
	if ip != "203.0.113.7" {
		t.Errorf("ip = %q, want the address nginx passed on", ip)
	}
}

// A crash in a run is recorded, and net/http still sees the panic.
func TestRecordPanic(t *testing.T) {
	_, s, path := newRecordingServer(t)
	s.tmpl = nil // the first render panics
	ts := httptest.NewUnstartedServer(s.routes())
	ts.Config.ErrorLog = log.New(io.Discard, "", 0)
	ts.Start()
	defer ts.Close()
	if resp, err := http.PostForm(ts.URL+"/run", url.Values{"dialect": {"oracle"}}); err == nil {
		resp.Body.Close()
		t.Fatal("the request succeeded, want the connection dropped")
	}
	closeStore(t, s)
	var msg string
	if err := openDB(t, path).QueryRow(`SELECT error FROM requests`).Scan(&msg); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(msg, "panic: ") || !strings.Contains(msg, "handleRun") {
		t.Errorf("error = %.200q, want the panic and its stack", msg)
	}
}

// Past its size limit the database stops growing, and runs are dropped with
// a warning instead.
func TestStoreSizeLimit(t *testing.T) {
	var logged bytes.Buffer
	var mu sync.Mutex
	log := slog.New(slog.NewTextHandler(lockedWriter{&mu, &logged}, nil))
	path := filepath.Join(t.TempDir(), "runs.db")
	const limit = 512 << 10
	st, err := openStore(path, limit, log)
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 100<<10)
	for range 20 {
		st.add(&record{At: time.Now(), SQL: big})
		time.Sleep(time.Millisecond) // stay under the queue's size
	}
	if err := st.close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > limit {
		t.Errorf("the database is %d bytes, over its %d byte limit", info.Size(), limit)
	}
	var n int
	if err := openDB(t, path).QueryRow(`SELECT count(*) FROM requests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 || n >= 20 {
		t.Errorf("%d of 20 runs stored, want some but not all", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(logged.String(), "run not recorded") || !strings.Contains(logged.String(), "full") {
		t.Errorf("no warning that the database is full; the log has:\n%s", logged.String())
	}
}

func TestStoreAfterClose(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "runs.db"), 1<<20, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.close(); err != nil {
		t.Fatal(err)
	}
	st.add(&record{At: time.Now()}) // a request that outlived the shutdown: dropped, not a panic
	if err := st.close(); err != nil {
		t.Errorf("a second close: %v", err)
	}
}

func TestOpenStoreBadPath(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, path := range []string{"", "runs.db?mode=ro", filepath.Join(t.TempDir(), "missing", "runs.db")} {
		if st, err := openStore(path, 1<<20, log); err == nil {
			st.close()
			t.Errorf("openStore(%q) succeeded, want an error", path)
		}
	}
}

// fill writes runs of about 10 KB until the database is more than 90% full,
// and returns how many it wrote. One run in three has a catalog of its own;
// another in three shares one catalog with the others like it.
func fill(t *testing.T, st *store) int64 {
	t.Helper()
	text := strings.Repeat("x", 10<<10)
	for i := int64(1); i < 10000; i++ {
		r := &record{At: time.Now(), SQL: text, Stmts: []stmtRecord{{Index: 1, Line: 1, Text: "SELECT 1"}}}
		switch i % 3 {
		case 0:
			r.Catalog = fmt.Sprintf(`{"objects": [], "run": %d}`, i)
		case 1:
			r.Catalog = `{"objects": []}`
		}
		if err := st.write(r); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		used, err := st.usedBytes()
		if err != nil {
			t.Fatal(err)
		}
		if used > st.limit/10*9 {
			return i
		}
	}
	t.Fatal("the database never filled up")
	return 0
}

func scan(t *testing.T, db *sql.DB, query string, dest ...any) {
	t.Helper()
	if err := db.QueryRow(query).Scan(dest...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func TestPrune(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "runs.db"), 1<<20, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	written := fill(t, st)
	above, to := st.limit/10*9, st.limit/10*8

	deleted, before, after, err := st.prune(above, to)
	if err != nil {
		t.Fatal(err)
	}
	if deleted == 0 || before <= above || after > to {
		t.Fatalf("prune deleted %d runs, %d bytes used before and %d after; want some deleted, down to %d",
			deleted, before, after, to)
	}
	// The oldest runs went; the newest stayed.
	var n, first, last int64
	scan(t, st.db, `SELECT count(*), min(id), max(id) FROM requests`, &n, &first, &last)
	if n != written-deleted || first != deleted+1 || last != written {
		t.Errorf("runs %d to %d left (%d), want %d to %d", first, last, n, deleted+1, written)
	}
	// With them went their statements and the catalogs only they used.
	var orphans, catalogs, inUse int
	scan(t, st.db, `SELECT count(*) FROM statements WHERE request_id NOT IN (SELECT id FROM requests)`, &orphans)
	scan(t, st.db, `SELECT count(*) FROM catalogs`, &catalogs)
	scan(t, st.db, `SELECT count(DISTINCT catalog_sha256) FROM requests`, &inUse)
	if orphans != 0 || catalogs != inUse {
		t.Errorf("%d statements of deleted runs left; %d catalogs stored, %d in use", orphans, catalogs, inUse)
	}

	// Below the threshold there is nothing to do.
	if deleted, _, _, err := st.prune(above, to); deleted != 0 || err != nil {
		t.Errorf("a second prune deleted %d runs (err %v), want none", deleted, err)
	}
	// New runs reuse the freed pages: the file does not grow.
	var pagesBefore, pagesAfter int64
	scan(t, st.db, `PRAGMA page_count`, &pagesBefore)
	for range 5 {
		if err := st.write(&record{At: time.Now(), SQL: strings.Repeat("y", 10<<10)}); err != nil {
			t.Fatal(err)
		}
	}
	scan(t, st.db, `PRAGMA page_count`, &pagesAfter)
	if pagesAfter != pagesBefore {
		t.Errorf("the file grew from %d to %d pages, want the freed ones reused", pagesBefore, pagesAfter)
	}
}

// The writer prunes on its own, every pruneEvery, and logs it.
func TestPruneEvery(t *testing.T) {
	defer func(d time.Duration) { pruneEvery = d }(pruneEvery)
	pruneEvery = 20 * time.Millisecond
	var logged bytes.Buffer
	var mu sync.Mutex
	log := slog.New(slog.NewTextHandler(lockedWriter{&mu, &logged}, nil))
	st, err := openStore(filepath.Join(t.TempDir(), "runs.db"), 1<<20, log)
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	fill(t, st)
	// The prune is logged after its last delete, so wait for both.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		used, err := st.usedBytes()
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		pruned := strings.Contains(logged.String(), "pruned the run log")
		mu.Unlock()
		if pruned && used <= st.limit/10*8 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("5 seconds after the database filled up, %d of %d bytes are used; a prune logged: %v",
				used, st.limit, pruned)
		}
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

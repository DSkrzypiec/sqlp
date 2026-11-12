package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure Go: no cgo
)

// The store keeps every run of the page in a SQLite database, as material
// for finding mistakes in sqlp: valid SQL the parser rejects, wrong analysis
// results, misleading diagnostics. Query it with the sqlite3 shell;
// deploy/README.md has examples.
//
// Writes happen on one goroutine, so a request never waits for the disk.
// When the writer falls behind, or the database reaches its size limit, runs
// are dropped and the drop is logged. The same goroutine deletes the oldest
// runs when the database is nearly full: see prune.

const schema = `
-- Catalogs, stored once each.
CREATE TABLE IF NOT EXISTS catalogs (
  sha256 TEXT PRIMARY KEY,
  text   TEXT NOT NULL
);

-- One row per run of the page.
CREATE TABLE IF NOT EXISTS requests (
  id             INTEGER PRIMARY KEY,
  at             TEXT NOT NULL,    -- when it arrived, UTC: 2026-11-11T10:02:15.286Z
  ip             TEXT NOT NULL,
  build          TEXT NOT NULL,    -- the commit that served it, as on /healthz
  dialect        TEXT NOT NULL,    -- as sent, even when unknown
  action         TEXT NOT NULL,    -- qstruct, outcols or ast, as sent
  use_catalog    INTEGER NOT NULL, -- 1 when a catalog was given and used
  expand_views   INTEGER NOT NULL,
  catalog_sha256 TEXT REFERENCES catalogs(sha256),
  sql            TEXT NOT NULL,
  statements     INTEGER NOT NULL, -- in the script, including any past those shown
  syntax_errors  INTEGER NOT NULL, -- statements shown with a syntax error
  error          TEXT,             -- the run failed as a whole: bad catalog, too long, busy, ...
  notes          TEXT,             -- JSON array of the notes shown, such as a run stopped for time
  duration_ms    REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS requests_at ON requests(at);
-- Deleting a catalog looks for runs that still use it.
CREATE INDEX IF NOT EXISTS requests_catalog ON requests(catalog_sha256);

-- One row per statement shown.
CREATE TABLE IF NOT EXISTS statements (
  request_id   INTEGER NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
  idx          INTEGER NOT NULL, -- from 1, as on the page
  line         INTEGER NOT NULL, -- where it starts in the request's sql
  text         TEXT NOT NULL,
  syntax_error TEXT,             -- the message shown, with line and column
  message      TEXT,             -- why no result is shown, such as a statement the action does not cover
  diagnostics  TEXT,             -- JSON array of the analysis diagnostics shown
  result       TEXT,             -- the analysis as JSON, or for the syntax tree the formatted SQL
  PRIMARY KEY (request_id, idx)
);
`

// record is one run of the page.
type record struct {
	At         time.Time
	IP         string
	Build      string
	Dialect    string
	Action     string
	UseCatalog bool
	Expand     bool
	Catalog    string // the catalog's text, when used
	SQL        string
	Statements int
	Error      string
	Notes      []string
	Duration   time.Duration
	Stmts      []stmtRecord
}

// stmtRecord is one statement of a run, with what the page showed for it.
type stmtRecord struct {
	Index, Line int
	Text        string
	SyntaxError string
	Message     string
	Diagnostics []string
	Result      string
}

type store struct {
	db    *sql.DB
	limit int64 // bytes; the database never grows past it
	queue chan *record
	done  chan struct{}
	log   *slog.Logger

	// closing guards closed and the queue: a request that outlives the
	// server's shutdown must not send on the closed queue.
	closing sync.RWMutex
	closed  bool

	mu       sync.Mutex
	lastWarn time.Time
	missed   int // warnings not logged since lastWarn
}

const (
	queueSize  = 64          // runs waiting to be written
	pageSize   = 4096        // SQLite's default, which max_page_count counts in
	warnEvery  = time.Minute // at most one warning a minute in the log
	timeFormat = "2006-01-02T15:04:05.000Z"
)

// pruneEvery is how often the writer checks whether the database is nearly
// full. A variable, so that tests can shorten it.
var pruneEvery = 5 * time.Minute

// openStore opens or creates the database at path. maxBytes bounds its size:
// past it, writes fail and runs are dropped, so a flood of large requests
// cannot fill the disk. Well before that, the oldest runs are deleted.
func openStore(path string, maxBytes int64, log *slog.Logger) (*store, error) {
	if path == "" || strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("store: unusable database path %q", path)
	}
	pages := max(maxBytes/pageSize, 64)
	pragmas := []string{"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(NORMAL)", "foreign_keys(ON)",
		fmt.Sprintf("max_page_count(%d)", pages)}
	dsn := "file:" + path + "?_pragma=" + strings.Join(pragmas, "&_pragma=")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	db.SetMaxOpenConns(1) // one writer; the sqlite3 shell can read alongside, thanks to WAL
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: %s: %w", path, err)
	}
	s := &store{db: db, limit: pages * pageSize, queue: make(chan *record, queueSize), done: make(chan struct{}), log: log}
	go s.run(pruneEvery)
	return s, nil
}

// add queues r to be written. It never waits.
func (s *store) add(r *record) {
	s.closing.RLock()
	defer s.closing.RUnlock()
	if s.closed {
		s.warn("run not recorded: the store is closed")
		return
	}
	select {
	case s.queue <- r:
	default:
		s.warn("run not recorded: the writer is behind")
	}
}

// close writes the queued runs and closes the database. Runs added after it
// are dropped.
func (s *store) close() error {
	s.closing.Lock()
	if s.closed {
		s.closing.Unlock()
		return nil
	}
	s.closed = true
	close(s.queue)
	s.closing.Unlock()
	<-s.done
	return s.db.Close()
}

// run writes the queued runs until the queue is closed, and prunes the
// database at the start and every pruneEvery. Doing both on one goroutine
// means pruning never competes with a write: the runs that arrive meanwhile
// wait in the queue.
func (s *store) run(every time.Duration) {
	defer close(s.done)
	s.pruneIfFull()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case r, ok := <-s.queue:
			if !ok {
				return
			}
			if err := s.write(r); err != nil {
				s.warn("run not recorded", "err", err)
			}
		case <-tick.C:
			s.pruneIfFull()
		}
	}
}

// pruneIfFull deletes the oldest runs once the database is 90% full, down to
// 80%. The rest of the limit is room for the runs until the next check.
func (s *store) pruneIfFull() {
	deleted, before, after, err := s.prune(s.limit/10*9, s.limit/10*8)
	if err != nil {
		s.log.Error("pruning the run log", "err", err, "runs_deleted", deleted)
		return
	}
	if deleted > 0 {
		s.log.Info("pruned the run log", "runs_deleted", deleted, "mb_before", before>>20, "mb_after", after>>20)
	}
}

// prune deletes the oldest runs, when the database holds more than above
// bytes, until it holds no more than to. It returns how many runs it deleted
// and the bytes in use before and after. SQLite reuses the freed pages, so
// the file keeps its size but stops growing.
func (s *store) prune(above, to int64) (deleted, before, after int64, err error) {
	used, err := s.usedBytes()
	if err != nil || used <= above {
		return 0, used, used, err
	}
	before = used
	for used > to {
		var runs int64
		if err := s.db.QueryRow(`SELECT count(*) FROM requests`).Scan(&runs); err != nil {
			return deleted, before, used, err
		}
		if runs == 0 {
			break // what is left is the schema and, perhaps, catalogs no run uses
		}
		// As many runs as the excess holds, at the average size of a run.
		n := int64(float64(used-to)/float64(used)*float64(runs)) + 1
		gone, err := s.deleteOldest(n)
		deleted += gone
		if err != nil {
			return deleted, before, used, err
		}
		if used, err = s.usedBytes(); err != nil {
			return deleted, before, used, err
		}
	}
	return deleted, before, used, nil
}

// deleteOldest deletes the n oldest runs, their statements and the catalogs
// no run uses any more.
func (s *store) deleteOldest(n int64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM requests WHERE id IN (SELECT id FROM requests ORDER BY id LIMIT ?)`, n)
	if err != nil {
		return 0, err
	}
	gone, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM catalogs WHERE sha256 NOT IN
		(SELECT catalog_sha256 FROM requests WHERE catalog_sha256 IS NOT NULL)`); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return gone, nil
}

// usedBytes is the size of the pages in use: the file's size less its free
// pages.
func (s *store) usedBytes() (int64, error) {
	var pages, free, size int64
	err := s.db.QueryRow(`SELECT page_count, freelist_count, page_size
		FROM pragma_page_count(), pragma_freelist_count(), pragma_page_size()`).Scan(&pages, &free, &size)
	return (pages - free) * size, err
}

// warn logs at most once every warnEvery, counting what it leaves out.
func (s *store) warn(msg string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.lastWarn) < warnEvery {
		s.missed++
		return
	}
	s.log.Warn(msg, append(args, "warnings_skipped", s.missed)...)
	s.lastWarn, s.missed = time.Now(), 0
}

func (s *store) write(r *record) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var catalog any
	if r.Catalog != "" {
		sum := sha256.Sum256([]byte(r.Catalog))
		h := hex.EncodeToString(sum[:])
		if _, err := tx.Exec(`INSERT OR IGNORE INTO catalogs (sha256, text) VALUES (?, ?)`, h, r.Catalog); err != nil {
			return err
		}
		catalog = h
	}
	syntaxErrors := 0
	for _, st := range r.Stmts {
		if st.SyntaxError != "" {
			syntaxErrors++
		}
	}
	res, err := tx.Exec(`INSERT INTO requests (at, ip, build, dialect, action, use_catalog, expand_views,
		catalog_sha256, sql, statements, syntax_errors, error, notes, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.At.UTC().Format(timeFormat), r.IP, r.Build, r.Dialect, r.Action, r.UseCatalog, r.Expand,
		catalog, r.SQL, r.Statements, syntaxErrors, nullable(r.Error), jsonList(r.Notes),
		float64(r.Duration.Microseconds())/1000)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	for _, st := range r.Stmts {
		if _, err := tx.Exec(`INSERT INTO statements (request_id, idx, line, text, syntax_error, message, diagnostics, result)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, st.Index, st.Line, st.Text, nullable(st.SyntaxError), nullable(st.Message), jsonList(st.Diagnostics),
			nullable(st.Result)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// nullable stores an empty string as NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// jsonList stores a list as a JSON array, and an empty one as NULL.
func jsonList(items []string) any {
	if len(items) == 0 {
		return nil
	}
	data, _ := json.Marshal(items)
	return string(data)
}

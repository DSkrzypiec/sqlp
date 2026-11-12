// Command corpusgen freezes engine verdicts into testdata/ for plain go test.
// It is a separate module because pg_query_go needs cgo. Nothing is executed
// on PostgreSQL: pg_query is its parser as a library. SQLite statements run in
// sqlite3 -safe, in a scratch directory, because the corpus is untrusted.
//
//	cd tools/corpusgen && go run . -root ../.. -pg DIR -sqlite DIR -sqlite-bin PATH
//	cd tools/corpusgen && go run . -bigquery
//	cd tools/corpusgen && go run . -bq-probe FILE
//
// -pg is src/test/regress/sql of PostgreSQL 16; -sqlite is test/ of SQLite 3.51.
// -bigquery asks BigQuery itself through gcloud's login (see bigquery.go), and
// -bq-probe prints BigQuery's verdict on each statement of FILE.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	pg "github.com/pganalyze/pg_query_go/v5"

	"sqlp/dialect"
	"sqlp/internal/corpus"
)

const (
	exprCount             = 2000
	mutationsPerStatement = 4
)

type engine struct {
	d       *dialect.Dialect
	rejects func(string) bool
	// sameTree and sameQuery report whether the engine groups both statements
	// alike, for constant expressions and for queries on the sample tables.
	sameTree, sameQuery func(sql, shape string) bool
	report              *stats
	// write is false on a pass that only collects the engine's answers.
	write bool
	// checkable, when set, says whether the engine can compare sql with its
	// shape at all; the others are left without a shape and not counted as
	// mismatches.
	checkable func(sql string) bool
	// ansi freezes the statements of corpus.ANSI as ansi.jsonl, for an engine
	// whose verdicts the table's labels do not hold.
	ansi bool
}

type stats struct {
	statements, rejected, opaque, shaped, unshaped, accepts, rejects int
	samples                                                          []string
}

func main() {
	root := flag.String("root", "../..", "repository root")
	pgDir := flag.String("pg", "", "PostgreSQL src/test/regress/sql")
	sqliteDir := flag.String("sqlite", "", "SQLite test/")
	sqliteBin := flag.String("sqlite-bin", "", "sqlite3 3.51 built with SQLITE_ENABLE_UPDATE_DELETE_LIMIT")
	ansi := flag.Bool("ansi", false, "check the ANSI table's dialect lists against the engines")
	bq := flag.Bool("bigquery", false, "freeze BigQuery verdicts (needs gcloud logged in)")
	bqProbe := flag.String("bq-probe", "", "print BigQuery's verdict on each statement of this file")
	home, _ := os.UserHomeDir()
	bqCache := flag.String("bq-cache", filepath.Join(home, ".cache", "sqlp", "bigquery.jsonl"), "BigQuery answers already received")
	flag.IntVar(&workers, "workers", workers, "statements checked at a time")
	flag.Parse()

	if *bqProbe != "" {
		newBigQuery(*bqCache).probe(*bqProbe)
		return
	}
	if *bq {
		freezeBigQuery(*root, newBigQuery(*bqCache))
		return
	}

	if *ansi {
		checkANSI(*sqliteBin)
		return
	}

	if *pgDir != "" {
		e := engine{d: dialect.Postgres, rejects: pgRejects, sameTree: pgSameTree, sameQuery: pgSameTree, report: &stats{}, write: true}
		valid := func(s string) bool { return !pgRejects(s) }
		extract := func(script string) []string { return corpus.PostgresStatements(script, valid) }
		e.freeze(*root, *pgDir, "*.sql", extract, pgSameTree)
	}
	if *sqliteBin != "" {
		e := engine{d: dialect.SQLite, rejects: sqliteRejects(*sqliteBin), sameTree: sqliteSameTree(*sqliteBin), sameQuery: sqliteSameQuery(*sqliteBin), report: &stats{}, write: true}
		e.freeze(*root, *sqliteDir, "*.test", corpus.SQLiteStatements, nil)
	}
}

func (e engine) freeze(root, dir, glob string, extract func(string) []string, suiteTree func(sql, shape string) bool) {
	out := filepath.Join(root, "testdata", "corpus", e.d.Name)
	for _, dir := range []string{out, filepath.Join(root, "testdata", "known")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatal(err)
		}
	}
	var known []corpus.Known
	var files []string
	if dir != "" {
		var err error
		if files, err = filepath.Glob(filepath.Join(dir, glob)); err != nil {
			log.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			log.Fatal(err)
		}
		var list []string
		for _, s := range extract(string(raw)) {
			if usable(e.d, s) && !seen[s] {
				seen[s] = true
				list = append(list, s)
			}
		}
		name := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		known = append(known, e.writeFile(filepath.Join(out, name+".jsonl"), list, suiteTree)...)
	}
	known = append(known, e.writeFile(filepath.Join(out, "generated_exprs.jsonl"), corpus.Exprs(e.d.Name, exprCount), e.sameTree)...)
	known = append(known, e.freezeCurated(root, out)...)
	if e.ansi {
		var list []string
		for _, c := range corpus.ANSI {
			if !slices.Contains(list, c.SQL) {
				list = append(list, c.SQL)
			}
		}
		known = append(known, e.writeFile(filepath.Join(out, "ansi.jsonl"), list, e.sameQuery)...)
	}
	if !e.write {
		return
	}
	if err := corpus.Write(filepath.Join(root, "testdata", "known", e.d.Name+".jsonl"), known); err != nil {
		log.Fatal(err)
	}
	r := e.report
	fmt.Printf("%s: %d statements; engine rejects %d; opaque %d; tree-verified %d, tree mismatch %d; accepts-invalid %d, rejects-valid %d\n",
		e.d.Name, r.statements, r.rejected, r.opaque, r.shaped, r.unshaped, r.accepts, r.rejects)
	for _, s := range r.samples {
		fmt.Println(s)
	}
}

// checkANSI prints each table entry whose dialect list disagrees with the
// engines, as the label it should have, and fails if there are any.
func checkANSI(sqliteBin string) {
	if sqliteBin == "" {
		log.Fatal("-ansi needs -sqlite-bin")
	}
	sqls := make([]string, len(corpus.ANSI))
	for i, c := range corpus.ANSI {
		sqls[i] = c.SQL
	}
	pgr := parallel(sqls, pgRejects)
	sqr := parallel(sqls, sqliteRejects(sqliteBin))
	wrong := 0
	for i, c := range corpus.ANSI {
		if c.Accepted("postgres") != !pgr[i] || c.Accepted("sqlite") != !sqr[i] {
			wrong++
			fmt.Printf("ANSI %s %q\n", corpus.DialectsLabel(!pgr[i], !sqr[i]), c.SQL)
		}
	}
	fmt.Printf("ansi: %d cases, %d wrong\n", len(corpus.ANSI), wrong)
	if wrong > 0 {
		os.Exit(1)
	}
}

func usable(d *dialect.Dialect, s string) bool {
	if s == "" || !utf8.ValidString(s) || strings.IndexByte(s, 0) >= 0 {
		return false
	}
	return d != dialect.SQLite || corpus.SQLiteUsable(s)
}

// freezeCurated freezes select/*.sql, one statement per line, plus mutations
// of each statement, as curated_<name>.jsonl. Lists for one dialect only sit
// in a directory named after it and are frozen as curated_<dialect>_<name>.jsonl,
// so adding one does not mean regenerating the other dialects.
func (e engine) freezeCurated(root, out string) []corpus.Known {
	files, err := filepath.Glob(filepath.Join(root, "tools", "corpusgen", "select", "*.sql"))
	if err != nil {
		log.Fatal(err)
	}
	own, err := filepath.Glob(filepath.Join(root, "tools", "corpusgen", e.d.Name, "*.sql"))
	if err != nil {
		log.Fatal(err)
	}
	files = append(files, own...)
	var known []corpus.Known
	seen := map[string]bool{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			log.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		var list []string
		for _, s := range append(lines, corpus.Mutations(lines, e.d.Lexer, mutationsPerStatement)...) {
			if usable(e.d, s) && !seen[s] {
				seen[s] = true
				list = append(list, s)
			}
		}
		name := "curated_" + strings.TrimSuffix(filepath.Base(f), ".sql") + ".jsonl"
		if filepath.Base(filepath.Dir(f)) == e.d.Name {
			name = "curated_" + e.d.Name + "_" + strings.TrimSuffix(filepath.Base(f), ".sql") + ".jsonl"
		}
		known = append(known, e.writeFile(filepath.Join(out, name), list, e.sameQuery)...)
	}
	return known
}

// writeFile freezes list; sameTree is nil when the engine exposes no way to
// verify grouping for these statements.
func (e engine) writeFile(path string, list []string, sameTree func(sql, shape string) bool) []corpus.Known {
	rejected := parallel(list, e.rejects)
	type verified struct {
		shape string
		ok    bool
	}
	shapes := make([]verified, len(list))
	for i, s := range list {
		if p := corpus.Parse(s, e.d); !rejected[i] && p.Err == nil && !p.Opaque {
			shapes[i].shape = corpus.Shape(p.Stmts, e.d)
		}
	}
	useTrees := sameTree != nil
	if useTrees {
		idx := make([]string, len(list))
		for i := range list {
			idx[i] = fmt.Sprint(i)
		}
		for n, same := range parallel(idx, func(k string) bool {
			var i int
			fmt.Sscan(k, &i)
			if shapes[i].shape != "" && e.checkable != nil && !e.checkable(list[i]) {
				shapes[i].shape = ""
			}
			return shapes[i].shape != "" && sameTree(list[i], shapes[i].shape)
		}) {
			shapes[n].ok = same
		}
	}
	r := e.report
	var rows []corpus.Record
	var known []corpus.Known
	for i, s := range list {
		p := corpus.Parse(s, e.d)
		rec := corpus.Record{SQL: s, Reject: rejected[i]}
		r.statements++
		if rejected[i] {
			r.rejected++
		}
		switch {
		case p.Opaque:
			r.opaque++
		case rejected[i] && p.Err == nil:
			r.accepts++
			known = append(known, corpus.Known{SQL: s, Kind: corpus.AcceptsInvalid})
		case !rejected[i] && p.Err != nil:
			r.rejects++
			known = append(known, corpus.Known{SQL: s, Kind: corpus.RejectsValid})
		case useTrees && shapes[i].ok:
			rec.Shape = shapes[i].shape
			r.shaped++
		case useTrees && shapes[i].shape != "":
			r.unshaped++
			if len(r.samples) < 60 {
				r.samples = append(r.samples, fmt.Sprintf("MISMATCH %s\n     --> %s", oneLine(s), oneLine(shapes[i].shape)))
			}
		}
		rows = append(rows, rec)
	}
	if len(rows) > 0 && e.write {
		if err := corpus.Write(path, rows); err != nil {
			log.Fatal(err)
		}
	}
	return known
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}

// workers is how many statements parallel checks at a time.
var workers = 8

func parallel(list []string, f func(string) bool) []bool {
	out := make([]bool, len(list))
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				out[i] = f(list[i])
			}
		}()
	}
	for i := range list {
		next <- i
	}
	close(next)
	wg.Wait()
	return out
}

func pgRejects(sql string) bool {
	_, err := pg.Parse(sql)
	return err != nil
}

// pgTree is the parse tree without source positions.
func pgTree(sql string) (string, bool) {
	js, err := pg.ParseToJSON(sql)
	if err != nil {
		return "", false
	}
	var v any
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		return "", false
	}
	strip(v)
	b, _ := json.Marshal(v)
	return string(b), true
}

func strip(v any) {
	switch v := v.(type) {
	case map[string]any:
		delete(v, "location")
		delete(v, "stmt_location")
		delete(v, "stmt_len")
		delete(v, "stmtLocation")
		delete(v, "stmtLen")
		for _, c := range v {
			strip(c)
		}
	case []any:
		for _, c := range v {
			strip(c)
		}
	}
}

func pgSameTree(sql, shape string) bool {
	a, ok1 := pgTree(sql)
	b, ok2 := pgTree(shape)
	return ok1 && ok2 && a == b
}

const sqliteSchema = "CREATE TABLE t(a,b,c,d,e,f,g,h,id,x,y,name,s,j,v,rowid2);" +
	"CREATE TABLE t1(a,b,c,d,e);CREATE TABLE t3(a,b,c);CREATE TABLE u(a,b,id,x,y);CREATE TABLE a(id,kind,x,y);CREATE TABLE b(id,kind,x,y);" +
	"CREATE TABLE c(id);CREATE TABLE d(id);CREATE INDEX idx ON t(a);CREATE VIEW v AS SELECT 1 AS a;"

var sqliteSyntax = regexp.MustCompile(`syntax error|unrecognized token|incomplete input|join type|unknown table option|a JOIN clause is required|error in generated column|should come after`)

func runSQLite(bin, script string) string {
	dir, err := os.MkdirTemp("", "sqlp-corpus")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out capped
	cmd := exec.CommandContext(ctx, bin, "-safe", "-batch", "-list", "-noheader", ":memory:", script)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = &out, &out
	_ = cmd.Run()
	return out.String()
}

// capped keeps the first megabyte: a damaged recursive query can print forever.
type capped struct{ strings.Builder }

func (c *capped) Write(p []byte) (int, error) {
	if room := 1<<20 - c.Len(); room > 0 {
		c.Builder.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// sqliteRejects tries with and without tables: semantic actions run during
// parsing, so "no such table" or "already exists" can pre-empt a syntax error.
func sqliteRejects(bin string) func(string) bool {
	return func(sql string) bool {
		return sqliteSyntax.MatchString(runSQLite(bin, sql)) ||
			sqliteSyntax.MatchString(runSQLite(bin, sqliteSchema+sql))
	}
}

// sqliteSameTree compares values, because the CLI exposes no parse tree. It
// only works for the constant expressions from corpus.Exprs.
func sqliteSameTree(bin string) func(string, string) bool {
	return func(sql, shape string) bool {
		a := strings.TrimSuffix(strings.TrimPrefix(sql, "SELECT "), ";")
		b := strings.TrimSuffix(strings.TrimPrefix(shape, "SELECT "), ";")
		x := runSQLite(bin, "SELECT quote("+a+");")
		y := runSQLite(bin, "SELECT quote("+b+");")
		return x == y && !strings.Contains(x, "rror")
	}
}

const sqliteData = "INSERT INTO t(a,b,c,id,x,y,name) VALUES (1,2,3,1,10,20,'ann'),(2,2,NULL,2,20,10,'bob')," +
	"(3,1,5,3,30,30,'cy'),(NULL,4,6,4,NULL,5,'di');" +
	"INSERT INTO u(a,b,id,x,y) VALUES (1,1,1,10,5),(2,NULL,2,20,6),(2,3,3,NULL,7),(5,6,4,40,8);" +
	"INSERT INTO a(id,kind,x,y) VALUES (1,'x',10,1),(2,'y',20,2),(3,'x',30,3);" +
	"INSERT INTO b(id,kind,x,y) VALUES (1,'x',1,1),(3,'z',2,2),(4,'y',3,3);" +
	"INSERT INTO c(id) VALUES (1),(2);INSERT INTO d(id) VALUES (2),(3);"

// sqliteSameQuery runs both forms on sample rows and compares the results,
// ignoring row order, because the CLI exposes no parse tree.
func sqliteSameQuery(bin string) func(string, string) bool {
	run := func(sql string) string {
		lines := strings.Split(runSQLite(bin, sqliteSchema+sqliteData+strings.TrimSuffix(sql, ";")+";"), "\n")
		slices.Sort(lines)
		return strings.Join(lines, "\n")
	}
	return func(sql, shape string) bool {
		x := run(sql)
		return x == run(shape) && !strings.Contains(x, "rror")
	}
}

// freezeBigQuery asks BigQuery about every statement first, without writing.
// It writes nothing while some message is unclassified, since the verdict on
// those statements would be a guess.
func freezeBigQuery(root string, b *bigQuery) {
	b.setUp()
	e := engine{d: dialect.BigQuery, rejects: b.rejects, sameTree: b.sameValue, sameQuery: b.sameQuery, checkable: b.checkable, report: &stats{}, ansi: true}
	e.freeze(root, "", "", nil, nil)
	if b.reportUnknown() {
		os.Exit(1)
	}
	e.write, e.report = true, &stats{}
	e.freeze(root, "", "", nil, nil)
	note := "Verdicts in this directory are BigQuery's (GoogleSQL), from dry runs taken on " + b.dayRange() +
		",\nwritten by tools/corpusgen -bigquery. BigQuery has no versions; regenerate to\nfollow it, and review verdicts that change.\n"
	if err := os.WriteFile(filepath.Join(root, "testdata", "corpus", "bigquery", "ORACLE"), []byte(note), 0o644); err != nil {
		log.Fatal(err)
	}
}

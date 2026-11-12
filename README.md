# sqlp

A from-scratch, multi-dialect SQL parser and generator in Go, with no dependencies
outside the standard library.

| Dialect | Pinned version | Name for `-dialect` |
|---|---|---|
| PostgreSQL | 16 | `postgres` |
| SQLite | 3.51 | `sqlite` |

## Using it

```go
// All statements or an error, never both.
stmts, err := sqlp.Parse("SELECT a::int FROM t; SELECT 2", dialect.Postgres)
fmt.Println(sqlp.Format(stmts, dialect.Postgres))

// One statement, or one expression.
stmt, err := sqlp.ParseOne("SELECT 1", dialect.SQLite)
expr, err := sqlp.ParseExpr("a + 1", dialect.SQLite)

// Convert between dialects. Warnings name anything the target cannot express.
out, warnings, err := sqlp.Transpile(sql, dialect.Postgres, dialect.SQLite)
```

For scripts where you want a result per statement, use `ParseScript`. It keeps going
past syntax errors and tells you where each statement is:

```go
script, err := sqlp.ParseScript(ctx, sql, dialect.Postgres, sqlp.Options{})
for _, s := range script.Statements {
    // s.Node or s.Err, plus s.Text, s.Span and s.Opaque
}
```

The returned `err` covers only the whole script: the size limit and cancellation.
`TranspileScript` does the same for transpiling, with warnings per statement.

For untrusted input, use `ParseScript` or `ParseWithLimits` with a context and
`Limits`.

### Query structure

`analyze.QStruct` returns the objects a SELECT or CREATE VIEW depends on, as a
DAG of CTEs, subqueries, tables, views and table functions:

```go
g, err := analyze.QStruct(stmt, dialect.Postgres, analyze.Options{
    Catalog:     cat,  // optional *catalog.Catalog: resolves full names
    ExpandViews: true, // follow view definitions from the catalog
})
fmt.Print(g)         // one line per node: "x: cte -> shop.public.orders"
json.Marshal(g)      // {"root": "query", "nodes": [{"id": ..., "children": [...]}]}
```

Without a catalog, names are kept as written and marked `unchecked`. With one,
they resolve through the dialect's search path and case rules to full names
such as `shop.public.orders`, or are marked `not_found`. The `catalog` package
documentation shows how to load a catalog from PostgreSQL or SQLite.

### Columns used

`analyze.ListOutCols` lists the distinct table and view columns that a SELECT or
CREATE VIEW uses. Each column has the clauses that use it:

- `select`: the result is computed from it.
- `join`: an ON, USING or NATURAL condition matches on it.

Columns used only in WHERE, GROUP BY or ORDER BY are left out. CTEs, subqueries and
aliases are followed back to the tables, and with `ExpandViews` views are too:

```go
c, err := analyze.ListOutCols(stmt, dialect.Postgres, analyze.Options{Catalog: cat})
fmt.Print(c)    // one line per column: "shop.public.orders.id: select, join"
json.Marshal(c) // {"columns": [{"schema": "public", "object": "orders", "column": "id", "clauses": [...]}]}
```

With a catalog that lists columns, `*` becomes the object's columns. Without one,
it stays one `star` entry. A catalog also settles which table an unqualified
column belongs to. Without one, a column that several tables could have is
reported as `ambiguous`, with a diagnostic naming the candidates.

### Web UI

`cmd/sqlp_serv` is a small web page around the library: SQL and catalog editors, a
dialect and an action (query structure, columns used or syntax tree). It runs at
https://sqlp.dskrzypiec.dev; `deploy/README.md` covers deploying it, and
`deploy/redeploy.sh` rebuilds and restarts it after a change.

```sh
go run -C cmd/sqlp_serv . -addr 127.0.0.1:9393
```

It is a module of its own, because it records every run in a SQLite database
(`-db`) and the driver should not become a dependency of the library.

From the command line:

```sh
go run ./cmd/sqlp -dialect sqlite "SELECT a FROM t WHERE b > 1"
go run ./cmd/sqlp -qstruct -catalog catalog.json "SELECT * FROM orders"
go run ./cmd/sqlp -outcols -catalog catalog.json "SELECT * FROM orders"
```

## Running the tests

Normal test runs need no database, no server and no cgo.

```sh
go test ./...                          # everything, every dialect
go test ./... -dialect=postgres        # one dialect
go test ./... -dialect=sqlite,postgres # a list of dialects
go test ./... -short                   # skip the slower mutation test
```

A misspelled dialect fails the run. To run one part:

```sh
go test . -run 'TestCorpus/postgres'
go test . -run 'TestConformance/sqlite/common'
go test . -run TestANSI -v             # also logs a per-dialect feature summary
```

### What the tests cover

| Group | Test | What it checks |
|---|---|---|
| Hand-written tables | `TestConformance` | Statements that must parse, be rewritten to a canonical form, or be rejected |
| Official test suites | `TestCorpus` | About 33,000 statements per dialect from the PostgreSQL 16 and SQLite 3.51 suites |
| Generated expressions | `TestCorpus` | Operator precedence, with about 2,000 random expressions per dialect |
| Curated SELECT lists | `TestCorpus` | CTEs, subqueries, joins, set operations, windows and more, with mutated copies |
| ANSI feature list | `TestANSI` | Standard SQL by feature ID, recording which engines accept each statement |
| Scripts | `TestParseScript*` | Per-statement results, error recovery and source spans |
| Transpiling | `TestTranspile*` | Output and warnings between dialects |
| Limits and performance | `TestMaxDepth*`, `TestPerf*`, ... | Nesting, size and timeout limits, and pathological inputs |
| Dialect tables | `TestDialectTablesArePinned` | Every precedence entry and claimed feature is exercised by some test |
| Query structure | `TestQStruct*` | Dependency graphs, name resolution with and without a catalog, CTE scoping |
| Columns used | `TestListOutCols*` | Column lineage through CTEs, subqueries, joins, set operations and views |

For each statement in `TestCorpus`, the test checks that:

1. The parser accepts or rejects it the same way the real engine did.
2. Where a tree was verified, its fully parenthesized form matches the frozen one.
3. Formatting and parsing it again gives the same text and tree.

Any disagreement with an engine must be listed in `testdata/known/<dialect>.jsonl`.
A listed entry that no longer disagrees fails too, so the list stays accurate.

### Fuzzing and benchmarks

```sh
go test -run '^$' -fuzz=FuzzParse -fuzztime=120s .
go test -run '^$' -bench Corpus -benchmem . -dialect=postgres
```

## Where expected results come from

Expected results are never written by hand. Real engines produce them once, and the
results are committed under `testdata/`:

- **PostgreSQL:** `pg_query_go`, which is PostgreSQL's own parser as a library. No
  server runs and nothing is executed. It also returns parse trees, so grouping is
  compared directly.
- **SQLite:** the `sqlite3` binary in `-safe` mode. It exposes no parse tree, so
  grouping is checked by evaluating both forms on sample data.

### Regenerating the corpus

Do this after a parser fix that changes results, after adding cases, or when moving
to a new engine version, and then review the diff. It needs a C compiler and network
access.

Fetch PostgreSQL's regression SQL, then build SQLite with its optional
`UPDATE`/`DELETE ... LIMIT` grammar. Keep these outside the repository:

```sh
curl -fsSL https://codeload.github.com/postgres/postgres/tar.gz/refs/tags/REL_16_11 -o pg.tgz
tar -xzf pg.tgz        # creates postgres-REL_16_11/

curl -fsSLO https://www.sqlite.org/2025/sqlite-src-3510000.zip
unzip sqlite-src-3510000.zip
mkdir sqlite-build && cd sqlite-build
../sqlite-src-3510000/configure --disable-tcl --update-limit
make sqlite3
```

Then, with `$PG` and `$SQLITE` pointing at those directories:

```sh
cd tools/corpusgen   # a separate module, because pg_query_go needs cgo
go run . -root ../.. \
  -pg $PG/src/test/regress/sql \
  -sqlite $SQLITE/sqlite-src-3510000/test \
  -sqlite-bin $SQLITE/sqlite-build/sqlite3
```

`corpusgen` also rewrites `testdata/known/`, so check that diff closely: a new entry
means a new disagreement with an engine.

### Adding test cases

- **SELECT and query syntax:** add a line to `tools/corpusgen/select/<feature>.sql`,
  valid or invalid, and regenerate.
- **Other syntax:** add to the tables in `identity_*_test.go` and `errors_test.go`, and
  confirm the verdict against the engine first.
- **Standard SQL:** add to `corpus.ANSI` in `internal/corpus/ansi.go`, then check it:

  ```sh
  cd tools/corpusgen && go run . -ansi -sqlite-bin $SQLITE/sqlite-build/sqlite3
  ```

Every test package must import `sqlp/internal/testkit`, or `-dialect` is rejected as
an undefined flag.

`AGENT_TODO.md` has working rules for contributors, known gaps and how to run the
live-engine oracle tests (`-tags oracle`).

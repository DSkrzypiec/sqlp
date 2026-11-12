# AGENT_TODO

Handover notes for whoever continues this project. Last full review: 2026-11-10, at
commit `e5bad07`. Every package was read, and suspicious spots were probed against
`pg_query` (PostgreSQL 16) and the SQLite 3.51.0 binary.

`sqlp` is a from-scratch, multi-dialect SQL parser and generator in Go, covering
PostgreSQL 16 and SQLite 3.51. On top of it sit a query-structure analysis
(`analyze`, `catalog`) and a small public web UI (`cmd/sqlp_serv`, running at
https://sqlp.dskrzypiec.dev). It is a first sketch. Go vs C++ is undecided, and
wrapping the parser behind an API in Go is the likely direction. BigQuery is the
third dialect, in progress (section 9).

For a code tour, read in this order: `sqlp.go`, `ast/ast.go`, `dialect/`,
`lexer/lexer.go`, `parser/parser.go`, `parser/query.go`, `parser/expr.go`,
`gen/gen.go`, `harness_test.go`, then `analyze/qstruct.go`, `analyze/columns.go` and
`cmd/sqlp_serv/server.go`.

**Order of work.** Section 1 lists confirmed bugs, nearly all fixed on 2026-11-10;
the few open ones are marked `[ ]`. Section 2 lists features that are missing or half done. Read the rules in
section 3 before changing code.

## 1. Confirmed bugs (fix these first)

Each item was reproduced on 2026-11-10: SQL verdicts with `pg_query` or the SQLite
3.51.0 binary, timings on a local copy of the server. Items marked "by reading" were
not reproduced.

**Status, 2026-11-10 (second pass).** Sections 1.1 to 1.8 were worked through in
order. What is fixed is summarised below so the reasons stay findable; what is still
open is marked `[ ]`. Every statement used to check a fix is now in the tables of
`identity_*_test.go`, `errors_test.go`, `transpile_test.go` or `lexer/lexer_test.go`,
and each one's verdict was checked against `pg_query` or SQLite 3.51 (section 3).

Fixing an accept/reject item means adding the statement to the tables in
`identity_*_test.go` or `errors_test.go`, or to a curated list, after checking the
engine's verdict again (section 3).

### 1.1 Web UI: one request could run for 45 seconds, one reply could be 17 MB

Fixed. `analyze.QStruct` is linear (`TestPerfQStruct`); one 2 s deadline covers
parsing and is checked between statements (`TestRunDeadline`); syntax trees, graphs,
diagnostics, messages and error lines have per-request budgets, and `render` caps a
reply at 2 MB (`TestRunWorstCases`); the page shows a message when a request fails.
`QStruct` itself still takes no context, so the work per request is bounded by the
deadline times one statement's analysis (section 2).

- [x] Applied on the host on 2026-11-11: `CPUQuota=100%` (`redeploy.sh` now installs a
      changed unit), and nginx's `limit_req` and `limit_conn` for `/run`, copied by hand
      into the certbot-edited site (backup: `/etc/nginx/sqlp.dskrzypiec.dev.bak-2026-11-11`).
      Checked: of 30 rapid requests, 14 got through and 16 got 429; other paths are not
      limited; the app still sees the client's address on `/run`.
- [x] `web/static/app.js` error messages checked in headless Chromium on 2026-11-11:
      429, 413, 500 and an unreachable server each show their message.

### 1.2 Wrong output or misparse

Fixed:
- Lexer: a PostgreSQL `--` comment also ends at `\r` (`CommentEndsAtCR`); a
  vertical tab is not whitespace in either engine; a fraction after `.` must start
  with a digit (`1._5`); SQLite wants a digit right after `0x` (`RadixSeparator` is
  PostgreSQL only); PostgreSQL `E'…'` strings whose octal or hex escapes give a zero
  byte or invalid UTF-8 are rejected (`CheckEscapeString`); `Unescape` joins UTF-16
  surrogate pairs.
- `N'abc'` is read as `NCHAR 'abc'`, as PostgreSQL's lexer does (`NationalStrings`).
- `EXTRACT` keeps its field as written: a word, a quoted name (`EXTRACT("Year" …)`
  was rejected) or a string (`ast.Extract.FieldString`).
- `REVOKE GRANT OPTION` needs `FOR`, and then privileges `ON` objects.
- `INITIALLY`/`DEFERRABLE`: `ast.Deferrable` keeps the words in the order written.
  A PostgreSQL table constraint, constraint trigger or `ALTER … ADD CONSTRAINT`
  takes them through `parseConstraintAttrs`, gram.y's `processCASbits` checks
  included (`CHECK` cannot be `DEFERRABLE`, `UNIQUE` cannot be `NOT VALID`, …); a
  SQLite table constraint takes them only on a foreign key.
- No more dropped text: `CREATE SEQUENCE … AS` keeps the whole type and rejects
  arrays; `CREATE EXTENSION` keeps every option in order (`ExtensionOption`);
  `CREATE OR REPLACE` and `TEMP` are only accepted where an engine takes them;
  `IDENTITY ()` is rejected; `ALTER … ADD GENERATED … AS IDENTITY` is modelled
  (`AddIdentity`); sequence options `RESTART [WITH]` and `SEQUENCE NAME` exist.

### 1.3 Transpile output without a warning

Fixed: `Transpile` and `TranspileScript` parse every output statement with the
target's parser and warn when it fails and the generator gave no warning of its own.
Over the corpora that turned 6,235 silent SQLite → PG failures into 0 and 10,172
PG → SQLite failures into 2, both semantic errors that PostgreSQL rejects too
(`gtest_err_*`: two generation expressions, or `DEFAULT` with `GENERATED`). Silent meaning changes now warn or are
rewritten: array subscripts, prefix operators unknown to the target, bit strings
(PG `X'1F'`), SQLite blobs (become `'\x…'::BYTEA`), `LIKE` in `CREATE TABLE`,
`POSITION` (becomes `instr`), `ILIKE` ↔ case rules of `LIKE` (SQLite `LIKE` becomes
`ILIKE`), negative `LIMIT` (becomes `LIMIT ALL`), `==`, collations, `rowid`, lossy
type renames (`SERIAL`, `TIMESTAMPTZ`, `JSONB`, `UUID`, `WITH TIME ZONE`), `MATCH`
names and `ON INSERT` from SQLite, and calls of built-in functions the target does
not have (`Dialect.Functions`: pg_proc of 16.11 and SQLite's library functions).

- [ ] Output that both parsers accept but that means something else is only caught
      for the constructs listed above. Known gaps: SQLite's free-form type names
      reach PostgreSQL unchanged (`STRING`, `UNSIGNED BIG INT`), and functions that
      both dialects have can differ (`random()`, `format()`, `round()` on text).
- [ ] Latent: `gen.returning` drops `RETURNING` silently for a target without it
      (unreachable with the two current dialects).
- [ ] The counts above come from a throwaway tool; nothing in the test suite
      transpiles the corpora (section 2, Tests).

### 1.4 PostgreSQL rejects valid SQL

Fixed, all 40 listed statements: `FOR READ ONLY`; quoted schemas in `OPERATOR()`;
`SETOF` in type names (`ast.DataType.Setof`); `b_expr` positions (`DEFAULT`,
`POSITION`) now follow gram.y (`parseBExpr`), which also keeps `DEFAULT 'x' COLLATE
"C"` as a constraint; `SUBSTRING` and `TRIM` forms; `EXPLAIN (SELECT 1)`; index
parameters on constraints (`IndexWith`); hash partition bounds in either order;
`STORAGE`/`COMPRESSION DEFAULT`; column `OPTIONS`; `ONLY (t)` everywhere
(`parseRelationExpr`); type names in `DROP TYPE|DOMAIN` and `COMMENT ON TYPE|DOMAIN`;
`SET … FROM CURRENT`, `SET TIME ZONE INTERVAL …`, `SET a = $1`; `SHOW`/`RESET` of
`TRANSACTION ISOLATION LEVEL` and `SESSION AUTHORIZATION`; `COPY BINARY`,
`USING DELIMITERS`; `DO` with parts in any order (`ast.DoPart`); function
`TRANSFORM FOR TYPE`, `SET TIME ZONE`, `RESET`.

### 1.5 SQLite rejects valid SQL

Fixed: a byte-order mark between tokens is whitespace (`BOMIsSpace`); Tcl-style
parameters `$a::b(c)` and `#a` (`TclParams`); `REFERENCES … MATCH any-name`,
`ON INSERT`, and these clauses in any order (`LooseReferences`, which replaces
`MatchPartial`); `IF` as a name after `DROP … IF EXISTS`.

- [ ] Known disagreement, an engine artifact: in `testdata/known/sqlite.jsonl`,
      `CREATE TRIGGER AFTER UPDATE ON a1 BEGIN INSERT INTO a4 DEFAULT VALUES; END` is
      recorded as valid only because SQLite reports the missing table before it
      parses the body. With the table present SQLite rejects `DEFAULT VALUES` in a
      trigger, as sqlp does.
- [ ] Note: `CREATE TABLE main.if (a)` passes SQLite's parser and then fails when
      SQLite reads back its own schema. sqlp accepts it.

### 1.6 PostgreSQL accepts invalid SQL

Fixed, all listed items except the opaque statements below: `OFFSET … ROWS` and
`FETCH FIRST` values; at most three name parts for a table (`MaxRelationParts`);
mixed `ARRAY[[1],2]`; argument counts of `COALESCE`, `NULLIF`, `GREATEST`, `LEAST`,
`GROUPING`, `NORMALIZE` (no longer raw text); `VARIADIC` placement; `DISTINCT` or
`VARIADIC` with `WITHIN GROUP`; `ANY` in a `DEFAULT`; `CREATE TABLE (cols) AS` with
types; ordering in `PARTITION BY`; `OF typ ()`; index expressions (no `::`, no
window function without parentheses) and index names; `CREATE RECURSIVE VIEW`
without columns; `CREATE SCHEMA` elements; constraints in composite types; one-part
names for `REINDEX SCHEMA|DATABASE`, `DROP SCHEMA|EXTENSION|ROLE|TRIGGER` and
`COMMENT ON` roles, schemas and the like; `COMMENT ON … ON` only for constraints,
triggers, rules and policies; `DEFAULT VALUES` alone; `GRANT` details (`ALL` needs
`ON`, only `WITH GRANT OPTION` on objects, no `CASCADE`); `COPY (…)` and `PREPARE`
only take PreparableStmt; `CALL` takes a plain call; `COST`/`ROWS` take numbers;
`SET` forms with grammar of their own and `BEGIN READ ONLY,`; `SET TIME ZONE on`.
The utilities `CHECKPOINT`, `CLOSE`, `DISCARD`, `LOAD`, `DECLARE`, `FETCH`, `MOVE`,
`REASSIGN`, `PREPARE TRANSACTION` and `COMMIT|ROLLBACK PREPARED` are still kept as
text, but their grammar is checked (`ast.Opaque.Checked`), and `TestCorpus` checks
the verdicts of such statements.

- [ ] Opaque statements still accept any tail: 45 corpus statements that PostgreSQL
      rejects pass (`DROP OPERATOR` 10, `CREATE PUBLICATION` 7, `CREATE STATISTICS` 5,
      `ALTER TABLE` actions 4, `DROP AGGREGATE` 3, and others). Only the first words
      of `SECURITY LABEL`, `IMPORT FOREIGN SCHEMA` are checked, and `CLUSTER` not at
      all (`CLUSTER foo bar` passes). Modelling the statement is the fix (section 2).
- [ ] `SET TIME ZONE year` passes: gram.y takes only a plain identifier there, and
      the dialect has no table of unreserved keywords to tell `year` from `foo`.
- [ ] The one entry in `testdata/known/postgres.jsonl`: an unknown `XMLTABLE` column
      option passes because XML and JSON constructor arguments are raw text.

### 1.7 SQLite accepts invalid SQL

Fixed, all listed items: `current_date()` and friends (keywords, never calls); at most
`schema.table.column`; upsert rules (none after `DEFAULT VALUES`, only the last
without a target); `DEFAULT` takes no parameter and no bare `(SELECT …)`; one-part
names after `COLLATE`, `REFERENCES` and `CREATE INDEX … ON`; a column before table
constraints; `(cols) AS SELECT`; trigger bodies (no alias, `WITH`, `DEFAULT VALUES`,
`ORDER BY` or `LIMIT` on DML); `IF` as the name of a created object; `#1` (a
register reference, which SQLite allows only in its own nested parses) is a
parameter token that the parser rejects, as in SQLite, rather than a lexing error.

### 1.8 Smaller bugs outside the parser

Fixed: the `catalog` loading queries (SQLite uses `pragma_table_xinfo`, checked on
3.51 with generated columns; PostgreSQL uses a left join, checked only by parsing);
error and span columns count characters, offsets stay in bytes, and both are
documented; `cmd/sqlp -catalog` rejects unknown fields; stale comments in
`dialect/`, `README.md` and `catalog.Add`. A zero-value `QStructGraph` marshals
without panicking (checked).

- [ ] Decided, by reading: `CREATE VIEW v AS SELECT … FROM v` shows the
      self-reference as a second node `v#2`. That is the old `v` that `CREATE OR
      REPLACE` reads, so it stays.
- [ ] Note only: SQLite output prints `[x]` and `` `x` `` as `"x"`. In a SQLite build
      that still allows double-quoted strings (the library default), an unknown `"x"`
      silently becomes a string, while `[x]` is always an identifier. The 3.51
      command-line shell disables that fallback, so both forms fail the same way there.

### 1.9 Checked and found correct

So that nobody repeats the work:

- The PostgreSQL keyword tables match `kwlist.h` of PG 16 exactly (471 keywords:
  101 reserved, 23 type/function names, 55 column names, 39 that need `AS`).
- All 148 SQLite keywords were tried as a table name and as a column name on 3.51.
  `sqliteReserved` agrees; `IF` is special only after `CREATE … [IF NOT EXISTS]`.
- Generator round trips: 35,512 of 35,521 PostgreSQL corpus statements keep an
  identical `pg_query` tree after `sqlp.Format`. The other 9 are harmless
  normalisations: `START TRANSACTION` → `BEGIN`, element order in `CREATE TABLE … OF`,
  and a table constraint printed after the columns.
- SQLite → PG rewrites that work: `LIMIT 5, 10` → `LIMIT 10 OFFSET 5`; `[a]` and
  `` `a` `` → `"a"`; `'a' "b"` → `AS "b"`; `ISNULL`/`NOTNULL`/`a NOT NULL` →
  `IS [NOT] NULL`; `END TRANSACTION` → `COMMIT`; `RELEASE s` → `RELEASE SAVEPOINT s`;
  the type map (`DATETIME`, `DOUBLE`, `BLOB`).
- `analyze/analyze.go`, `cmd/sqlp_serv/graph.go`, `web/`, `internal/astdump`,
  `internal/testkit` and `ast/`: nothing found.

## 2. Incomplete features

**Web UI and API**

- [x] `cmd/sqlp_serv`: an HTMX page with SQL and catalog editors, three actions (query
  structure as an SVG graph, columns used as a table, syntax tree), deploy scripts in
  `deploy/`.
- [x] The web UI records every run in a SQLite database (`cmd/sqlp_serv/store.go`,
  `-db`), as material for finding mistakes. `deploy/README.md` has queries. The server
  is a module of its own, so the driver stays out of the library's `go.mod`. Every 5
  minutes it deletes the oldest runs if the database is 90% full (of 1 GB).
- [ ] Nothing reads the run log yet but the `sqlite3` shell.
- [x] The SQL editor highlights SQL in the chosen dialect (CodeMirror 5, vendored in
  `web/static/codemirror/`; `deploy/README.md` lists it).
- [ ] The web UI has no transpile action and no formatted-SQL output, although the
  library has both (`TranspileScript`, `Format`). `cmd/sqlp` has neither as well: it
  prints the syntax tree or, with `-qstruct` or `-outcols`, the analysis as JSON.
- [ ] No JSON HTTP API. The server returns HTML fragments only.
- [ ] No JSON for the AST. Interface-typed children need a type tag.
  `internal/astdump` (the generic tree behind `cmd/sqlp` and the web UI) is a
  starting point; enums already have names (`ast/strings.go`).
- [ ] AST walker/visitor (`ast.Walk` or similar). None exists. `analyze` and
  `internal/astdump` each walk the tree with their own reflection code; table
  extraction, lineage and rewrites need a shared one.
- [ ] Stable, documented AST shape, once clients depend on it.

**Schema context and query analysis (`catalog`, `analyze`)**

- [x] `catalog` package: plain data that callers fill in (objects with optional
  kind, columns in order with optional types, optional view definitions, a
  default database), JSON, `Add` for loading rows, `Validate`.
- [x] Decided with the owner: each analysis gets its own small result type, built
  on shared unexported name resolution in `analyze`. No general-purpose
  intermediate tree is exported.
- [x] `analyze.QStruct`: the dependency DAG of a SELECT or CREATE VIEW (CTEs,
  subqueries, objects, table functions). Name rules are dialect data
  (`dialect.NameRules`). Run on every corpus SELECT and in `FuzzParse`.
- [x] `analyze.ListOutCols` (2026-11-11): the distinct table and view columns a
  SELECT or CREATE VIEW uses, each tagged with clauses: `select` (the output is
  computed from it) and `join` (ON, USING or NATURAL). It sees through CTEs,
  subqueries, set operations, aliases and their column lists, LATERAL and table
  functions, recursive CTEs, and views with `ExpandViews`. With a catalog it expands
  `*` and settles unqualified columns; without one, a column several relations
  could have is `ambiguous`.
  - How it works (`analyze/columns.go`): every relation is a list of columns, each
    with a sorted set of interned sources; columns a catalog does not list are an
    `openCols` entry that turns a looked-up name into that column of the table. A
    recursive CTE reads itself through stand-in sources resolved when it is done.
    The CTE lookup is shared with `QStruct` (`analyze/scope.go`).
  - Decided with the owner: WHERE, GROUP BY, HAVING and ORDER BY columns are left
    out; join columns are in, tagged `join`. A USING column counts both sides. An
    object the catalog lists without columns has unknown columns, not zero.
  - New dialect data: `NameRules.NamedExprs` (PostgreSQL's FigureColname, checked
    against `pg_query` raw trees), `UsingColumnsFirst` (checked on SQLite 3.51:
    SQLite keeps the left column in place) and `SystemColumns`.
  - Limits: 2,000 columns per relation, and past 200,000 column entries per
    analysis each new relation's columns are merged into one entry (a diagnostic
    says so; results may then list too much, never too little).
    `TestPerfListOutCols` and `TestRunWorstCases` hold the worst inputs found
    under 0.5 s; one of them used to exhaust memory.
- [ ] `ListOutCols` limits: SQLite names an unaliased expression column after its
  text (`max(a)`), which the AST does not keep, so outer references to such a
  column are not matched (a diagnostic says so). PostgreSQL-only cases are checked
  only for syntax (`pg_query`), not for name resolution; SQLite cases with a
  catalog were run against tables created from it. Not covered: INSERT/UPDATE/
  DELETE/MERGE (RETURNING, SET), WHERE/GROUP BY/ORDER BY as clauses of their own,
  column types, and SQLite WITHOUT ROWID tables (the catalog cannot say, so `rowid`
  always resolves).
- [ ] `QStruct` has no limits of its own: no node budget and no context (section 1.1).
- [ ] QStruct expectations are written by hand. SQLite's authorizer (`.auth on`)
  agreed with the SQLite cases checked by hand, but it only reports tables whose
  columns are read. Freezing its output for the corpus would make it a real
  oracle.
- [ ] A non-temporary SQLite view binds unqualified names to its own schema, not
  the search path. View expansion uses the search path for now.
- [ ] Not in QStruct yet: INSERT/UPDATE/DELETE/MERGE, CREATE TABLE AS, whole
  scripts, data-modifying CTEs (reported as a diagnostic), and subqueries inside
  XML/JSON constructor arguments (raw text; reported as a diagnostic).
- [ ] Building a catalog from a DDL script. View columns need the query's output
  column names.
- [ ] The PostgreSQL loading query in the `catalog` documentation has only been
  parsed, never run against a server. The SQLite one was run on 3.51 (and see
  section 1.8 for what both miss).

**Parser**

- [ ] Source positions on AST nodes. Only tokens, errors and `ParseScript` statements
  have them. Do this early: it changes every node.
- [x] Statement-level error recovery: `ParseScript` reports each statement with its
  source text and span and resumes at the next `;`. `Parse` stays all or nothing.
- [ ] Comments are dropped. The lexer can emit them (`Comment` tokens); the parser
  and AST do not keep them.
- [ ] BigQuery dialect, the first real test of the dialect-as-data design (section 9).
  Snowflake comes later.

**Transpiling**

- [ ] Today: identifiers, operators, precedence-safe parentheses, a type map, a few
  statement rewrites, and warnings for a short list of constructs. Section 1.3
  measures how far that is from the promise of a warning for everything else.
- [ ] No table of what each target supports, so `gen` cannot warn by statement kind
  or node kind.
- [ ] No rewrites for the common cases: SQLite `IS` → `IS NOT DISTINCT FROM`,
  `==` → `=`, parameter styles, function names (`ifnull`, `iif`, `group_concat`,
  `total`), `ILIKE`, typed literals, `EXTRACT`.

**Statements kept as raw text (`ast.Opaque`, `ast.AlterOpaque`)**

- [ ] 3,729 of the 33,111 statements from PostgreSQL's regression suite in the
  corpus (11%) are only recognized by their leading keywords and checked for
  balanced parentheses; 572 of them are `ALTER TABLE` with an opaque action. There
  are 121 distinct verbs. Most frequent: `DROP FUNCTION` (288), `CREATE ROLE` (135),
  `FETCH` (131), `ALTER PUBLICATION` (128), `CREATE AGGREGATE` (121), `CREATE RULE`
  (117), `CREATE STATISTICS` (102), `ALTER FOREIGN …` (91), `DECLARE` (90),
  `ALTER OPERATOR` (87), `ALTER TYPE` (87), `CREATE PUBLICATION` (83),
  `ALTER INDEX` (81), `CREATE OPERATOR` (80), `CREATE USER` (76), `SET SESSION` (75).
  Work down that list. Find the call sites with `grep -n 'parseOpaque(' parser/*.go`.
- [ ] XML/JSON constructors (`XMLELEMENT`, `JSON_OBJECT`, ...; `RawArgFuncs` in
  `dialect/postgres.go`) keep their arguments as raw text in `FuncCall.RawArgs`.

**Tests**

- [ ] Nothing checks transpiled output against the target engine at scale. The
  numbers in section 1.3 came from a throwaway tool. `corpusgen` could freeze, per
  corpus statement, whether the target parser accepts the output, and the test
  could then require a warning wherever it does not. `pg_query` covers SQLite → PG
  without a server.
- [ ] Nothing checks the dialect keyword tables against the engines (section 1.9
  was a one-off check).
- [ ] `FuzzParse` covers `Parse`, `ParseScript`, `Format`, `QStruct` and
  `ListOutCols`, but not `Transpile`.
- [x] Time and size bounds are tested for the analyses and the web UI:
  `TestPerfQStruct`, `TestPerfListOutCols`, `TestRunWorstCases` (section 1.1).
- [ ] Packages with no tests of their own: `dialect`, `gen`, `internal/astdump`,
  `internal/corpus`, `internal/testkit`. They are covered through the root tests.

## 3. Working instructions

**Commands**

```sh
go build ./... && go vet ./... && go vet -tags oracle . && go test ./...
go test ./... -dialect=postgres      # or sqlite, bigquery, or a list such as sqlite,postgres; default all
go build -o sqlp ./cmd/sqlp && ./sqlp -dialect sqlite "SELECT 1"
go vet -C cmd/sqlp_serv ./... && go test -C cmd/sqlp_serv ./...  # the web UI: own module
go run -C cmd/sqlp_serv . -addr 127.0.0.1:9393   # the web UI, locally
deploy/redeploy.sh                   # tests, build, install, restart the live UI
```

Every test package must import `sqlp/internal/testkit`, or `-dialect` is rejected
as an undefined flag. Dialect tables are listed in `conformance`
(`harness_test.go`).

Normal test runs need no engine. Engines only produce expected results, which are
then committed. For PostgreSQL the source is `pg_query_go` v5 (the real PG 16
parser as a library, so no server and nothing executed).

**Frozen corpora.** `TestCorpus` replays `testdata/corpus/<dialect>/*.jsonl` with no
engine. Each record holds the engine's verdict and, where verified, a `shape`: the
statement with all grouping parenthesized. PostgreSQL shapes are frozen only when
`pg_query` builds the same tree for the shape and the original. SQLite exposes no
tree, so only generated constant expressions have shapes, verified by evaluating
both forms. Every disagreement must be listed in `testdata/known/<dialect>.jsonl`; a
stale entry fails. Opaque statements are skipped. Regenerate after a parser fix and
review the diff:

```sh
cd tools/corpusgen   # separate module: pg_query_go needs cgo
go run . -root ../.. -pg $PG/src/test/regress/sql \
  -sqlite $SQLITE/test -sqlite-bin $SQLITE_BUILD/sqlite3
```

The sqlite3 binary must be 3.51 built from full source with
`SQLITE_ENABLE_UPDATE_DELETE_LIMIT`. Do not use a system `sqlite3` for verdicts: it
is usually older.

**Curated SELECT lists.** `tools/corpusgen/select/<feature>.sql` holds one statement
per line, valid or invalid. `corpusgen` freezes each with 4 token-level mutations as
`testdata/corpus/<dialect>/curated_<feature>.jsonl`. To add a case, add a line and
regenerate; never write the expected result by hand. SQLite's tree check compares
query results on sample rows, so compound queries, which SQLite cannot parenthesize,
have no frozen tree.

**ANSI list.** `corpus.ANSI` (`internal/corpus/ansi.go`) is a table of standard SQL
by feature ID, valid IDs being those in `testdata/ansi/sql_features.txt`. Each entry
says which engines accept it. `TestANSI` holds the parser to that, and `go test .
-run TestANSI -v` logs a per-dialect summary. After editing the table, confirm it
against the engines; a wrong entry is printed with the label it should have:

```sh
cd tools/corpusgen && go run . -ansi -sqlite-bin $SQLITE_BUILD/sqlite3
```

**Other checks.** `TestDialectTablesArePinned` moves each precedence entry and turns
off each claimed feature, and fails if nothing notices; `-short` skips it. Fuzz with
`go test -run '^$' -fuzz=FuzzParse -fuzztime=120s .`. Benchmark with
`go test -run '^$' -bench Corpus -benchmem . -dialect=postgres`.

**Rules**

- Never guess whether SQL is valid. Ask a real engine. Guessed expectations were
  wrong many times during the first build (for example `SELECT ARRAY[1,2]` is
  valid SQLite, `DROP TABLE a, b` is not).
- Change the code or a test only after checking the engine's verdict.
- Keep tests concise and table-driven, with no spaghetti. This is the owner's
  explicit request. Add cases to the existing tables (`postgresIdentity`,
  `sqliteRewrites`, `postgresFailures`, ...) using the helpers `identity`,
  `rewrites` and `failures` in `harness_test.go`.
- The parser must not test a dialect by name. Dialect differences go in
  `dialect.Dialect`, as table data or a `Features` flag.
- One AST for all dialects (sqlglot's "superset AST"). A dialect-only construct
  gets a node or a field, not a separate tree.
- To add syntax: AST node, then parser, then generator, then an identity test,
  then an oracle run.
- `gen` output is canonical SQL and also the test oracle: every identity test
  parses, generates, and compares.
- `Transpile` must add a warning for anything the target cannot express. Never
  drop a construct silently.
- API style, agreed with the owner: small result types made for one purpose,
  plain-data inputs that callers can fill in by hand, nothing generic.
- The web UI is public. Try load and worst-case inputs on a local copy
  (`-addr 127.0.0.1:<other port>`), never on the live service.

## 4. Untrusted input in the parser (done)

All of this is fixed for the parser. The analysis and the web UI behind it are not
bounded yet: see section 1.1.

Measured with the `postgres` dialect. SQLite was not measured; `ParenQueries` is
only set for PostgreSQL, so the first two items should not apply to it.

- [x] **Exponential backtracking: a 370-byte query hangs the parser.** Fixed:
  `tryParenBodyQuery` (`parser/expr.go`) and `parseParenFactor`
  (`parser/query.go`) now parse the ambiguous `(query)` once and, if it turns
  out not to be the whole body, reinterpret the already-parsed result as a
  `Subquery`/`SubqueryRef` and continue from there (`continueExprPrec` /
  `continueJoins`) instead of backtracking and reparsing. A first attempt at
  memoizing the backtracking (keeping `p.try`) only traded the exponential
  blowup for a quadratic one — a failed `p.try` still costs time proportional
  to call-stack depth even when the memo hits, since panic/recover walks the
  stack. Removing the backtracking entirely fixed both. Reviewed the other
  `p.try` sites (`tryOverlay`, `tryTypedString`, `parser/server.go:468`): none
  of them recurse into themselves, so they're not a blowup risk and were left
  alone. Regression tests: `TestPerfNestedParenExpr`,
  `TestPerfNestedParenJoin` (both shapes, 200 levels, root package). Confirmed
  linear to 20,000 levels / 300 KB (~200ms) by hand; not committed as a test
  since that's slower than the suite should be.

- [x] **Quadratic scan on runs of `(`.** Fixed: `isQueryStartAtPos`
  (`parser/query.go`) memoizes by absolute token position on `Parser`, so a
  run of k unclosed `(` is scanned once total instead of once per nesting
  level. (This alone didn't fix the exponential bug above — that cost was in
  backtracking, not this scan — but it's a real, independent fix.) Regression
  test: `TestPerfLongUnclosedParenRun` (root package).

- [x] **No nesting limit.** Fixed: `Parser.enterDepth`/`exitDepth`
  (`parser/parser.go`) guard `parseExprPrec`, `parseQuery`, and
  `parseTableExpr`, returning `*parser.Error` past the limit. Configurable via
  the new `parser.Limits{MaxDepth: ...}` (also exported as `sqlp.Limits`),
  default `DefaultMaxDepth = 1000` (SQLite's own default), used through the
  new `parser.ParseWithLimits` / `parser.ParseExprWithLimits` (and
  `sqlp.ParseWithLimits` / `sqlp.ParseExprWithLimits`); `Parse`/`ParseExpr`
  keep their old signatures and use the default. Regression tests:
  `TestMaxDepthExceeded`, `TestMaxDepthWithinLimitSucceeds`,
  `TestMaxDepthConfigurable` (root package).

- [x] **Non-syntax panics escape.** Fixed: `Parser.recover` now converts any
  non-`*Error` panic into an internal `*parser.Error` instead of re-panicking.
  Added `recoverAny` as the same backstop for a panic in the lexer, before a
  `Parser` exists to attribute a position to (`newParser` runs before either
  `defer` is in place). A real Go stack overflow still can't be recovered —
  that's what the depth limit above is for. Regression test:
  `TestRecoverNonSyntaxPanic` (`parser` package).

- [x] Added a request size limit and a parse timeout, bundled into the same
  `Limits`/`ParseWithLimits` mechanism as the depth limit above:
  `Limits.MaxBytes` (default `DefaultMaxBytes = 4 MiB`) is checked before
  tokenizing; `ParseWithLimits`/`ParseExprWithLimits` take a `context.Context`,
  checked every 1024 tokens consumed (`Parser.checkContext`). Regression
  tests: `TestMaxBytesExceeded`, `TestParseContextCancellation`,
  `TestParseContextTimeout` (root package).

- [x] Added a fuzz target: `FuzzParse` (root package, `fuzz_test.go`), seeded
  from the existing identity/failure corpora plus the two pathological shapes
  above, asserting `ParseWithLimits` (under a 2s context timeout) and `Format`
  never panic. Ran `go test -fuzz=FuzzParse -fuzztime=60s .`: 575k execs, 0
  failures, nothing new landed in `testdata/fuzz` (only crashers get persisted
  there). Worth a longer run periodically, not on every change.

## 5. Code improvements

Longest functions (lines), the first places to refactor:

| Function | Lines | Note |
|---|---|---|
| `gen/gen.go` `expr` | 280 | one switch over all expressions; move cases to methods |
| `gen/stmt.go` `stmt` | 209 | same |
| `gen/extended.go` `extendedStmt` | 135 | see the historical split below |
| `cmd/sqlp_serv/graph.go` `layout` | 132 | ranking, ordering and drawing in one function |
| `parser/expr.go` `parseWordExpr` | 112 | keyword dispatch mixed with parsing |
| `parser/parser.go` `parseStatement` | 103 | could be a table keyed by first keyword |

- [ ] **Historical split.** `ast/extended.go`, `gen/extended.go` and
  `parser/server.go` hold statements added later. Newer nodes embed
  `stmtBase`/`alterBase`; older ones have hand-written marker methods. Unify them
  and organize by topic.
- [ ] **`parser/ddl.go`** (1,341 lines) is the largest parser file and should be
  split (tables, indexes, views, types, ALTER).
- [ ] **Three paren-balance scanners** do almost the same thing: `takeRaw`
  (`parser/parser.go`), `takeRawArgs` (`parser/expr.go`) and `takeRawArg`
  (`parser/ddl.go`). Merge them.
- [ ] **Two reflection walkers**, `walk` in `analyze/qstruct.go` and the one in
  `internal/astdump`. An `ast.Walk` (section 2) would replace the first.
- [ ] **Panic-based control flow.** `p.try` uses panic/recover to backtrack, so
  every failed attempt is expensive. Most `try` uses were removed (section 4);
  the remaining ones do not recurse.
- [ ] **Stringly-typed AST fields** that have a closed set of values and should be
  enums: `Insert.Or` (`"REPLACE"`), `Raise.Action`, and the results of
  `parseBehavior` (`CASCADE`/`RESTRICT`).
- [ ] **`ast.Deferrable`** needs a way to say "`INITIALLY` written without
  `DEFERRABLE`" and to keep the written order (section 1.2).
- [ ] **`dialect.Features`** is one flat struct of 78 booleans. Group it
  (lexing, query, DML, DDL) before adding a third dialect.
- [ ] **`scriptStatement`** (`parser/script.go`) indexes `p.toks[len(p.toks)-2:]`
  before its own `len(p.toks) > 1` check. It is never called with fewer than two
  tokens, so the check is dead rather than wrong; tidy it.
- [ ] **`internal/astdump`** indents by depth, so output size is quadratic for very
  deep trees. The depth limit and the web UI's `maxTreeNodes` bound it.

## 6. Oracle (differential testing against real engines)

`go test -tags oracle` compares the parser's verdicts with real engines.

**Safety**

- Statements are really executed. Use only a **throwaway** PostgreSQL server,
  never one holding real data. On a shared machine, use a container: the setup
  below trusts local connections.
- Downloaded test suites are untrusted data. Keep them in their own directory and
  run `sqlite3` with `-safe`.
- Run the official suite as the unprivileged `fuzz` role.

**Setup on a new machine** (needs PostgreSQL 16 and `sqlite3`; `$W` is a scratch
directory outside the repo)

```sh
initdb -D $W/pgdata16 -U sqlp --auth=trust
pg_ctl -D $W/pgdata16 -l $W/pg.log -w start \
  -o "-c listen_addresses=127.0.0.1 -c port=54329 -c unix_socket_directories=''"
export PGHOST=127.0.0.1 PGPORT=54329 PGUSER=sqlp PGDATABASE=postgres
psql -c 'CREATE ROLE fuzz LOGIN'
# ORACLE_PSQL=/path/to/psql if psql is not PostgreSQL 16 on PATH
```

The official suites are not in this repo. Fetch them again: `src/test/regress/sql`
from the PostgreSQL 16 source, and `test/*.test` from the SQLite source.

**Runs**

```sh
go test -tags oracle -run 'TestOracleAccepted|TestOracleRejected' .
ORACLE_MUTATIONS=12000 go test -tags oracle -v -run TestOracleMutations -timeout 590s .
ORACLE_SQLITE_CORPUS=$W/corpus/sqlite/test \
ORACLE_PG_CORPUS=$W/corpus/pg/src/test/regress/sql \
ORACLE_CORPUS_PGUSER=fuzz \
  go test -tags oracle -v -run TestOracleCorpus -timeout 595s .
```

**Reading the results**

- A PostgreSQL statement counts as a syntax error only when the `LOCATION` after
  `ERROR` is `scan.l` or `gram.y`. `jsonpath_scan.l` does not count, and neither
  does a `WARNING`'s location.
- SQLite can report a semantic error before reaching a syntax error, so each
  statement runs both with and without a schema.
- Opaque statements are excluded from the PostgreSQL "parser accepts" numbers.

## 7. Baseline to compare against

**2026-11-11, after the section 1 fixes, go1.27.3 linux/arm64.** `go build`,
`go vet` (with and without `-tags oracle`) and `go test ./...` pass: 6,740 tests and
subtests in about 10 seconds; 6,861 after `ListOutCols` was added the same day. The corpora were regenerated with `tools/corpusgen`
from PostgreSQL REL_16_11 and SQLite 3.51.0.

| Frozen corpus | SQLite | PostgreSQL |
|---|---|---|
| Official suite statements | 32,493 | 33,111 |
| Rejected by the engine | 228 | 136 |
| Parsed by sqlp | 32,264 | 33,021 (3,424 opaque) |
| Curated SELECT statements, with mutations | 4,567 | 4,572 |
| Generated expressions | 2,000 | 2,100 |
| Known disagreements (`testdata/known`) | 1 | 1 |
| Opaque statements accepted although the engine rejects them | 0 | 45 |
| Statements with an engine-verified shape (corpusgen) | 2,573 | 32,133 |

Measured by throwaway tools, not by the test suite:

| Check | 2026-11-10 (`e5bad07`) | 2026-11-11 |
|---|---|---|
| PG corpus statements with an identical `pg_query` tree after `Format` | 35,510 of 35,521 | 35,512 of 35,521 |
| SQLite → PG transpile, unwarned outputs rejected by `pg_query` | 6,235 | 0 |
| PG → SQLite transpile, unwarned outputs rejected by SQLite 3.51 | 10,172 | 2 (semantic) |
| `QStruct` on a 204 KB worst-case query | 44.3 s | about 30 ms |
| `ListOutCols` on the worst inputs of `TestPerfListOutCols` (about 200 KB each) | n/a | under 0.1 s each |

**Live-engine oracle run of 2026-11-09** (PostgreSQL 16.11 and sqlite3 3.51). It has
not been repeated since, and commit `890f88b` fixed most of the disagreements it
lists, so expect smaller numbers:

| Check | SQLite | PostgreSQL |
|---|---|---|
| Official suite statements | 34,542 | 34,185 |
| Parsed | 34,252 | 34,111 (3,890 opaque) |
| Both reject | 277 | 69 |
| Parser rejects, engine accepts | 13 | 5 |
| Parser accepts, engine rejects | 0 | 12 |
| 12,000 mutated statements: parser rejects, engine accepts | 1 | 6 |
| 12,000 mutated statements: parser accepts, engine rejects | 2 | 39 |
| Generator round trip, statements | 34,252 | 30,209 |
| Generator round trip: unparsable, unstable, or engine-rejected | 0 | 0 |

Most of the 13 SQLite rejections are test-suite artifacts (internal `#2` registers,
Tcl templating, semantic errors reported first). The mutation test also recorded 50
PostgreSQL statements as opaque, against a 0.5% budget.

## 8. Project housekeeping

- [ ] `go.mod` requires Go 1.26.5.
- [ ] `deploy/` covers only `sqlp_serv`. Changes to the installed nginx site are made
  by hand, because certbot edits that copy (`deploy/README.md`).

## 9. BigQuery dialect (in progress, started 2026-11-12)

Decided with the owner on 2026-11-12: BigQuery is the third dialect. SELECT comes
first, then DML, then the rest. The phases below are the agreed order.

**Oracle: BigQuery dry runs.** There is no local engine and no version to pin, so
the engine is BigQuery itself, through dry runs (free; they parse and resolve but
execute nothing). As for the other dialects, verdicts are frozen into
`testdata/corpus/bigquery/`, and plain `go test` needs no network. Regenerating
needs `gcloud` logged in on the machine (`gcloud auth print-access-token`); the
project is gcloud's current one (`gcloud config get-value project`), a BigQuery
sandbox without billing. Its dataset `sqlp_oracle` (location US) holds the
sample tables, and `corpusgen` recreates them, because sandbox tables expire
after 60 days.

Findings of the spike (2026-11-12):

- Use `jobs.insert` with `dryRun`. `jobs.query` returns no statistics. The job
  gives `statementType`, the result `schema` (with BigQuery's own names for
  unnamed columns, such as `f0_`) and `referencedTables`. Always send
  `useLegacySql: false`: the REST API defaults to legacy SQL.
- BigQuery parses a whole statement or script before anything else, so a syntax
  error always wins over "Not found", "must be qualified with a dataset" or a
  billing error. About 30 dry runs a second with 8 workers.
- **Verdicts.** A statement is rejected when the message starts with
  `Syntax error:`, or when it is one of the parser messages without that prefix,
  such as `CAST operators are not supported` (`::`), `FOR UPDATE is not supported`
  and `EXCEPT must be followed by ALL, DISTINCT, or "("`. Other "not supported"
  messages are semantic: `Duplicate column names in the result are not
  supported`. So `corpusgen` keeps a reviewed table of messages, and it stops on
  a message it has not seen before instead of guessing.
- The sandbox refuses every DML statement that parses, even a dry run, with
  `billingNotEnabled` (in a script: `invalidQuery` with the same message, after
  the whole script parsed). That still means "parsed", so DML verdicts work.
  The refusal comes before analysis, so everything checked after parsing is
  invisible without billing: what DML means (statement type, tables), rules
  such as "UPDATE and DELETE need WHERE" (the reference says so; sqlp accepts
  both without it), and whether `THEN RETURN` (parsed, undocumented, left out)
  is allowed. Dry runs stay free with billing on.
- Constant queries execute for 0 bytes. With `maximumBytesBilled` set to 1, a
  query that reads a table fails instead of being billed. Grouping is checked
  by evaluating a generated expression and its fully parenthesized form, as for
  SQLite. BigQuery is strictly typed, so the generator must build well-typed
  expressions.
- Not guessable: `SELECT ARRAY[1,2]` is valid BigQuery. `SELECT @p` dry-runs
  without declaring `p`; `?` needs positional mode.

**Correctness checks**, each with its oracle:

1. Accept or reject: frozen verdicts, `TestCorpus`, `testdata/known/bigquery.jsonl`.
2. Grouping: generated typed expressions, evaluated in both forms.
3. Meaning of a whole statement: the dry run of the statement and of
   `sqlp.Format`'s output must give the same statement type, schema and
   referenced tables. This stands in for `pg_query`'s tree comparison.
4. Analyses: `referencedTables` checks `QStruct`'s objects; the schema checks
   output column names for `ListOutCols` and `NameRules`.
5. Dialect tables: reserved words probed one by one (`SELECT 1 AS kw`,
   `SELECT kw FROM t`); function names with `SELECT fn()` ("Function not found"
   versus "No matching signature").
6. Transpile to and from BigQuery: dry-run the output, as in section 1.3.

**Completeness.** `testdata/bigquery/features.txt` will list each syntax feature of
the BigQuery SQL reference (lexical structure, query syntax, pipe syntax,
operators, each DDL, DML and DCL statement, procedural language, BQML, other
statements) with its doc URL, and a table like `corpus.ANSI` gives each feature
a valid example and a near miss. Corpus sources, all frozen with dry-run
verdicts: the curated SELECT lists with their mutations (BigQuery accepts 443 of
the 972 statements, and disagrees with PostgreSQL on 251 and with SQLite on 208),
new curated BigQuery lists, the code samples of the BigQuery reference
(Apache 2.0), ZetaSQL's parser and analyzer test files (Apache 2.0), the ANSI
table, and later the web UI's run log (ask the owner before sending visitors' SQL
to Google). Each regeneration reports feature coverage, agreement per source,
known disagreements, opaque statements, round trips and verified shapes in
section 7.

**Running the oracle.** Everything is in `tools/corpusgen` (`bigquery.go`):

```sh
cd tools/corpusgen
go run . -bq-docs-fetch $W/bqdocs           # save the examples of the BigQuery SQL reference (4 minutes)
go run . -bigquery -bq-docs $W/bqdocs       # freeze testdata/corpus/bigquery and testdata/known/bigquery.jsonl
go run . -bq-probe FILE                     # BigQuery's verdict on each statement of FILE (one per line, or ---- between)
```

The reference examples (`docs_*.jsonl`, 189 pages) are the completeness measure:
they cover what BigQuery users write, which the curated lists do not. They are
Apache 2.0 code samples; `testdata/corpus/bigquery/NOTICE` says so.

- Answers are cached in `~/.cache/sqlp/bigquery.jsonl` (`-bq-cache`); delete it to
  take fresh verdicts. A rerun of everything takes seconds; new statements need
  a dry run each (about 30 a second).
- Before writing anything, `-bigquery` asks about every statement. If a message is
  in neither `bqRejects` nor `bqAccepts`, it lists the messages, writes nothing
  and exits 1. Classify by the rule written above the tables: **reject** what
  BigQuery refuses for the statement's structure (which clauses, operators and
  forms appear where), whatever the tables, columns, types and functions are;
  **accept** refusals that depend on names, types or what kind of function
  something is.
- Shapes: constant queries are evaluated (`sameValue`, rows compared as a set,
  billing capped at 1 byte); other statements must dry-run to the same type,
  schema and tables (`sameMeaning`). `checkable` leaves out statements BigQuery
  cannot resolve, so a printed `MISMATCH` is a real disagreement.
- Mutations of the curated lists are made with the dialect's lexer, so a lexer
  change changes the corpus: rerun `-bigquery` after one.
- Lists that only BigQuery needs are in `tools/corpusgen/bigquery/*.sql`, frozen as
  `curated_bigquery_<name>.jsonl`, so the other dialects need no regeneration.
  The statements of `corpus.ANSI` are frozen as `ansi.jsonl`; the table's labels
  hold only PostgreSQL and SQLite verdicts (`corpus.ANSIDialects`).
- Statements spanning lines go into `lexer/lexer_test.go` (`bqCfg`), checked with
  `-bq-probe` the same way.

**Status, 2026-11-12, after DML and the query rules.** 11,238 frozen
statements, 5,954 rejected by BigQuery, 4,125 with a verified shape, no
mismatch. Known disagreements: 356 valid statements sqlp rejects (before the
day's work on the reference examples: 1,102) and 44 invalid ones it accepts
(206). Rejected valid: 202 DDL, 98 GQL graph queries (out of scope for now), 27
queries (11 of them `GRAPH_TABLE` and `MATCH_RECOGNIZE`, 12 from BigQuery's
order of checks), 21 scripts, 4 `EXPORT`/`LOAD`, 4 others. Accepted invalid: 40
DDL, 2 transaction statements, 1 query (`GROUP BY 1` whose output is a whole
row), 1 script. DML: none either way.
Shapes of constant queries are compared by value, except where a function's
value changes from run to run (`bqVolatile`); FOR SYSTEM_TIME is left out
because the sample tables are recreated on every run.

**Phases**

- [x] 0. Oracle: `corpusgen -bigquery` and `-bq-probe`, the message tables, the
  sample tables, the verdict cache, shapes by value or by dry run.
- [x] 1. `dialect.Features` grouped by area (`Expr`, `Names`, `Query`, `DML`,
  `DDL`, `Utility`); `dialect.All` lists the dialects for tests. `ExtendedDDL`
  is split in phase 5, when BigQuery needs parts of it. Source positions on AST
  nodes are not part of this work.
- [x] 2. Lexer (`lexer/backslash.go`): backslash escapes, decoded and checked as
  BigQuery does (octal needs 3 digits up to `\377`, `\x` 2 hex digits, no
  surrogates, no `\u` in bytes); `"…"` strings; raw, bytes and triple-quoted
  literals; literals joined across whitespace and comments; `#` comments; ASCII
  bare names; `\r` ends a comment. An unexpected character no longer ends a
  script: `ParseScript` resumes at the next `;`.
- [ ] 3. Queries. Done, each a dialect flag pinned by frozen statements:
  set operators (`ALL`/`DISTINCT` required, no mixing, `BY NAME`,
  `CORRESPONDING`, `INNER`/`FULL`/`LEFT` modes), typed literals only for
  BigQuery's types, `ARRAY<T>`, `STRUCT<…>`, `RANGE<T>`, `STRUCT(…)`, `[1, 2]`,
  `.` and `[ ]` on any operand, `@name` and `@@sysvar`, INT64 literals, `UNNEST`
  (FROM, `WITH OFFSET`, `IN UNNEST`, `LIKE ANY UNNEST`), `* EXCEPT/REPLACE`,
  `SELECT AS STRUCT|VALUE`, aggregate modifiers (`IGNORE NULLS`, `WHERE`, `HAVING
  MAX`, `LIMIT`), `SAFE_CAST`, `CAST … FORMAT`, `EXTRACT`, `INTERVAL 5 DAY`,
  `LIKE ANY (…)`, `GROUP BY ALL`, named arguments, `TABLE t`/`MODEL m` arguments,
  trailing commas, keywords after a dot, `FOR SYSTEM_TIME AS OF`, `TABLESAMPLE …
  PERCENT`, pipe syntax (`FROM t |> …`, every step but `PIVOT`/`UNPIVOT`), `JOIN`
  rules (`ON` needed except for arrays), and the structural rules (no clauses or
  `*` without `FROM`, grouping sets alone, `LIMIT` literals, no `VALUES`, CTE
  column lists, `MATERIALIZED`, `NATURAL`, `GROUPS`, `FILTER`). The analyses
  follow pipes, star modifiers, `UNNEST`, `TABLE t` arguments and `BY NAME`.
  Also done: `SELECT WITH DIFFERENTIAL_PRIVACY`, `(expr).* EXCEPT (…)`, `PIVOT`
  and `UNPIVOT`, multi-level aggregation, `STRICT CORRESPONDING`, method calls,
  and the rules of `tools/corpusgen/bigquery/rules.sql` (2026-11-12): calls in
  any form BigQuery parses (`COUNT(DISTINCT)`, `SUM(ORDER BY x)`, `COUNT(*
  WHERE c)`), no `ALL` in calls or `EXCLUDE` in frames, frame offsets that are
  literals or parameters (`WindowRules`), `WINDOW c AS b`, no window function or
  `QUALIFY` without FROM, `( … )` in FROM only around a join and without alias
  (`ParenJoinOnly`), `(query) AS x` naming the rows for the pipe steps after it,
  no `IN`/`EXISTS` subquery in `ON` (`JoinOnRules`), `ROLLUP` and `CUBE` nested
  only in `GROUPING SETS` and no tuple or `STRUCT(…)` keys, `INTERVAL '…'` with
  any date part and never without one, no bytes after a type name, `TABLE t`
  only as a table function's argument and `TABLE f(…) [AS x]` there, plain
  arguments for table functions, `EXTEND … WINDOW`. Left in the known list: 17
  queries, of which 13 only because BigQuery reports a name, type or signature
  error before the rule sqlp applies (tuples in GROUP BY, `LIMIT` values, `IN`
  and `EXISTS` in `ON`, frame offsets); `a.1`, a field named by a number (3);
  `(x).(SAFE.LEFT)(…)` (1); and 11 with `GRAPH_TABLE` or `MATCH_RECOGNIZE`.
  The reserved words are checked: `corpusgen -bigquery-keywords` tries every
  keyword sqlp knows (286) as an alias, and all agree (MATCH_RECOGNIZE is
  reserved though the reference's list leaves it out). Still to do: a
  function-name probe (`SELECT fn()`: "Function not found" or not) to fill
  `dialect.BigQuery.Functions` for Transpile's warnings.
- [x] 4. DML: optional `INTO` (`INSERT`, `MERGE`) and `FROM` (`DELETE`); `MERGE`
  with `NOT MATCHED BY TARGET|SOURCE` and `INSERT ROW`; `TRUNCATE TABLE t` only;
  no `WITH` before DML, `INSERT … AS alias`, `DEFAULT VALUES`, `SET (a, b) =`,
  `RETURNING`, `DO NOTHING`, `OVERRIDING` (each a `DMLFeatures` flag that
  PostgreSQL or SQLite sets; Transpile warns when the target lacks one). Not
  done: `THEN RETURN` and the WHERE rule, see the oracle findings above.
- [ ] 5. DDL and DCL: `OPTIONS`, `PARTITION BY`, `CLUSTER BY`, tables, views,
  functions, procedures, schemas, `ALTER`, policies, indexes, `GRANT`.
- [ ] 6. Procedural language: `DECLARE`, `SET`, `IF`, loops, `BEGIN … EXCEPTION`,
  `EXECUTE IMMEDIATE`, transactions, `CALL`, `RAISE`, `ASSERT`.
- [ ] 7. BQML, `EXPORT DATA`, `LOAD DATA`.
- [ ] 8. Web UI, transpile, run-log triage. Done 2026-11-12: BigQuery is a dialect
  of the web UI, with its own sample query and catalog (`web/samples/`, files
  because of the backquotes). A dialect change swaps an editor that still holds
  a sample to the new dialect's (`samples` block in the page, `app.js`), and the
  reset buttons send the dialect. `TestSamples` requires every dialect's sample
  to resolve fully; BigQuery's also dry-ran on copies of its tables. Highlighting
  is CodeMirror's SQL mode set up for BigQuery (`text/x-bigquery` in `app.js`).
  htmx no longer copies `style` attributes on swaps
  (`htmx.config.attributesToSettle`): with CodeMirror on the SQL textarea, that
  copy broke the CSP now and then. Analysis name rules for BigQuery, all checked
  with dry runs: columns, aliases and CTEs ignore case, datasets and tables do
  not (`NameRules.CaseSensitiveObjects`); a quoted `project.dataset.table` is a
  path whose implicit alias is the whole quoted name (`NameRules.QuotedPaths`);
  unqualified names are in the default dataset (search path `""`); `USING`
  columns come first. Still to do: transpile to and from BigQuery, run-log
  triage with dry runs (ask the owner first: it sends visitors' SQL to Google).

Every phase follows section 3: AST node, parser, generator, identity test, then
an oracle run. No verdict is written by hand.

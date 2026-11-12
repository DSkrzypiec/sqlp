package main

// The BigQuery oracle. BigQuery has no local engine, so statements are sent to
// BigQuery itself as dry runs: free, parsed and resolved, never executed.
// Constant expressions are executed to compare their values, with
// maximumBytesBilled set to 1, so a query that would read a table fails instead
// of being billed. Access goes through the REST API with the token of the
// logged-in gcloud user and gcloud's current project; nothing is stored in the
// repository. Every answer is cached outside the repository, so a rerun only
// asks about new statements. Delete the cache to take fresh verdicts.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"sqlp/dialect"
)

const (
	bqAPI      = "https://bigquery.googleapis.com/bigquery/v2/projects/"
	bqLocation = "US"
	bqDataset  = "sqlp_oracle"
)

// bqTables are the sample tables, named and shaped like corpusgen's SQLite
// schema so the curated statements resolve. Sandbox tables expire after 60
// days, so they are recreated on every run.
var bqTables = []string{
	"CREATE OR REPLACE TABLE t (a INT64, b INT64, c INT64, d INT64, e INT64, f INT64, g INT64, h INT64, id INT64, x INT64, y INT64, name STRING, s STRING, j JSON, v INT64, rowid2 INT64)",
	"CREATE OR REPLACE TABLE t1 (a INT64, b INT64, c INT64, d INT64, e INT64)",
	"CREATE OR REPLACE TABLE t3 (a INT64, b INT64, c INT64)",
	"CREATE OR REPLACE TABLE u (a INT64, b INT64, id INT64, x INT64, y INT64)",
	"CREATE OR REPLACE TABLE a (id INT64, kind STRING, x INT64, y INT64)",
	"CREATE OR REPLACE TABLE b (id INT64, kind STRING, x INT64, y INT64)",
	"CREATE OR REPLACE TABLE c (id INT64)",
	"CREATE OR REPLACE TABLE d (id INT64)",
	"CREATE OR REPLACE VIEW v AS SELECT 1 AS a",
}

// The two message tables decide verdicts. A statement is rejected when
// BigQuery refuses it for its structure: which clauses, operators and forms
// appear where, whatever the tables, columns, types and functions are. It
// counts as accepted when the refusal depends on names, types or what kind of
// function something is, since a parser cannot know those. A message in
// neither table stops corpusgen until someone classifies it.
var bqRejects = messageTable(
	`Syntax error: `,
	`CAST operators are not supported`,
	`FOR UPDATE is not supported`,
	`EXCEPT must be followed by ALL, DISTINCT, or "\("`,
	`Natural join not supported`,
	`(INTERSECT|EXCEPT) ALL is not supported`,
	`LATERAL join is not supported`,
	`TABLE statements are not supported`,
	`\w+( OUTER)? JOIN must have an immediately following ON or USING clause`,
	`(USING|ON) clause cannot be used with CROSS JOIN`,
	`Quantified comparisons \([^)]*\) are not supported`,
	`Query without FROM clause cannot (have an?|use) `,
	`SELECT \* must have a FROM clause`,
	`Analytic function not allowed in SELECT without FROM clause`,
	`Each function argument is an expression, not a query`,
	`LIMIT ALL is not supported`,
	`(LIMIT|OFFSET) expects an integer literal or parameter`,
	`(LIMIT|OFFSET) must not be null`,
	`The GROUP BY clause only supports (ROLLUP|CUBE|GROUPING SETS) when there are no other grouping elements`,
	`GROUP BY \(\) is only allowed when there are no other grouping items`,
	`Struct constructors are not supported within group by keys`,
	`(Starting|Ending) window framing expression cannot be `,
	`Window framing expression must be a literal or parameter`,
	`Window framing expression cannot (evaluate to a negative value|be NULL)`,
	`If a window has a PARTITION BY, it cannot reference a named window`,
	`Invalid interval literal\. Expected INTERVAL keyword to be followed by an INT64 expression or STRING literal`,
	`WHERE clause is not supported inside Window function call`,
	`Units for SYSTEM sampling must be PERCENT`,
	`Unsupported sampling method `,
	`PIVOT expression must specify an alias`,
	`The INTERVAL keyword followed by an integer expression can only specify a single datetime field`,
	`COLLATE is not supported`,
	`Invalid (hex )?integer literal`,
	`The LIKE ANY\|SOME\|ALL operator does not support subquery expression as patterns`,
	`(EXISTS|IN) subquery is not supported inside join predicate`,
	`Expected keyword `,
	`Invalid identifier after SELECT WITH`,
	`Unexpected identifier `,
	`Statement not supported: `,
	`\S+ is not a supported object type`,
	`\S+ is not a supported nested object type`,
	`CHECK (CONSTRAINT is|constraints are) not supported`,
	`Enforcement of (foreign|primary) keys is not supported`,
	`Unsupported generated column expression`,
	`Transaction modes are not supported`,
)

var bqAccepts = messageTable(
	// missing objects, names and parameters
	`Not found: `,
	`Table "[^"]*" must be qualified with a dataset`,
	`Unrecognized name: `,
	`Unrecognized window alias `,
	`Function not found: `,
	`Table-valued function not found: `,
	`Procedure is not found: `,
	`Type not found: `,
	`Already Exists: `,
	`Access Denied: `,
	`Cannot DROP (DEFAULT value|NOT NULL constraint) from column `,
	`Name .* not found inside `,
	`Cannot access field `,
	`Column name .* is ambiguous`,
	`Duplicate table alias `,
	`Name .* in GROUP BY clause is ambiguous`,
	`Positional parameters are not supported`,
	// types
	`Could not cast `,
	`Invalid cast from `,
	`Argument \* can only be used in COUNT\(\*\)`,
	`A SELECT WITH DIFFERENTIAL_PRIVACY query must `,
	`WHERE clause not supported in \w+ with `,
	`'FOR SYSTEM_TIME AS OF' expression for table `,
	`.* argument of .* should be of type `,
	`Column .* of type .* cannot be used as `,
	`Values referenced in FROM clause must be arrays`,
	`Array elements of types .* do not have a common supertype`,
	`Field name .* does not exist in `,
	`Invalid [A-Z]+ literal`,
	`Bitwise operator \S+ requires `,
	`Subscript access using \[\w+\] is not supported on values of type `,
	`\w+ does not support type parameters`,
	`Timestamp precision type parameter is not supported`,
	`No matching signature `,
	`.* is not defined for arguments of type `,
	`ORDER BY does not support expressions of type `,
	`Operand of IS (NOT )?UNKNOWN must be coercible to BOOL`,
	`Operands of NOT cannot be literal NULL`,
	`WHERE clause should return type BOOL`,
	`Column \d+ in .* has incompatible types`,
	`Column .* of type .* cannot be used in `,
	`Cannot execute IN subquery with uncomparable types`,
	`Grouping by expressions of type .* is not allowed`,
	`Scalar subquery cannot have more than one column`,
	`Subquery of type IN must have only one output column`,
	`Duplicate column names in the result are not supported`,
	`Invalid field name `,
	// what a function is, or which columns are grouped
	`SELECT list expression references .* which is neither grouped nor aggregated`,
	`The HAVING clause only allows aggregation if GROUP BY or SELECT list aggregation is present`,
	`The QUALIFY clause requires analytic function`,
	`Aggregate function .* does not support ORDER BY in arguments`,
	`Aggregate function .* not allowed in `,
	`TABLESAMPLE is not allowed with array scans`,
	// recursive CTEs, whose rules depend on which CTE refers to which
	`A query containing a recursive reference must not use `,
	`A recursive reference from inside an expression subquery is not allowed`,
	`A subquery containing a recursive reference must not contain `,
	`Multiple recursive references to table `,
	`Recursive query does not have the form `,
	`Recursive reference is not allowed in non-recursive UNION term`,
	`Table '.*' must not be recursively referenced from inside an inner recursive query`,
	`Unsupported WITH entry dependency cycle`,
	`Column '.*' is introduced by WITH`,
	`Function \w+ has both having expression and group by list`,
	`COUNT\(\*\) cannot be used with DISTINCT`,
	`Unsupported having modifier in the aggregate function`,
	`Window framing expression (has type|for ROWS can only be of integer type)`,
	`FORMAT is not allowed for cast from`,
	// which side of a join a subquery reads, and whether a name in it is a
	// table or a CTE (a CTE is allowed)
	`Subquery in join predicate should (depend on one of join sides|only depend on exactly one join side)`,
	`Unsupported subquery with table in join predicate`,
	// INTERVAL's date part, read as a name
	`Unsupported interval datetime field `,
	`A valid date part name is required`,
	`Invalid INTERVAL value `,
	// values: projects, connections, options, timestamps, lengths
	`Invalid project ID `,
	`Unknown project ID`,
	`Connection name should conform to the pattern`,
	`Error validating procedure body`,
	`Invalid time travel timestamp`,
	`Table '.*' is referenced with multiple snapshot read timestamps`,
	`\S+ is not a valid option for `,
	`STRING\(\d+\) has maximum length`,
	`Field offset \d+ is out of bounds`,
	`Operands of LIKE cannot be literal NULL`,
	`Search query parser error`,
	`Unsupported remote model endpoint`,
	`Location specified in query .* is not consistent with current execution region`,
	// the request: a temporary function needs a query after it in one script
	`CREATE TEMPORARY FUNCTION statements must be followed by an actual query`,
	// the sandbox project
	`Use of CREATE TEMPORARY TABLE requires a script or session`,
)

// bqPosition is the source position at the end of a message.
var bqPosition = regexp.MustCompile(` at \[\d+:\d+\]$`)

func messageTable(patterns ...string) *regexp.Regexp {
	return regexp.MustCompile(`^(` + strings.Join(patterns, `|`) + `)`)
}

// bqAnswer is what BigQuery said about one statement, as cached.
type bqAnswer struct {
	Mode    string   `json:"mode"` // "dry" or "run"
	SQL     string   `json:"sql"`
	Day     string   `json:"day"`
	OK      bool     `json:"ok,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	Message string   `json:"message,omitempty"`
	Type    string   `json:"type,omitempty"`   // statementType
	Schema  []string `json:"schema,omitempty"` // name:TYPE
	Tables  []string `json:"tables,omitempty"` // referenced tables, dataset.table
	Rows    string   `json:"rows,omitempty"`   // run only
}

type bigQuery struct {
	project string
	client  *http.Client

	mu      sync.Mutex
	token   string
	tokenAt time.Time
	cache   map[string]bqAnswer
	cacheW  *os.File
	unknown map[string]string // message -> one statement that got it
	days    map[string]bool   // days of the answers that were used
}

func newBigQuery(cachePath string) *bigQuery {
	out, err := exec.Command("gcloud", "config", "get-value", "project").Output()
	project := strings.TrimSpace(string(out))
	if err != nil || project == "" {
		log.Fatal("BigQuery needs gcloud logged in, with a current project (gcloud config set project ...)")
	}
	b := &bigQuery{project: project, client: &http.Client{Timeout: 2 * time.Minute},
		cache: map[string]bqAnswer{}, unknown: map[string]string{}, days: map[string]bool{}}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		log.Fatal(err)
	}
	if f, err := os.Open(cachePath); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 1<<26)
		for sc.Scan() {
			var a bqAnswer
			if json.Unmarshal(sc.Bytes(), &a) == nil {
				b.cache[a.Mode+"\x00"+a.SQL] = a
			}
		}
		f.Close()
	}
	if b.cacheW, err = os.OpenFile(cachePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err != nil {
		log.Fatal(err)
	}
	return b
}

func (b *bigQuery) accessToken() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.token == "" || time.Since(b.tokenAt) > 30*time.Minute {
		out, err := exec.Command("gcloud", "auth", "print-access-token").Output()
		if err != nil {
			log.Fatal("gcloud auth print-access-token failed; run gcloud auth login")
		}
		b.token, b.tokenAt = strings.TrimSpace(string(out)), time.Now()
	}
	return b.token
}

type bqError struct {
	Error struct {
		Message string `json:"message"`
		Errors  []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
	} `json:"error"`
}

// post sends body and decodes the reply into out. A refused request comes back
// as reason and message; transient failures are retried.
func (b *bigQuery) post(path string, body, out any) (reason, message string) {
	data, _ := json.Marshal(body)
	for try := 0; ; try++ {
		req, _ := http.NewRequest("POST", bqAPI+b.project+path, bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+b.accessToken())
		req.Header.Set("Content-Type", "application/json")
		resp, err := b.client.Do(req)
		if err == nil {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				if err := json.Unmarshal(raw, out); err != nil {
					log.Fatalf("BigQuery reply: %v", err)
				}
				return "", ""
			}
			var e bqError
			_ = json.Unmarshal(raw, &e)
			if len(e.Error.Errors) > 0 {
				reason = e.Error.Errors[0].Reason
			}
			message = e.Error.Message
			retry := resp.StatusCode == 429 || resp.StatusCode >= 500 ||
				reason == "rateLimitExceeded" || reason == "backendError" || reason == "internalError"
			if !retry {
				return reason, message
			}
		}
		if try == 6 {
			log.Fatalf("BigQuery keeps failing: %v %s %s", err, reason, message)
		}
		time.Sleep(time.Duration(1<<try) * time.Second)
	}
}

type bqField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type bqTableRef struct {
	DatasetID string `json:"datasetId"`
	TableID   string `json:"tableId"`
}

func (b *bigQuery) remember(a bqAnswer) bqAnswer {
	a.Day = time.Now().UTC().Format("2006-01-02")
	line, _ := json.Marshal(a)
	b.mu.Lock()
	b.cache[a.Mode+"\x00"+a.SQL] = a
	b.cacheW.Write(append(line, '\n'))
	b.mu.Unlock()
	return a
}

func (b *bigQuery) cached(mode, sql string) (bqAnswer, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a, ok := b.cache[mode+"\x00"+sql]
	return a, ok
}

// dry dry-runs sql with the sample dataset as default dataset.
func (b *bigQuery) dry(sql string) bqAnswer {
	if a, ok := b.cached("dry", sql); ok {
		return a
	}
	body := map[string]any{
		"configuration": map[string]any{"dryRun": true, "query": map[string]any{
			"query": sql, "useLegacySql": false,
			"defaultDataset": map[string]string{"projectId": b.project, "datasetId": bqDataset},
		}},
		"jobReference": map[string]string{"location": bqLocation},
	}
	var job struct {
		Statistics struct {
			Query struct {
				StatementType    string                     `json:"statementType"`
				Schema           struct{ Fields []bqField } `json:"schema"`
				ReferencedTables []bqTableRef               `json:"referencedTables"`
			} `json:"query"`
		} `json:"statistics"`
	}
	a := bqAnswer{Mode: "dry", SQL: sql}
	if a.Reason, a.Message = b.post("/jobs", body, &job); a.Reason == "" && a.Message == "" {
		q := job.Statistics.Query
		a.OK, a.Type = true, q.StatementType
		for _, f := range q.Schema.Fields {
			a.Schema = append(a.Schema, f.Name+":"+f.Type)
		}
		for _, t := range q.ReferencedTables {
			a.Tables = append(a.Tables, t.DatasetID+"."+t.TableID)
		}
	}
	return b.remember(a)
}

// run executes sql. It is only used for constant expressions.
func (b *bigQuery) run(sql string) bqAnswer {
	if a, ok := b.cached("run", sql); ok {
		return a
	}
	var res struct {
		JobComplete bool                       `json:"jobComplete"`
		Schema      struct{ Fields []bqField } `json:"schema"`
		Rows        json.RawMessage            `json:"rows"`
	}
	a := bqAnswer{Mode: "run", SQL: sql}
	body := map[string]any{"query": sql, "useLegacySql": false, "maximumBytesBilled": "1",
		"timeoutMs": 60000, "location": bqLocation}
	if a.Reason, a.Message = b.post("/queries", body, &res); a.Reason == "" && a.Message == "" {
		a.OK = res.JobComplete
		for _, f := range res.Schema.Fields {
			a.Schema = append(a.Schema, f.Name+":"+f.Type)
		}
		a.Rows = string(res.Rows)
	}
	return b.remember(a)
}

// setUp creates the sample dataset if needed, and the sample tables.
func (b *bigQuery) setUp() {
	var ignored map[string]any
	body := map[string]any{"datasetReference": map[string]string{"projectId": b.project, "datasetId": bqDataset},
		"location": bqLocation, "description": "sqlp parser oracle: sample tables for dry runs"}
	if reason, msg := b.post("/datasets", body, &ignored); reason != "" && reason != "duplicate" {
		log.Fatalf("creating dataset %s: %s %s", bqDataset, reason, msg)
	}
	for _, ddl := range bqTables {
		q := map[string]any{"query": ddl, "useLegacySql": false, "location": bqLocation, "timeoutMs": 60000,
			"defaultDataset": map[string]string{"projectId": b.project, "datasetId": bqDataset}}
		if reason, msg := b.post("/queries", q, &ignored); reason != "" || msg != "" {
			log.Fatalf("creating sample table: %s %s\n%s", reason, msg, ddl)
		}
	}
}

// verdict classifies a dry run. known is false for a message that is in
// neither table; rejects then records it for review.
func verdict(a bqAnswer) (reject, known bool) {
	switch {
	case a.OK:
		return false, true
	case a.Reason == "billingNotEnabled", strings.HasPrefix(a.Message, "Billing has not been enabled"):
		// DML in the sandbox, checked after parsing and before analysis; in a
		// script the whole script parsed.
		return false, true
	case bqRejects.MatchString(a.Message):
		return true, true
	case bqAccepts.MatchString(a.Message):
		return false, true
	}
	return false, false
}

func (b *bigQuery) rejects(sql string) bool {
	a := b.dry(sql)
	reject, known := verdict(a)
	b.mu.Lock()
	b.days[a.Day] = true
	if msg := bqPosition.ReplaceAllString(a.Message, ""); !known {
		if _, ok := b.unknown[msg]; !ok {
			b.unknown[msg] = sql
		}
	}
	b.mu.Unlock()
	return reject
}

// sameValue runs both constant queries and compares types and values. Rows
// come in no fixed order unless the query has ORDER BY, so they are compared
// as a sorted list.
func (b *bigQuery) sameValue(sql, shape string) bool {
	x, y := b.run(sql), b.run(shape)
	return x.OK && y.OK && slices.Equal(sortedRows(x.Rows), sortedRows(y.Rows)) && slices.Equal(x.Schema, y.Schema)
}

func sortedRows(rows string) []string {
	var list []json.RawMessage
	_ = json.Unmarshal([]byte(rows), &list)
	out := make([]string, len(list))
	for i, r := range list {
		var buf bytes.Buffer
		_ = json.Compact(&buf, r)
		out[i] = buf.String()
	}
	slices.Sort(out)
	return out
}

// sameMeaning dry-runs both statements: the parenthesized form must resolve
// to the same statement type, result columns and tables. Grouping that changes
// a type or a name shows; grouping that changes only values does not, which is
// what sameValue is for.
func (b *bigQuery) sameMeaning(sql, shape string) bool {
	x, y := b.dry(sql), b.dry(strings.TrimSuffix(shape, ";"))
	// BigQuery lists the referenced tables in no fixed order.
	return x.OK && y.OK && x.Type == y.Type && slices.Equal(x.Schema, y.Schema) &&
		slices.Equal(slices.Sorted(slices.Values(x.Tables)), slices.Sorted(slices.Values(y.Tables)))
}

// checkable reports whether BigQuery can tell anything about a statement's
// grouping: it must resolve, and a constant query must also evaluate.
func (b *bigQuery) checkable(sql string) bool {
	x := b.dry(sql)
	// The sample tables are recreated on every run, so FOR SYSTEM_TIME AS OF
	// a moment ago can fall before they existed.
	if !x.OK || strings.Contains(strings.ToUpper(sql), "SYSTEM_TIME") {
		return false
	}
	return x.Type != "SELECT" || len(x.Tables) > 0 || bqVolatile.MatchString(sql) || b.run(sql).OK
}

// bqVolatile finds functions whose value changes from one run to the next;
// such queries are compared by what they resolve to, not by their values.
var bqVolatile = regexp.MustCompile(`(?i)\b(CURRENT_(DATE|DATETIME|TIME|TIMESTAMP)|RAND|GENERATE_UUID|NEW_KEYSET|SESSION_USER|AI\.\w+)\b`)

// sameQuery checks a curated statement: by its values when it reads no table,
// so execution is free, otherwise by what it resolves to.
func (b *bigQuery) sameQuery(sql, shape string) bool {
	if x := b.dry(sql); x.OK && x.Type == "SELECT" && len(x.Tables) == 0 && !bqVolatile.MatchString(sql) {
		return b.sameValue(sql, shape)
	}
	return b.sameMeaning(sql, shape)
}

// reportUnknown prints the messages nobody has classified yet and reports
// whether there were any.
func (b *bigQuery) reportUnknown() bool {
	if len(b.unknown) == 0 {
		return false
	}
	fmt.Printf("bigquery: %d messages are in neither bqRejects nor bqAccepts; classify them and run again:\n", len(b.unknown))
	for _, msg := range slices.Sorted(func(yield func(string) bool) {
		for m := range b.unknown {
			if !yield(m) {
				return
			}
		}
	}) {
		fmt.Printf("  %s\n      e.g. %s\n", oneLine(msg), oneLine(b.unknown[msg]))
	}
	return true
}

// dayRange is the span of days on which the verdicts used were taken.
func (b *bigQuery) dayRange() string {
	days := slices.Sorted(func(yield func(string) bool) {
		for d := range b.days {
			if !yield(d) {
				return
			}
		}
	})
	if len(days) == 0 {
		return "none"
	}
	if days[0] == days[len(days)-1] {
		return days[0]
	}
	return days[0] + " to " + days[len(days)-1]
}

// probe prints what BigQuery says about each statement of a file: one per
// line, or separated by lines holding only ----.
func (b *bigQuery) probe(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	text := strings.TrimSpace(string(raw))
	var list []string
	if strings.Contains(text, "\n----\n") {
		list = strings.Split(text, "\n----\n")
	} else {
		list = strings.Split(text, "\n")
	}
	answers := make([]bqAnswer, len(list))
	idx := make([]string, len(list))
	for i := range list {
		idx[i] = fmt.Sprint(i)
	}
	parallel(idx, func(k string) bool {
		var i int
		fmt.Sscan(k, &i)
		answers[i] = b.dry(strings.TrimSpace(list[i]))
		return false
	})
	for i, a := range answers {
		reject, known := verdict(a)
		v := map[bool]string{true: "REJECT", false: "accept"}[reject]
		if !known {
			v = "UNKNOWN"
		}
		fmt.Printf("%-7s %s\n", v, oneLine(list[i]))
		switch {
		case a.OK:
			fmt.Printf("        %s %s %s\n", a.Type, strings.Join(a.Schema, ","), strings.Join(a.Tables, ","))
		default:
			fmt.Printf("        [%s] %s\n", a.Reason, oneLine(a.Message))
		}
	}
}

const bqDocsRoot = "https://docs.cloud.google.com/bigquery/docs/reference/standard-sql/"

// fetchDocs saves the SQL examples of every page of the BigQuery SQL
// reference as dir/docs_<page>.html: only the code blocks, which
// corpus.BigQueryDocStatements reads. Like the test suites of the other
// engines, the pages are untrusted data and stay outside the repository.
func fetchDocs(dir string) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}
	client := &http.Client{Timeout: time.Minute}
	get := func(url string) []byte {
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("User-Agent", "sqlp-corpusgen")
		resp, err := client.Do(req)
		if err != nil {
			log.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Fatalf("%s: %s", url, resp.Status)
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Fatal(err)
		}
		return data
	}
	link := regexp.MustCompile(`href="/bigquery/docs/reference/standard-sql/([a-z0-9_-]+)"`)
	block := regexp.MustCompile(`(?s)<pre[^>]*syntax="GoogleSQL"[^>]*>.*?</pre>`)
	seen := map[string]bool{}
	pages := []string{"query-syntax"}
	for _, m := range link.FindAllSubmatch(get(bqDocsRoot+"query-syntax"), -1) {
		if p := string(m[1]); !seen[p] && p != "query-syntax" {
			seen[p] = true
			pages = append(pages, p)
		}
	}
	blocks := 0
	for _, p := range pages {
		found := block.FindAll(get(bqDocsRoot+p), -1)
		blocks += len(found)
		if err := os.WriteFile(filepath.Join(dir, "docs_"+p+".html"), bytes.Join(found, []byte("\n")), 0o644); err != nil {
			log.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
	}
	fmt.Printf("bigquery docs: %d pages, %d code blocks, in %s\n", len(pages), blocks, dir)
}

// checkKeywords tries every keyword sqlp knows of, in any dialect, as an alias
// (SELECT 1 AS word): BigQuery refuses a reserved word there with a syntax
// error. It prints the words where dialect.BigQuery.Reserved disagrees and
// reports whether there were any.
func (b *bigQuery) checkKeywords() bool {
	words := map[string]bool{}
	for _, d := range dialect.All {
		for _, set := range []map[string]bool{d.Reserved, d.FuncNameKeywords, d.ColNameKeywords, d.AsOnlyLabels,
			d.TypeKeywords, d.SpecialFuncs, d.BareFuncs, d.NotAlias, d.TypedLiterals} {
			for w := range set {
				words[w] = true
			}
		}
	}
	for _, w := range strings.Fields(`QUALIFY PIVOT UNPIVOT OFFSET ORDINAL SAFE_OFFSET SAFE_ORDINAL REPLACE OPTIONS
		CLUSTER EXTEND AGGREGATE RENAME DROP CALL ASSERT EXPORT LOAD MODEL TABLE SYSTEM_TIME DIFFERENTIAL_PRIVACY
		AGGREGATION_THRESHOLD CORRESPONDING STRICT NAME UNNEST STRUCT ARRAY RANGE INTERVAL JSON GRAPH_TABLE MATCH_RECOGNIZE
		SAFE_CAST DECLARE BEGIN EXCEPTION RAISE RETURN LOOP WHILE REPEAT UNTIL ITERATE LEAVE BREAK CONTINUE ELSEIF
		EXECUTE IMMEDIATE TRANSACTION COMMIT ROLLBACK TRUNCATE MERGE MATCHED SOURCE TARGET TEMP TEMPORARY VIEW
		MATERIALIZED SCHEMA FUNCTION PROCEDURE RETURNS LANGUAGE CONNECTION FILES SNAPSHOT CLONE COPY POLICY ACCESS
		GRANT REVOKE FILTER INDEX SEARCH VECTOR`) {
		words[w] = true
	}
	list := slices.Sorted(maps.Keys(words))
	reserved := parallel(list, func(w string) bool {
		reject, _ := verdict(b.dry("SELECT 1 AS " + w))
		return reject
	})
	wrong := 0
	for i, w := range list {
		if reserved[i] != dialect.BigQuery.Reserved[w] {
			wrong++
			fmt.Printf("%-28s BigQuery: reserved=%v, dialect.BigQuery.Reserved=%v\n", w, reserved[i], dialect.BigQuery.Reserved[w])
		}
	}
	fmt.Printf("bigquery keywords: %d words tried, %d disagree\n", len(list), wrong)
	return wrong > 0
}

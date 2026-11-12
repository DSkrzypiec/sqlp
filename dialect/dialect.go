// Package dialect describes SQL dialects as data: lexical rules, reserved
// words, operator precedence and feature flags. The parser and generator
// consult these tables; they contain no parsing code themselves.
package dialect

import (
	"strings"

	"sqlp/lexer"
)

// Dialect is the full description of one SQL dialect.
type Dialect struct {
	Name  string
	Lexer lexer.Config

	// Reserved holds upper-cased keywords that can never be used as a bare
	// identifier (column, table, alias) without quoting.
	Reserved map[string]bool
	// FuncNameKeywords holds reserved words that may nevertheless be used as
	// a function or type name (PostgreSQL's type_func_name category).
	FuncNameKeywords map[string]bool
	// ColNameKeywords holds words usable as column names but not as function
	// or type names, unless the grammar has a dedicated production for them
	// (PostgreSQL's col_name category).
	ColNameKeywords map[string]bool
	// RawArgFuncs have argument syntax of their own (XML and JSON constructors);
	// their argument text is kept verbatim, checked only for balanced parens.
	RawArgFuncs map[string]bool
	// SpecialFuncs and TypeKeywords are the ColNameKeywords that do have one.
	SpecialFuncs map[string]bool
	TypeKeywords map[string]bool
	// NotAlias holds words that are valid names but never a bare alias
	// (SQLite's join keywords: `FROM a LEFT JOIN b`).
	NotAlias map[string]bool
	// AsOnlyLabels holds words that need AS to be a select-list label
	// (PostgreSQL's AS_LABEL keywords). Only used with Features.KeywordLabels.
	AsOnlyLabels map[string]bool
	// TypedLiterals, when set, holds the only type names that may come before a
	// string literal, as in DATE '2020-01-01' (BigQuery). Otherwise any type
	// name may (Features.Expr.TypedStringLiteral).
	TypedLiterals map[string]bool
	// BareFuncs holds keywords that are function calls without parentheses.
	BareFuncs map[string]bool
	// Functions holds the upper-cased names of the built-in functions, so
	// that Transpile can warn about a call the target does not have.
	Functions map[string]bool
	// FoldsToLower is true when unquoted identifiers fold to lower case.
	FoldsToLower bool
	// Names says how names in a query find the objects they refer to.
	Names NameRules

	// Precedence maps an infix operator (operator text or upper-cased keyword
	// such as "AND", "IS", "BETWEEN") to its binding power. Higher binds
	// tighter. Operators missing from the table use CustomOpPrecedence when
	// Lexer.CustomOperators is set, otherwise they are not infix operators.
	Precedence map[string]int
	// NonAssoc marks precedence levels whose operators cannot be chained.
	NonAssoc             map[int]bool
	CustomOpPrecedence   int
	NotPrecedence        int // prefix NOT
	UnaryMinusPrecedence int // prefix - and + (and ~ without custom operators)

	Features Features
}

// Features lists syntax that is only valid in some dialects, by area.
type Features struct {
	Expr    ExprFeatures
	Names   NameFeatures
	Query   QueryFeatures
	DML     DMLFeatures
	DDL     DDLFeatures
	Utility UtilityFeatures
}

// ExprFeatures lists expression syntax.
type ExprFeatures struct {
	DoubleColonCast      bool // x::type
	TryCast              bool // TRY_CAST(x AS t)
	ArrayLiterals        bool // ARRAY[1,2], a[1], a[1:2]
	RowConstructor       bool // ROW(a, b)
	DefaultExpr          bool // DEFAULT as a value in VALUES / SET
	Ilike                bool // ILIKE
	IsAsOperator         bool // a IS b for arbitrary b (SQLite)
	DistinctFromNonAssoc bool // a IS DISTINCT FROM b IS NULL is an error
	Overlaps             bool // (a, b) OVERLAPS (c, d)
	BetweenSymmetric     bool // BETWEEN [A]SYMMETRIC
	IsUnknown            bool // IS [NOT] UNKNOWN keyword form
	IsOtherPredicates    bool // IS [NOT] DOCUMENT, NORMALIZED, JSON (PostgreSQL)
	NotNullPostfix       bool // a NOT NULL as a predicate (SQLite)
	TypedStringLiteral   bool // DATE '2020-01-01', int4 '7'
	LikeEscape           bool // LIKE pattern ESCAPE char
	ChainedIn            bool // a IN (1) IN (2): IN ends with its own parenthesis (PostgreSQL)
	AggregateFilter      bool // f(x) FILTER (WHERE cond)
	AtTimeZone           bool
	NamedArgs            bool // f(a => 1)
	VariadicArgs         bool // f(VARIADIC arr)
	WithinGroup          bool
	StandardFuncSyntax   bool // EXTRACT(x FROM y), TRIM(BOTH ...), OVERLAY, POSITION(a IN b)
	FreeFormTypeNames    bool // any run of words is a type name (SQLite)
	AtParams             bool // @name, @`name`, @ name and @@system_variable (BigQuery)
	FreePostfix          bool // . and [ ] apply to any operand, not only names, parentheses and subqueries (BigQuery)
	BracketArrays        bool // [1, 2] is an array without the word ARRAY (BigQuery)
	AngleTypes           bool // ARRAY<T>, STRUCT<a T>, RANGE<T>, STRUCT(...) values, ARRAY<T>[...] (BigQuery)
	Extract              bool // EXTRACT(part FROM x) without StandardFuncSyntax; WEEK(MONDAY), AT TIME ZONE (BigQuery)
	SafeCast             bool // SAFE_CAST(x AS t) (BigQuery)
	CastFormat           bool // CAST(x AS t FORMAT 'fmt' [AT TIME ZONE tz]) (BigQuery)
	IntervalUnits        bool // INTERVAL expr unit, as in INTERVAL 5 DAY (BigQuery)
	LikeQuantified       bool // LIKE ANY|SOME|ALL (pattern, ...) (BigQuery)
	AggregateModifiers   bool // f(x IGNORE NULLS WHERE c HAVING MAX y ORDER BY z LIMIT n) (BigQuery)
	AggregateAll         bool // f(ALL x)
	TableArgs            bool // TABLE t and MODEL m as function arguments (BigQuery)
	MethodCalls          bool // (expr).F(args), chained calls for F(expr, args) (BigQuery)
	WithExprs            bool // WITH(a AS 1, b AS a + 1, a * b) (BigQuery)
	Int64Literals        bool // integer literals must fit INT64, -9223372036854775808 included (BigQuery)
	SimpleTypeNames      bool // a type is a name with optional (literals or MAX), none in CAST; no DOUBLE PRECISION, WITH TIME ZONE, INTERVAL DAY or [] (BigQuery)
}

// NameFeatures lists how names, labels and qualified names may be written.
type NameFeatures struct {
	KeywordLabels      bool // any keyword allowed after AS and after a dot
	QualifiedFunctions bool // schema.func(...) in expressions
	StringAlias        bool // string literals usable as names (SQLite)
	MaxNameParts       int  // parts allowed in an object name; 0 means no limit
	MaxRelationParts   int  // parts allowed in a table name (PostgreSQL's qualified_name); 0 means no limit
	KeywordsAfterDot   bool // any keyword as a later part of a dotted name, but not after AS (BigQuery)
}

// QueryFeatures lists query syntax.
type QueryFeatures struct {
	StrictFrames          bool // gram.y's window frame consistency checks
	ParenQueries          bool // (SELECT ...) UNION (SELECT ...)
	IntersectBindsTighter bool // false: all set operators share one level
	SetQuantifiers        bool // UNION DISTINCT, INTERSECT ALL, EXCEPT ALL
	SetQuantifierRequired bool // set operators must say DISTINCT, or ALL after UNION (BigQuery)
	UnmixedSetOps         bool // one chain repeats one set operator; another needs parentheses (BigQuery)
	EmptySelectList       bool // SELECT FROM t
	SelectInto            bool
	DistinctOn            bool
	Qualify               bool
	Lateral               bool
	TableSample           bool
	TableKeyword          bool // TABLE name as a query
	OnlyTable             bool // FROM ONLY t
	IndexedBy             bool // FROM t INDEXED BY idx / NOT INDEXED
	JoinRequiresQual      bool // [INNER|LEFT|...] JOIN must have ON/USING
	JoinUsingAlias        bool // JOIN ... USING (a) AS x (PostgreSQL)
	ParenTableIsJoin      bool // ( ... ) in FROM must hold a join, not a lone table
	ParenJoinOnly         bool // ( ... ) in FROM is itself a JOIN, not a comma list or another ( ... ), and has no alias (BigQuery)
	ParenQueryAlias       bool // (query) AS alias, a name for the query's rows in the pipe steps after it (BigQuery)
	ValuesTail            bool // VALUES ... ORDER BY / LIMIT
	WithOrdinality        bool // FROM f() WITH ORDINALITY
	FreeJoinKeywords      bool // up to three join keywords in any order, ON after a comma (SQLite)
	InTable               bool // x IN tbl, x IN f(args), x IN () (SQLite)
	GroupingSets          bool // ROLLUP, CUBE, GROUPING SETS, GROUP BY ()
	GroupByAll            bool // GROUP BY ALL, grouping by every non-aggregate output (BigQuery)
	Unnest                bool // FROM UNNEST(a) [alias] WITH OFFSET, x IN UNNEST(a), LIKE ANY UNNEST(a) (BigQuery)
	StarModifiers         bool // * EXCEPT (a, b), * REPLACE (expr AS a) (BigQuery)
	SelectAs              bool // SELECT AS STRUCT, SELECT AS VALUE (BigQuery)
	TrailingComma         bool // SELECT a, b, FROM t (BigQuery)
	SamplePercent         bool // TABLESAMPLE SYSTEM (n PERCENT) (BigQuery)
	SystemTime            bool // FROM t [AS x] FOR SYSTEM_TIME AS OF expr (BigQuery)
	Pipes                 bool // FROM t |> WHERE ... and |> after any query (BigQuery)
	SetByName             bool // [FULL|LEFT|INNER] UNION ALL BY NAME [ON (...)], [STRICT] CORRESPONDING [BY (...)] (BigQuery)
	SelectWith            bool // SELECT WITH DIFFERENTIAL_PRIVACY | AGGREGATION_THRESHOLD [OPTIONS (...)] (BigQuery)
	NestedQueryClauses    bool // (q ORDER BY a) ORDER BY b: each level keeps its own clauses (BigQuery)
	Pivot                 bool // FROM t PIVOT (...), UNPIVOT (...), also as pipe steps (BigQuery)
	OrderByUsing          bool // ORDER BY x USING <
	FetchFirst            bool // FETCH FIRST n ROWS ONLY
	OffsetWithoutLimit    bool // OFFSET without LIMIT, LIMIT ALL
	LimitComma            bool // LIMIT offset, count (SQLite)
	Locking               bool // FOR UPDATE / FOR SHARE
	DataModifyingCTE      bool // WITH x AS (DELETE ... RETURNING *)
	CTESearchCycle        bool
	CTEColumns            bool // WITH x (a, b) AS (...)
	CTEMaterialized       bool // WITH x AS [NOT] MATERIALIZED (...)
	ValuesQuery           bool // VALUES (...) as a query, not only as INSERT's source
	NaturalJoin           bool // NATURAL JOIN
	WindowGroups          bool // GROUPS frames in windows
	FrameExclusion        bool // EXCLUDE CURRENT ROW | GROUP | TIES | NO OTHERS in frames
	// BigQuery's rules:
	//   - FromlessRules: a SELECT without FROM has no WHERE, GROUP BY, HAVING,
	//     ORDER BY, * or window function;
	//   - GroupingSetsAlone: ROLLUP, CUBE, GROUPING SETS and () are alone in GROUP
	//     BY, ROLLUP and CUBE nest only in GROUPING SETS, () only there, and no
	//     grouping key is a tuple or STRUCT(...);
	//   - LimitLiterals: LIMIT and OFFSET take an integer literal or a parameter;
	//   - WindowRules: a frame offset is a literal or a parameter, possibly cast,
	//     neither NULL nor negative, a window that names another one has no
	//     PARTITION BY, and WINDOW c AS b names another window without
	//     parentheses;
	//   - JoinOnRules: a join's ON has no IN or EXISTS subquery.
	FromlessRules     bool
	GroupingSetsAlone bool
	LimitLiterals     bool
	WindowRules       bool
	JoinOnRules       bool
}

// DMLFeatures lists INSERT, UPDATE, DELETE and MERGE syntax.
type DMLFeatures struct {
	Returning            bool
	InsertOr             bool // INSERT OR REPLACE ..., REPLACE INTO
	UpsertMulti          bool // several ON CONFLICT clauses (SQLite)
	ConflictOnConstraint bool // ON CONFLICT ON CONSTRAINT name
	BareDMLAlias         bool // UPDATE t x SET ... (no AS)
	DeleteUsing          bool
	OrderLimitInDML      bool // UPDATE/DELETE ... ORDER BY ... LIMIT (SQLite)
	Truncate             bool
	Merge                bool
	MergeBy              bool // NOT MATCHED BY TARGET|SOURCE, INSERT ROW; no DO NOTHING or OVERRIDING (BigQuery)
	OptionalDMLKeywords  bool // INSERT t, DELETE t, MERGE t: INTO and FROM may be left out (BigQuery)
	WithDML              bool // WITH ... INSERT|UPDATE|DELETE|MERGE
	InsertAlias          bool // INSERT INTO t AS x
	DefaultValues        bool // INSERT INTO t DEFAULT VALUES, also in MERGE
	SetTuples            bool // UPDATE t SET (a, b) = ...
	TruncateOptions      bool // TRUNCATE [TABLE] [ONLY] a, b [RESTART|CONTINUE IDENTITY] [CASCADE|RESTRICT]; without it TRUNCATE TABLE t
}

// DDLFeatures lists schema statement syntax.
type DDLFeatures struct {
	LooseReferences     bool // REFERENCES t: MATCH any name, ON INSERT, clauses in any order and repeated (SQLite)
	StoredGeneratedOnly bool // GENERATED ALWAYS AS (expr) must say STORED
	ExtendedDDL         bool // schemas, sequences, types, ALTER COLUMN, constraints in ALTER, ...
	MultiDrop           bool // DROP TABLE a, b
	IndexOpclass        bool // CREATE INDEX i ON t (a text_pattern_ops)
	WithoutRowid        bool // CREATE TABLE ... WITHOUT ROWID / STRICT
	Autoincrement       bool // column PRIMARY KEY AUTOINCREMENT
	ConflictClause      bool // constraint ON CONFLICT ROLLBACK|ABORT|... (SQLite)
	VirtualTables       bool // CREATE VIRTUAL TABLE
	TempTriggers        bool // CREATE TEMP TRIGGER
	// GoogleSQL is BigQuery's DDL grammar in place of the ones above:
	// OPTIONS (name = value) on objects, columns and fields, PARTITION BY
	// expr, CLUSTER BY, keys that are NOT ENFORCED, LIKE/COPY/CLONE sources,
	// external and snapshot tables, and the object kinds DROP takes.
	GoogleSQL bool
}

// UtilityFeatures lists transaction, session and maintenance statements.
type UtilityFeatures struct {
	TransactionModes bool // BEGIN ISOLATION LEVEL ..., START TRANSACTION, WORK, CHAIN
	TransactionKinds bool // BEGIN DEFERRED|IMMEDIATE|EXCLUSIVE
	SetShowReset     bool
	ExplainOptions   bool // EXPLAIN (ANALYZE, FORMAT JSON) and EXPLAIN ANALYZE
	ExplainQueryPlan bool // EXPLAIN QUERY PLAN
	Vacuum           bool
	VacuumInto       bool // VACUUM [schema] INTO 'file' (SQLite)
	Reindex          bool
	Pragma           bool
	AttachDetach     bool
	ServerUtilities  bool // GRANT, COPY, COMMENT ON, DO, CALL, LISTEN, PREPARE, LOCK, ...
}

// NameRules says how names in a query find the objects they refer to. Only
// query analysis (package analyze) uses them; the parser does not.
type NameRules struct {
	// IgnoreCase is true when names match regardless of ASCII case, quoted or
	// not (SQLite). Otherwise they match exactly, after FoldsToLower.
	IgnoreCase bool
	// CaseSensitiveObjects keeps IgnoreCase from applying to the names of
	// databases, schemas and objects, which then match exactly (BigQuery:
	// datasets and tables, but not columns, aliases or CTEs).
	CaseSensitiveObjects bool
	// QuotedPaths reads a quoted part of an object name that holds dots as
	// several parts: `project.dataset.table` (BigQuery). Empty pieces are
	// skipped, and pieces past the third stay in the object's name. Its
	// implicit alias is still the whole quoted part.
	QuotedPaths bool
	// SearchPath lists the schemas searched for an unqualified object name, in
	// order, when the caller gives none.
	SearchPath []string
	// ImplicitSchemas are searched before the search path unless it lists them
	// (PostgreSQL's pg_temp and pg_catalog).
	ImplicitSchemas []string
	// DefaultSchema holds objects that name no schema. Empty means the first
	// schema of the search path.
	DefaultSchema string
	// TempSchema holds temporary objects.
	TempSchema string
	// CTEsAlwaysRecursive is true when every CTE of a WITH clause can refer to
	// itself and to every other one, as if RECURSIVE were written (SQLite).
	// Otherwise a CTE sees only the ones before it unless RECURSIVE is given.
	CTEsAlwaysRecursive bool
	// NamedExprs is true when an output column without an alias is named
	// after what it computes: its function, the type it is cast to, or a word
	// such as case (PostgreSQL's FigureColname). Otherwise such a column is
	// named after its text (SQLite), which analysis does not try to match;
	// in both, a column reference keeps the column's name.
	NamedExprs bool
	// UsingColumnsFirst is true when the columns of JOIN ... USING come first
	// among the join's columns (PostgreSQL). Otherwise each stays where the
	// left side has it, and the right side's copy is dropped (SQLite).
	UsingColumnsFirst bool
	// SystemColumns are columns that every table has without listing them,
	// such as PostgreSQL's ctid or SQLite's rowid. SELECT * leaves them out.
	SystemColumns []string
}

// HasFunction reports whether upper-cased name is a built-in function.
func (d *Dialect) HasFunction(name string) bool {
	return d.Functions[name] || d.SpecialFuncs[name] || d.BareFuncs[name]
}

// IsReserved reports whether upper-cased word kw cannot be a bare identifier.
func (d *Dialect) IsReserved(kw string) bool { return d.Reserved[kw] }

// InfixPrecedence returns the binding power of an infix operator token
// (operator text or upper-cased keyword) and whether it is one.
func (d *Dialect) InfixPrecedence(op string) (int, bool) {
	if p, ok := d.Precedence[op]; ok {
		return p, true
	}
	if d.Lexer.CustomOperators && IsOperatorText(op) {
		return d.CustomOpPrecedence, true
	}
	return 0, false
}

// IsOperatorText reports whether s consists only of operator characters.
func IsOperatorText(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte("+-*/<>=~!@#%^&|`?", s[i]) < 0 {
			return false
		}
	}
	return true
}

// All lists the built-in dialects.
var All = []*Dialect{Postgres, SQLite, BigQuery}

// ByName returns a built-in dialect by (case-insensitive) name.
func ByName(name string) (*Dialect, bool) {
	switch strings.ToLower(name) {
	case "postgres", "postgresql", "pg":
		return Postgres, true
	case "sqlite", "sqlite3":
		return SQLite, true
	case "bigquery", "bq":
		return BigQuery, true
	}
	return nil, false
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

func union(sets ...map[string]bool) map[string]bool {
	m := map[string]bool{}
	for _, s := range sets {
		for k := range s {
			m[k] = true
		}
	}
	return m
}

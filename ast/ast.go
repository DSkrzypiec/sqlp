// Package ast defines the syntax tree shared by all dialects.
//
// There is one node type per construct regardless of dialect (sqlglot's
// "superset AST"): a PostgreSQL `x::int` and a SQLite `CAST(x AS INT)` are both
// a *Cast, differing only in Cast.Style. Dialect-only constructs (QUALIFY,
// INSERT OR REPLACE, PRAGMA, ...) get their own nodes or fields here instead
// of dialect-specific trees, which is what makes transpiling possible.
//
// Nodes are plain structs with exported fields. Optional children are
// pointers or nil interfaces; lists are slices.
package ast

// Node is implemented by every syntax tree node.
type Node interface{ node() }

// Expr is a scalar or row-valued expression.
type Expr interface {
	Node
	expr()
}

// Statement is a top-level SQL statement.
type Statement interface {
	Node
	stmt()
}

// QueryBody is the set-operation-level body of a query:
// a Select, a SetOp, a Values list, a ParenQuery or a TableBody.
type QueryBody interface {
	Node
	queryBody()
}

// TableExpr is an item of a FROM clause.
type TableExpr interface {
	Node
	tableExpr()
}

// ============================================================================
// Names and literals
// ============================================================================

// Ident is a single identifier. Quoted records whether it was written with
// quotes, which affects case folding and how it is printed.
type Ident struct {
	Name    string
	Quoted  bool
	Unicode bool // written U&"..."
	UEscape string
}

// Name is a possibly qualified identifier chain: a, a.b, a.b.c.
// It is used for tables, functions, types and — in expression position — for
// column references.
type Name struct {
	Parts []Ident
}

// Last returns the final (unqualified) part.
func (n *Name) Last() Ident { return n.Parts[len(n.Parts)-1] }

// Star is `*` or `t.*`. Except and Replace are BigQuery's
// * EXCEPT (a, b) and * REPLACE (expr AS a).
type Star struct {
	Qualifier *Name
	Except    []Ident
	Replace   []*SelectItem
}

// LiteralKind classifies a Literal.
type LiteralKind uint8

const (
	Null LiteralKind = iota
	True
	False
	Integer
	Float
	StringLit // Value is the logical string content
	BlobLit   // Value is the raw text, e.g. X'0F'
	BytesLit  // Value is the decoded content of a bytes literal, b'...' (BigQuery)
)

// Literal is a constant. For StringLit, Escape marks a PostgreSQL E'...'
// string whose Value is kept raw (backslash escapes untouched), and Unicode a
// U&'...' string, whose optional UESCAPE character is in UEscape.
type Literal struct {
	Kind    LiteralKind
	Value   string
	Escape  bool
	Unicode bool
	UEscape string
}

// TypedString is `DATE '2020-01-01'`, `INTERVAL '1 day'`, `int4 '7'`.
type TypedString struct {
	Type  *DataType
	Value string
}

// Raise is SQLite's RAISE(IGNORE) or RAISE(action, message).
type Raise struct {
	Action  string // IGNORE, ROLLBACK, ABORT, FAIL
	Message Expr
}

// Param is a bind parameter: $1, ?, ?2, :name, @name.
type Param struct {
	Text string
}

// Default is the DEFAULT keyword in VALUES / SET.
type Default struct{}

// Word is a bare word used as a value rather than as an identifier:
// `SET x TO on`, `PRAGMA journal_mode = WAL`, `EXPLAIN (FORMAT json)`.
type Word struct {
	Text string
}

// ============================================================================
// Operators
// ============================================================================

// Unary is a prefix operator: -, +, NOT, ~, @ ...
type Unary struct {
	Op   string
	Expr Expr
}

// Binary is any infix operator, including AND/OR, comparisons, || and
// dialect-specific operators (PostgreSQL @>, ->>, ...).
// The right operand may be a *Quantified for `= ANY (...)` forms.
type Binary struct {
	Op    string
	Left  Expr
	Right Expr
}

// Quantified is `ANY (subquery|array)`, `ALL (...)`, `SOME (...)` as the
// right operand of a comparison.
type Quantified struct {
	Kind   string    // ANY, ALL, SOME
	Expr   Expr      // *Subquery or array-valued expression
	List   []Expr    // LIKE ANY ('a%', 'b%'), when Expr is nil (BigQuery)
	Unnest *FuncCall // LIKE ANY UNNEST(patterns), when Expr is nil (BigQuery)
}

// IsKind classifies an Is predicate.
type IsKind uint8

const (
	IsNull IsKind = iota
	IsTrue
	IsFalse
	IsUnknown
	IsDistinctFrom
	IsOther // IS JSON ..., IS DOCUMENT, IS [form] NORMALIZED; Text holds the words
)

// Is is `x IS [NOT] NULL|TRUE|FALSE|UNKNOWN|DISTINCT FROM y`.
// It also represents the postfix ISNULL / NOTNULL forms.
type Is struct {
	Expr  Expr
	Not   bool
	Kind  IsKind
	Other Expr // for IsDistinctFrom
	Text  string
}

// Between is `x [NOT] BETWEEN [SYMMETRIC] low AND high`.
type Between struct {
	Expr      Expr
	Not       bool
	Symmetric bool
	Low, High Expr
}

// In is `x [NOT] IN (list)` or `x [NOT] IN (subquery)`.
type In struct {
	Expr     Expr
	Not      bool
	List     []Expr
	Subquery *Query
	Table    *Name     // SQLite: x IN tbl
	Func     *FuncCall // SQLite: x IN table_function(args)
}

// Like is `x [NOT] LIKE|ILIKE|SIMILAR TO|GLOB|REGEXP|MATCH pattern [ESCAPE e]`.
type Like struct {
	Expr    Expr
	Not     bool
	Op      string // LIKE, ILIKE, SIMILAR TO, GLOB, REGEXP, MATCH
	Pattern Expr
	Escape  Expr
}

// Collate is `x COLLATE name`.
type Collate struct {
	Expr      Expr
	Collation *Name
}

// AtTimeZone is `x AT TIME ZONE zone` (PostgreSQL).
type AtTimeZone struct {
	Expr Expr
	Zone Expr
}

// Subscript is `arr[i]` or `arr[lo:hi]` (PostgreSQL).
type Subscript struct {
	Expr  Expr
	Index Expr // for a[i]
	Slice bool
	Lower Expr // for a[lo:hi]; either may be nil
	Upper Expr
}

// ============================================================================
// Compound expressions
// ============================================================================

// Paren is an explicitly parenthesized expression. It is kept in the tree so
// output is faithful to the input.
type Paren struct {
	Expr Expr
}

// Tuple is a row constructor `(a, b)` or `ROW(a, b)`.
type Tuple struct {
	Exprs []Expr
	Row   bool // written with the ROW keyword
}

// Array is `ARRAY[a, b]` or `ARRAY(subquery)` (PostgreSQL).
type Array struct {
	Elems    []Expr
	Subquery *Query
	Nested   bool      // an inner [a, b] of a multi-dimensional literal
	Of       *DataType // the element type of ARRAY<T>[...] (BigQuery)
}

// Struct builds a STRUCT value (BigQuery): STRUCT(1 AS a, 'x'), or with a
// type, STRUCT<a INT64, b STRING>(1, 'x').
type Struct struct {
	Type   *DataType // STRUCT<...>, or nil
	Fields []*StructValue
}

// StructValue is one value of a STRUCT constructor; As names its field.
type StructValue struct {
	Expr Expr
	As   *Ident
}

// CastStyle is how a cast was written.
type CastStyle uint8

const (
	CastStandard    CastStyle = iota // CAST(x AS t)
	CastDoubleColon                  // x::t
	CastTry                          // TRY_CAST(x AS t)
	CastSafe                         // SAFE_CAST(x AS t) (BigQuery)
)

// Cast converts an expression to a type.
type Cast struct {
	Expr  Expr
	Type  *DataType
	Style CastStyle
	// Format and TimeZone are CAST(x AS STRING FORMAT 'YYYY' AT TIME ZONE 'UTC')
	// (BigQuery); nil when absent.
	Format, TimeZone Expr
}

// Case is a CASE expression. Operand is nil for the searched form.
type Case struct {
	Operand Expr
	Whens   []*When
	Else    Expr
}

// When is one WHEN ... THEN ... arm.
type When struct {
	Cond   Expr
	Result Expr
}

// Exists is `EXISTS (subquery)`.
type Exists struct {
	Query *Query
}

// Subquery is a parenthesized query used as a scalar/row expression.
type Subquery struct {
	Query *Query
}

// FuncCall is a function or aggregate call, possibly with a window.
type FuncCall struct {
	Name        *Name
	Args        []Expr
	Distinct    bool
	OrderBy     []*OrderItem // ORDER BY inside the argument list
	WithinGroup []*OrderItem // WITHIN GROUP (ORDER BY ...)
	Filter      Expr         // FILTER (WHERE ...)
	Over        *WindowSpec  // OVER (...) or OVER name
	NoParens    bool         // CURRENT_DATE, CURRENT_USER, ...
	RawArgs     *string      // argument text kept verbatim, for RawArgFuncs
	// BigQuery's aggregate modifiers: IGNORE or RESPECT NULLS, HAVING MAX|MIN,
	// and LIMIT after the ORDER BY.
	Nulls  string // "IGNORE", "RESPECT" or ""
	Having *HavingBound
	Limit  Expr
}

// HavingBound is HAVING MAX expr or HAVING MIN expr in an aggregate call.
type HavingBound struct {
	Max  bool
	Expr Expr
}

// NamedArg is `name => value` (or `name := value`) in a call.
type NamedArg struct {
	Name  Ident
	Value Expr
}

// Extract is `EXTRACT(field FROM expr)`. The field is a word or a quoted name
// (Field), or a string literal (FieldString, PostgreSQL).
type Extract struct {
	Field       Ident
	FieldString *Literal
	Expr        Expr
	FieldArg    *Ident // EXTRACT(WEEK(MONDAY) FROM d) (BigQuery)
	TimeZone    Expr   // EXTRACT(HOUR FROM ts AT TIME ZONE 'UTC') (BigQuery)
}

// Interval is BigQuery's INTERVAL expr unit, as in INTERVAL 5 DAY. A string
// with its fields, INTERVAL '1-2' YEAR TO MONTH, is a TypedString.
type Interval struct {
	Value Expr
	Unit  string
}

// Substring is `SUBSTRING(x FROM a FOR b)`; From/For may be nil.
type Substring struct {
	Expr    Expr
	From    Expr
	For     Expr
	Similar Expr // SUBSTRING(x SIMILAR p ESCAPE e)
	Escape  Expr
}

// Trim is `TRIM([LEADING|TRAILING|BOTH] [chars FROM] x)`.
// The strings are a list in PostgreSQL: TRIM(a, b, c), TRIM(FROM a, b).
type Trim struct {
	Where string // LEADING, TRAILING, BOTH or ""
	Chars Expr   // before FROM
	From  bool
	Exprs []Expr
}

// Field is field selection on a composite value: `(x).a`, `(x).*`.
type Field struct {
	Expr Expr
	Name Ident
	Star bool
}

// Overlay is `OVERLAY(x PLACING y FROM a [FOR b])`.
type Overlay struct {
	Expr    Expr
	Placing Expr
	From    Expr
	For     Expr
}

// Position is `POSITION(sub IN str)`.
type Position struct {
	Substr Expr
	Str    Expr
}

// ============================================================================
// Types
// ============================================================================

// TimeZoneSpec records WITH/WITHOUT TIME ZONE on time types.
type TimeZoneSpec uint8

const (
	TZUnspecified TimeZoneSpec = iota
	TZWith
	TZWithout
)

// DataType is a (possibly parameterized, possibly array) type name.
// Name keeps the words as written joined by single spaces, e.g.
// "integer", "DOUBLE PRECISION", "character varying", "UNSIGNED BIG INT".
type DataType struct {
	Setof bool // SETOF int (PostgreSQL)
	Name  string
	Args  []Expr // (10, 2), (255)
	// ArrayBounds has one entry per dimension (PostgreSQL): "" for [], "3" for [3].
	ArrayBounds []string
	TimeZone    TimeZoneSpec
	Fields      string // INTERVAL qualifier: YEAR, DAY TO SECOND(3), ...
	// Angle marks a type written with angle brackets (BigQuery): ARRAY<T> and
	// RANGE<T> have Of, STRUCT<a INT64, STRING> has Struct, and STRUCT<> none.
	Angle  bool
	Of     *DataType
	Struct []*StructField
}

// StructField is one field of a STRUCT type. Name is empty when unnamed.
type StructField struct {
	Name Ident
	Type *DataType
}

// ============================================================================
// Queries
// ============================================================================

// Query is a full query: optional WITH, a body, and the trailing clauses that
// apply to the whole set operation.
type Query struct {
	With    *With
	Body    QueryBody
	OrderBy []*OrderItem
	Limit   *Limit
	Locking []*Locking
}

// With is a WITH [RECURSIVE] clause.
type With struct {
	Recursive bool
	CTEs      []*CTE
}

// CTE is one common table expression.
type CTE struct {
	Name         Ident
	Columns      []Ident
	Materialized *bool     // nil = unspecified
	Query        *Query    // nil when DML is set
	DML          Statement // PostgreSQL data-modifying CTE
	Search       *CTESearch
	Cycle        *CTECycle
}

// CTESearch is SEARCH {DEPTH|BREADTH} FIRST BY cols SET col.
type CTESearch struct {
	Breadth bool
	By      []Ident
	Set     Ident
}

// CTECycle is CYCLE cols SET col [TO v DEFAULT d] USING path.
type CTECycle struct {
	Columns []Ident
	Set     Ident
	To      Expr
	Default Expr
	Using   Ident
}

// DistinctKind is the SELECT [ALL|DISTINCT] modifier.
type DistinctKind uint8

const (
	DistinctNone DistinctKind = iota
	DistinctAll
	Distinct
	DistinctOn
)

// Select is a single SELECT ... FROM ... block.
type Select struct {
	Distinct   DistinctKind
	DistinctOn []Expr
	Items      []*SelectItem
	Into       *IntoTable // PostgreSQL SELECT INTO
	From       []TableExpr
	Where      Expr
	GroupBy    *GroupBy
	Having     Expr
	Windows    []*NamedWindow
	Qualify    Expr   // Snowflake/DuckDB-style; parsed when the dialect allows it
	As         string // SELECT AS STRUCT or AS VALUE (BigQuery): "STRUCT", "VALUE" or ""
}

// IntoTable is the target of SELECT ... INTO [TEMP|UNLOGGED] [TABLE] name.
type IntoTable struct {
	Temp     bool
	Unlogged bool
	Name     *Name
}

// SelectItem is one projection with an optional alias.
type SelectItem struct {
	Expr  Expr
	Alias *Ident
}

// GroupBy is a GROUP BY clause. Items may contain *GroupingSet.
type GroupBy struct {
	Distinct bool
	All      bool
	Items    []Expr
}

// GroupingSet is ROLLUP(...), CUBE(...), GROUPING SETS(...), or the empty
// grouping set `()` (Kind == "" with no items).
type GroupingSet struct {
	Kind  string // ROLLUP, CUBE, GROUPING SETS, ""
	Items []Expr
}

// SetOpKind is UNION, INTERSECT or EXCEPT.
type SetOpKind uint8

const (
	Union SetOpKind = iota
	Intersect
	Except
)

// SetOp combines two query bodies.
type SetOp struct {
	Op          SetOpKind
	All         bool
	Left, Right QueryBody
}

// Values is a VALUES (...), (...) list.
type Values struct {
	Rows [][]Expr
}

// ParenQuery is a parenthesized query appearing where a body is expected,
// e.g. `(SELECT 1 ORDER BY 1) UNION (SELECT 2)`.
type ParenQuery struct {
	Query *Query
}

// TableBody is PostgreSQL's `TABLE name` shorthand.
type TableBody struct {
	Name *Name
}

// OrderItem is one ORDER BY element.
type OrderItem struct {
	Expr      Expr
	Direction string // ASC, DESC or ""
	Nulls     string // FIRST, LAST or ""
	Using     string // PostgreSQL ORDER BY x USING <
}

// Limit holds LIMIT/OFFSET/FETCH.
type Limit struct {
	Count    Expr // nil when absent or LIMIT ALL
	All      bool // LIMIT ALL
	Offset   Expr
	Fetch    bool // written as FETCH FIRST/NEXT ... ROWS ONLY
	WithTies bool
}

// Locking is FOR UPDATE / FOR SHARE ... (PostgreSQL).
type Locking struct {
	Strength string // UPDATE, NO KEY UPDATE, SHARE, KEY SHARE, READ ONLY
	Of       []*Name
	Wait     string // NOWAIT, SKIP LOCKED or ""
}

// NamedWindow is one `name AS (spec)` in a WINDOW clause.
type NamedWindow struct {
	Name Ident
	Spec *WindowSpec
}

// WindowSpec is a window definition. When only Name is set and NameOnly is
// true it was written as `OVER name` rather than `OVER (name)`.
type WindowSpec struct {
	Name        *Ident // base window
	NameOnly    bool
	PartitionBy []Expr
	OrderBy     []*OrderItem
	Frame       *Frame
}

// Frame is a window frame clause.
type Frame struct {
	Units   string // ROWS, RANGE, GROUPS
	Start   *FrameBound
	End     *FrameBound // nil when written without BETWEEN
	Exclude string      // CURRENT ROW, GROUP, TIES, NO OTHERS or ""
}

// FrameBoundKind classifies a frame bound.
type FrameBoundKind uint8

const (
	CurrentRow FrameBoundKind = iota
	UnboundedPreceding
	UnboundedFollowing
	Preceding // Expr PRECEDING
	Following // Expr FOLLOWING
)

// FrameBound is one end of a frame.
type FrameBound struct {
	Kind FrameBoundKind
	Expr Expr
}

// ============================================================================
// FROM clause
// ============================================================================

// Alias is `[AS] name [(col [type], ...)]`.
type Alias struct {
	Name    Ident
	Columns []*ColumnDef // Type may be nil
}

// TableRef is a plain table reference.
type TableRef struct {
	Name    *Name
	Only    bool // PostgreSQL ONLY
	Alias   *Alias
	Indexed *Indexed     // SQLite INDEXED BY / NOT INDEXED
	Sample  *TableSample // PostgreSQL TABLESAMPLE
}

// TableSample is TABLESAMPLE method (args) [REPEATABLE (seed)].
type TableSample struct {
	Method     *Name
	Args       []Expr
	Repeatable Expr
}

// Indexed is SQLite's `INDEXED BY name` (Name set) or `NOT INDEXED` (Not).
type Indexed struct {
	Not  bool
	Name Ident
}

// SubqueryRef is `[LATERAL] (query) [alias]`.
type SubqueryRef struct {
	Lateral bool
	Query   *Query
	Alias   *Alias
}

// FuncRef is `[LATERAL] func(...) [WITH ORDINALITY] [alias]`. With RowsFrom
// set it is `ROWS FROM (f(...), g(...))`, and Func is nil.
type FuncRef struct {
	Expr            Expr // a function in special syntax, such as CAST (x AS int)
	Lateral         bool
	Func            *FuncCall
	RowsFrom        []*FuncCall
	RowsFromColumns [][]*ColumnDef // parallel to RowsFrom: AS (a int, ...)
	Ordinality      bool
	Alias           *Alias
	// Offset is BigQuery's UNNEST(...) [alias] WITH OFFSET [[AS] name].
	Offset      bool
	OffsetAlias *Ident
}

// JoinKind classifies a join.
type JoinKind uint8

const (
	InnerJoin JoinKind = iota
	LeftJoin
	RightJoin
	FullJoin
	CrossJoin
)

// Join is `left [NATURAL] kind JOIN right [ON cond | USING (cols)]`.
type Join struct {
	Kind    JoinKind
	Natural bool
	Left    TableExpr
	Right   TableExpr
	On      Expr
	Using   []Ident
	Comma   bool // SQLite: FROM a, b ON cond
	// UsingAlias is `JOIN ... USING (a) AS x` (PostgreSQL 14).
	UsingAlias *Ident
}

// ParenTable is a parenthesized join tree with an optional alias.
type ParenTable struct {
	Expr  TableExpr
	Alias *Alias
}

// IsOpaque reports whether any part of s was kept as raw text.
func IsOpaque(s Statement) bool {
	switch st := s.(type) {
	case *Opaque:
		return true
	case *AlterTable:
		for _, a := range st.Actions {
			if _, ok := a.(*AlterOpaque); ok {
				return true
			}
		}
	}
	return false
}

// IsJoinTree reports whether t is a join, possibly inside unaliased parentheses.
func IsJoinTree(t TableExpr) bool {
	switch t := t.(type) {
	case *Join:
		return !t.Comma
	case *ParenTable:
		return t.Alias == nil && IsJoinTree(t.Expr)
	}
	return false
}

// ============================================================================
// DML
// ============================================================================

// Insert is INSERT / REPLACE.
type Insert struct {
	With          *With
	Or            string // SQLite INSERT OR REPLACE|IGNORE|...; "" if none
	Replace       bool   // SQLite REPLACE INTO
	Table         *Name
	Alias         *Ident
	Columns       []Expr // *Name or, in PostgreSQL, *Subscript / dotted *Name
	Overriding    string // PostgreSQL OVERRIDING SYSTEM|USER VALUE
	Source        *Query // nil when DefaultValues
	DefaultValues bool
	OnConflict    []*OnConflict
	Returning     []*SelectItem
}

// OnConflict is one ON CONFLICT clause (SQLite allows several).
type OnConflict struct {
	Target      []*IndexElem // ON CONFLICT (cols)
	TargetWhere Expr
	Constraint  *Ident // ON CONFLICT ON CONSTRAINT name
	DoNothing   bool
	Updates     []*Assignment // DO UPDATE SET ...
	Where       Expr
}

// Assignment is `col = value` or `(c1, c2) = (v1, v2)` / `(c1, c2) = (subquery)`.
// Targets are *Name or, in PostgreSQL, *Subscript (`SET a[1] = 2`).
type Assignment struct {
	Targets []Expr
	Paren   bool // written as (c1, ...) = ..., even with one target
	Value   Expr
}

// Update is an UPDATE statement.
type Update struct {
	With      *With
	Or        string // SQLite UPDATE OR ...
	Only      bool
	Table     *Name
	Alias     *Ident
	Indexed   *Indexed
	Set       []*Assignment
	From      []TableExpr
	Where     Expr
	Returning []*SelectItem
	OrderBy   []*OrderItem // SQLite
	Limit     *Limit       // SQLite
}

// Delete is a DELETE statement.
type Delete struct {
	With      *With
	Only      bool
	Table     *Name
	Alias     *Ident
	Indexed   *Indexed
	Using     []TableExpr
	Where     Expr
	Returning []*SelectItem
	OrderBy   []*OrderItem // SQLite
	Limit     *Limit       // SQLite
}

// Truncate is PostgreSQL TRUNCATE.
type Truncate struct {
	Tables   []*TableRef // only Name and Only are used
	Identity string      // RESTART IDENTITY, CONTINUE IDENTITY or ""
	Behavior string      // CASCADE, RESTRICT or ""
}

// ============================================================================
// DDL
// ============================================================================

// CreateTable is CREATE TABLE.
type CreateTable struct {
	Temp         bool
	Unlogged     bool
	IfNotExists  bool
	Name         *Name
	HasColumns   bool // a parenthesized element list was written, maybe empty
	Columns      []*ColumnDef
	Constraints  []*TableConstraint
	Likes        []*TableLike
	PartitionOf  *Name
	OfType       *Name
	Bound        *PartitionBound
	Inherits     []*Name
	PartitionBy  *PartitionSpec
	Using        string
	With         []Option
	OnCommit     string // PRESERVE ROWS, DELETE ROWS, DROP
	Tablespace   string
	AsQuery      *Query
	AsExecute    *Execute // CREATE TABLE ... AS EXECUTE name
	WithData     *bool    // CREATE TABLE AS ... WITH [NO] DATA
	WithoutRowid bool     // SQLite
	Strict       bool     // SQLite
}

// Option is a generic `name [value]` storage or utility option.
type Option struct {
	Name  string
	Value Expr
}

// TableLike is LIKE source [INCLUDING|EXCLUDING what]...
type TableLike struct {
	Source   *Name
	Options  []string // "INCLUDING ALL", "EXCLUDING DEFAULTS", ...
	Position int      // number of columns written before it; LIKE order matters
}

// PartitionSpec is PARTITION BY {RANGE|LIST|HASH} (elems).
type PartitionSpec struct {
	Strategy string
	Elems    []*IndexElem
}

// PartitionBound is FOR VALUES ... or DEFAULT.
type PartitionBound struct {
	Default   bool
	In        []Expr
	From, To  []Expr
	Modulus   Expr
	Remainder Expr
}

// ColumnDef is a column definition.
type ColumnDef struct {
	Name        Ident
	Type        *DataType // nil in SQLite / CREATE TABLE AS
	Storage     string    // PostgreSQL STORAGE x
	Compression string    // PostgreSQL COMPRESSION x
	Options     []Option  // PostgreSQL OPTIONS (name 'value', ...) for a foreign table
	Constraints []*ColumnConstraint
}

// ColumnConstraintKind classifies a column constraint.
type ColumnConstraintKind uint8

const (
	NotNullConstraint ColumnConstraintKind = iota
	NullConstraint
	PrimaryKeyConstraint
	UniqueConstraint
	CheckConstraint
	DefaultConstraint
	ReferencesConstraint
	AttributeConstraint // a lone [NOT] DEFERRABLE ..., or SQLite's lone CONSTRAINT name
	GeneratedConstraint // GENERATED ALWAYS AS (expr) [STORED|VIRTUAL]
	IdentityConstraint  // GENERATED {ALWAYS|BY DEFAULT} AS IDENTITY
	CollateConstraint
)

// ColumnConstraint is one constraint attached to a column.
type ColumnConstraint struct {
	Name            *Ident
	Kind            ColumnConstraintKind
	Expr            Expr   // CHECK, DEFAULT, GENERATED
	Direction       string // SQLite PRIMARY KEY ASC|DESC
	Autoincrement   bool   // SQLite
	OnConflict      string // SQLite ON CONFLICT ROLLBACK|ABORT|...
	References      *References
	Generated       string // STORED, VIRTUAL, ALWAYS, BY DEFAULT
	Collation       *Name
	Deferrable      *Deferrable
	Identity        []SequenceOption // GENERATED ... AS IDENTITY (options)
	IndexWith       []Option         // PRIMARY KEY or UNIQUE WITH (storage parameters)
	IndexTablespace string           // USING INDEX TABLESPACE x
	NoInherit       bool             // CHECK ... NO INHERIT
	NullsNotDist    bool             // UNIQUE NULLS NOT DISTINCT
}

// References is a foreign key target.
type References struct {
	Table    *Name
	Columns  []Ident
	Match    string // FULL, PARTIAL, SIMPLE; in SQLite any name, as written
	OnDelete string // CASCADE, SET NULL, SET DEFAULT, RESTRICT, NO ACTION
	OnUpdate string
	OnInsert string // SQLite, which ignores it
}

// Deferrable is a constraint's timing: [NOT] DEFERRABLE, INITIALLY DEFERRED
// or IMMEDIATE, or both. PostgreSQL also takes INITIALLY alone or first.
type Deferrable struct {
	Deferrable     string // DEFERRABLE, NOT DEFERRABLE or ""
	Initially      string // DEFERRED, IMMEDIATE or ""
	InitiallyFirst bool   // written INITIALLY ... [NOT] DEFERRABLE
}

// TableConstraintKind classifies a table constraint.
type TableConstraintKind uint8

const (
	TablePrimaryKey TableConstraintKind = iota
	TableUnique
	TableCheck
	TableForeignKey
	TableExclude
	TableNameOnly // SQLite's lone CONSTRAINT name
)

// TableConstraint is a table-level constraint.
type TableConstraint struct {
	Name            *Ident
	Kind            TableConstraintKind
	Columns         []*IndexElem // PK/UNIQUE/FK columns
	Expr            Expr         // CHECK
	References      *References
	OnConflict      string // SQLite
	Deferrable      *Deferrable
	Include         []Ident
	IndexWith       []Option // WITH (storage parameters) of the index
	UsingIndex      string   // PRIMARY KEY USING INDEX x
	IndexTablespace string   // ... USING INDEX TABLESPACE x
	Autoincrement   bool     // SQLite PRIMARY KEY (x AUTOINCREMENT)
	NullsNotDist    bool
	NoInherit       bool
	NotValid        bool
	Method          string         // EXCLUDE USING method
	Excludes        []*ExcludeElem // EXCLUDE (elem WITH op, ...)
	Where           Expr           // EXCLUDE ... WHERE (pred)
}

// ExcludeElem is `index_elem WITH operator`.
type ExcludeElem struct {
	Elem *IndexElem
	Op   string
}

// IndexElem is a column or expression in an index or key list.
type IndexElem struct {
	Expr           Expr
	Collation      *Name
	Opclass        *Name
	OpclassOptions []Option // gist_trgm_ops(siglen = 32)
	Direction      string
	Nulls          string
}

// CreateIndex is CREATE INDEX.
type CreateIndex struct {
	Unique       bool
	Concurrently bool
	IfNotExists  bool
	Name         *Name // nil for PostgreSQL's unnamed form
	Table        *Name
	Only         bool
	Method       string // USING btree
	Columns      []*IndexElem
	Include      []Ident
	NullsNotDist bool
	With         []Option
	Tablespace   string
	Where        Expr
}

// CreateView is CREATE VIEW.
type CreateView struct {
	OrReplace    bool
	Temp         bool
	Unlogged     bool
	Materialized bool
	IfNotExists  bool
	Name         *Name
	Columns      []Ident
	Using        string // materialized views: table access method
	With         []Option
	Tablespace   string
	Query        *Query
	CheckOption  string // CASCADED, LOCAL, or "" ; "CHECK" for a bare WITH CHECK OPTION
	WithData     *bool
}

// CreateSchema is CREATE SCHEMA.
type CreateSchema struct {
	IfNotExists   bool
	Name          *Ident
	Authorization Expr // *Name or *Word
	Elements      []Statement
}

// CreateSequence is CREATE SEQUENCE with its options kept as written.
type CreateSequence struct {
	Temp        bool
	Unlogged    bool
	IfNotExists bool
	Name        *Name
	Options     []SequenceOption
}

// SequenceOption is `INCREMENT BY 2`, `START WITH 1`, `NO MAXVALUE`...
type SequenceOption struct {
	Name  string // INCREMENT BY, MINVALUE, MAXVALUE, START WITH, RESTART [WITH], CACHE, CYCLE, NO CYCLE, OWNED BY, SEQUENCE NAME, AS
	Value Expr
	Type  *DataType // AS
}

// CreateEnum is CREATE TYPE name AS ENUM (...).
type CreateEnum struct {
	Name   *Name
	Labels []string
}

// CreateExtension is PostgreSQL CREATE EXTENSION.
type CreateExtension struct {
	IfNotExists bool
	Name        Ident
	Options     []ExtensionOption // in the order written; a repeat is kept
}

// ExtensionOption is one of SCHEMA s, VERSION v or CASCADE.
type ExtensionOption struct {
	Schema  *Ident
	Version string // a word or a quoted string, as written
	Cascade bool
}

// Drop is DROP <object kind> ...
type Drop struct {
	Kind         string // TABLE, VIEW, MATERIALIZED VIEW, INDEX, SCHEMA, SEQUENCE, TYPE, DOMAIN, ...
	IfExists     bool
	Concurrently bool
	Names        []*Name
	Types        []*DataType // DROP TYPE and DROP DOMAIN name types: int[], double precision
	On           *Name       // DROP TRIGGER|RULE|POLICY name ON table
	Behavior     string      // CASCADE, RESTRICT or ""
}

// AlterTable is ALTER TABLE with one or more actions.
type AlterTable struct {
	IfExists bool
	Only     bool
	Name     *Name
	Actions  []AlterAction
}

// AlterAction is one ALTER TABLE action.
type AlterAction interface {
	Node
	alterAction()
}

// AddColumn is ADD [COLUMN] [IF NOT EXISTS] coldef.
type AddColumn struct {
	IfNotExists bool
	Column      *ColumnDef
}

// DropColumn is DROP [COLUMN] [IF EXISTS] name [CASCADE|RESTRICT].
type DropColumn struct {
	IfExists bool
	Name     Ident
	Behavior string
}

// RenameColumn is RENAME [COLUMN] old TO new.
type RenameColumn struct {
	Old, New Ident
}

// RenameTable is RENAME TO new.
type RenameTable struct {
	New Ident
}

// RenameConstraint is RENAME CONSTRAINT old TO new.
type RenameConstraint struct {
	Old, New Ident
}

// AlterColumnKind classifies ALTER [COLUMN] actions.
type AlterColumnKind uint8

const (
	SetDefault AlterColumnKind = iota
	DropDefault
	SetNotNull
	DropNotNull
	SetType
	AddIdentity
)

// AlterColumn is ALTER [COLUMN] name <action>.
type AlterColumn struct {
	Name      Ident
	Kind      AlterColumnKind
	Expr      Expr             // SET DEFAULT expr; SET DATA TYPE ... USING expr
	Type      *DataType        // SET DATA TYPE
	Collation *Name            // SET DATA TYPE ... COLLATE c
	Generated string           // ADD GENERATED: ALWAYS or BY DEFAULT
	Identity  []SequenceOption // ADD GENERATED ... AS IDENTITY (options)
}

// AddConstraint is ADD table_constraint [NOT VALID].
type AddConstraint struct {
	Constraint *TableConstraint
}

// DropConstraint is DROP CONSTRAINT [IF EXISTS] name [CASCADE|RESTRICT].
type DropConstraint struct {
	IfExists bool
	Name     Ident
	Behavior string
}

// SetSchema is SET SCHEMA name.
type SetSchema struct {
	Schema Ident
}

// ============================================================================
// Transactions and utility statements
// ============================================================================

// Begin is BEGIN / START TRANSACTION with its modes kept as upper-cased words.
type Begin struct {
	Modes []string // DEFERRED, IMMEDIATE, EXCLUSIVE, ISOLATION LEVEL ..., READ ONLY ...
}

// Commit is COMMIT / END [AND CHAIN].
type Commit struct {
	Chain bool
}

// Rollback is ROLLBACK [AND CHAIN | TO [SAVEPOINT] name].
type Rollback struct {
	To    *Ident
	Chain bool
}

// Savepoint is SAVEPOINT name.
type Savepoint struct {
	Name Ident
}

// ReleaseSavepoint is RELEASE [SAVEPOINT] name.
type ReleaseSavepoint struct {
	Name Ident
}

// Explain is EXPLAIN [ANALYZE] [VERBOSE] [(options)] stmt
// and SQLite's EXPLAIN [QUERY PLAN] stmt.
type Explain struct {
	Analyze   bool
	Verbose   bool
	QueryPlan bool
	Options   []Option
	Statement Statement
}

// Set is SET [SESSION|LOCAL] name {TO|=} value, ... (PostgreSQL).
type Set struct {
	Scope       string // SESSION, LOCAL or ""
	Name        *Name
	Values      []Expr
	Default     bool
	FromCurrent bool // SET name FROM CURRENT
	TimeZone    bool // written SET TIME ZONE, which also takes an interval
}

// Show is SHOW name (PostgreSQL).
type Show struct {
	Name *Name
	All  bool
}

// Reset is RESET name | RESET ALL (PostgreSQL).
type Reset struct {
	Name *Name
	All  bool
}

// Pragma is SQLite PRAGMA [schema.]name [= value | (value)].
type Pragma struct {
	Name  *Name
	Value Expr
	Call  bool // written as name(value)
}

// Attach is SQLite ATTACH [DATABASE] expr AS expr [KEY expr].
type Attach struct {
	Expr   Expr
	Schema Expr
	Key    Expr
}

// Detach is SQLite DETACH [DATABASE] expr.
type Detach struct {
	Schema Expr
}

// Vacuum is VACUUM with options and targets.
type Vacuum struct {
	Options []string // PostgreSQL bare form: FULL, FREEZE, VERBOSE, ANALYZE
	Paren   []Option // PostgreSQL VACUUM (option [value], ...)
	Tables  []*MaintTarget
	Schema  *Ident // SQLite VACUUM schema
	Into    Expr   // SQLite VACUUM INTO 'file'
}

// MaintTarget is `table [(cols)]` in VACUUM and ANALYZE.
type MaintTarget struct {
	Name    *Name
	Columns []Ident
}

// Analyze is ANALYZE [VERBOSE | (options)] [table [(cols)], ...].
type Analyze struct {
	Verbose bool
	Paren   []Option
	Tables  []*MaintTarget
}

// Reindex is REINDEX [(options)] [kind] [CONCURRENTLY] name.
type Reindex struct {
	Paren        []Option
	Kind         string // TABLE, INDEX, SCHEMA, DATABASE, SYSTEM or ""
	Concurrently bool
	Name         *Name
}

// ============================================================================
// Marker methods
// ============================================================================

func (*Name) node()        {}
func (*Star) node()        {}
func (*Literal) node()     {}
func (*TypedString) node() {}
func (*Param) node()       {}
func (*Default) node()     {}
func (*Word) node()        {}
func (*Raise) node()       {}
func (*Overlay) node()     {}
func (*Field) node()       {}
func (*Unary) node()       {}
func (*Binary) node()      {}
func (*Quantified) node()  {}
func (*Is) node()          {}
func (*Between) node()     {}
func (*In) node()          {}
func (*Like) node()        {}
func (*Collate) node()     {}
func (*AtTimeZone) node()  {}
func (*Subscript) node()   {}
func (*Paren) node()       {}
func (*Tuple) node()       {}
func (*Struct) node()      {}
func (*Interval) node()    {}
func (*Array) node()       {}
func (*Cast) node()        {}
func (*Case) node()        {}
func (*Exists) node()      {}
func (*Subquery) node()    {}
func (*FuncCall) node()    {}
func (*NamedArg) node()    {}
func (*Extract) node()     {}
func (*Substring) node()   {}
func (*Trim) node()        {}
func (*Position) node()    {}
func (*GroupingSet) node() {}

func (*Name) expr()        {}
func (*Star) expr()        {}
func (*Literal) expr()     {}
func (*TypedString) expr() {}
func (*Param) expr()       {}
func (*Default) expr()     {}
func (*Word) expr()        {}
func (*Raise) expr()       {}
func (*Overlay) expr()     {}
func (*Field) expr()       {}
func (*Unary) expr()       {}
func (*Binary) expr()      {}
func (*Quantified) expr()  {}
func (*Is) expr()          {}
func (*Between) expr()     {}
func (*In) expr()          {}
func (*Like) expr()        {}
func (*Collate) expr()     {}
func (*AtTimeZone) expr()  {}
func (*Subscript) expr()   {}
func (*Paren) expr()       {}
func (*Tuple) expr()       {}
func (*Struct) expr()      {}
func (*Interval) expr()    {}
func (*Array) expr()       {}
func (*Cast) expr()        {}
func (*Case) expr()        {}
func (*Exists) expr()      {}
func (*Subquery) expr()    {}
func (*FuncCall) expr()    {}
func (*NamedArg) expr()    {}
func (*Extract) expr()     {}
func (*Substring) expr()   {}
func (*Trim) expr()        {}
func (*Position) expr()    {}
func (*GroupingSet) expr() {}

func (*Query) node()      {}
func (*Select) node()     {}
func (*SetOp) node()      {}
func (*Values) node()     {}
func (*ParenQuery) node() {}
func (*TableBody) node()  {}

func (*Select) queryBody()     {}
func (*SetOp) queryBody()      {}
func (*Values) queryBody()     {}
func (*ParenQuery) queryBody() {}
func (*TableBody) queryBody()  {}

func (*TableRef) node()    {}
func (*SubqueryRef) node() {}
func (*FuncRef) node()     {}
func (*Join) node()        {}
func (*ParenTable) node()  {}

func (*TableRef) tableExpr()    {}
func (*SubqueryRef) tableExpr() {}
func (*FuncRef) tableExpr()     {}
func (*Join) tableExpr()        {}
func (*ParenTable) tableExpr()  {}

func (*Insert) node()           {}
func (*Update) node()           {}
func (*Delete) node()           {}
func (*Truncate) node()         {}
func (*CreateTable) node()      {}
func (*CreateIndex) node()      {}
func (*CreateView) node()       {}
func (*CreateSchema) node()     {}
func (*CreateSequence) node()   {}
func (*CreateEnum) node()       {}
func (*CreateExtension) node()  {}
func (*Drop) node()             {}
func (*AlterTable) node()       {}
func (*Begin) node()            {}
func (*Commit) node()           {}
func (*Rollback) node()         {}
func (*Savepoint) node()        {}
func (*ReleaseSavepoint) node() {}
func (*Explain) node()          {}
func (*Set) node()              {}
func (*Show) node()             {}
func (*Reset) node()            {}
func (*Pragma) node()           {}
func (*Attach) node()           {}
func (*Detach) node()           {}
func (*Vacuum) node()           {}
func (*Analyze) node()          {}
func (*Reindex) node()          {}

func (*Query) stmt()            {}
func (*Insert) stmt()           {}
func (*Update) stmt()           {}
func (*Delete) stmt()           {}
func (*Truncate) stmt()         {}
func (*CreateTable) stmt()      {}
func (*CreateIndex) stmt()      {}
func (*CreateView) stmt()       {}
func (*CreateSchema) stmt()     {}
func (*CreateSequence) stmt()   {}
func (*CreateEnum) stmt()       {}
func (*CreateExtension) stmt()  {}
func (*Drop) stmt()             {}
func (*AlterTable) stmt()       {}
func (*Begin) stmt()            {}
func (*Commit) stmt()           {}
func (*Rollback) stmt()         {}
func (*Savepoint) stmt()        {}
func (*ReleaseSavepoint) stmt() {}
func (*Explain) stmt()          {}
func (*Set) stmt()              {}
func (*Show) stmt()             {}
func (*Reset) stmt()            {}
func (*Pragma) stmt()           {}
func (*Attach) stmt()           {}
func (*Detach) stmt()           {}
func (*Vacuum) stmt()           {}
func (*Analyze) stmt()          {}
func (*Reindex) stmt()          {}

func (*AddColumn) node()        {}
func (*DropColumn) node()       {}
func (*RenameColumn) node()     {}
func (*RenameTable) node()      {}
func (*RenameConstraint) node() {}
func (*AlterColumn) node()      {}
func (*AddConstraint) node()    {}
func (*DropConstraint) node()   {}
func (*SetSchema) node()        {}

func (*AddColumn) alterAction()        {}
func (*DropColumn) alterAction()       {}
func (*RenameColumn) alterAction()     {}
func (*RenameTable) alterAction()      {}
func (*RenameConstraint) alterAction() {}
func (*AlterColumn) alterAction()      {}
func (*AddConstraint) alterAction()    {}
func (*DropConstraint) alterAction()   {}
func (*SetSchema) alterAction()        {}

// Non-expression, non-statement helper nodes.
func (*With) node()             {}
func (*CTE) node()              {}
func (*SelectItem) node()       {}
func (*GroupBy) node()          {}
func (*OrderItem) node()        {}
func (*Limit) node()            {}
func (*Locking) node()          {}
func (*NamedWindow) node()      {}
func (*WindowSpec) node()       {}
func (*Frame) node()            {}
func (*FrameBound) node()       {}
func (*Alias) node()            {}
func (*Indexed) node()          {}
func (*When) node()             {}
func (*DataType) node()         {}
func (*OnConflict) node()       {}
func (*Assignment) node()       {}
func (*ColumnDef) node()        {}
func (*ColumnConstraint) node() {}
func (*References) node()       {}
func (*Deferrable) node()       {}
func (*TableConstraint) node()  {}
func (*IndexElem) node()        {}

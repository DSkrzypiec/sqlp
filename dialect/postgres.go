package dialect

import "sqlp/lexer"

// The keyword tables below were generated from pg_get_keywords() on
// PostgreSQL 16 and checked by hand against src/include/parser/kwlist.h of
// 16.11 (471 keywords). No test checks them.

// pgReserved is the reserved_keyword category (catcode R).
var pgReserved = set(
	"ALL", "ANALYSE", "ANALYZE", "AND", "ANY", "ARRAY", "AS", "ASC", "ASYMMETRIC", "BOTH", "CASE",
	"CAST", "CHECK", "COLLATE", "COLUMN", "CONSTRAINT", "CREATE", "CURRENT_CATALOG",
	"CURRENT_DATE", "CURRENT_ROLE", "CURRENT_TIME", "CURRENT_TIMESTAMP", "CURRENT_USER", "DEFAULT",
	"DEFERRABLE", "DESC", "DISTINCT", "DO", "ELSE", "END", "EXCEPT", "FALSE", "FETCH", "FOR",
	"FOREIGN", "FROM", "GRANT", "GROUP", "HAVING", "IN", "INITIALLY", "INTERSECT", "INTO",
	"LATERAL", "LEADING", "LIMIT", "LOCALTIME", "LOCALTIMESTAMP", "NOT", "NULL", "OFFSET", "ON",
	"ONLY", "OR", "ORDER", "PLACING", "PRIMARY", "REFERENCES", "RETURNING", "SELECT",
	"SESSION_USER", "SOME", "SYMMETRIC", "SYSTEM_USER", "TABLE", "THEN", "TO", "TRAILING", "TRUE",
	"UNION", "UNIQUE", "USER", "USING", "VARIADIC", "WHEN", "WHERE", "WINDOW", "WITH",
)

// pgTypeFuncName is the type_func_name_keyword category (catcode T): reserved
// as column names, but allowed as function and type names.
var pgTypeFuncName = set(
	"AUTHORIZATION", "BINARY", "COLLATION", "CONCURRENTLY", "CROSS", "CURRENT_SCHEMA", "FREEZE",
	"FULL", "ILIKE", "INNER", "IS", "ISNULL", "JOIN", "LEFT", "LIKE", "NATURAL", "NOTNULL",
	"OUTER", "OVERLAPS", "RIGHT", "SIMILAR", "TABLESAMPLE", "VERBOSE",
)

// pgColName is the col_name_keyword category (catcode C).
var pgColName = set(
	"BETWEEN", "BIGINT", "BIT", "BOOLEAN", "CHAR", "CHARACTER", "COALESCE", "DEC", "DECIMAL",
	"EXISTS", "EXTRACT", "FLOAT", "GREATEST", "GROUPING", "INOUT", "INT", "INTEGER", "INTERVAL",
	"JSON_ARRAY", "JSON_ARRAYAGG", "JSON_OBJECT", "JSON_OBJECTAGG", "LEAST", "NATIONAL", "NCHAR",
	"NONE", "NORMALIZE", "NULLIF", "NUMERIC", "OUT", "OVERLAY", "POSITION", "PRECISION", "REAL",
	"ROW", "SETOF", "SMALLINT", "SUBSTRING", "TIME", "TIMESTAMP", "TREAT", "TRIM", "VALUES",
	"VARCHAR", "XMLATTRIBUTES", "XMLCONCAT", "XMLELEMENT", "XMLEXISTS", "XMLFOREST",
	"XMLNAMESPACES", "XMLPARSE", "XMLPI", "XMLROOT", "XMLSERIALIZE", "XMLTABLE",
)

// pgAsOnly lists the keywords with barelabel = false.
var pgAsOnly = set(
	"ARRAY", "AS", "CHAR", "CHARACTER", "CREATE", "DAY", "EXCEPT", "FETCH", "FILTER", "FOR",
	"FROM", "GRANT", "GROUP", "HAVING", "HOUR", "INTERSECT", "INTO", "ISNULL", "LIMIT", "MINUTE",
	"MONTH", "NOTNULL", "OFFSET", "ON", "ORDER", "OVER", "OVERLAPS", "PRECISION", "RETURNING",
	"SECOND", "TO", "UNION", "VARYING", "WHERE", "WINDOW", "WITH", "WITHIN", "WITHOUT", "YEAR",
)

// Postgres describes PostgreSQL 16.
var Postgres = &Dialect{
	Name: "postgres",
	Lexer: lexer.Config{
		IdentQuotes:        `"`,
		DollarQuotes:       true,
		EscapeStrings:      true,
		BlobLiterals:       true,
		BitStrings:         true,
		UnicodeStrings:     true,
		NationalStrings:    true,
		NoEmptyQuotedIdent: true,
		StringContinuation: true,
		RadixLiterals:      true,
		DigitSeparators:    true,
		RadixSeparator:     true,
		ParamPrefixes:      "$",
		CustomOperators:    true,
		NestedComments:     true,
		CommentEndsAtCR:    true,
	},
	Reserved:         union(pgReserved, pgTypeFuncName),
	FuncNameKeywords: pgTypeFuncName,
	ColNameKeywords:  pgColName,
	SpecialFuncs: set("COALESCE", "EXISTS", "EXTRACT", "GREATEST", "GROUPING", "LEAST", "NORMALIZE",
		"NULLIF", "OVERLAY", "POSITION", "ROW", "SUBSTRING", "TREAT", "TRIM"),
	RawArgFuncs: set("XMLCONCAT", "XMLELEMENT", "XMLEXISTS", "XMLFOREST", "XMLPARSE", "XMLPI", "XMLROOT",
		"XMLSERIALIZE", "XMLTABLE", "XMLNAMESPACES", "XMLATTRIBUTES", "JSON_ARRAY", "JSON_ARRAYAGG",
		"JSON_OBJECT", "JSON_OBJECTAGG", "TREAT"),
	TypeKeywords: set("BIGINT", "BIT", "BOOLEAN", "CHAR", "CHARACTER", "DEC", "DECIMAL", "FLOAT", "INT",
		"INTEGER", "INTERVAL", "NATIONAL", "NCHAR", "NUMERIC", "REAL", "SMALLINT", "TIME", "TIMESTAMP",
		"VARCHAR"),
	AsOnlyLabels: pgAsOnly,
	BareFuncs: set("CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP", "LOCALTIME", "LOCALTIMESTAMP",
		"CURRENT_USER", "SESSION_USER", "SYSTEM_USER", "CURRENT_ROLE", "CURRENT_CATALOG",
		"CURRENT_SCHEMA", "USER"),
	Functions:    pgFunctions,
	FoldsToLower: true,
	Names: NameRules{
		SearchPath:        []string{"public"}, // the default "$user", public, without a user
		ImplicitSchemas:   []string{"pg_temp", "pg_catalog"},
		TempSchema:        "pg_temp",
		NamedExprs:        true,
		UsingColumnsFirst: true,
		SystemColumns:     []string{"tableoid", "xmin", "cmin", "xmax", "cmax", "ctid"},
	},

	// From the %left/%nonassoc block of gram.y (PostgreSQL >= 9.5).
	Precedence: map[string]int{
		"OR": 1, "AND": 2,
		"IS": 4, "ISNULL": 4, "NOTNULL": 4,
		"<": 5, ">": 5, "=": 5, "<=": 5, ">=": 5, "<>": 5, "!=": 5,
		"BETWEEN": 6, "IN": 6, "LIKE": 6, "ILIKE": 6, "SIMILAR": 6,
		"ESCAPE":   7,
		"OPERATOR": 8, "||": 8, // generic operators share this level
		"+": 9, "-": 9,
		"*": 10, "/": 10, "%": 10,
		"^":       11,
		"AT":      12,
		"COLLATE": 13,
		"[":       15,
		"::":      16,
		".":       17,
	},
	NonAssoc:             map[int]bool{5: true, 6: true},
	CustomOpPrecedence:   8,
	NotPrecedence:        3,
	UnaryMinusPrecedence: 14,

	Features: Features{
		Expr: ExprFeatures{
			AggregateFilter: true, ChainedIn: true,
			LikeEscape:      true,
			DoubleColonCast: true, ArrayLiterals: true, RowConstructor: true, DefaultExpr: true,
			Ilike: true, IsUnknown: true, IsOtherPredicates: true, BetweenSymmetric: true, Overlaps: true,
			DistinctFromNonAssoc: true, TypedStringLiteral: true, AtTimeZone: true, NamedArgs: true,
			VariadicArgs: true, WithinGroup: true, StandardFuncSyntax: true,
		},
		Names: NameFeatures{
			KeywordLabels: true, QualifiedFunctions: true, MaxRelationParts: 3,
		},
		Query: QueryFeatures{
			CTEColumns: true, CTEMaterialized: true, ValuesQuery: true, NaturalJoin: true, WindowGroups: true,
			StrictFrames: true, ParenQueries: true, IntersectBindsTighter: true,
			SetQuantifiers: true, EmptySelectList: true, SelectInto: true, DistinctOn: true,
			Lateral: true, TableSample: true, TableKeyword: true, OnlyTable: true,
			JoinRequiresQual: true, JoinUsingAlias: true, ParenTableIsJoin: true, WithOrdinality: true, ValuesTail: true,
			GroupingSets: true, OrderByUsing: true, FetchFirst: true, OffsetWithoutLimit: true,
			Locking: true, DataModifyingCTE: true, CTESearchCycle: true,
		},
		DML: DMLFeatures{
			Returning: true, ConflictOnConstraint: true, BareDMLAlias: true, DeleteUsing: true,
			Truncate: true, Merge: true,
		},
		DDL: DDLFeatures{
			StoredGeneratedOnly: true, ExtendedDDL: true, MultiDrop: true, IndexOpclass: true,
		},
		Utility: UtilityFeatures{
			TransactionModes: true, SetShowReset: true, ExplainOptions: true, Vacuum: true,
			Reindex: true, ServerUtilities: true,
		},
	},
}

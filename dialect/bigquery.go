package dialect

import "sqlp/lexer"

// bigQueryReserved is the reserved keyword list of the BigQuery lexical
// reference (GoogleSQL for BigQuery, 2026-11-12), plus MATCH_RECOGNIZE, which
// BigQuery reserves though the list leaves it out. tools/corpusgen
// -bigquery-keywords tries every keyword sqlp knows against BigQuery.
var bigQueryReserved = set(
	"ALL", "AND", "ANY", "ARRAY", "AS", "ASC", "ASSERT_ROWS_MODIFIED", "AT", "BETWEEN", "BY",
	"CASE", "CAST", "COLLATE", "CONTAINS", "CREATE", "CROSS", "CUBE", "CURRENT", "DEFAULT",
	"DEFINE", "DESC", "DISTINCT", "ELSE", "END", "ENUM", "ESCAPE", "EXCEPT", "EXCLUDE", "EXISTS",
	"EXTRACT", "FALSE", "FETCH", "FOLLOWING", "FOR", "FROM", "FULL", "GRAPH_TABLE", "GROUP",
	"GROUPING", "GROUPS", "HASH", "HAVING", "IF", "IGNORE", "IN", "INNER", "INTERSECT", "INTERVAL",
	"INTO", "IS", "JOIN", "LATERAL", "LEFT", "LIKE", "LIMIT", "LOOKUP", "MATCH_RECOGNIZE", "MERGE", "NATURAL", "NEW",
	"NO", "NOT", "NULL", "NULLS", "OF", "ON", "OR", "ORDER", "OUTER", "OVER", "PARTITION",
	"PRECEDING", "PROTO", "QUALIFY", "RANGE", "RECURSIVE", "RESPECT", "RIGHT", "ROLLUP", "ROWS",
	"SELECT", "SET", "SOME", "STRUCT", "TABLESAMPLE", "THEN", "TO", "TREAT", "TRUE", "UNBOUNDED",
	"UNION", "UNNEST", "USING", "WHEN", "WHERE", "WINDOW", "WITH", "WITHIN",
)

// BigQuery describes GoogleSQL as BigQuery accepts it. BigQuery has no
// versions; the frozen corpus says on which day its verdicts were taken.
var BigQuery = &Dialect{
	Name: "bigquery",
	Lexer: lexer.Config{
		IdentQuotes:         "`",
		NoEmptyQuotedIdent:  true,
		BackslashEscapes:    true,
		DoubleQuotedStrings: true,
		PrefixedStrings:     true,
		AdjacentStrings:     true,
		HashComments:        true,
		ASCIIIdents:         true,
		CommentEndsAtCR:     true,
		PlainQuestionMarks:  true,
		ParamPrefixes:       "?", // @name is parsed: BigQuery allows `@ name`
		Operators: []string{
			"|>", "<<", ">>", "<=", ">=", "<>", "!=", "||", "=>", "@@",
			"=", "<", ">", "+", "-", "*", "/", "&", "|", "^", "~", "@",
		},
	},
	Reserved: bigQueryReserved,
	// Reserved words that are also functions, checked with dry runs.
	FuncNameKeywords: set("IF", "LEFT", "RIGHT", "GROUPING", "COLLATE", "RANGE"),
	// Checked with dry runs: columns, aliases and CTEs match in any case,
	// datasets and tables only in their own; unqualified names are in the
	// request's default dataset, which the empty schema name stands for;
	// `dataset.table` is a path; the columns of JOIN ... USING come first; a
	// CTE sees only the ones before it.
	Names: NameRules{
		IgnoreCase: true, CaseSensitiveObjects: true, QuotedPaths: true, SearchPath: []string{""},
		UsingColumnsFirst: true,
	},
	TypedLiterals: set("DATE", "DATETIME", "TIME", "TIMESTAMP", "NUMERIC", "BIGNUMERIC", "DECIMAL",
		"BIGDECIMAL", "JSON", "INTERVAL", "RANGE"),

	// From the operator precedence table of the BigQuery reference. Operators
	// of one level are left-associative, except comparisons, which do not
	// associate at all.
	Precedence: map[string]int{
		"OR": 1, "AND": 2,
		"=": 4, "<": 4, ">": 4, "<=": 4, ">=": 4, "!=": 4, "<>": 4,
		"LIKE": 4, "BETWEEN": 4, "IN": 4, "IS": 4,
		"|":  5,
		"^":  6,
		"&":  7,
		"<<": 8, ">>": 8,
		"+": 9, "-": 9,
		"*": 10, "/": 10, "||": 10,
		"[": 12, ".": 12,
	},
	NonAssoc:             map[int]bool{4: true},
	NotPrecedence:        3,
	UnaryMinusPrecedence: 11,

	Features: Features{
		Expr: ExprFeatures{
			ArrayLiterals: true, TypedStringLiteral: true, AtParams: true, Int64Literals: true,
			FreePostfix: true, BracketArrays: true, AngleTypes: true, Extract: true, SafeCast: true,
			CastFormat: true, IntervalUnits: true, LikeQuantified: true, IsUnknown: true, NamedArgs: true,
			AggregateModifiers: true, TableArgs: true, MethodCalls: true, WithExprs: true,
		},
		Names: NameFeatures{
			QualifiedFunctions: true, MaxRelationParts: 3, KeywordsAfterDot: true,
		},
		Query: QueryFeatures{
			ParenQueries: true, SetQuantifierRequired: true, UnmixedSetOps: true, Qualify: true,
			GroupingSets: true, GroupByAll: true, Unnest: true, StarModifiers: true, SelectAs: true,
			TrailingComma: true, TableSample: true, SamplePercent: true, SystemTime: true, Pipes: true,
			SetByName: true, JoinRequiresQual: true, FromlessRules: true, GroupingSetsAlone: true,
			LimitLiterals: true, SelectWith: true, NestedQueryClauses: true, Pivot: true,
		},
	},
}

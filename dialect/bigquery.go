package dialect

import "sqlp/lexer"

// bigQueryReserved is the reserved keyword list of the BigQuery lexical
// reference (GoogleSQL for BigQuery, 2026-11-12). tools/corpusgen
// -bigquery-keywords probes every word against BigQuery.
var bigQueryReserved = set(
	"ALL", "AND", "ANY", "ARRAY", "AS", "ASC", "ASSERT_ROWS_MODIFIED", "AT", "BETWEEN", "BY",
	"CASE", "CAST", "COLLATE", "CONTAINS", "CREATE", "CROSS", "CUBE", "CURRENT", "DEFAULT",
	"DEFINE", "DESC", "DISTINCT", "ELSE", "END", "ENUM", "ESCAPE", "EXCEPT", "EXCLUDE", "EXISTS",
	"EXTRACT", "FALSE", "FETCH", "FOLLOWING", "FOR", "FROM", "FULL", "GRAPH_TABLE", "GROUP",
	"GROUPING", "GROUPS", "HASH", "HAVING", "IF", "IGNORE", "IN", "INNER", "INTERSECT", "INTERVAL",
	"INTO", "IS", "JOIN", "LATERAL", "LEFT", "LIKE", "LIMIT", "LOOKUP", "MERGE", "NATURAL", "NEW",
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
		ParamPrefixes:       "?", // @name is parsed: BigQuery allows `@ name`
		Operators: []string{
			"<<", ">>", "<=", ">=", "<>", "!=", "||", "=>", "@@",
			"=", "<", ">", "+", "-", "*", "/", "&", "|", "^", "~", "@",
		},
	},
	Reserved: bigQueryReserved,
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
			FreePostfix: true, BracketArrays: true,
		},
		Names: NameFeatures{
			QualifiedFunctions: true, MaxRelationParts: 3,
		},
		Query: QueryFeatures{
			ParenQueries: true, SetQuantifierRequired: true, UnmixedSetOps: true, Qualify: true,
			GroupingSets: true,
		},
	},
}

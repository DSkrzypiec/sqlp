package dialect

import "sqlp/lexer"

// sqliteReserved lists the keywords SQLite rejects as a bare column or table
// name, determined by probing a real sqlite3 3.51 with every keyword. No test
// re-checks it. Words such as WINDOW, OVER, FILTER and LEFT are contextual,
// and IF only cannot be the name after CREATE ... [IF NOT EXISTS].
var sqliteReserved = set(
	"ADD", "ALL", "ALTER", "AND", "AS", "AUTOINCREMENT", "BETWEEN", "CASE", "CHECK", "COLLATE",
	"COMMIT", "CONSTRAINT", "CREATE", "DEFAULT", "DEFERRABLE", "DELETE", "DISTINCT", "DROP",
	"ELSE", "ESCAPE", "EXCEPT", "EXISTS", "FOREIGN", "FROM", "GROUP", "HAVING", "IN", "INDEX",
	"INSERT", "INTERSECT", "INTO", "IS", "ISNULL", "JOIN", "LIMIT", "NOT", "NOTHING", "NOTNULL",
	"NULL", "ON", "OR", "ORDER", "PRIMARY", "REFERENCES", "RETURNING", "SELECT", "SET", "TABLE",
	"THEN", "TO", "TRANSACTION", "UNION", "UNIQUE", "UPDATE", "USING", "VALUES", "WHEN", "WHERE",
)

// SQLite describes SQLite 3.
var SQLite = &Dialect{
	Name: "sqlite",
	Lexer: lexer.Config{
		IdentQuotes:      "\"`[",
		BlobLiterals:     true,
		DigitSeparators:  true,
		LaxBlockComments: true,
		StrictBlobs:      true,
		ParamPrefixes:    "?:@$#",
		TclParams:        true,
		BOMIsSpace:       true,
		Operators: []string{
			"->>", "->", "||", "<<", ">>", "<=", ">=", "<>", "!=", "==",
			"=", "<", ">", "+", "-", "*", "/", "%", "&", "|", "~",
		},
	},
	Reserved:         sqliteReserved,
	FuncNameKeywords: set("LIKE", "GLOB", "REGEXP", "MATCH"),
	NotAlias:         set("CROSS", "FULL", "INNER", "LEFT", "NATURAL", "OUTER", "RIGHT", "INDEXED"),
	BareFuncs:        set("CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP"),
	// From pragma_function_list of SQLite 3.51, without what the sqlite3
	// shell and optional extensions add (readfile, sha3, fts, rtree, ...).
	Functions: set(
		"ABS", "ACOS", "ACOSH", "ASIN", "ASINH", "ATAN", "ATAN2", "ATANH", "AVG", "CEIL", "CEILING",
		"CHANGES", "CHAR", "COALESCE", "CONCAT", "CONCAT_WS", "COS", "COSH", "COUNT", "CUME_DIST",
		"CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP", "DATE", "DATETIME", "DEGREES",
		"DENSE_RANK", "EXP", "FIRST_VALUE", "FLOOR", "FORMAT", "GLOB", "GROUP_CONCAT", "HEX", "IF",
		"IFNULL", "IIF", "INSTR", "JSON", "JSONB", "JSONB_ARRAY", "JSONB_EXTRACT", "JSONB_GROUP_ARRAY",
		"JSONB_GROUP_OBJECT", "JSONB_INSERT", "JSONB_OBJECT", "JSONB_PATCH", "JSONB_REMOVE",
		"JSONB_REPLACE", "JSONB_SET", "JSON_ARRAY", "JSON_ARRAY_LENGTH", "JSON_ERROR_POSITION",
		"JSON_EXTRACT", "JSON_GROUP_ARRAY", "JSON_GROUP_OBJECT", "JSON_INSERT", "JSON_OBJECT",
		"JSON_PATCH", "JSON_PRETTY", "JSON_QUOTE", "JSON_REMOVE", "JSON_REPLACE", "JSON_SET",
		"JSON_TYPE", "JSON_VALID", "JULIANDAY", "LAG", "LAST_INSERT_ROWID", "LAST_VALUE", "LEAD",
		"LENGTH", "LIKE", "LIKELIHOOD", "LIKELY", "LN", "LOAD_EXTENSION", "LOG", "LOG10", "LOG2",
		"LOWER", "LTRIM", "MAX", "MIN", "MOD", "NTH_VALUE", "NTILE", "NULLIF", "OCTET_LENGTH",
		"PERCENT_RANK", "PI", "POW", "POWER", "PRINTF", "QUOTE", "RADIANS", "RANDOM", "RANDOMBLOB",
		"RANK", "REPLACE", "ROUND", "ROW_NUMBER", "RTRIM", "SIGN", "SIN", "SINH",
		"SQLITE_COMPILEOPTION_GET", "SQLITE_COMPILEOPTION_USED", "SQLITE_SOURCE_ID", "SQLITE_VERSION",
		"SQRT", "STRFTIME", "STRING_AGG", "SUBSTR", "SUBSTRING", "SUM", "TAN", "TANH", "TIME",
		"TIMEDIFF", "TOTAL", "TOTAL_CHANGES", "TRIM", "TRUNC", "TYPEOF", "UNHEX", "UNICODE", "UNISTR",
		"UNISTR_QUOTE", "UNIXEPOCH", "UNLIKELY", "UPPER", "ZEROBLOB",
	),
	Names: NameRules{
		IgnoreCase: true,
		// Attached databases come after these, in attach order; a caller that
		// uses them passes its own search path.
		SearchPath:          []string{"temp", "main"},
		DefaultSchema:       "main",
		TempSchema:          "temp",
		CTEsAlwaysRecursive: true,
		// Every table except a WITHOUT ROWID one, which a catalog does not mark.
		SystemColumns: []string{"rowid", "oid", "_rowid_"},
	},

	// From the %left/%right block of parse.y.
	Precedence: map[string]int{
		"OR": 1, "AND": 2,
		"IS": 4, "ISNULL": 4, "NOTNULL": 4, "MATCH": 4, "LIKE": 4, "GLOB": 4,
		"REGEXP": 4, "BETWEEN": 4, "IN": 4, "=": 4, "==": 4, "<>": 4, "!=": 4,
		"<": 5, "<=": 5, ">": 5, ">=": 5,
		"ESCAPE": 6,
		"&":      7, "|": 7, "<<": 7, ">>": 7,
		"+": 8, "-": 8,
		"*": 9, "/": 9, "%": 9,
		"||": 10, "->": 10, "->>": 10,
		"COLLATE": 11,
	},
	NotPrecedence:        3,
	UnaryMinusPrecedence: 12,

	Features: Features{
		Expr: ExprFeatures{
			AggregateFilter: true,
			LikeEscape:      true,
			IsAsOperator:    true, NotNullPostfix: true, FreeFormTypeNames: true,
		},
		Names: NameFeatures{
			StringAlias: true, MaxNameParts: 2,
		},
		Query: QueryFeatures{
			CTEColumns: true, CTEMaterialized: true, ValuesQuery: true, NaturalJoin: true, WindowGroups: true,
			IndexedBy: true, LimitComma: true, FreeJoinKeywords: true, InTable: true,
		},
		DML: DMLFeatures{
			Returning: true, InsertOr: true, UpsertMulti: true, OrderLimitInDML: true,
		},
		DDL: DDLFeatures{
			LooseReferences: true, WithoutRowid: true, Autoincrement: true, ConflictClause: true,
			VirtualTables: true, TempTriggers: true,
		},
		Utility: UtilityFeatures{
			TransactionKinds: true, ExplainQueryPlan: true, Vacuum: true, VacuumInto: true,
			Reindex: true, Pragma: true, AttachDetach: true,
		},
	},
}

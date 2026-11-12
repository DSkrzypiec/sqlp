package sqlp

// bigqueryIdentity holds BigQuery SQL that must round-trip unchanged. Every
// statement was accepted by a BigQuery dry run (tools/corpusgen -bq-probe).
var bigqueryIdentity = []string{
	"SELECT 1",
	"SELECT a, b FROM t WHERE a = 1 ORDER BY b DESC LIMIT 10",
	"SELECT * FROM t LIMIT 1 OFFSET 2",
	"SELECT `a b` FROM t",
	"SELECT a FROM sqlp_oracle.t",
	"SELECT * FROM t AS x JOIN u AS y ON x.a = y.a",
	"SELECT CAST(a AS STRING) FROM t",
	"SELECT a FROM t UNION ALL SELECT a FROM u",
	"SELECT a FROM t UNION DISTINCT SELECT a FROM u",
	"SELECT a FROM t INTERSECT DISTINCT SELECT a FROM u",
	"SELECT a FROM t EXCEPT DISTINCT SELECT a FROM u",
	"SELECT 1 EXCEPT DISTINCT SELECT 2 EXCEPT DISTINCT SELECT 3",
	"SELECT 1 UNION ALL (SELECT 2 INTERSECT DISTINCT SELECT 3)",
	"(SELECT 1) UNION ALL (SELECT 2)",
	"SELECT a FROM t QUALIFY ROW_NUMBER() OVER (PARTITION BY a ORDER BY b) = 1",
	"SELECT a, SUM(b) FROM t GROUP BY ROLLUP (a)",
	"SELECT a, SUM(b) FROM t GROUP BY GROUPING SETS ((a), ())",
	"SELECT ARRAY[1, 2]",
	"SELECT DATE '2020-01-01', TIMESTAMP '2020-01-01 00:00:00'",
	"SELECT 1 ^ 2 | 3 & 4 << 1",
}

// bigqueryFailures holds statements a BigQuery dry run rejects.
var bigqueryFailures = []failure{
	{"SELECT 1::INT64", "unexpected"},
	{"SELECT 1 % 2", "unexpected"},
	{"SELECT * FROM t OFFSET 2", "unexpected"},
	{"SELECT a FROM t UNION SELECT a FROM u", "expected ALL or DISTINCT"},
	{"SELECT 1 INTERSECT ALL SELECT 2", "expected DISTINCT"},
	{"SELECT 1 EXCEPT ALL SELECT 2", "expected DISTINCT"},
	{"SELECT 1 UNION ALL SELECT 2 UNION DISTINCT SELECT 3", "different set operations need parentheses"},
	{"SELECT 1 UNION ALL SELECT 2 INTERSECT DISTINCT SELECT 3", "different set operations need parentheses"},
	{"SELECT 1 INTERSECT DISTINCT SELECT 2 UNION ALL SELECT 3", "different set operations need parentheses"},
}

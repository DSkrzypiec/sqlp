package corpus

import (
	"fmt"
	"html"
	"math/rand"
	"regexp"
	"strings"

	"sqlp/dialect"
)

// BigQuery is strictly typed, so an expression that mixes types at random
// fails before its grouping can be checked. bigQueryExprs builds typed trees
// instead and prints each with only the parentheses that the precedence table
// of the BigQuery reference requires. corpusgen then has BigQuery evaluate the
// statement and the parser's fully parenthesized form, so a wrong level in the
// reference, in the generator or in the dialect shows up as a different value.

// Binding powers from the BigQuery reference; higher binds tighter.
// Comparisons do not associate; every other binary level is left-associative.
const (
	bqOr = iota + 1
	bqAnd
	bqNot
	bqCmp
	bqBitOr
	bqXor
	bqBitAnd
	bqShift
	bqAdd
	bqMul
	bqUnary
	bqAtom
)

type bqType int

const (
	bqInt bqType = iota
	bqFloat
	bqBool
	bqString
)

type bqExpr struct {
	s     string
	level int
}

type bqGen struct{ r *rand.Rand }

func bigQueryExprs(n int) []string {
	g := bqGen{rand.New(rand.NewSource(1))}
	types := []bqType{bqInt, bqInt, bqBool, bqBool, bqBool, bqFloat, bqString}
	seen := map[string]bool{}
	var out []string
	for len(out) < n {
		e := g.expr(types[g.r.Intn(len(types))], 2+g.r.Intn(3))
		if s := "SELECT " + e.s; e.level < bqAtom && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// operand prints e for a position that needs at least binding power min.
func operand(e bqExpr, min int) string {
	if e.level < min {
		return "(" + e.s + ")"
	}
	return e.s
}

func (g *bqGen) binary(l bqExpr, op string, level int, r bqExpr) bqExpr {
	left := level
	if level == bqCmp {
		left = level + 1
	}
	return bqExpr{operand(l, left) + " " + op + " " + operand(r, level+1), level}
}

func (g *bqGen) pick(ops ...string) string { return ops[g.r.Intn(len(ops))] }

var bqPrimes = []int{2, 3, 5, 7, 11, 13, 17, 19, 23, 29, 31, 37, 41, 43, 47, 53, 59, 61, 67, 71, 73, 79, 83, 89, 97}

func (g *bqGen) atom(t bqType) bqExpr {
	switch t {
	case bqFloat:
		return bqExpr{fmt.Sprintf("%d.5", bqPrimes[g.r.Intn(len(bqPrimes))]), bqAtom}
	case bqBool:
		return bqExpr{g.pick("TRUE", "FALSE"), bqAtom}
	case bqString:
		return bqExpr{"'" + g.pick("a", "b", "ab", "ba", "abc", "x") + "'", bqAtom}
	}
	return bqExpr{fmt.Sprint(bqPrimes[g.r.Intn(len(bqPrimes))]), bqAtom}
}

func (g *bqGen) num(depth int) bqExpr {
	if g.r.Intn(3) == 0 {
		return g.expr(bqFloat, depth)
	}
	return g.expr(bqInt, depth)
}

func (g *bqGen) expr(t bqType, depth int) bqExpr {
	if depth <= 0 || g.r.Intn(4) == 0 {
		return g.atom(t)
	}
	d := depth - 1
	switch t {
	case bqInt:
		switch g.r.Intn(8) {
		case 0:
			op := g.pick("-", "~")
			x := operand(g.expr(bqInt, d), bqUnary)
			if strings.HasPrefix(x, "-") { // "--" would start a comment
				x = "(" + x + ")"
			}
			return bqExpr{op + x, bqUnary}
		case 1:
			return g.binary(g.expr(bqInt, d), "*", bqMul, g.expr(bqInt, d))
		case 2, 3:
			return g.binary(g.expr(bqInt, d), g.pick("+", "-"), bqAdd, g.expr(bqInt, d))
		case 4: // small shifts, so that products do not overflow
			return g.binary(g.expr(bqInt, d), g.pick("<<", ">>"), bqShift, bqExpr{g.pick("1", "2", "3", "5"), bqAtom})
		case 5:
			return g.binary(g.expr(bqInt, d), "&", bqBitAnd, g.expr(bqInt, d))
		case 6:
			return g.binary(g.expr(bqInt, d), "^", bqXor, g.expr(bqInt, d))
		default:
			return g.binary(g.expr(bqInt, d), "|", bqBitOr, g.expr(bqInt, d))
		}
	case bqFloat:
		if g.r.Intn(2) == 0 { // a literal divisor is never zero
			return g.binary(g.num(d), "/", bqMul, g.atom([]bqType{bqInt, bqFloat}[g.r.Intn(2)]))
		}
		if op := g.pick("+", "-", "*"); op == "*" {
			return g.binary(g.expr(bqFloat, d), op, bqMul, g.num(d))
		} else {
			return g.binary(g.expr(bqFloat, d), op, bqAdd, g.num(d))
		}
	case bqString:
		return g.binary(g.expr(bqString, d), "||", bqMul, g.expr(bqString, d))
	}
	// bqBool
	switch g.r.Intn(12) {
	case 0, 1:
		return g.binary(g.num(d), g.pick("=", "<", ">", "<=", ">=", "!=", "<>"), bqCmp, g.num(d))
	case 2:
		return g.binary(g.expr(bqString, d), g.pick("=", "<", "<>"), bqCmp, g.expr(bqString, d))
	case 3:
		pattern := bqExpr{"'" + g.pick("a%", "%b", "_b%", "x") + "'", bqAtom}
		return g.binary(g.expr(bqString, d), g.pick("LIKE", "NOT LIKE"), bqCmp, pattern)
	case 4:
		x := g.expr([]bqType{bqInt, bqFloat, bqBool, bqString}[g.r.Intn(4)], d)
		return bqExpr{operand(x, bqCmp+1) + g.pick(" IS NULL", " IS NOT NULL"), bqCmp}
	case 5:
		return bqExpr{operand(g.expr(bqBool, d), bqCmp+1) + g.pick(" IS TRUE", " IS NOT TRUE", " IS FALSE", " IS NOT FALSE"), bqCmp}
	case 6:
		s := operand(g.num(d), bqCmp+1) + g.pick(" BETWEEN ", " NOT BETWEEN ") + operand(g.num(d), bqCmp+1) +
			" AND " + operand(g.num(d), bqCmp+1)
		return bqExpr{s, bqCmp}
	case 7:
		s := operand(g.num(d), bqCmp+1) + g.pick(" IN (", " NOT IN (") + g.num(d).s + ", " + g.num(d).s + ")"
		return bqExpr{s, bqCmp}
	case 8:
		return g.binary(g.num(d), g.pick("IS DISTINCT FROM", "IS NOT DISTINCT FROM"), bqCmp, g.num(d))
	case 9:
		return bqExpr{"NOT " + operand(g.expr(bqBool, d), bqNot), bqNot}
	case 10:
		return g.binary(g.expr(bqBool, d), "AND", bqAnd, g.expr(bqBool, d))
	default:
		return g.binary(g.expr(bqBool, d), "OR", bqOr, g.expr(bqBool, d))
	}
}

// The BigQuery reference marks its SQL examples as <pre ... syntax="GoogleSQL">,
// and on the BigQuery ML pages as syntax="SQL".
var (
	bqCodeBlock = regexp.MustCompile(`(?s)<pre[^>]*syntax="(?:GoogleSQL|SQL)"[^>]*>(.*?)</pre>`)
	htmlTag     = regexp.MustCompile(`<[^>]+>`)
	// Result tables drawn in block comments: /*---+---+ ... +---*/.
	bqResultTable = regexp.MustCompile(`(?s)/\*[-+].*?\*/`)
	// Grammar notation rather than SQL: [, ...], { a | b }, [ DISTINCT ].
	bqNotation = regexp.MustCompile(`\[ ?, ?\.\.\.\]|\.\.\.|\{[^{}]*\|[^{}]*\}|\[ [A-Z]`)
	// Procedural statements keep their block whole: cut at ";", BEGIN ... END
	// would fall apart.
	bqScript = regexp.MustCompile(`(?im)^\s*(DECLARE\s|BEGIN\b|IF\s|LOOP\b|WHILE\s|REPEAT\b|FOR\s+\w+\s+IN\b|EXECUTE\s+IMMEDIATE\b|CREATE\s+(OR\s+REPLACE\s+)?(TEMP(ORARY)?\s+)?PROCEDURE\b)`)
)

// BigQueryDocStatements extracts the SQL examples of a page of the BigQuery
// reference: every statement of every example, except grammar notation. A
// block with procedural statements stays one script.
func BigQueryDocStatements(page string) []string {
	var out []string
	for _, m := range bqCodeBlock.FindAllStringSubmatch(page, -1) {
		block := html.UnescapeString(htmlTag.ReplaceAllString(m[1], ""))
		block = strings.TrimSpace(bqResultTable.ReplaceAllString(block, ""))
		switch {
		case block == "" || bqNotation.MatchString(block):
		case bqScript.MatchString(block):
			out = append(out, block)
		default:
			for _, s := range splitStatements(block, dialect.BigQuery) {
				if s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

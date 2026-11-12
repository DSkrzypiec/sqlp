package sqlp

import (
	"context"
	"strings"
	"testing"
	"time"

	"sqlp/dialect"
	"sqlp/internal/testkit"
)

// eachDialect runs f as a subtest for every selected dialect.
func eachDialect(t *testing.T, f func(t *testing.T, d *dialect.Dialect)) {
	t.Helper()
	for _, d := range dialect.All {
		testkit.Run(t, d.Name, func(t *testing.T) { f(t, d) })
	}
}

func parens(n int) string { return strings.Repeat("(", n) + "1" + strings.Repeat(")", n) }

// Deep recursion must return a *parser.Error, not crash the process with a Go
// stack overflow, which cannot be recovered.
func TestMaxDepthExceeded(t *testing.T) {
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		_, err := ParseExpr(parens(5000), d)
		if err == nil || !strings.Contains(err.Error(), "max nesting depth") {
			t.Fatalf("expected a depth-limit error, got %v", err)
		}
	})
}

func TestMaxDepthWithinLimitSucceeds(t *testing.T) {
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		if _, err := ParseExpr(parens(500), d); err != nil {
			t.Fatalf("500 levels should be within the default limit: %v", err)
		}
	})
}

func TestMaxDepthConfigurable(t *testing.T) {
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		_, err := ParseExprWithLimits(context.Background(), parens(50), d, Limits{MaxDepth: 10})
		if err == nil || !strings.Contains(err.Error(), "max nesting depth") {
			t.Fatalf("expected a depth-limit error with MaxDepth: 10, got %v", err)
		}
	})
}

func TestMaxBytesExceeded(t *testing.T) {
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		sql := "SELECT " + strings.Repeat("1", 100)
		_, err := ParseWithLimits(context.Background(), sql, d, Limits{MaxBytes: 50})
		if err == nil || !strings.Contains(err.Error(), "byte limit") {
			t.Fatalf("expected a byte-limit error, got %v", err)
		}
	})
}

// longUnion is valid in every dialect and long enough to reach a context check.
var longUnion = strings.Repeat("SELECT 1 UNION ALL ", 5000) + "SELECT 1"

func TestParseContextCancellation(t *testing.T) {
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := ParseWithLimits(ctx, longUnion, d, Limits{})
		if err == nil || !strings.Contains(err.Error(), "parse aborted") {
			t.Fatalf("expected the parse to abort, got %v", err)
		}
	})
}

func TestParseContextTimeout(t *testing.T) {
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		time.Sleep(time.Millisecond)
		_, err := ParseWithLimits(ctx, longUnion, d, Limits{})
		if err == nil || !strings.Contains(err.Error(), "parse aborted") {
			t.Fatalf("expected the parse to abort, got %v", err)
		}
	})
}

// Package testkit selects which SQL dialects a test run covers:
//
//	go test ./...                     # every dialect
//	go test ./... -dialect=postgres
//	go test ./... -dialect=sqlite,postgres
//
// Every test package must import it, or go test reports the flag as undefined.
package testkit

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Dialects names every dialect under test, as in dialect.Dialect.Name. It is
// not derived from package dialect because the lexer's tests import testkit
// and dialect imports lexer.
var Dialects = []string{"postgres", "sqlite"}

var dialectFlag = flag.String("dialect", "", "comma-separated dialects to test: "+
	strings.Join(Dialects, ", ")+" (default all)")

// selected parses the flag once. A misspelled dialect stops the run: treating
// it as "no dialects" would pass while testing nothing.
var selected = sync.OnceValue(func() []string {
	if *dialectFlag == "" {
		return Dialects
	}
	names := strings.Split(*dialectFlag, ",")
	for _, name := range names {
		if !slices.Contains(Dialects, name) {
			fmt.Fprintf(os.Stderr, "invalid -dialect value %q: want %s\n", name, strings.Join(Dialects, ", "))
			os.Exit(2)
		}
	}
	return names
})

// Enabled reports whether the run covers the named dialect.
func Enabled(name string) bool { return slices.Contains(selected(), name) }

// Require skips the test unless the run covers at least one named dialect.
func Require(t *testing.T, names ...string) {
	t.Helper()
	if !slices.ContainsFunc(names, Enabled) {
		t.Skipf("not selected by -dialect: %s", strings.Join(names, ", "))
	}
}

// Run runs f as a subtest called name if the run covers that dialect.
func Run(t *testing.T, name string, f func(t *testing.T)) {
	t.Helper()
	if Enabled(name) {
		t.Run(name, f)
	}
}

// Command sqlp parses SQL and prints its syntax tree, or as JSON, with
// -qstruct the objects a query depends on and with -outcols the columns it
// uses.
//
//	go run ./cmd/sqlp -dialect sqlite "SELECT a FROM t WHERE b > 1"
//	go run ./cmd/sqlp -qstruct -catalog catalog.json "SELECT * FROM orders"
//	go run ./cmd/sqlp -outcols -catalog catalog.json "SELECT * FROM orders"
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"sqlp"
	"sqlp/analyze"
	"sqlp/ast"
	"sqlp/catalog"
	"sqlp/dialect"
	"sqlp/internal/astdump"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is main without the process: it returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sqlp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dialectName := flags.String("dialect", "postgres", "SQL dialect: postgres, sqlite or bigquery")
	qstruct := flags.Bool("qstruct", false, "print the objects each statement depends on, as JSON")
	outcols := flags.Bool("outcols", false, "print the columns each statement uses, as JSON")
	catalogFile := flags.String("catalog", "", "with -qstruct or -outcols: a JSON catalog to resolve names against")
	expandViews := flags.Bool("expand-views", false, "with -qstruct or -outcols: follow the catalog's view definitions")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "usage: sqlp [-dialect postgres|sqlite|bigquery] [-qstruct|-outcols [-catalog file.json] [-expand-views]] \"query\"\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return 2
	}
	d, ok := dialect.ByName(*dialectName)
	if !ok {
		fmt.Fprintf(stderr, "sqlp: unknown dialect %q (want postgres, sqlite or bigquery)\n", *dialectName)
		return 2
	}
	if *qstruct && *outcols {
		fmt.Fprintln(stderr, "sqlp: give -qstruct or -outcols, not both")
		return 2
	}
	if !*qstruct && !*outcols && (*catalogFile != "" || *expandViews) {
		fmt.Fprintln(stderr, "sqlp: -catalog and -expand-views need -qstruct or -outcols")
		return 2
	}
	stmts, err := sqlp.Parse(flags.Arg(0), d)
	if err != nil {
		fmt.Fprintln(stderr, "sqlp:", err)
		return 1
	}
	if *qstruct || *outcols {
		analyse := func(s ast.Statement, d *dialect.Dialect, o analyze.Options) (any, error) {
			return analyze.QStruct(s, d, o)
		}
		if *outcols {
			analyse = func(s ast.Statement, d *dialect.Dialect, o analyze.Options) (any, error) {
				return analyze.ListOutCols(s, d, o)
			}
		}
		return printAnalysis(stmts, d, analyse, *catalogFile, *expandViews, stdout, stderr)
	}
	var out strings.Builder
	for _, s := range stmts {
		astdump.Tree(s).Write(&out)
	}
	fmt.Fprint(stdout, out.String())
	return 0
}

// printAnalysis prints the result of analyse for each statement as JSON.
func printAnalysis(stmts []ast.Statement, d *dialect.Dialect, analyse func(ast.Statement, *dialect.Dialect, analyze.Options) (any, error),
	catalogFile string, expandViews bool, stdout, stderr io.Writer) int {
	opts := analyze.Options{ExpandViews: expandViews}
	if catalogFile != "" {
		data, err := os.ReadFile(catalogFile)
		if err != nil {
			fmt.Fprintln(stderr, "sqlp:", err)
			return 1
		}
		opts.Catalog = new(catalog.Catalog)
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields() // a misspelled field must not pass in silence
		if err := dec.Decode(opts.Catalog); err != nil {
			fmt.Fprintf(stderr, "sqlp: %s: %v\n", catalogFile, err)
			return 1
		}
	}
	for _, s := range stmts {
		res, err := analyse(s, d, opts)
		if err != nil {
			fmt.Fprintln(stderr, "sqlp:", err)
			return 1
		}
		out, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			fmt.Fprintln(stderr, "sqlp:", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s\n", out)
	}
	return 0
}

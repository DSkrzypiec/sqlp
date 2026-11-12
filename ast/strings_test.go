package ast

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	_ "sqlp/internal/testkit" // defines the -dialect flag go test ./... passes
)

var enumStrings = map[string]func(uint8) string{
	"LiteralKind":          func(v uint8) string { return LiteralKind(v).String() },
	"IsKind":               func(v uint8) string { return IsKind(v).String() },
	"CastStyle":            func(v uint8) string { return CastStyle(v).String() },
	"TimeZoneSpec":         func(v uint8) string { return TimeZoneSpec(v).String() },
	"DistinctKind":         func(v uint8) string { return DistinctKind(v).String() },
	"SetOpKind":            func(v uint8) string { return SetOpKind(v).String() },
	"FrameBoundKind":       func(v uint8) string { return FrameBoundKind(v).String() },
	"JoinKind":             func(v uint8) string { return JoinKind(v).String() },
	"ColumnConstraintKind": func(v uint8) string { return ColumnConstraintKind(v).String() },
	"TableConstraintKind":  func(v uint8) string { return TableConstraintKind(v).String() },
	"AlterColumnKind":      func(v uint8) string { return AlterColumnKind(v).String() },
}

// TestEveryEnumValueHasName reads the const blocks from source, so a new enum
// or a new value without a name fails here.
func TestEveryEnumValueHasName(t *testing.T) {
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, file := range pkgs["ast"].Files {
		for _, decl := range file.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST && gen.Lparen.IsValid() {
				if spec := gen.Specs[0].(*ast.ValueSpec); spec.Type != nil {
					counts[spec.Type.(*ast.Ident).Name] = len(gen.Specs)
				}
			}
		}
	}
	if len(counts) == 0 {
		t.Fatal("found no enums")
	}
	for name, n := range counts {
		str, ok := enumStrings[name]
		if !ok {
			t.Errorf("enum %s is missing from enumStrings", name)
			continue
		}
		seen := map[string]bool{}
		for v := range n {
			s := str(uint8(v))
			if s == "" || strings.HasPrefix(s, "Unknown(") || seen[s] {
				t.Errorf("%s value %d has name %q", name, v, s)
			}
			seen[s] = true
		}
	}
}

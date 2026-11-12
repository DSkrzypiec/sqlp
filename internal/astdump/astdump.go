// Package astdump turns an AST value into a plain tree of labelled entries,
// for printing (cmd/sqlp) and for display in the web UI (cmd/sqlp_serv).
package astdump

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"sqlp/ast"
)

// Kind is the shape of a Node.
type Kind uint8

const (
	Leaf   Kind = iota // a value that fits on one line
	Struct             // an AST node; Text is its type name
	List               // a slice; its children are the elements
)

// Node is one entry of the tree.
type Node struct {
	// Field is the struct field this entry is the value of, "-" for a list
	// element, or "" for the root.
	Field    string
	Kind     Kind
	Text     string // the type name of a Struct, or the value of a Leaf
	Children []*Node
}

// Tree builds the tree for v. Fields holding zero values are left out to keep
// it readable; enums are always kept, because their zero values are meaningful
// (Null, InnerJoin, Union, ...).
func Tree(v any) *Node { return build("", reflect.ValueOf(v)) }

func build(field string, v reflect.Value) *Node {
	for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if s, ok := leaf(v); ok {
		return &Node{Field: field, Kind: Leaf, Text: s}
	}
	switch v.Kind() {
	case reflect.Struct:
		n := &Node{Field: field, Kind: Struct, Text: v.Type().Name()}
		for i := range v.NumField() {
			f, info := v.Field(i), v.Type().Field(i)
			if info.IsExported() && !omit(f) {
				n.Children = append(n.Children, build(info.Name, f))
			}
		}
		return n
	case reflect.Slice:
		n := &Node{Field: field, Kind: List}
		for i := range v.Len() {
			n.Children = append(n.Children, build("-", v.Index(i)))
		}
		return n
	}
	return &Node{Field: field, Kind: Leaf, Text: fmt.Sprint(v.Interface())}
}

// Size is the number of entries in the tree.
func (n *Node) Size() int {
	size := 1
	for _, c := range n.Children {
		size += c.Size()
	}
	return size
}

// Write prints the tree as indented text, one entry per line.
func (n *Node) Write(w *strings.Builder) { n.write(w, 0) }

func (n *Node) write(w *strings.Builder, depth int) {
	prefix := ""
	switch n.Field {
	case "":
	case "-":
		prefix = "- "
	default:
		prefix = n.Field + ": "
	}
	pad := strings.Repeat("  ", depth)
	if n.Kind == List {
		fmt.Fprintf(w, "%s%s\n", pad, strings.TrimRight(prefix, " "))
	} else {
		fmt.Fprintf(w, "%s%s%s\n", pad, prefix, n.Text)
	}
	for _, c := range n.Children {
		c.write(w, depth+1)
	}
}

func omit(field reflect.Value) bool {
	if _, isEnum := field.Interface().(fmt.Stringer); isEnum {
		return false
	}
	return field.IsZero() || field.Kind() == reflect.Slice && field.Len() == 0
}

// leaf renders values that fit on one line. Names and identifiers are shown
// as written, unless they carry U&"..." details that the short form would lose.
func leaf(v reflect.Value) (string, bool) {
	if !v.IsValid() {
		return "nil", true
	}
	if v.Kind() == reflect.String {
		return strconv.Quote(v.String()), true
	}
	switch n := v.Interface().(type) {
	case fmt.Stringer:
		return n.String(), true
	case ast.Ident:
		if !n.Unicode {
			return "Ident " + identText(n), true
		}
	case ast.Name:
		parts := make([]string, len(n.Parts))
		for i, p := range n.Parts {
			if p.Unicode {
				return "", false
			}
			parts[i] = identText(p)
		}
		return "Name " + strings.Join(parts, "."), true
	}
	return "", false
}

func identText(id ast.Ident) string {
	if id.Quoted {
		return strconv.Quote(id.Name)
	}
	return id.Name
}

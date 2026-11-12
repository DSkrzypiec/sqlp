// Package analyze answers questions about parsed queries. Each question has
// its own small result type: QStructGraph for the objects a query depends on,
// OutCols for the columns it uses.
//
// A catalog is optional. Without one, names are taken as written. With one,
// they resolve to the full names of the objects they refer to, following the
// dialect's rules for case and for searching schemas.
package analyze

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"sqlp/ast"
	"sqlp/catalog"
	"sqlp/dialect"
	"sqlp/lexer"
)

// Options holds the schema context shared by every analysis.
type Options struct {
	// Catalog lists the objects that queries can refer to. Nil means names
	// are taken as written.
	Catalog *catalog.Catalog
	// SearchPath lists the schemas searched for unqualified names, in order.
	// Nil means the dialect's default.
	SearchPath []string
	// ExpandViews follows the Definition of catalog views, so that a view's
	// own dependencies become part of the result.
	ExpandViews bool
}

// Diagnostic reports something an analysis could not see into or that looks
// wrong, such as an unused CTE or a view definition that does not parse.
type Diagnostic struct {
	Message string `json:"message"`
}

// NameStatus says how far a name was checked against the catalog.
type NameStatus string

const (
	Resolved  NameStatus = ""          // found in the catalog
	NotFound  NameStatus = "not_found" // a catalog was given, and it has no such object or column
	Unchecked NameStatus = "unchecked" // there was nothing to check it against
	// Ambiguous is a column that more than one relation could have (ListOutCols).
	Ambiguous NameStatus = "ambiguous"
)

// resolver matches names written in a query to catalog objects.
type resolver struct {
	d      *dialect.Dialect
	cat    *catalog.Catalog // nil without a catalog
	path   []string         // schemas searched for unqualified names, in order
	schema string           // the schema of catalog objects that name none
	byName map[string][]*catalog.Object
}

func newResolver(d *dialect.Dialect, o Options) (*resolver, error) {
	r := &resolver{d: d, cat: o.Catalog}
	path := o.SearchPath
	if path == nil {
		path = d.Names.SearchPath
	}
	for _, s := range d.Names.ImplicitSchemas {
		if !slices.ContainsFunc(path, func(p string) bool { return r.sameObject(p, s) }) {
			r.path = append(r.path, s)
		}
	}
	r.path = append(r.path, path...)
	r.schema = d.Names.DefaultSchema
	if r.schema == "" && len(path) > 0 {
		r.schema = path[0]
	}
	if r.cat == nil {
		return r, nil
	}
	if err := r.cat.Validate(); err != nil {
		return nil, err
	}
	// Names that differ only in ways the dialect ignores are the same object.
	var errs []error
	seen := map[string]int{}
	r.byName = map[string][]*catalog.Object{}
	for i := range r.cat.Objects {
		o := &r.cat.Objects[i]
		k := r.nameKey(r.full(o))
		if j, ok := seen[k]; ok {
			errs = append(errs, fmt.Errorf("catalog: objects[%d] (%s) and objects[%d] (%s) are the same object in %s",
				j, r.cat.Objects[j].Name, i, o.Name, d.Name))
			continue
		}
		seen[k] = i
		r.byName[r.objectKey(o.Object)] = append(r.byName[r.objectKey(o.Object)], o)
	}
	return r, errors.Join(errs...)
}

// ident returns the name an identifier stands for: escapes decoded, and
// folded to lower case when the dialect folds unquoted names.
func (r *resolver) ident(id ast.Ident) string {
	s := id.Name
	if id.Unicode {
		esc := byte('\\')
		if id.UEscape != "" {
			esc = id.UEscape[0]
		}
		s = lexer.DecodeUnicode(s, esc)
	}
	if !id.Quoted && r.d.FoldsToLower {
		s = asciiLower(s)
	}
	return s
}

// written turns a name from a query into a catalog.Name, without lookup.
func (r *resolver) written(n *ast.Name) (catalog.Name, error) {
	var parts []string
	for _, p := range n.Parts {
		if !p.Quoted || !r.d.Names.QuotedPaths {
			parts = append(parts, r.ident(p))
			continue
		}
		for _, piece := range strings.Split(r.ident(p), ".") {
			if piece != "" {
				parts = append(parts, piece)
			}
		}
	}
	if r.d.Names.QuotedPaths && len(parts) > 3 { // `a.b.c.d` is table c.d of dataset b
		parts = append(parts[:2], strings.Join(parts[2:], "."))
	}
	switch len(parts) {
	case 1:
		return catalog.Name{Object: parts[0]}, nil
	case 2:
		return catalog.Name{Schema: parts[0], Object: parts[1]}, nil
	case 3:
		return catalog.Name{Database: parts[0], Schema: parts[1], Object: parts[2]}, nil
	}
	return catalog.Name{Object: strings.Join(parts, ".")}, fmt.Errorf("%s has too many dotted parts for an object name", strings.Join(parts, "."))
}

// key is what two names must share to match.
func (r *resolver) key(s string) string {
	if r.d.Names.IgnoreCase {
		return asciiLower(s)
	}
	return s
}

func (r *resolver) same(a, b string) bool { return r.key(a) == r.key(b) }

// objectKey is what two names of databases, schemas or objects must share.
func (r *resolver) objectKey(s string) string {
	if r.d.Names.CaseSensitiveObjects {
		return s
	}
	return r.key(s)
}

func (r *resolver) sameObject(a, b string) bool { return r.objectKey(a) == r.objectKey(b) }

func (r *resolver) nameKey(n catalog.Name) string {
	return r.objectKey(n.Database) + "\x00" + r.objectKey(n.Schema) + "\x00" + r.objectKey(n.Object)
}

// full is a catalog object's name with the catalog's database and the default
// schema filled in.
func (r *resolver) full(o *catalog.Object) catalog.Name {
	n := o.Name
	if n.Database == "" {
		n.Database = r.cat.Database
	}
	if n.Schema == "" {
		n.Schema = r.schema
	}
	return n
}

// object finds the catalog object a written relation name refers to. It
// returns the name to report: the object's full name when found, otherwise
// the name as written.
func (r *resolver) object(w catalog.Name) (catalog.Name, *catalog.Object, NameStatus) {
	if r.cat == nil {
		return w, nil, Unchecked
	}
	cands := r.byName[r.objectKey(w.Object)]
	inSchema := func(schema string) *catalog.Object {
		for _, o := range cands {
			full := r.full(o)
			if r.sameObject(full.Schema, schema) && (w.Database == "" || full.Database == "" || r.sameObject(w.Database, full.Database)) {
				return o
			}
		}
		return nil
	}
	var found *catalog.Object
	if w.Schema != "" {
		found = inSchema(w.Schema)
	} else {
		for _, s := range r.path {
			if found = inSchema(s); found != nil {
				break
			}
		}
	}
	if found == nil {
		return w, nil, NotFound
	}
	n := r.full(found)
	if n.Database == "" {
		n.Database = w.Database
	}
	return n, found, Resolved
}

// created is the full name of an object a statement creates, such as the view
// in CREATE VIEW. Without a catalog it is the name as written.
func (r *resolver) created(w catalog.Name, temp bool) catalog.Name {
	if r.cat == nil {
		return w
	}
	if w.Database == "" {
		w.Database = r.cat.Database
	}
	if w.Schema == "" {
		w.Schema = r.schema
		if temp {
			w.Schema = r.d.Names.TempSchema
		}
	}
	return w
}

func asciiLower(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if c := b[j]; c >= 'A' && c <= 'Z' {
					b[j] = c + 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

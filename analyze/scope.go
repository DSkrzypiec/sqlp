package analyze

import "sqlp/ast"

// scope is one WITH clause and how many of its CTEs are visible.
type scope struct {
	parent  *scope
	with    *ast.With
	visible int
	index   map[string]int    // name key to the first CTE of with that has it, plus one
	found   map[string]cteRef // the lookups already made from this scope
}

// cteRef is a CTE and the scope to read its query in.
type cteRef struct {
	cte   *ast.CTE
	scope *scope
}

// cteFinder matches relation names to the CTEs in scope. Every analysis
// shares it, so they all follow the same visibility rules.
type cteFinder struct {
	r     *resolver
	names map[string]bool // the name keys of every CTE seen so far
}

func newCTEFinder(r *resolver) *cteFinder {
	return &cteFinder{r: r, names: map[string]bool{}}
}

// with opens the scope of a WITH clause inside sc.
func (f *cteFinder) with(w *ast.With, sc *scope) *scope {
	s := &scope{parent: sc, with: w, visible: len(w.CTEs), index: map[string]int{}, found: map[string]cteRef{}}
	for i, c := range w.CTEs {
		key := f.r.key(f.r.ident(c.Name))
		f.names[key] = true
		if s.index[key] == 0 {
			s.index[key] = i + 1
		}
	}
	return s
}

// find returns the CTE an unqualified name refers to, and the scope to read
// that CTE's query in. A lookup walks the enclosing WITH clauses, so two
// shortcuts keep a deeply nested query with many names cheap: names that no
// WITH clause defines are turned away at once, and each scope remembers what
// it has looked up.
func (f *cteFinder) find(name string, sc *scope) (*ast.CTE, *scope) {
	key := f.r.key(name)
	if sc == nil || !f.names[key] {
		return nil, nil
	}
	if hit, ok := sc.found[key]; ok {
		return hit.cte, hit.scope
	}
	var hit cteRef
	for s := sc; s != nil; s = s.parent {
		i := s.index[key] - 1
		if i < 0 || i >= s.visible {
			continue
		}
		hit = cteRef{s.with.CTEs[i], s}
		if !s.with.Recursive && !f.r.d.Names.CTEsAlwaysRecursive {
			// Without RECURSIVE, a CTE sees only the ones before it.
			hit.scope = &scope{parent: s.parent, with: s.with, visible: i, index: s.index, found: map[string]cteRef{}}
		}
		break
	}
	sc.found[key] = hit
	return hit.cte, hit.scope
}

package sqlp

import (
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"sqlp/dialect"
	"sqlp/internal/corpus"
)

// pin is one expectation a dialect table change should be able to break.
type pin struct {
	sql, shape, out string
	reject          bool
}

func (p pin) holds(d *dialect.Dialect) bool {
	r := corpus.Parse(p.sql, d)
	if (r.Err != nil) != p.reject {
		return false
	}
	return p.reject ||
		(p.shape == "" || corpus.Shape(r.Stmts, d) == p.shape) &&
			(p.out == "" || strings.TrimSuffix(Format(r.Stmts, d), ";") == p.out)
}

func pinsFor(t *testing.T, d *dialect.Dialect) []pin {
	t.Helper()
	var pins []pin
	for _, c := range conformance {
		if c.d != d {
			continue
		}
		for _, s := range c.sets {
			for _, sql := range s.identity {
				pins = append(pins, pin{sql: sql, out: sql})
			}
			for _, r := range s.rewrites {
				pins = append(pins, pin{sql: r.in, out: r.out})
			}
			for _, f := range s.failures {
				pins = append(pins, pin{sql: f.sql, reject: true})
			}
		}
	}
	for _, c := range corpus.ANSI {
		pins = append(pins, pin{sql: c.SQL, reject: !c.Accepted(d.Name)})
	}
	known, err := corpus.Read[corpus.Known](filepath.Join("testdata", "known", d.Name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	disagree := map[string]bool{}
	for _, k := range known {
		disagree[k.SQL] = true
	}
	files, _ := filepath.Glob(filepath.Join("testdata", "corpus", d.Name, "*.jsonl"))
	for _, f := range files {
		rows, err := corpus.Read[corpus.Record](f)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if !disagree[r.SQL] && !(r.Reject && corpus.Parse(r.SQL, d).Opaque) {
				pins = append(pins, pin{sql: r.SQL, shape: r.Shape, reject: r.Reject})
			}
		}
	}
	return pins
}

// mutation is an altered copy of a dialect. Only pins whose text contains
// token can notice it; an empty token means any pin can.
type mutation struct {
	d     *dialect.Dialect
	token string
}

// mutations moves each precedence entry to its neighbouring levels and turns
// off each feature the dialect claims. Turning a feature on for a dialect that
// lacks it proves little, since the foreign syntax usually fails anyway.
func mutations(d *dialect.Dialect) map[string]mutation {
	out := map[string]mutation{}
	levels := slices.Compact(slices.Sorted(maps.Values(d.Precedence)))
	for op, prec := range d.Precedence {
		i := slices.Index(levels, prec)
		for _, j := range []int{i - 1, i + 1} {
			if j < 0 || j >= len(levels) {
				continue
			}
			m := *d
			m.Precedence = maps.Clone(d.Precedence)
			m.Precedence[op] = levels[j]
			out[fmt.Sprintf("%s: %s moved to %d", d.Name, op, levels[j])] = mutation{&m, op}
		}
	}
	groups := reflect.TypeFor[dialect.Features]()
	for g := range groups.NumField() {
		features := groups.Field(g).Type
		for i := range features.NumField() {
			m := *d
			if f := reflect.ValueOf(&m.Features).Elem().Field(g).Field(i); f.Kind() == reflect.Bool && f.Bool() {
				f.SetBool(false)
				out[fmt.Sprintf("%s: %s off", d.Name, features.Field(i).Name)] = mutation{&m, ""}
			}
		}
	}
	return out
}

// TestDialectTablesArePinned applies each mutation in turn and requires some
// frozen expectation to break. A change nothing notices means that entry could
// be wrong without any test failing.
func TestDialectTablesArePinned(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	eachDialect(t, func(t *testing.T, d *dialect.Dialect) {
		pins := pinsFor(t, d)
		for _, p := range pins {
			if !p.holds(d) {
				t.Fatalf("expectation fails on the unmodified dialect: %s", p.sql)
			}
		}
		muts := mutations(d)
		for _, name := range slices.Sorted(maps.Keys(muts)) {
			if _, ok := unobservable[name]; ok {
				continue
			}
			m := muts[name]
			if !slices.ContainsFunc(pins, func(p pin) bool {
				return strings.Contains(strings.ToUpper(p.sql), m.token) && !p.holds(m.d)
			}) {
				t.Errorf("%s: no test noticed", name)
			}
		}
		for name := range unobservable {
			if strings.HasPrefix(name, d.Name+":") {
				if _, ok := muts[name]; !ok {
					t.Errorf("stale entry in unobservable: %s", name)
				}
			}
		}
	})
}

// unobservable lists mutations no frozen statement can detect, and why.
var unobservable = map[string]string{
	"postgres: . moved to 16":     postfixOnly,
	"postgres: :: moved to 15":    postfixOnly,
	"postgres: :: moved to 17":    postfixOnly,
	"postgres: [ moved to 16":     postfixOnly,
	"postgres: AtTimeZone off":    "only decides a Transpile warning, which TestTranspile covers",
	"postgres: ESCAPE moved to 6": escapeOnly,
	"postgres: Ilike off":         "only decides how Transpile carries LIKE and ILIKE over, which TestTranspile covers",
	"sqlite: ESCAPE moved to 5":   escapeOnly,
	"sqlite: MATCH moved to 2":    noMatch,
	"sqlite: MATCH moved to 5":    noMatch,
}

const (
	postfixOnly = "postfix operators apply left to right at any level"
	escapeOnly  = "no infix operator sits between this level and the next"
	noMatch     = "the CLI cannot evaluate MATCH, so no grouping can be verified"
)

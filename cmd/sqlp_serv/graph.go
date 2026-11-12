package main

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"sqlp/analyze"
)

// drawing is a QStructGraph laid out for SVG. Sources sit on the left and
// the root on the right, so arrows follow the data: a -> b means b reads a.
type drawing struct {
	ArrowID       string // unique per drawing, for the arrowhead marker
	Width, Height int
	Boxes         []box
	Edges         []edge
}

type box struct {
	X, Y, W, H   int
	TextX        int
	LabelY, SubY int
	Label, Sub   string
	Title        string // tooltip: full name, kind and status
	Class        string
}

type edge struct {
	Path  string
	Class string
}

const (
	boxH     = 46
	colGap   = 72
	rowGap   = 16
	margin   = 20
	topSpace = 40 // room above the first row for recursive loops
	charW    = 8  // px per character of 13px monospace, rounded up
	padX     = 12
	maxLabel = 40 // longer IDs are cut, with the full name in the tooltip
)

// layout places each node in a column by its longest distance from the
// root, orders each column to reduce crossings, and routes the edges.
func layout(g *analyze.QStructGraph, arrowID string) drawing {
	nodes := g.Nodes()

	// Back edges close cycles (a recursive CTE reading itself). They are drawn
	// but do not decide columns.
	type link struct{ from, to *analyze.QStructNode }
	back := map[link]bool{}
	state := map[*analyze.QStructNode]int{} // 1: on the DFS stack, 2: done
	var post []*analyze.QStructNode
	var visit func(n *analyze.QStructNode)
	visit = func(n *analyze.QStructNode) {
		state[n] = 1
		for _, c := range n.Children {
			switch state[c] {
			case 0:
				visit(c)
			case 1:
				back[link{n, c}] = true
			}
		}
		state[n] = 2
		post = append(post, n)
	}
	visit(g.Root)

	depth := map[*analyze.QStructNode]int{}
	parents := map[*analyze.QStructNode][]*analyze.QStructNode{}
	maxDepth := 0
	for i := len(post) - 1; i >= 0; i-- { // parents before children
		n := post[i]
		for _, c := range n.Children {
			if back[link{n, c}] {
				continue
			}
			parents[c] = append(parents[c], n)
			depth[c] = max(depth[c], depth[n]+1)
			maxDepth = max(maxDepth, depth[c])
		}
	}

	columns := make([][]*analyze.QStructNode, maxDepth+1) // by depth: root first
	for _, n := range nodes {
		columns[depth[n]] = append(columns[depth[n]], n)
	}

	// Order each column by the average position of its neighbours in the
	// columns already placed, sweeping away from the root and back.
	pos := map[*analyze.QStructNode]float64{}
	place := func(col []*analyze.QStructNode) {
		for i, n := range col {
			pos[n] = (float64(i) + 0.5) / float64(len(col))
		}
	}
	for _, col := range columns {
		place(col)
	}
	children := func(n *analyze.QStructNode) []*analyze.QStructNode {
		var out []*analyze.QStructNode
		for _, c := range n.Children {
			if !back[link{n, c}] {
				out = append(out, c)
			}
		}
		return out
	}
	order := func(col []*analyze.QStructNode, neighbours func(*analyze.QStructNode) []*analyze.QStructNode) {
		key := map[*analyze.QStructNode]float64{}
		for _, n := range col {
			key[n] = pos[n]
			if ns := neighbours(n); len(ns) > 0 {
				sum := 0.0
				for _, m := range ns {
					sum += pos[m]
				}
				key[n] = sum / float64(len(ns))
			}
		}
		slices.SortStableFunc(col, func(a, b *analyze.QStructNode) int {
			switch {
			case key[a] < key[b]:
				return -1
			case key[a] > key[b]:
				return 1
			}
			return 0
		})
		place(col)
	}
	for range 3 {
		for d := 1; d <= maxDepth; d++ {
			order(columns[d], func(n *analyze.QStructNode) []*analyze.QStructNode { return parents[n] })
		}
		for d := maxDepth - 1; d >= 0; d-- {
			order(columns[d], children)
		}
	}

	// Geometry: the deepest column on the left, the root on the right.
	boxes := map[*analyze.QStructNode]*box{}
	colW := make([]int, maxDepth+1)
	tallest := 0
	for d, col := range columns {
		for _, n := range col {
			b := newBox(n)
			boxes[n] = b
			colW[d] = max(colW[d], b.W)
		}
		tallest = max(tallest, len(col)*boxH+(len(col)-1)*rowGap)
	}
	x := margin
	for d := maxDepth; d >= 0; d-- {
		col := columns[d]
		y := topSpace + (tallest-(len(col)*boxH+(len(col)-1)*rowGap))/2
		for _, n := range col {
			b := boxes[n]
			b.X, b.Y = x+(colW[d]-b.W)/2, y
			b.TextX = b.X + padX
			b.LabelY, b.SubY = b.Y+19, b.Y+36
			y += boxH + rowGap
		}
		x += colW[d] + colGap
	}

	out := drawing{ArrowID: arrowID, Width: x - colGap + margin, Height: topSpace + tallest + margin}
	for _, n := range nodes {
		out.Boxes = append(out.Boxes, *boxes[n])
		for _, c := range n.Children {
			out.Edges = append(out.Edges, route(boxes[c], boxes[n]))
		}
	}
	return out
}

func newBox(n *analyze.QStructNode) *box {
	label := n.ID
	if utf8.RuneCountInString(label) > maxLabel {
		label = string([]rune(label)[:maxLabel-1]) + "…"
	}
	sub := strings.ReplaceAll(string(n.Kind), "_", " ")
	switch n.Status {
	case analyze.NotFound:
		sub += " · not in catalog"
	case analyze.Unchecked:
		if n.Kind != analyze.QStructFunction {
			sub += " · unchecked"
		}
	}
	title := n.ID
	if full := n.Name.String(); full != "" && full != n.ID {
		title += " (" + full + ")"
	}
	title += ": " + sub
	w := max(utf8.RuneCountInString(label), utf8.RuneCountInString(sub))*charW + 2*padX
	class := "node k-" + string(n.Kind)
	if n.Status != analyze.Resolved {
		class += " s-" + string(n.Status)
	}
	return &box{W: w, H: boxH, Label: label, Sub: sub, Title: title, Class: class}
}

// route draws an edge from the source's right side to the reader's left
// side. A node reading itself gets a loop above it.
func route(from, to *box) edge {
	if from == to {
		x1, x2, y := from.X+from.W/3, from.X+2*from.W/3, from.Y
		return edge{Class: "edge loop", Path: fmt.Sprintf("M%d %d C%d %d %d %d %d %d", x1, y, x1, y-34, x2, y-34, x2, y-2)}
	}
	x1, y1 := from.X+from.W, from.Y+from.H/2
	x2, y2 := to.X-2, to.Y+to.H/2
	bend := max((x2-x1)/2, 40)
	class := "edge"
	if x2 <= x1 {
		class += " back"
	}
	return edge{Class: class, Path: fmt.Sprintf("M%d %d C%d %d %d %d %d %d", x1, y1, x1+bend, y1, x2-bend, y2, x2, y2)}
}

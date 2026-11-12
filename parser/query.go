package parser

import (
	"sqlp/ast"
	"sqlp/lexer"
)

// isQueryStart reports whether the upcoming tokens begin a query.
func (p *Parser) isQueryStart() bool { return p.isQueryStartAtPos(p.pos) }

func (p *Parser) isQueryStartAt(n int) bool { return p.isQueryStartAtPos(p.pos + n) }

// isQueryStartAtPos answers isQueryStart for an absolute token position,
// memoized: a run of k unclosed '(' would otherwise be rescanned from every
// nesting level, which is quadratic in k (see AGENT_TODO.md).
func (p *Parser) isQueryStartAtPos(pos int) bool {
	if p.queryStartMemo == nil {
		p.queryStartMemo = make([]int8, len(p.toks)+1)
	}
	memoPos := pos
	if memoPos >= len(p.queryStartMemo) {
		memoPos = len(p.queryStartMemo) - 1
	}
	if m := p.queryStartMemo[memoPos]; m != 0 {
		return m == 1
	}
	result := false
	switch t := p.tokAt(pos); t.Kind {
	case lexer.Ident:
		switch t.Value {
		case "SELECT", "VALUES", "WITH":
			result = true
		case "TABLE":
			result = p.d.Features.Query.TableKeyword
		}
	case lexer.Op:
		if t.Text == "(" && p.d.Features.Query.ParenQueries {
			result = p.isQueryStartAtPos(pos + 1)
		}
	}
	if result {
		p.queryStartMemo[memoPos] = 1
	} else {
		p.queryStartMemo[memoPos] = 2
	}
	return result
}

// parseQuery parses WITH? body ORDER BY? LIMIT? OFFSET? FETCH? FOR?
func (p *Parser) parseQuery() *ast.Query {
	p.enterDepth()
	defer p.exitDepth()
	var with *ast.With
	if p.isKw("WITH") {
		with = p.parseWith()
	}
	return p.parseQueryAfterWith(with)
}

func (p *Parser) parseQueryAfterWith(with *ast.With) *ast.Query {
	q := &ast.Query{With: with, Body: p.parseQueryBody()}
	p.parseQueryTail(q)
	return q
}

// parseQueryTail parses ORDER BY, then LIMIT/OFFSET/FETCH and locking clauses,
// which PostgreSQL accepts in either order.
func (p *Parser) parseQueryTail(q *ast.Query) {
	if !p.d.Features.Query.ValuesTail && endsWithValues(q.Body) && p.isKw("ORDER", "LIMIT") {
		p.unexpected("end of query")
	}
	if p.acceptKwSeq("ORDER", "BY") {
		q.OrderBy = p.parseOrderItems()
	}
	// Limits and locking clauses each form one block, in either order.
	const limits, locking = 1, 2
	block, switches := 0, 0
	enter := func(next int) {
		if block != 0 && block != next {
			if switches++; switches > 1 {
				p.unexpected("end of query")
			}
		}
		block = next
	}
	for {
		switch {
		case p.isKw("LIMIT"), p.isKw("OFFSET") && p.d.Features.Query.OffsetWithoutLimit:
			enter(limits)
			p.parseLimit(&q.Limit)
		case p.isKw("FETCH") && p.d.Features.Query.FetchFirst:
			enter(limits)
			p.parseFetch(&q.Limit)
		case p.isKw("FOR") && p.d.Features.Query.Locking:
			enter(locking)
			if n := len(q.Locking); n > 0 && (q.Locking[n-1].Strength == "READ ONLY" || p.isKwAt(1, "READ")) {
				if q.Locking[n-1].Strength != "READ ONLY" {
					p.next()
				}
				p.unexpected("UPDATE or SHARE") // FOR READ ONLY stands alone
			}
			q.Locking = append(q.Locking, p.parseLocking())
		default:
			p.checkWithTies(q)
			p.checkParenQueryOptions(q)
			return
		}
	}
}

func endsWithValues(b ast.QueryBody) bool {
	switch b := b.(type) {
	case *ast.Values:
		return true
	case *ast.SetOp:
		return endsWithValues(b.Right)
	}
	return false
}

// checkParenQueryOptions applies gram.y's insertSelectOptions: clauses outside
// a parenthesized query merge into it, so each may appear on only one side.
func (p *Parser) checkParenQueryOptions(q *ast.Query) {
	paren, ok := q.Body.(*ast.ParenQuery)
	for ok {
		inner := paren.Query
		switch {
		case len(q.OrderBy) > 0 && len(inner.OrderBy) > 0:
			p.errorf(p.peek().Pos, "multiple ORDER BY clauses not allowed")
		case q.Limit != nil && inner.Limit != nil:
			p.errorf(p.peek().Pos, "multiple LIMIT clauses not allowed")
		case q.With != nil && inner.With != nil:
			p.errorf(p.peek().Pos, "multiple WITH clauses not allowed")
		}
		paren, ok = inner.Body.(*ast.ParenQuery)
	}
}

// checkWithTies applies gram.y's restrictions on FETCH ... WITH TIES.
func (p *Parser) checkWithTies(q *ast.Query) {
	if q.Limit == nil || !q.Limit.WithTies {
		return
	}
	if len(q.OrderBy) == 0 {
		p.errorf(p.peek().Pos, "WITH TIES cannot be specified without ORDER BY clause")
	}
	for _, lk := range q.Locking {
		if lk.Wait == "SKIP LOCKED" {
			p.errorf(p.peek().Pos, "SKIP LOCKED and WITH TIES options cannot be used together")
		}
	}
}

func (p *Parser) parseLimit(dst **ast.Limit) {
	if *dst == nil {
		*dst = &ast.Limit{}
	}
	l := *dst
	if p.isKw("LIMIT") {
		if l.Count != nil || l.All || l.Fetch {
			p.errorf(p.peek().Pos, "multiple LIMIT clauses not allowed")
		}
		p.next()
		if p.d.Features.Query.OffsetWithoutLimit && p.acceptKw("ALL") {
			l.All = true
		} else {
			l.Count = p.parseExpr()
			if p.d.Features.Query.LimitComma && p.acceptOp(",") { // LIMIT offset, count
				l.Offset = l.Count
				l.Count = p.parseExpr()
			}
		}
	}
	if p.isKw("OFFSET") {
		if l.Offset != nil {
			p.errorf(p.peek().Pos, "multiple OFFSET clauses not allowed")
		}
		p.next()
		l.Offset = p.parseExpr()
		if p.d.Features.Query.FetchFirst && p.isKw("ROW", "ROWS") {
			if !isFetchValue(l.Offset) {
				p.unexpected("end of OFFSET; with ROWS, only a constant or a parenthesized expression")
			}
			p.next()
		}
	}
}

// isFetchValue reports whether e is gram.y's select_fetch_first_value: a
// c_expr, or a signed numeric constant.
func isFetchValue(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Unary:
		lit, ok := e.Expr.(*ast.Literal)
		return (e.Op == "-" || e.Op == "+") && ok && (lit.Kind == ast.Integer || lit.Kind == ast.Float)
	case *ast.Cast:
		return e.Style != ast.CastDoubleColon
	case *ast.Binary, *ast.Is, *ast.Like, *ast.In, *ast.Between, *ast.AtTimeZone, *ast.Collate, *ast.Quantified:
		return false
	}
	return true
}

func (p *Parser) parseFetch(dst **ast.Limit) {
	if *dst == nil {
		*dst = &ast.Limit{}
	}
	l := *dst
	if l.Count != nil || l.All || l.Fetch {
		p.errorf(p.peek().Pos, "multiple LIMIT clauses not allowed")
	}
	p.expectKw("FETCH")
	if p.acceptOneOf("FIRST", "NEXT") == "" {
		p.unexpected("FIRST or NEXT")
	}
	l.Fetch = true
	if !p.isKw("ROW", "ROWS") || p.isKwAt(1, "ROW", "ROWS") { // a column may be called rows
		pos := p.peek().Pos
		l.Count = p.parseExprPrec(100) // a single primary, so ROWS is not swallowed
		if !isFetchValue(l.Count) {
			p.errorf(pos, "FETCH FIRST takes a constant or a parenthesized expression")
		}
	}
	if p.acceptOneOf("ROW", "ROWS") == "" {
		p.unexpected("ROW or ROWS")
	}
	switch {
	case p.acceptKw("ONLY"):
	case p.acceptKwSeq("WITH", "TIES"):
		l.WithTies = true
	default:
		p.unexpected("ONLY or WITH TIES")
	}
}

func (p *Parser) parseLocking() *ast.Locking {
	p.expectKw("FOR")
	lk := &ast.Locking{}
	switch {
	case p.acceptKwSeq("NO", "KEY", "UPDATE"):
		lk.Strength = "NO KEY UPDATE"
	case p.acceptKwSeq("KEY", "SHARE"):
		lk.Strength = "KEY SHARE"
	case p.acceptKw("UPDATE"):
		lk.Strength = "UPDATE"
	case p.acceptKw("SHARE"):
		lk.Strength = "SHARE"
	case p.acceptKwSeq("READ", "ONLY"): // parses to the same tree as no clause
		lk.Strength = "READ ONLY"
		return lk
	default:
		p.unexpected("UPDATE or SHARE")
	}
	if p.acceptKw("OF") {
		lk.Of = p.parseNameList()
	}
	switch {
	case p.acceptKw("NOWAIT"):
		lk.Wait = "NOWAIT"
	case p.acceptKwSeq("SKIP", "LOCKED"):
		lk.Wait = "SKIP LOCKED"
	}
	return lk
}

// --- WITH ----------------------------------------------------------------------

func (p *Parser) parseWith() *ast.With {
	p.expectKw("WITH")
	w := &ast.With{}
	if p.isKw("RECURSIVE") && (!p.d.Features.Names.KeywordLabels || !p.isOpAt(1, "(") && !p.isKwAt(1, "AS")) { // else a CTE named recursive
		p.next()
		w.Recursive = true
	}
	for {
		w.CTEs = append(w.CTEs, p.parseCTE())
		if !p.acceptOp(",") {
			return w
		}
	}
}

func (p *Parser) parseCTE() *ast.CTE {
	cte := &ast.CTE{Name: p.parseIdent()}
	if p.isOp("(") {
		cte.Columns = p.parseParenIdentList()
	}
	p.expectKw("AS")
	switch {
	case p.acceptKwSeq("NOT", "MATERIALIZED"):
		f := false
		cte.Materialized = &f
	case p.acceptKw("MATERIALIZED"):
		t := true
		cte.Materialized = &t
	}
	p.expectOp("(")
	switch {
	case p.d.Features.Query.DataModifyingCTE && p.isKw("INSERT"):
		cte.DML = p.parseInsert(nil)
	case p.d.Features.Query.DataModifyingCTE && p.isKw("UPDATE"):
		cte.DML = p.parseUpdate(nil)
	case p.d.Features.Query.DataModifyingCTE && p.isKw("DELETE"):
		cte.DML = p.parseDelete(nil)
	case p.d.Features.Query.DataModifyingCTE && p.isKw("MERGE"):
		cte.DML = p.parseMerge(nil)
	case p.d.Features.Query.DataModifyingCTE && p.isKw("WITH"):
		switch st := p.parseWithStatement().(type) {
		case *ast.Query:
			cte.Query = st
		default:
			cte.DML = st
		}
	default:
		cte.Query = p.parseQuery()
	}
	p.expectOp(")")
	if p.d.Features.Query.CTESearchCycle {
		if p.acceptKw("SEARCH") {
			s := &ast.CTESearch{Breadth: p.acceptKw("BREADTH")}
			if !s.Breadth {
				p.expectKw("DEPTH")
			}
			p.expectKw("FIRST", "BY")
			s.By = p.parseIdentList()
			p.expectKw("SET")
			s.Set = p.parseIdent()
			cte.Search = s
		}
		if p.acceptKw("CYCLE") {
			c := &ast.CTECycle{Columns: p.parseIdentList()}
			p.expectKw("SET")
			c.Set = p.parseIdent()
			if p.acceptKw("TO") {
				c.To = p.parseExprPrec(100)
				p.expectKw("DEFAULT")
				c.Default = p.parseExprPrec(100)
			}
			p.expectKw("USING")
			c.Using = p.parseIdent()
			cte.Cycle = c
		}
	}
	return cte
}

// --- set operations ------------------------------------------------------------

// parseQueryBody parses UNION/EXCEPT chains. INTERSECT binds tighter only in
// dialects that say so: in SQLite all compound operators are left-associative
// at one level.
func (p *Parser) parseQueryBody() ast.QueryBody {
	if !p.d.Features.Query.IntersectBindsTighter {
		left := p.parseQueryPrimary()
		for {
			var op ast.SetOpKind
			pos := p.peek().Pos
			switch {
			case p.acceptKw("UNION"):
				op = ast.Union
			case p.acceptKw("EXCEPT"):
				op = ast.Except
			case p.acceptKw("INTERSECT"):
				op = ast.Intersect
			default:
				return left
			}
			all := p.parseSetQuantifier(op)
			left = p.setOp(pos, op, all, left, p.parseQueryPrimary)
		}
	}
	left := p.parseIntersectTerm()
	for {
		var op ast.SetOpKind
		pos := p.peek().Pos
		switch {
		case p.acceptKw("UNION"):
			op = ast.Union
		case p.acceptKw("EXCEPT"):
			op = ast.Except
		default:
			return left
		}
		all := p.parseSetQuantifier(op)
		left = p.setOp(pos, op, all, left, p.parseIntersectTerm)
	}
}

func (p *Parser) parseIntersectTerm() ast.QueryBody {
	left := p.parseQueryPrimary()
	for pos := p.peek().Pos; p.acceptKw("INTERSECT"); pos = p.peek().Pos {
		all := p.parseSetQuantifier(ast.Intersect)
		left = p.setOp(pos, ast.Intersect, all, left, p.parseQueryPrimary)
	}
	return left
}

func (p *Parser) parseSetQuantifier(op ast.SetOpKind) bool {
	f := p.d.Features.Query
	if (f.SetQuantifiers || op == ast.Union) && p.acceptKw("ALL") {
		return true
	}
	if (f.SetQuantifiers || f.SetQuantifierRequired) && p.acceptKw("DISTINCT") || !f.SetQuantifierRequired {
		return false
	}
	if op == ast.Union {
		p.unexpected("ALL or DISTINCT")
	}
	p.unexpected("DISTINCT")
	return false
}

// setOp adds the operation at pos to a chain. Where the dialect wants it, a
// chain repeats one operator and quantifier, and any other needs parentheses.
func (p *Parser) setOp(pos int, op ast.SetOpKind, all bool, left ast.QueryBody, right func() ast.QueryBody) ast.QueryBody {
	if prev, ok := left.(*ast.SetOp); ok && p.d.Features.Query.UnmixedSetOps && (prev.Op != op || prev.All != all) {
		p.errorf(pos, "different set operations need parentheses")
	}
	return &ast.SetOp{Op: op, All: all, Left: left, Right: right()}
}

func (p *Parser) parseQueryPrimary() ast.QueryBody {
	switch {
	case p.isOp("(") && p.d.Features.Query.ParenQueries:
		p.next()
		q := p.parseQuery()
		p.expectOp(")")
		return &ast.ParenQuery{Query: q}
	case p.isKw("SELECT"):
		return p.parseSelect()
	case p.isKw("VALUES"):
		return p.parseValues()
	case p.isKw("TABLE") && p.d.Features.Query.TableKeyword:
		p.next()
		return &ast.TableBody{Name: p.parseName()}
	}
	p.unexpected("SELECT")
	return nil
}

func (p *Parser) parseValues() *ast.Values {
	p.expectKw("VALUES")
	v := &ast.Values{}
	for {
		p.expectOp("(")
		v.Rows = append(v.Rows, p.parseExprList())
		p.expectOp(")")
		if !p.acceptOp(",") {
			return v
		}
	}
}

// --- SELECT --------------------------------------------------------------------

func (p *Parser) parseSelect() *ast.Select {
	p.expectKw("SELECT")
	s := &ast.Select{}
	switch {
	case p.acceptKw("ALL"):
		s.Distinct = ast.DistinctAll
	case p.acceptKw("DISTINCT"):
		s.Distinct = ast.Distinct
		if p.isKw("ON") && p.d.Features.Query.DistinctOn {
			p.next()
			p.expectOp("(")
			s.Distinct = ast.DistinctOn
			s.DistinctOn = p.parseExprList()
			p.expectOp(")")
		}
	}
	if p.d.Features.Query.SelectAs && p.isKw("AS") && p.isKwAt(1, "STRUCT", "VALUE") {
		p.next()
		s.As = p.next().Value
	}
	if !(p.d.Features.Query.EmptySelectList && s.Distinct != ast.Distinct && s.Distinct != ast.DistinctOn && p.atSelectListEnd()) {
		s.Items = p.parseSelectItems()
	}
	if p.d.Features.Query.SelectInto && p.isKw("INTO") {
		s.Into = p.parseIntoTable()
	}
	if p.acceptKw("FROM") {
		s.From = p.parseFromList()
	}
	if p.acceptKw("WHERE") {
		s.Where = p.parseExpr()
	}
	if p.acceptKwSeq("GROUP", "BY") {
		s.GroupBy = p.parseGroupBy()
	}
	if p.acceptKw("HAVING") {
		s.Having = p.parseExpr()
	}
	if p.isWindowClause() {
		p.next()
		for {
			nw := &ast.NamedWindow{Name: p.parseIdent()}
			p.expectKw("AS")
			nw.Spec = p.parseWindowSpec()
			s.Windows = append(s.Windows, nw)
			if !p.acceptOp(",") {
				break
			}
		}
	}
	if p.d.Features.Query.Qualify && p.acceptKw("QUALIFY") {
		s.Qualify = p.parseExpr()
	}
	return s
}

// isWindowClause recognizes a WINDOW clause. SQLite's WINDOW is contextual:
// like its tokenizer, this requires `WINDOW name AS` to follow.
func (p *Parser) isWindowClause() bool {
	if !p.isKw("WINDOW") {
		return false
	}
	next := p.peekN(1)
	return (next.Kind == lexer.Ident || next.Kind == lexer.QuotedIdent) && p.isKwAt(2, "AS")
}

func (p *Parser) atSelectListEnd() bool {
	t := p.peek()
	switch t.Kind {
	case lexer.EOF:
		return true
	case lexer.Op:
		return t.Text == ";" || t.Text == ")"
	case lexer.Ident:
		return p.isKw("FROM", "WHERE", "GROUP", "HAVING", "WINDOW", "ORDER", "LIMIT", "OFFSET",
			"FETCH", "FOR", "UNION", "INTERSECT", "EXCEPT", "INTO")
	}
	return false
}

func (p *Parser) parseIntoTable() *ast.IntoTable {
	p.expectKw("INTO")
	into := &ast.IntoTable{}
	if p.isKw("GLOBAL", "LOCAL") && p.isKwAt(1, "TEMP", "TEMPORARY") {
		p.next()
	}
	// These are unreserved, so `INTO temp FROM t` names a table called temp.
	nameFollows := func() bool {
		next := p.peekN(1)
		return next.Kind == lexer.QuotedIdent || next.Kind == lexer.Ident && !p.isKwAt(1, "FROM", "WHERE", "GROUP", "ORDER")
	}
	if p.isKw("TEMP", "TEMPORARY") && nameFollows() {
		p.next()
		into.Temp = true
	} else if p.isKw("UNLOGGED") && nameFollows() {
		p.next()
		into.Unlogged = true
	}
	p.acceptKw("TABLE")
	into.Name = p.parseName()
	return into
}

// parseStarModifiers parses BigQuery's * EXCEPT (a, b) and * REPLACE (expr AS a).
func (p *Parser) parseStarModifiers(s *ast.Star) *ast.Star {
	if !p.d.Features.Query.StarModifiers {
		return s
	}
	if p.isKw("EXCEPT") && p.isOpAt(1, "(") {
		p.pos += 2
		for {
			s.Except = append(s.Except, p.parseAnyIdent())
			if !p.acceptOp(",") {
				break
			}
		}
		p.expectOp(")")
	}
	if p.isKw("REPLACE") && p.isOpAt(1, "(") {
		p.pos += 2
		for {
			item := &ast.SelectItem{Expr: p.parseExpr()}
			p.expectKw("AS")
			name := p.parseAnyIdent()
			item.Alias = &name
			s.Replace = append(s.Replace, item)
			if !p.acceptOp(",") {
				break
			}
		}
		p.expectOp(")")
	}
	return s
}

func (p *Parser) parseSelectItems() []*ast.SelectItem {
	items := []*ast.SelectItem{p.parseSelectItem()}
	for p.acceptOp(",") {
		items = append(items, p.parseSelectItem())
	}
	return items
}

func (p *Parser) parseSelectItem() *ast.SelectItem {
	if p.acceptOp("*") {
		return &ast.SelectItem{Expr: p.parseStarModifiers(&ast.Star{})}
	}
	if !p.d.Features.Names.KeywordLabels && p.canBeIdent() && p.isOpAt(1, ".") && p.isOpAt(2, "*") {
		// SQLite only has t.* at the top level of a select list, without alias.
		name := &ast.Name{Parts: []ast.Ident{p.parseIdent()}}
		p.pos += 2
		return &ast.SelectItem{Expr: p.parseStarModifiers(&ast.Star{Qualifier: name})}
	}
	item := &ast.SelectItem{Expr: p.parseExpr()}
	switch {
	case p.acceptKw("AS"):
		id := p.parseLabel()
		item.Alias = &id
	case p.isBareLabel():
		id := p.parseAnyIdent()
		item.Alias = &id
	}
	return item
}

// isBareLabel reports whether the current token can be a select-list alias
// without AS.
func (p *Parser) isBareLabel() bool {
	t := p.peek()
	if t.Kind == lexer.Ident && p.d.Features.Names.KeywordLabels {
		return !p.d.AsOnlyLabels[t.Value]
	}
	return p.canBeBareAlias()
}

// canBeBareAlias reports whether the current token can be an alias without AS.
func (p *Parser) canBeBareAlias() bool {
	t := p.peek()
	if t.Kind == lexer.Ident && (p.d.NotAlias[t.Value] || p.isWindowClause()) {
		return false
	}
	return p.canBeIdent()
}

func (p *Parser) parseGroupBy() *ast.GroupBy {
	g := &ast.GroupBy{}
	if p.d.Features.Query.GroupByAll && p.acceptKw("ALL") { // every non-aggregate output, no items
		g.All = true
		return g
	}
	if p.d.Features.Query.GroupingSets && !p.d.Features.Query.GroupByAll { // PostgreSQL's quantifier
		switch {
		case p.acceptKw("DISTINCT"):
			g.Distinct = true
		case p.acceptKw("ALL"):
			g.All = true
		}
	}
	g.Items = p.parseGroupItems()
	return g
}

func (p *Parser) parseGroupItem() ast.Expr {
	if p.d.Features.Query.GroupingSets {
		switch {
		case p.isKw("ROLLUP", "CUBE") && p.isOpAt(1, "("):
			kind := p.next().Value
			p.expectOp("(")
			gs := &ast.GroupingSet{Kind: kind, Items: p.parseGroupItems()}
			p.expectOp(")")
			return gs
		case p.isKwSeq("GROUPING", "SETS") && p.isOpAt(2, "("):
			p.next()
			p.next()
			p.expectOp("(")
			gs := &ast.GroupingSet{Kind: "GROUPING SETS", Items: p.parseGroupItems()}
			p.expectOp(")")
			return gs
		case p.isOp("(") && p.isOpAt(1, ")"):
			p.next()
			p.next()
			return &ast.GroupingSet{}
		}
	}
	return p.parseExpr()
}

func (p *Parser) parseGroupItems() []ast.Expr {
	items := []ast.Expr{p.parseGroupItem()}
	for p.acceptOp(",") {
		items = append(items, p.parseGroupItem())
	}
	return items
}

// --- FROM ------------------------------------------------------------------------

func (p *Parser) parseFromList() []ast.TableExpr {
	from := []ast.TableExpr{p.parseTableExpr()}
	for p.acceptOp(",") {
		next := p.parseTableExprAfterComma()
		if j, ok := next.(*ast.Join); ok && j.Comma {
			j.Left = from[len(from)-1]
			from[len(from)-1] = j
			continue
		}
		from = append(from, next)
	}
	return from
}

// parseTableExprAfterComma parses the operand after a comma in FROM. SQLite
// treats that comma as a join operator, so it may carry ON or USING.
func (p *Parser) parseTableExprAfterComma() ast.TableExpr {
	if !p.d.Features.Query.FreeJoinKeywords {
		return p.parseTableExpr()
	}
	factor := p.parseTableFactor()
	if !p.isKw("ON", "USING") {
		return p.continueJoins(factor)
	}
	j := &ast.Join{Comma: true, Right: factor}
	if p.acceptKw("ON") {
		j.On = p.parseExpr()
	} else {
		p.expectKw("USING")
		j.Using = p.parseParenIdentList()
	}
	return j
}

// parseTableExpr parses a table factor followed by any number of joins.
func (p *Parser) parseTableExpr() ast.TableExpr {
	p.enterDepth()
	defer p.exitDepth()
	left := p.parseTableFactor()
	if p.d.Features.Query.FreeJoinKeywords && p.isKw("ON", "USING") {
		p.errorf(p.peek().Pos, "a JOIN clause is required before %s", p.peek().Value)
	}
	return p.continueJoins(left)
}

func (p *Parser) continueJoins(left ast.TableExpr) ast.TableExpr {
	for {
		j, ok := p.parseJoin(left)
		if !ok {
			return left
		}
		left = j
	}
}

func (p *Parser) parseJoin(left ast.TableExpr) (*ast.Join, bool) {
	j := &ast.Join{Left: left}
	save := p.pos
	if p.d.Features.Query.FreeJoinKeywords {
		p.parseFreeJoinKeywords(j)
	} else {
		p.parseJoinKeywords(j)
	}
	if !p.acceptKw("JOIN") {
		p.pos = save
		return nil, false
	}
	j.Right = p.parseTableFactor()
	strict := p.d.Features.Query.JoinRequiresQual
	needsQual := strict && j.Kind != ast.CrossJoin && !j.Natural
	if needsQual {
		// a JOIN b JOIN c ON x ON y nests to the right.
		for {
			inner, ok := p.parseJoin(j.Right)
			if !ok {
				break
			}
			j.Right = inner
		}
	}
	if needsQual || !strict {
		switch {
		case p.acceptKw("ON"):
			j.On = p.parseExpr()
		case p.acceptKw("USING"):
			j.Using = p.parseParenIdentList()
			if strict && p.acceptKw("AS") {
				id := p.parseIdent()
				j.UsingAlias = &id
			}
		case needsQual:
			p.unexpected("ON or USING")
		}
	}
	return j, true
}

// parseJoinKeywords parses standard [NATURAL] [kind [OUTER]] before JOIN.
func (p *Parser) parseJoinKeywords(j *ast.Join) {
	j.Natural = p.acceptKw("NATURAL")
	if j.Natural && p.isKw("CROSS") {
		p.unexpected("JOIN")
	}
	switch {
	case p.acceptKw("INNER"):
		j.Kind = ast.InnerJoin
	case p.acceptKw("LEFT"):
		j.Kind = ast.LeftJoin
		p.acceptKw("OUTER")
	case p.acceptKw("RIGHT"):
		j.Kind = ast.RightJoin
		p.acceptKw("OUTER")
	case p.acceptKw("FULL"):
		j.Kind = ast.FullJoin
		p.acceptKw("OUTER")
	case p.acceptKw("CROSS"):
		j.Kind = ast.CrossJoin
	}
}

// parseFreeJoinKeywords follows sqlite3JoinType: one to three join keywords
// in any order, rejecting only contradictory or incomplete combinations.
func (p *Parser) parseFreeJoinKeywords(j *ast.Join) {
	var left, right, inner, outer, cross bool
	start := p.peek().Pos
	n := 0
	for n < 3 {
		word := p.acceptOneOf("NATURAL", "LEFT", "RIGHT", "FULL", "INNER", "OUTER", "CROSS")
		if word == "" {
			break
		}
		n++
		switch word {
		case "NATURAL":
			j.Natural = true
		case "LEFT":
			left = true
		case "RIGHT":
			right = true
		case "FULL":
			left, right = true, true
		case "INNER":
			inner = true
		case "OUTER":
			outer = true
		case "CROSS":
			cross, inner = true, true
		}
	}
	if !p.isKw("JOIN") {
		return // not a join; the caller rewinds
	}
	if inner && (left || right || outer) || outer && !left && !right {
		p.errorf(start, "unknown join type")
	}
	switch {
	case left && right:
		j.Kind = ast.FullJoin
	case left:
		j.Kind = ast.LeftJoin
	case right:
		j.Kind = ast.RightJoin
	case cross:
		j.Kind = ast.CrossJoin
	}
}

// isSpecialSyntaxFunc reports a function call at the current token whose
// arguments use keywords, so it parses as an expression rather than a name.
func (p *Parser) isSpecialSyntaxFunc() bool {
	if !p.d.Features.Expr.StandardFuncSyntax {
		return false
	}
	switch {
	case p.isKwSeq("COLLATION", "FOR"):
		return p.isOpAt(2, "(")
	case p.isKw("CAST", "EXTRACT", "OVERLAY", "POSITION", "SUBSTRING", "TRIM"):
		return p.isOpAt(1, "(")
	}
	return false
}

func (p *Parser) parseTableFactor() ast.TableExpr {
	lateral := p.d.Features.Query.Lateral && p.acceptKw("LATERAL")
	if p.isOp("(") {
		return p.parseParenFactor(lateral)
	}
	if p.d.Features.Query.Lateral && p.isKwSeq("ROWS", "FROM") && p.isOpAt(2, "(") {
		return p.parseRowsFrom(lateral)
	}
	if t := p.peek(); p.d.Features.Expr.StandardFuncSyntax && t.Kind == lexer.Ident && p.d.BareFuncs[t.Value] && !p.isBareFuncCall() {
		fc := p.parseBareFunc().(*ast.FuncCall) // SESSION_USER and friends are table functions too
		return &ast.FuncRef{Lateral: lateral, Func: fc, Alias: p.parseAlias(true)}
	}
	if p.isSpecialSyntaxFunc() {
		fr := &ast.FuncRef{Lateral: lateral, Expr: p.parsePrimary()}
		fr.Ordinality = p.d.Features.Query.WithOrdinality && p.acceptKwSeq("WITH", "ORDINALITY")
		fr.Alias = p.parseAlias(true)
		return fr
	}
	if p.d.Features.Query.Unnest && p.isKw("UNNEST") && p.isOpAt(1, "(") {
		fr := &ast.FuncRef{Func: p.parseUnnest(), Alias: p.parseAlias(false)}
		if p.acceptKwSeq("WITH", "OFFSET") {
			fr.Offset = true
			if a := p.parseAlias(false); a != nil {
				fr.OffsetAlias = &a.Name
			}
		}
		return fr
	}
	only, name, namePos := p.parseRelationExprMaybeCall()
	if p.isOp("(") {
		if p.d.Features.Query.FreeJoinKeywords && p.isOpAt(1, "*") {
			p.next()
			p.unexpected("expression")
		}
		fr := &ast.FuncRef{Lateral: lateral, Func: p.parseFuncCall(name)}
		if fr.Func.Over != nil || fr.Func.Filter != nil || fr.Func.WithinGroup != nil {
			p.errorf(p.peek().Pos, "window and aggregate clauses are not allowed in FROM")
		}
		fr.Ordinality = p.d.Features.Query.WithOrdinality && p.acceptKwSeq("WITH", "ORDINALITY")
		fr.Alias = p.parseAlias(true)
		return fr
	}
	if lateral {
		p.unexpected("subquery or function after LATERAL")
	}
	p.checkRelationParts(name, namePos)
	tr := &ast.TableRef{Name: name, Only: only, Alias: p.parseAlias(false)}
	if p.d.Features.Query.TableSample && p.isKw("TABLESAMPLE") {
		tr.Sample = p.parseTableSample()
	}
	tr.Indexed = p.parseIndexed()
	return tr
}

func (p *Parser) parseParenFactor(lateral bool) ast.TableExpr {
	subquery := func() ast.TableExpr {
		p.expectOp("(")
		q := p.parseQuery()
		p.expectOp(")")
		return &ast.SubqueryRef{Lateral: lateral, Query: q, Alias: p.parseAlias(false)}
	}
	if p.isQueryStartAt(1) {
		if !p.isOpAt(1, "(") {
			return subquery()
		}
		// `((SELECT 1) JOIN b ...)` starts like a query but is a join tree.
		// Parse the inner query once and, if it isn't the whole parenthesized
		// content, reinterpret it as a table factor and continue from
		// there — never by backtracking and reparsing (see AGENT_TODO.md).
		p.expectOp("(")
		q := p.parseQuery()
		if p.isOp(")") {
			p.next()
			return &ast.SubqueryRef{Lateral: lateral, Query: q, Alias: p.parseAlias(false)}
		}
		ref := &ast.SubqueryRef{Lateral: lateral, Query: unwrapParenQuery(q), Alias: p.parseAlias(false)}
		return p.finishParenTable(p.continueJoins(ref))
	}
	if lateral {
		p.unexpected("subquery or function after LATERAL")
	}
	p.expectOp("(")
	return p.finishParenTable(p.parseTableExpr())
}

func (p *Parser) finishParenTable(inner ast.TableExpr) ast.TableExpr {
	for p.d.Features.Query.FreeJoinKeywords && p.acceptOp(",") { // SQLite: (a, b)
		inner = &ast.Join{Comma: true, Left: inner, Right: p.parseTableFactor()}
	}
	if p.d.Features.Query.ParenTableIsJoin && !ast.IsJoinTree(inner) {
		p.unexpected("JOIN")
	}
	p.expectOp(")")
	return &ast.ParenTable{Expr: inner, Alias: p.parseAlias(false)}
}

func (p *Parser) parseRowsFrom(lateral bool) ast.TableExpr {
	p.expectKw("ROWS", "FROM")
	p.expectOp("(")
	fr := &ast.FuncRef{Lateral: lateral}
	for {
		fr.RowsFrom = append(fr.RowsFrom, p.parseFuncCall(p.parseName()))
		var defs []*ast.ColumnDef
		if p.isKw("AS") && p.isOpAt(1, "(") {
			p.pos += 2
			for {
				defs = append(defs, &ast.ColumnDef{Name: p.parseIdent(), Type: p.parseType()})
				if !p.acceptOp(",") {
					break
				}
			}
			p.expectOp(")")
		}
		fr.RowsFromColumns = append(fr.RowsFromColumns, defs)
		if !p.acceptOp(",") {
			break
		}
	}
	p.expectOp(")")
	fr.Ordinality = p.d.Features.Query.WithOrdinality && p.acceptKwSeq("WITH", "ORDINALITY")
	fr.Alias = p.parseAlias(true)
	return fr
}

// acceptLegacyStar consumes PostgreSQL's noise `t *`, meaning t with children.
// parseRelationExpr parses PostgreSQL's relation_expr: name, name *, ONLY name
// or ONLY (name), and reports whether ONLY was written.
func (p *Parser) parseRelationExpr() (bool, *ast.Name) {
	only, name, pos := p.parseRelationExprMaybeCall()
	p.checkRelationParts(name, pos)
	return only, name
}

// parseRelationExprMaybeCall is parseRelationExpr for FROM, where the name may
// turn out to be a function's, which takes any number of parts. The caller
// checks the parts once it knows, using the returned offset of the name.
func (p *Parser) parseRelationExprMaybeCall() (bool, *ast.Name, int) {
	if !p.d.Features.Query.OnlyTable || !p.acceptKw("ONLY") {
		pos := p.peek().Pos
		name := p.parseName()
		p.acceptLegacyStar()
		return false, name, pos
	}
	paren := p.acceptOp("(")
	pos := p.peek().Pos
	name := p.parseName()
	if paren {
		p.expectOp(")")
	}
	return true, name, pos
}

func (p *Parser) acceptLegacyStar() {
	if p.d.Features.Query.OnlyTable {
		p.acceptOp("*")
	}
}

func (p *Parser) parseTableSample() *ast.TableSample {
	p.expectKw("TABLESAMPLE")
	ts := &ast.TableSample{Method: p.parseName()}
	p.expectOp("(")
	ts.Args = p.parseExprList()
	p.expectOp(")")
	if p.acceptKw("REPEATABLE") {
		p.expectOp("(")
		ts.Repeatable = p.parseExpr()
		p.expectOp(")")
	}
	return ts
}

// parseAlias parses an optional `[AS] name [(col, ...)]`. For functions
// returning records (typed) columns may carry types, all or none of them, and
// `AS (col type, ...)` needs no name.
func (p *Parser) parseAlias(typed bool) *ast.Alias {
	a := &ast.Alias{}
	switch {
	case p.acceptKw("AS"):
		if !typed || !p.isOp("(") {
			a.Name = p.parseIdent()
		}
	case p.canBeBareAlias():
		a.Name = p.parseIdent()
	default:
		return nil
	}
	if p.d.Features.DDL.ExtendedDDL && p.acceptOp("(") {
		for {
			cd := &ast.ColumnDef{Name: p.parseIdent()}
			if typed && !p.isOp(",") && !p.isOp(")") {
				cd.Type = p.parseType()
			}
			if len(a.Columns) > 0 && (a.Columns[0].Type == nil) != (cd.Type == nil) {
				p.unexpected("the same form for every column of the alias")
			}
			a.Columns = append(a.Columns, cd)
			if !p.acceptOp(",") {
				break
			}
		}
		p.expectOp(")")
		if a.Name.Name == "" && a.Columns[0].Type == nil {
			p.unexpected("column types in AS (...)")
		}
	}
	return a
}

// parseReturning parses RETURNING items if the dialect supports it.
func (p *Parser) parseReturning() []*ast.SelectItem {
	if !p.d.Features.DML.Returning || !p.acceptKw("RETURNING") {
		return nil
	}
	return p.parseSelectItems()
}

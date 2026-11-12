package parser

import (
	"context"
	"strings"

	"sqlp/ast"
	"sqlp/dialect"
	"sqlp/lexer"
)

// Script is the result of ParseScript: one entry per non-empty statement.
type Script struct {
	Statements []Statement
}

// Statement is one statement of a script. Exactly one of Node and Err is set.
type Statement struct {
	Node   ast.Statement
	Err    *Error
	Text   string // exact source text, without the terminating ';'
	Span   Span
	Opaque bool // recognized by its leading keywords, not really parsed
}

// Span locates text in the source. Offsets are in bytes and End is just past
// the last byte; lines and columns count from 1, and columns count characters
// (Unicode code points).
type Span struct {
	Start, End                                 int
	StartLine, StartColumn, EndLine, EndColumn int
}

// ParseScript parses a ';'-separated script and keeps going past statements
// that do not parse, reporting each in its Statement.Err. The returned error
// covers only what affects the whole script: the size limit and cancellation.
//
// After a bad statement parsing resumes at the next ';', skipping BEGIN ... END
// bodies. Brackets are deliberately ignored, or one unclosed '(' would swallow
// the rest of the script. Recovery is best effort: a body that is itself broken
// can resume in the wrong place.
func ParseScript(ctx context.Context, sql string, d *dialect.Dialect, limits Limits) (s *Script, err error) {
	defer func() {
		if err != nil {
			s = nil
		}
	}()
	defer recoverAny(&err)
	if err := checkSize(sql, limits); err != nil {
		return nil, err
	}
	p := &Parser{d: d, src: sql, toks: scriptTokens(sql, d), ctx: ctx, maxDepth: limits.maxDepth()}
	s = &Script{}
	for {
		for p.isOp(";") {
			p.pos++
		}
		if p.peek().Kind == lexer.EOF {
			return s, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, p.errorAt(p.peek().Pos, "parse aborted: "+err.Error())
		}
		s.Statements = append(s.Statements, p.scriptStatement())
	}
}

// scriptTokens lexes sql, keeping a lexing error as the last token before EOF
// so that the statements in front of it still parse.
func scriptTokens(sql string, d *dialect.Dialect) []lexer.Token {
	lx := lexer.New(sql, d.Lexer)
	var toks []lexer.Token
	for {
		t := lx.Next()
		if t.Kind == lexer.Comment {
			continue
		}
		toks = append(toks, t)
		switch t.Kind {
		case lexer.EOF:
			return toks
		case lexer.Illegal:
			return append(toks, lexer.Token{Kind: lexer.EOF, Pos: len(sql)})
		}
	}
}

func (p *Parser) scriptStatement() Statement {
	start := p.pos
	p.depth = 0
	node, err := p.tryStatement()
	if err == nil {
		return p.locate(start, Statement{Node: node, Opaque: ast.IsOpaque(node)})
	}
	bad := err.(*Error)
	if t := p.toks[len(p.toks)-2:][0]; len(p.toks) > 1 && t.Kind == lexer.Illegal && bad.Pos >= t.Pos {
		bad = p.errorAt(t.Pos, t.Value)
	}
	p.skipStatement(start)
	return p.locate(start, Statement{Err: bad})
}

func (p *Parser) tryStatement() (node ast.Statement, err error) {
	defer p.recover(&err)
	node = p.parseStatement()
	if p.peek().Kind != lexer.EOF && !p.isOp(";") {
		p.expectOp(";")
	}
	return node, nil
}

// skipStatement moves to the ';' or EOF ending the statement at start.
func (p *Parser) skipStatement(start int) {
	p.pos = start
	body, words, trigger := 0, 0, false
	for ; ; p.pos++ {
		t := p.peek()
		switch t.Kind {
		case lexer.EOF, lexer.Illegal:
			return
		case lexer.Op:
			if t.Text == ";" && body == 0 {
				return
			}
		case lexer.Ident:
			if words++; words <= 6 && t.Value == "TRIGGER" && p.toks[start].Value == "CREATE" {
				trigger = true
			}
			switch {
			case t.Value == "BEGIN" && body == 0 && (trigger || p.isKwAt(1, "ATOMIC")):
				body = 1
			case t.Value == "CASE" && body > 0:
				body++
			case t.Value == "END" && body > 0:
				body--
			}
		}
	}
}

// locate fills in where the statement from token start to p.pos sits in the
// source, and leaves p.pos past any lexing error.
func (p *Parser) locate(start int, s Statement) Statement {
	end := len(strings.TrimRight(p.src, " \t\r\n"))
	if p.peek().Kind == lexer.Illegal {
		p.pos++
	} else {
		last := p.toks[p.pos-1]
		end = last.Pos + len(last.Text)
	}
	from := p.toks[start].Pos
	s.Text = p.src[from:end]
	s.Span = Span{Start: from, End: end}
	s.Span.StartLine, s.Span.StartColumn = p.lineCol(from)
	s.Span.EndLine, s.Span.EndColumn = p.lineCol(end)
	return s
}

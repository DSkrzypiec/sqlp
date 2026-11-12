package ast

// BigQuery's procedural language (UtilityFeatures.Scripting). A statement
// list holds the statements of a block or branch, each written with its ';'.

// Declare is DECLARE names [type] [DEFAULT expr].
type Declare struct {
	stmtBase
	Names   []Ident
	Type    *DataType
	Default Expr
}

// Assign is SET name = expr, SET (names) = expr or SET @@system_variable =
// expr. Each target is a *Name or, for a system variable, a *Param.
type Assign struct {
	stmtBase
	Targets []Expr
	Paren   bool
	Value   Expr
}

// Block is [label:] BEGIN statements [EXCEPTION WHEN ERROR THEN statements]
// END [label]. EndLabel is set when the label is repeated after END.
type Block struct {
	stmtBase
	Label        *Ident
	Body         []Statement
	HasException bool
	Exception    []Statement
	EndLabel     bool
}

// CondBranch is one condition and the statements it guards: IF's and
// ELSEIF's, or CASE's WHEN.
type CondBranch struct {
	Cond Expr
	Body []Statement
}

// IfStmt is IF cond THEN ... [ELSEIF cond THEN ...]... [ELSE ...] END IF.
type IfStmt struct {
	stmtBase
	Branches []*CondBranch
	HasElse  bool
	Else     []Statement
}

// CaseStmt is CASE [operand] WHEN x THEN ... [ELSE ...] END CASE.
type CaseStmt struct {
	stmtBase
	Operand Expr
	Whens   []*CondBranch
	HasElse bool
	Else    []Statement
}

// LoopStmt is a loop, with an optional label:
//
//	LOOP ... END LOOP
//	WHILE cond DO ... END WHILE
//	REPEAT ... UNTIL cond END REPEAT
//	FOR var IN (query) DO ... END FOR
type LoopStmt struct {
	stmtBase
	Label    *Ident
	Kind     string // LOOP, WHILE, REPEAT or FOR
	Cond     Expr   // WHILE's and REPEAT's
	Var      *Ident // FOR's
	Query    *Query // FOR's
	Body     []Statement
	EndLabel bool
}

// LoopControl is BREAK, LEAVE, CONTINUE or ITERATE [label].
type LoopControl struct {
	stmtBase
	Word  string
	Label *Ident
}

// RaiseStmt is RAISE [USING MESSAGE = expr]; without a message it re-raises
// the exception being handled.
type RaiseStmt struct {
	stmtBase
	Message Expr
}

// ExecuteImmediate is EXECUTE IMMEDIATE sql [INTO vars] [USING expr [AS
// name], ...].
type ExecuteImmediate struct {
	stmtBase
	SQL   Expr
	Into  []Ident
	Using []*SelectItem
}

// Assert is ASSERT expr [AS description].
type Assert struct {
	stmtBase
	Expr        Expr
	Description Expr
}

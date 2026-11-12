package ast

// stmtBase gives embedding types the Statement marker methods; alterBase does
// the same for AlterAction.
type stmtBase struct{}

func (stmtBase) node() {}
func (stmtBase) stmt() {}

type alterBase struct{}

func (alterBase) node()        {}
func (alterBase) alterAction() {}

// Opaque is a statement recognized by its leading keywords but not modelled
// in detail. Text is the exact source of the statement. Its syntax has NOT
// been validated beyond balanced parentheses.
type Opaque struct {
	stmtBase
	Verb string // upper-cased leading keywords, e.g. "ALTER ROLE"
	Text string
	// Checked is set when the parser checked the statement's grammar and
	// kept only its text; otherwise any tail was accepted.
	Checked bool
}

// AlterOpaque is an ALTER TABLE action kept verbatim (see Opaque).
type AlterOpaque struct {
	alterBase
	Text string
}

// Merge is MERGE INTO target USING source ON cond WHEN ...
type Merge struct {
	stmtBase
	With   *With
	Only   bool
	Target *Name
	Alias  *Alias
	Source TableExpr
	On     Expr
	Whens  []*MergeWhen
}

// MergeWhen is one WHEN [NOT] MATCHED [AND cond] THEN action arm.
type MergeWhen struct {
	NotMatched    bool
	By            string // NOT MATCHED BY TARGET or BY SOURCE (BigQuery), or ""
	InsertRow     bool   // INSERT ROW (BigQuery)
	Cond          Expr
	DoNothing     bool
	Delete        bool
	Update        []*Assignment
	Insert        bool
	Columns       []Expr // *Name, or *Subscript / dotted *Name
	Overriding    string
	DefaultValues bool
	Values        []Expr
}

// Grant is GRANT or REVOKE of privileges on objects, or of role membership
// when ObjectKind and Objects are empty and IsRole is true.
type Grant struct {
	stmtBase
	Revoke     bool
	OptionFor  string // REVOKE x OPTION FOR: GRANT, ADMIN, INHERIT, SET
	All        bool   // ALL [PRIVILEGES]
	Privileges []*Privilege
	IsRole     bool   // role membership form
	ObjectKind string // TABLE, SEQUENCE, SCHEMA, ... ; "" for the implicit TABLE
	AllIn      string // TABLES, SEQUENCES, FUNCTIONS, ... (ALL x IN SCHEMA)
	Objects    []*Name
	Grantees   []Expr // *Name or *Word (PUBLIC, CURRENT_USER, ...)
	WithOption string // GRANT OPTION, ADMIN OPTION, INHERIT TRUE, ...
	GrantedBy  Expr
	Behavior   string
}

// Privilege is one privilege with an optional column list.
type Privilege struct {
	Name    Ident
	Columns []Ident
}

// Copy is COPY table|(query) FROM|TO target.
type Copy struct {
	stmtBase
	Binary     bool // COPY BINARY t
	Table      *Name
	Columns    []Ident
	Delimiters *Literal // USING DELIMITERS 'x', before the options
	Query      Statement
	To         bool
	Program    bool
	Target     Expr // *Literal, or *Word for STDIN/STDOUT
	ParenOpts  []Option
	LegacyOpts []Option
	LegacyWith bool
	Where      Expr
}

// CommentOn is COMMENT ON kind object IS 'text'|NULL.
type CommentOn struct {
	stmtBase
	Kind   string
	Object *Name
	Type   *DataType // TYPE and DOMAIN name a type instead: int[], double precision
	On     *Name     // TRIGGER/CONSTRAINT/RULE/POLICY name ON table
	// OnDomain is COMMENT ON CONSTRAINT c ON DOMAIN d.
	OnDomain bool
	Comment  Expr // *Literal
}

// Do is DO [LANGUAGE l] 'code'.
// PostgreSQL's grammar takes the parts in any order and number.
type Do struct {
	stmtBase
	Parts []DoPart
}

// DoPart is LANGUAGE l or the code.
type DoPart struct {
	Language string // as written: a word or a quoted string
	Code     string // when Language is ""
}

// Call is CALL proc(args).
type Call struct {
	stmtBase
	Func *FuncCall
}

// Prepare is PREPARE name [(types)] AS statement.
type Prepare struct {
	stmtBase
	Name      Ident
	Types     []*DataType
	Statement Statement
}

// Execute is EXECUTE name [(args)].
type Execute struct {
	stmtBase
	Name Ident
	Args []Expr
}

// Deallocate is DEALLOCATE [PREPARE] name|ALL.
type Deallocate struct {
	stmtBase
	Name *Ident
	All  bool
}

// Listen is LISTEN channel.
type Listen struct {
	stmtBase
	Channel Ident
}

// Unlisten is UNLISTEN channel|*.
type Unlisten struct {
	stmtBase
	Channel *Ident
}

// Notify is NOTIFY channel [, 'payload'].
type Notify struct {
	stmtBase
	Channel Ident
	Payload *string
}

// Lock is LOCK [TABLE] [ONLY] names [IN mode MODE] [NOWAIT].
type Lock struct {
	stmtBase
	Tables []*TableRef // only Name and Only are used
	Mode   string
	NoWait bool
}

// RefreshMatView is REFRESH MATERIALIZED VIEW.
type RefreshMatView struct {
	stmtBase
	Concurrently bool
	Name         *Name
	WithData     *bool
}

// CreateDomain is CREATE DOMAIN name [AS] type [constraints].
type CreateDomain struct {
	stmtBase
	Name        *Name
	Type        *DataType
	Constraints []*ColumnConstraint
}

// CreateCompositeType is CREATE TYPE name AS (attrs), or a shell type
// (CREATE TYPE name) when Shell is set.
type CreateCompositeType struct {
	stmtBase
	Name    *Name
	Shell   bool
	Columns []*ColumnDef
}

// CreateFunction is CREATE FUNCTION or CREATE PROCEDURE.
type CreateFunction struct {
	stmtBase
	Procedure    bool
	OrReplace    bool
	Name         *Name
	Params       []*FuncParam
	Returns      *DataType
	ReturnsSetOf bool
	ReturnsTable []*ColumnDef
	Options      []FuncOption
	Return       Expr        // RETURN expr
	Body         []Statement // BEGIN ATOMIC ... END
	Atomic       bool
}

// ReturnStmt is RETURN expr inside BEGIN ATOMIC.
type ReturnStmt struct {
	stmtBase
	Expr Expr
}

// FuncParam is `[mode] [name] type [DEFAULT expr]`.
type FuncParam struct {
	Mode    string
	Name    *Ident
	Type    *DataType
	Default Expr
}

// FuncOption is a function attribute. Value is printed after Name when set.
type FuncOption struct {
	Name   string
	Values []Expr
	Types  []*DataType // TRANSFORM FOR TYPE a, FOR TYPE b
}

// TriggerEvent is INSERT, DELETE, TRUNCATE or UPDATE [OF cols].
type TriggerEvent struct {
	Kind    string
	Columns []Ident
}

// CreateTrigger covers both PostgreSQL triggers (Function set) and SQLite
// triggers (Body set).
type CreateTrigger struct {
	stmtBase
	OrReplace   bool
	Temp        bool
	Constraint  bool
	IfNotExists bool
	Name        *Name
	Timing      string // BEFORE, AFTER, INSTEAD OF or ""
	Events      []TriggerEvent
	Table       *Name
	From        *Name
	Deferrable  *Deferrable
	Referencing []string // "OLD TABLE AS x", ...
	ForEach     string   // ROW, STATEMENT or ""
	When        Expr
	Function    *FuncCall
	Body        []Statement
}

// CreateVirtualTable is SQLite's CREATE VIRTUAL TABLE name USING module(args).
// Args are kept as source text: their syntax belongs to the module.
type CreateVirtualTable struct {
	stmtBase
	IfNotExists bool
	Name        *Name
	Module      Ident
	HasArgs     bool
	Args        []string
}

func (*CTESearch) node()      {}
func (*CTECycle) node()       {}
func (*IntoTable) node()      {}
func (*TableSample) node()    {}
func (*TableLike) node()      {}
func (*PartitionSpec) node()  {}
func (*PartitionBound) node() {}
func (*ExcludeElem) node()    {}
func (*MergeWhen) node()      {}
func (*Privilege) node()      {}
func (*FuncParam) node()      {}
func (*MaintTarget) node()    {}

// BigQuery's ALTER actions, beside those of AlterTable's own.
type (
	// SetOptions is SET OPTIONS (...).
	SetOptions struct {
		alterBase
		Options []Option
	}
	// SetDefaultCollate is SET DEFAULT COLLATE 'collation'.
	SetDefaultCollate struct {
		alterBase
		Collate string
	}
	// DropPrimaryKey is DROP PRIMARY KEY [IF EXISTS].
	DropPrimaryKey struct {
		alterBase
		IfExists bool
	}
	// AddReplica is a schema's ADD REPLICA name [OPTIONS (...)].
	AddReplica struct {
		alterBase
		Name    Ident
		Options []Option
	}
	// DropReplica is a schema's DROP REPLICA name.
	DropReplica struct {
		alterBase
		Name Ident
	}
)

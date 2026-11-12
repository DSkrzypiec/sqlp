package ast

// BigQuery's ML, export and load statements (DDLFeatures.GoogleSQL).

// CreateModel is CREATE [OR REPLACE] [TEMP] MODEL [IF NOT EXISTS] name
// [TRANSFORM (items)] [INPUT (columns) OUTPUT (columns)] [REMOTE [WITH
// CONNECTION name|DEFAULT]] [OPTIONS (...)] [AS query | AS (training_data AS
// (query), custom_holiday AS (query))].
type CreateModel struct {
	stmtBase
	OrReplace         bool
	Temp              bool
	IfNotExists       bool
	Name              *Name
	Transform         []*SelectItem
	Input, Output     []*ColumnDef
	Remote            bool
	Connection        *Name
	ConnectionDefault bool
	Options           []Option
	AsQuery           *Query
	TrainingData      *Query
	CustomHoliday     *Query
}

// ExportData is EXPORT DATA [WITH CONNECTION name] [OPTIONS (...)] AS query.
type ExportData struct {
	stmtBase
	Connection *Name
	Options    []Option
	Query      *Query
}

// ExportModel is EXPORT MODEL name [OPTIONS (...)].
type ExportModel struct {
	stmtBase
	Name    *Name
	Options []Option
}

// LoadData is LOAD DATA {INTO|OVERWRITE} [TEMP TABLE] name [[OVERWRITE]
// PARTITIONS (column = value)] [(columns)] [PARTITION BY expr] [CLUSTER BY
// exprs] [OPTIONS (...)] FROM FILES (...) [WITH PARTITION COLUMNS [(columns)]]
// [WITH CONNECTION name].
type LoadData struct {
	stmtBase
	Overwrite           bool
	Temp                bool
	Table               *Name
	Partitions          *Option // PARTITIONS (column = value)
	OverwritePartitions bool
	HasColumns          bool
	Columns             []*ColumnDef
	PartitionExpr       Expr
	ClusterBy           []Expr
	Options             []Option
	Files               []Option
	HasPartitionColumns bool
	PartitionColumns    []*ColumnDef
	Connection          *Name
}

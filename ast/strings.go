package ast

import "strconv"

var (
	literalKindNames = []string{
		Null: "Null", True: "True", False: "False", Integer: "Integer", Float: "Float",
		StringLit: "StringLit", BlobLit: "BlobLit", BytesLit: "BytesLit",
	}
	isKindNames = []string{
		IsNull: "IsNull", IsTrue: "IsTrue", IsFalse: "IsFalse", IsUnknown: "IsUnknown",
		IsDistinctFrom: "IsDistinctFrom", IsOther: "IsOther",
	}
	castStyleNames = []string{
		CastStandard: "CastStandard", CastDoubleColon: "CastDoubleColon", CastTry: "CastTry", CastSafe: "CastSafe",
	}
	timeZoneSpecNames = []string{
		TZUnspecified: "TZUnspecified", TZWith: "TZWith", TZWithout: "TZWithout",
	}
	distinctKindNames = []string{
		DistinctNone: "DistinctNone", DistinctAll: "DistinctAll", Distinct: "Distinct", DistinctOn: "DistinctOn",
	}
	setOpKindNames = []string{
		Union: "Union", Intersect: "Intersect", Except: "Except",
	}
	frameBoundKindNames = []string{
		CurrentRow: "CurrentRow", UnboundedPreceding: "UnboundedPreceding", UnboundedFollowing: "UnboundedFollowing",
		Preceding: "Preceding", Following: "Following",
	}
	joinKindNames = []string{
		InnerJoin: "InnerJoin", LeftJoin: "LeftJoin", RightJoin: "RightJoin", FullJoin: "FullJoin", CrossJoin: "CrossJoin",
	}
	columnConstraintKindNames = []string{
		NotNullConstraint: "NotNullConstraint", NullConstraint: "NullConstraint",
		PrimaryKeyConstraint: "PrimaryKeyConstraint", UniqueConstraint: "UniqueConstraint",
		CheckConstraint: "CheckConstraint", DefaultConstraint: "DefaultConstraint",
		ReferencesConstraint: "ReferencesConstraint", AttributeConstraint: "AttributeConstraint",
		GeneratedConstraint: "GeneratedConstraint", IdentityConstraint: "IdentityConstraint",
		CollateConstraint: "CollateConstraint",
	}
	tableConstraintKindNames = []string{
		TablePrimaryKey: "TablePrimaryKey", TableUnique: "TableUnique", TableCheck: "TableCheck",
		TableForeignKey: "TableForeignKey", TableExclude: "TableExclude", TableNameOnly: "TableNameOnly",
	}
	alterColumnKindNames = []string{
		SetDefault: "SetDefault", DropDefault: "DropDefault", SetNotNull: "SetNotNull",
		DropNotNull: "DropNotNull", SetType: "SetType", AddIdentity: "AddIdentity",
		SetColumnOptions: "SetColumnOptions", SetIdentity: "SetIdentity", DropGenerated: "DropGenerated",
	}
)

func enumName(names []string, v uint8) string {
	if int(v) < len(names) {
		return names[v]
	}
	return "Unknown(" + strconv.Itoa(int(v)) + ")"
}

func (k LiteralKind) String() string          { return enumName(literalKindNames, uint8(k)) }
func (k IsKind) String() string               { return enumName(isKindNames, uint8(k)) }
func (k CastStyle) String() string            { return enumName(castStyleNames, uint8(k)) }
func (k TimeZoneSpec) String() string         { return enumName(timeZoneSpecNames, uint8(k)) }
func (k DistinctKind) String() string         { return enumName(distinctKindNames, uint8(k)) }
func (k SetOpKind) String() string            { return enumName(setOpKindNames, uint8(k)) }
func (k FrameBoundKind) String() string       { return enumName(frameBoundKindNames, uint8(k)) }
func (k JoinKind) String() string             { return enumName(joinKindNames, uint8(k)) }
func (k ColumnConstraintKind) String() string { return enumName(columnConstraintKindNames, uint8(k)) }
func (k TableConstraintKind) String() string  { return enumName(tableConstraintKindNames, uint8(k)) }
func (k AlterColumnKind) String() string      { return enumName(alterColumnKindNames, uint8(k)) }

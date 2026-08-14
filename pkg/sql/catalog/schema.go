// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package catalog maps go-qrdb's persisted, Raft-replicated relational
// table catalog (service.TableDef) onto go-mysql-server's query-engine
// types, and provides the binary row and key codecs that store rows under
// those types.
//
// SchemaOf, PrimaryKeySchemaOf, ColumnTypeOf, and TypeStringOf translate
// between the canonical MySQL type strings a TableDef carries (e.g.
// "VARCHAR(255)", "DECIMAL(10,2)", "BIGINT UNSIGNED") and go-mysql-server's
// sql.Type values. RowCodec and KeyCodec (see rowcodec.go and keycodec.go)
// then use those sql.Type values to encode and decode rows and primary
// keys.
//
// GEOMETRY and the other spatial types are explicitly out of scope: they
// are rejected by ColumnTypeOf with a typed error at DDL time, not
// discovered as a decode failure on first insert.
package catalog

import (
	"strconv"
	"strings"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/sqltypes"
	"github.com/dolthub/vitess/go/vt/proto/query"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// SchemaOf builds a go-mysql-server schema from a table definition. Column
// order matches def.Columns exactly, which is what keeps ADD COLUMN a
// metadata-only operation: RowCodec relies on that same order to treat an
// older row as a prefix of the current column list.
//
// A table declaring no PRIMARY KEY gets one extra, trailing column: the
// hidden system row id that physically keys it (see rowid.go). It is
// appended rather than prepended so every declared column keeps the ordinal
// RowCodec, the secondary-index specs and the query planner all address it
// by.
func SchemaOf(def *service.TableDef) (sqle.Schema, error) {
	if def == nil {
		return nil, &NilTableDefError{}
	}
	if len(def.Columns) == 0 {
		return nil, &EmptyColumnsError{Table: def.Name}
	}

	pkNames := make(map[string]bool, len(def.PrimaryKey))
	for _, pk := range def.PrimaryKey {
		pkNames[pk.Name] = true
	}

	schema := make(sqle.Schema, 0, len(def.Columns)+1)
	for _, col := range def.Columns {
		t, err := ColumnTypeOf(col)
		if err != nil {
			return nil, err
		}
		schema = append(schema, &sqle.Column{
			Name:          col.Name,
			Type:          t,
			Nullable:      col.Nullable,
			PrimaryKey:    pkNames[col.Name],
			AutoIncrement: col.AutoIncrement,
			Comment:       col.Comment,
			Extra:         col.Extra,
			Default:       ColumnDefaultOf(col),
			Source:        def.Name,
		})
	}
	if HasRowID(def) {
		schema = append(schema, rowIDColumn(def.Name))
	}
	return schema, nil
}

// ColumnDefaultOf reconstructs the go-mysql-server column default a
// TableDef column declares, or nil when it declares none.
//
// ColumnDef.Default is persisted in exactly the canonical, self-describing
// text (*sql.ColumnDefaultValue).String() emits -- which is what
// pkg/sql/provider's DDL path writes; see columnDefFromSQL -- so it is
// handed straight back with no discriminator to recombine: a literal is
// stored verbatim, an expression parenthesized, and the bare
// CURRENT_TIMESTAMP forms unparenthesized because MySQL both accepts and
// emits them that way (and the engine's parser rejects them parenthesized
// for a non-temporal column). See defaultIsExpression for the one place
// that still has to tell the two apart.
//
// The result is an *unresolved* default: the expression text is handed back
// to go-mysql-server's own parser rather than parsed here. That is the
// integration contract the engine defines for a table implementation --
// go-mysql-server's own memory provider does exactly this (see
// memory.NewPartitionedTableWithCollation) and every path that needs a
// usable default resolves it through planbuilder.ResolveSchemaDefaults:
// INSERT (planbuilder.Builder.buildInsertInto), information_schema.COLUMNS
// (the resolve_column_defaults analyzer rule) and SHOW CREATE TABLE. Doing
// it any other way would mean a second, qrdb-local SQL expression parser
// that could only ever handle the literal half.
func ColumnDefaultOf(col service.ColumnDef) *sqle.ColumnDefaultValue {
	text := strings.TrimSpace(col.Default)
	if text == "" {
		return nil
	}
	return sqle.NewUnresolvedColumnDefaultValue(text)
}

// defaultIsExpression reports whether a column's persisted default text
// denotes an expression rather than a literal, reading that off the text
// itself: an expression is parenthesized, or is one of the bare
// CURRENT_TIMESTAMP forms. text is expected already trimmed.
//
// The parentheses are a sound discriminator on their own because the only
// literal that can contain one is a string literal, and a string literal is
// always quoted -- so its first byte is a quote, never '('.
//
// Only RowCodec.defaultFor needs this: it fills a column added after a row
// was written and can evaluate a literal statically but not an expression.
// Every other reader hands the text to go-mysql-server's own parser, which
// makes the same distinction itself.
func defaultIsExpression(text string) bool {
	if bareTemporalDefaults[strings.ToUpper(text)] {
		return true
	}
	return strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")")
}

// bareTemporalDefaults lists the expression defaults MySQL writes without
// parentheses, mirroring the special case in
// (*sql.ColumnDefaultValue).String(). NOW() is the form the engine's own
// expression tree renders CURRENT_TIMESTAMP as, so both spellings appear in
// persisted catalogs depending on which the DDL statement used.
var bareTemporalDefaults = map[string]bool{
	"NOW()":                true,
	"NOW(0)":               true,
	"NOW(1)":               true,
	"NOW(2)":               true,
	"NOW(3)":               true,
	"NOW(4)":               true,
	"NOW(5)":               true,
	"NOW(6)":               true,
	"CURRENT_TIMESTAMP":    true,
	"CURRENT_TIMESTAMP()":  true,
	"CURRENT_TIMESTAMP(0)": true,
	"CURRENT_TIMESTAMP(1)": true,
	"CURRENT_TIMESTAMP(2)": true,
	"CURRENT_TIMESTAMP(3)": true,
	"CURRENT_TIMESTAMP(4)": true,
	"CURRENT_TIMESTAMP(5)": true,
	"CURRENT_TIMESTAMP(6)": true,
}

// PrimaryKeySchemaOf builds a go-mysql-server primary key schema from a
// table definition, resolving each declared PKColumn to its ordinal
// position in the schema.
//
// A table with no declared primary key returns a PrimaryKeySchema with an
// empty key ordinal set, even though such a table IS physically keyed, by
// the hidden row id column SchemaOf appends (see rowid.go). That is
// deliberate and matches MySQL: PkOrdinals is what SHOW CREATE TABLE renders
// as a PRIMARY KEY clause and what information_schema reports as a key, and
// a keyless InnoDB table reports neither -- its DB_ROW_ID is an
// implementation detail, not a declared key.
func PrimaryKeySchemaOf(def *service.TableDef) (sqle.PrimaryKeySchema, error) {
	schema, err := SchemaOf(def)
	if err != nil {
		return sqle.PrimaryKeySchema{}, err
	}
	if len(def.PrimaryKey) == 0 {
		return sqle.NewPrimaryKeySchema(schema), nil
	}

	ordByName := make(map[string]int, len(schema))
	for i, c := range schema {
		ordByName[c.Name] = i
	}

	ords := make([]int, 0, len(def.PrimaryKey))
	for _, pk := range def.PrimaryKey {
		idx, ok := ordByName[pk.Name]
		if !ok {
			return sqle.PrimaryKeySchema{}, &ColumnNotFoundError{Table: def.Name, Column: pk.Name}
		}
		ords = append(ords, idx)
	}
	return sqle.NewPrimaryKeySchema(schema, ords...), nil
}

// ColumnTypeOf resolves a column's canonical MySQL type string (e.g.
// "VARCHAR(255)", "BIGINT UNSIGNED", "DECIMAL(10,2)") to a go-mysql-server
// sql.Type. GEOMETRY and the other spatial types are rejected with a
// *SpatialTypeUnsupportedError; any other unrecognized type name is
// rejected with a *UnsupportedColumnTypeError.
func ColumnTypeOf(col service.ColumnDef) (sqle.Type, error) {
	name, params, unsigned, err := parseTypeString(col.Type)
	if err != nil {
		return nil, err
	}

	if spatialTypeNames[name] {
		return nil, &SpatialTypeUnsupportedError{Column: col.Name, Type: col.Type}
	}

	build, ok := columnTypeBuilders[name]
	if !ok {
		return nil, &UnsupportedColumnTypeError{Column: col.Name, Type: col.Type}
	}

	t, err := build(params, unsigned)
	if err != nil {
		return nil, &InvalidColumnTypeError{Column: col.Name, Type: col.Type, Reason: err.Error()}
	}
	return t, nil
}

// TypeStringOf is the inverse of ColumnTypeOf: it reconstructs the
// canonical MySQL type string for a go-mysql-server sql.Type, for use when
// reconstructing DDL (e.g. SHOW CREATE TABLE) from a resolved schema.
func TypeStringOf(t sqle.Type) string {
	if t == nil {
		return ""
	}
	if f, ok := typeStringFormatters[t.Type()]; ok {
		return f(t)
	}
	return strings.ToUpper(t.Type().String())
}

// ---------------------------------------------------------------------------
// Type string parsing
// ---------------------------------------------------------------------------

// spatialTypeNames lists the spatial type names rejected by ColumnTypeOf.
var spatialTypeNames = map[string]bool{
	"GEOMETRY":           true,
	"POINT":              true,
	"LINESTRING":         true,
	"POLYGON":            true,
	"MULTIPOINT":         true,
	"MULTILINESTRING":    true,
	"MULTIPOLYGON":       true,
	"GEOMETRYCOLLECTION": true,
}

// parseTypeString splits a canonical MySQL type string such as
// "VARCHAR(255)", "BIGINT UNSIGNED", or "ENUM('a','b')" into its base type
// name, its parenthesized parameters (unquoted, in order), and whether it
// carries an UNSIGNED modifier.
func parseTypeString(raw string) (name string, params []string, unsigned bool, err error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil, false, &InvalidTypeStringError{Type: raw, Reason: "empty type string"}
	}

	upper := strings.ToUpper(s)
	const unsignedSuffix = " UNSIGNED"
	if strings.HasSuffix(upper, unsignedSuffix) {
		unsigned = true
		s = strings.TrimSpace(s[:len(s)-len(unsignedSuffix)])
	}

	idx := strings.IndexByte(s, '(')
	if idx < 0 {
		return strings.ToUpper(s), nil, unsigned, nil
	}
	if !strings.HasSuffix(s, ")") {
		return "", nil, false, &InvalidTypeStringError{Type: raw, Reason: "unbalanced parentheses"}
	}

	name = strings.ToUpper(strings.TrimSpace(s[:idx]))
	inner := s[idx+1 : len(s)-1]

	if name == "ENUM" || name == "SET" {
		params, err = splitQuotedList(inner)
		if err != nil {
			return "", nil, false, &InvalidTypeStringError{Type: raw, Reason: err.Error()}
		}
		return name, params, unsigned, nil
	}

	for _, p := range strings.Split(inner, ",") {
		params = append(params, strings.TrimSpace(p))
	}
	return name, params, unsigned, nil
}

// splitQuotedList splits the comma-separated, single-quoted string literal
// list inside an ENUM(...) or SET(...) declaration into unquoted values,
// unescaping the SQL ” quote-escaping convention.
func splitQuotedList(s string) ([]string, error) {
	var out []string
	i, n := 0, len(s)
	for i < n {
		for i < n && (s[i] == ' ' || s[i] == ',') {
			i++
		}
		if i >= n {
			break
		}
		if s[i] != '\'' {
			return nil, &InvalidTypeStringError{Type: s, Reason: "expected quoted string literal"}
		}
		i++
		var b strings.Builder
		closed := false
		for i < n {
			if s[i] == '\'' {
				if i+1 < n && s[i+1] == '\'' {
					b.WriteByte('\'')
					i += 2
					continue
				}
				i++
				closed = true
				break
			}
			b.WriteByte(s[i])
			i++
		}
		if !closed {
			return nil, &InvalidTypeStringError{Type: s, Reason: "unterminated string literal"}
		}
		out = append(out, b.String())
		for i < n && s[i] == ' ' {
			i++
		}
		if i < n && s[i] == ',' {
			i++
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Type builders: canonical type string -> sql.Type
// ---------------------------------------------------------------------------

// typeBuilder constructs a sql.Type from a type's parenthesized parameters
// and whether it carries an UNSIGNED modifier.
type typeBuilder func(params []string, unsigned bool) (sqle.Type, error)

// columnTypeBuilders dispatches on the base type name extracted by
// parseTypeString. Map-based dispatch keeps this list declarative and
// avoids a long type-name switch.
var columnTypeBuilders = map[string]typeBuilder{
	"TINYINT":    buildIntType(types.Int8, types.Uint8),
	"SMALLINT":   buildIntType(types.Int16, types.Uint16),
	"MEDIUMINT":  buildIntType(types.Int24, types.Uint24),
	"INT":        buildIntType(types.Int32, types.Uint32),
	"INTEGER":    buildIntType(types.Int32, types.Uint32),
	"BIGINT":     buildIntType(types.Int64, types.Uint64),
	"FLOAT":      buildConstType(types.Float32),
	"DOUBLE":     buildConstType(types.Float64),
	"DECIMAL":    buildDecimalType,
	"CHAR":       buildCharType,
	"VARCHAR":    buildVarcharType,
	"TINYTEXT":   buildConstType(types.TinyText),
	"TEXT":       buildConstType(types.Text),
	"MEDIUMTEXT": buildConstType(types.MediumText),
	"LONGTEXT":   buildConstType(types.LongText),
	"BINARY":     buildBinaryType,
	"VARBINARY":  buildVarbinaryType,
	"TINYBLOB":   buildConstType(types.TinyBlob),
	"BLOB":       buildConstType(types.Blob),
	"MEDIUMBLOB": buildConstType(types.MediumBlob),
	"LONGBLOB":   buildConstType(types.LongBlob),
	"DATE":       buildConstType(types.Date),
	"DATETIME":   buildDatetimeType(sqltypes.Datetime, types.Datetime),
	"TIMESTAMP":  buildDatetimeType(sqltypes.Timestamp, types.Timestamp),
	"TIME":       buildConstType(types.Time),
	"YEAR":       buildConstType(types.Year),
	"BIT":        buildBitType,
	"JSON":       buildConstType(types.JSON),
	"BOOLEAN":    buildConstType(types.Boolean),
	"BOOL":       buildConstType(types.Boolean),
	"ENUM":       buildEnumType,
	"SET":        buildSetType,
}

// buildConstType returns a typeBuilder that ignores its parameters and
// always returns t, for types with no parameters that affect encoding.
func buildConstType(t sqle.Type) typeBuilder {
	return func(_ []string, _ bool) (sqle.Type, error) {
		return t, nil
	}
}

// buildIntType returns a typeBuilder that selects between a type's signed
// and unsigned forms. Any legacy display-width parameter (e.g. "INT(11)")
// is accepted and ignored, matching modern MySQL's treatment of integer
// display widths as non-authoritative.
func buildIntType(signed, unsigned_ sqle.Type) typeBuilder {
	return func(_ []string, unsigned bool) (sqle.Type, error) {
		if unsigned {
			return unsigned_, nil
		}
		return signed, nil
	}
}

// buildDecimalType builds a DECIMAL(precision,scale) type, defaulting to
// MySQL's DECIMAL(10,0) when parameters are omitted.
func buildDecimalType(params []string, _ bool) (sqle.Type, error) {
	precision, scale := uint8(10), uint8(0)
	var err error
	if len(params) >= 1 {
		if precision, err = parseUint8(params[0]); err != nil {
			return nil, err
		}
	}
	if len(params) >= 2 {
		if scale, err = parseUint8(params[1]); err != nil {
			return nil, err
		}
	}
	return types.CreateColumnDecimalType(precision, scale)
}

// buildCharType builds a CHAR(n) type, defaulting to MySQL's CHAR(1) when
// the length is omitted.
func buildCharType(params []string, _ bool) (sqle.Type, error) {
	length, err := parseOptionalLength(params, 1)
	if err != nil {
		return nil, err
	}
	return types.CreateString(sqltypes.Char, length, sqle.Collation_Default)
}

// buildVarcharType builds a VARCHAR(n) type. Unlike CHAR, MySQL requires an
// explicit length for VARCHAR.
func buildVarcharType(params []string, _ bool) (sqle.Type, error) {
	length, err := parseRequiredLength(params)
	if err != nil {
		return nil, err
	}
	return types.CreateString(sqltypes.VarChar, length, sqle.Collation_Default)
}

// buildBinaryType builds a BINARY(n) type, defaulting to MySQL's BINARY(1)
// when the length is omitted.
func buildBinaryType(params []string, _ bool) (sqle.Type, error) {
	length, err := parseOptionalLength(params, 1)
	if err != nil {
		return nil, err
	}
	return types.CreateBinary(sqltypes.Binary, length)
}

// buildVarbinaryType builds a VARBINARY(n) type. Unlike BINARY, MySQL
// requires an explicit length for VARBINARY.
func buildVarbinaryType(params []string, _ bool) (sqle.Type, error) {
	length, err := parseRequiredLength(params)
	if err != nil {
		return nil, err
	}
	return types.CreateBinary(sqltypes.VarBinary, length)
}

// buildDatetimeType returns a typeBuilder for DATE-like temporal types that
// accept an optional fractional-seconds precision, e.g. DATETIME(6).
func buildDatetimeType(baseType query.Type, zeroPrecision sqle.Type) typeBuilder {
	return func(params []string, _ bool) (sqle.Type, error) {
		if len(params) == 0 {
			return zeroPrecision, nil
		}
		precision, err := parseUint8(params[0])
		if err != nil {
			return nil, err
		}
		return types.CreateDatetimeType(baseType, int(precision))
	}
}

// buildBitType builds a BIT(n) type. MySQL requires an explicit bit count.
func buildBitType(params []string, _ bool) (sqle.Type, error) {
	if len(params) == 0 {
		return nil, &InvalidTypeStringError{Type: "BIT", Reason: "BIT requires a bit count"}
	}
	n, err := parseUint8(params[0])
	if err != nil {
		return nil, err
	}
	return types.CreateBitType(n)
}

// buildEnumType builds an ENUM('a','b',...) type from its unquoted values.
func buildEnumType(params []string, _ bool) (sqle.Type, error) {
	if len(params) == 0 {
		return nil, &InvalidTypeStringError{Type: "ENUM", Reason: "ENUM requires at least one value"}
	}
	return types.CreateEnumType(params, sqle.Collation_Default)
}

// buildSetType builds a SET('a','b',...) type from its unquoted values.
func buildSetType(params []string, _ bool) (sqle.Type, error) {
	if len(params) == 0 {
		return nil, &InvalidTypeStringError{Type: "SET", Reason: "SET requires at least one value"}
	}
	return types.CreateSetType(params, sqle.Collation_Default)
}

// parseOptionalLength parses params[0] as a length, returning def when
// params is empty.
func parseOptionalLength(params []string, def int64) (int64, error) {
	if len(params) == 0 {
		return def, nil
	}
	return parseInt64(params[0])
}

// parseRequiredLength parses params[0] as a length, failing when params is
// empty.
func parseRequiredLength(params []string) (int64, error) {
	if len(params) == 0 {
		return 0, &InvalidTypeStringError{Type: "", Reason: "a length parameter is required"}
	}
	return parseInt64(params[0])
}

// parseInt64 parses s as a base-10 int64, wrapping strconv's error in a
// typed error.
func parseInt64(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, &InvalidTypeStringError{Type: s, Reason: "not a valid integer: " + err.Error()}
	}
	return n, nil
}

// parseUint8 parses s as a base-10 uint8, wrapping strconv's error in a
// typed error.
func parseUint8(s string) (uint8, error) {
	n, err := strconv.ParseUint(s, 10, 8)
	if err != nil {
		return 0, &InvalidTypeStringError{Type: s, Reason: "not a valid unsigned 8-bit integer: " + err.Error()}
	}
	return uint8(n), nil
}

// ---------------------------------------------------------------------------
// Type string formatting: sql.Type -> canonical type string
// ---------------------------------------------------------------------------

// typeStringFormatters dispatches on a sql.Type's query.Type discriminator.
// Map-based dispatch here mirrors columnTypeBuilders above.
var typeStringFormatters = map[query.Type]func(sqle.Type) string{
	sqltypes.Int8:      formatInt8,
	sqltypes.Uint8:     constString("TINYINT UNSIGNED"),
	sqltypes.Int16:     constString("SMALLINT"),
	sqltypes.Uint16:    constString("SMALLINT UNSIGNED"),
	sqltypes.Int24:     constString("MEDIUMINT"),
	sqltypes.Uint24:    constString("MEDIUMINT UNSIGNED"),
	sqltypes.Int32:     constString("INT"),
	sqltypes.Uint32:    constString("INT UNSIGNED"),
	sqltypes.Int64:     constString("BIGINT"),
	sqltypes.Uint64:    constString("BIGINT UNSIGNED"),
	sqltypes.Float32:   constString("FLOAT"),
	sqltypes.Float64:   constString("DOUBLE"),
	sqltypes.Decimal:   formatDecimal,
	sqltypes.Char:      formatStringType,
	sqltypes.VarChar:   formatStringType,
	sqltypes.Binary:    formatStringType,
	sqltypes.VarBinary: formatStringType,
	sqltypes.Text:      formatStringType,
	sqltypes.Blob:      formatStringType,
	sqltypes.Date:      constString("DATE"),
	sqltypes.Datetime:  formatDatetime("DATETIME"),
	sqltypes.Timestamp: formatDatetime("TIMESTAMP"),
	sqltypes.Time:      constString("TIME"),
	sqltypes.Year:      constString("YEAR"),
	sqltypes.Bit:       formatBit,
	sqltypes.Enum:      formatEnum,
	sqltypes.Set:       formatSet,
	sqltypes.TypeJSON:  constString("JSON"),
}

// constString returns a formatter that ignores its argument and always
// returns s, for types with no parameters that affect their type string.
func constString(s string) func(sqle.Type) string {
	return func(sqle.Type) string { return s }
}

// formatInt8 distinguishes BOOLEAN (TINYINT(1)) from a plain TINYINT.
// MySQL itself makes no distinction: BOOLEAN is a display-width alias for
// TINYINT(1).
func formatInt8(t sqle.Type) string {
	if nt, ok := t.(sqle.NumberType); ok && nt.DisplayWidth() == 1 {
		return "BOOLEAN"
	}
	return "TINYINT"
}

// formatDecimal reconstructs DECIMAL(precision,scale).
func formatDecimal(t sqle.Type) string {
	dt := t.(sqle.DecimalType)
	return "DECIMAL(" + strconv.Itoa(int(dt.Precision())) + "," + strconv.Itoa(int(dt.Scale())) + ")"
}

// formatStringType reconstructs the parameterized string/binary type names:
// CHAR(n), VARCHAR(n), BINARY(n), VARBINARY(n), and the TEXT/BLOB size
// tiers.
func formatStringType(t sqle.Type) string {
	st := t.(sqle.StringType)
	switch t.Type() {
	case sqltypes.Char:
		return "CHAR(" + strconv.FormatInt(st.Length(), 10) + ")"
	case sqltypes.VarChar:
		return "VARCHAR(" + strconv.FormatInt(st.Length(), 10) + ")"
	case sqltypes.Binary:
		return "BINARY(" + strconv.FormatInt(st.Length(), 10) + ")"
	case sqltypes.VarBinary:
		return "VARBINARY(" + strconv.FormatInt(st.Length(), 10) + ")"
	case sqltypes.Text:
		return textBlobTierName("TEXT", st.MaxByteLength())
	case sqltypes.Blob:
		return textBlobTierName("BLOB", st.MaxByteLength())
	default:
		return t.Type().String()
	}
}

// textBlobTierName picks the TINY/plain/MEDIUM/LONG tier of a TEXT or BLOB
// type based on its maximum byte length.
func textBlobTierName(base string, maxByteLength int64) string {
	switch {
	case maxByteLength <= types.TinyTextBlobMax:
		return "TINY" + base
	case maxByteLength <= types.TextBlobMax:
		return base
	case maxByteLength <= types.MediumTextBlobMax:
		return "MEDIUM" + base
	default:
		return "LONG" + base
	}
}

// formatDatetime returns a formatter for DATETIME- and TIMESTAMP-like types
// that includes the fractional-seconds precision only when it is non-zero.
func formatDatetime(name string) func(sqle.Type) string {
	return func(t sqle.Type) string {
		dt := t.(sqle.DatetimeType)
		if dt.Precision() == 0 {
			return name
		}
		return name + "(" + strconv.Itoa(dt.Precision()) + ")"
	}
}

// formatBit reconstructs BIT(n).
func formatBit(t sqle.Type) string {
	bt := t.(types.BitType)
	return "BIT(" + strconv.Itoa(int(bt.NumberOfBits())) + ")"
}

// formatEnum reconstructs ENUM('a','b',...).
func formatEnum(t sqle.Type) string {
	et := t.(sqle.EnumType)
	return "ENUM(" + quotedValueList(et.Values()) + ")"
}

// formatSet reconstructs SET('a','b',...).
func formatSet(t sqle.Type) string {
	st := t.(sqle.SetType)
	return "SET(" + quotedValueList(st.Values()) + ")"
}

// quotedValueList renders a list of ENUM/SET member values as single-quoted,
// comma-separated SQL string literals with ” quote-escaping.
func quotedValueList(values []string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	return strings.Join(parts, ",")
}

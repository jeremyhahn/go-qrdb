//go:build qrdb_sql

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

package catalog

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/cockroachdb/apd/v3"
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/sqltypes"
	"github.com/dolthub/vitess/go/vt/proto/query"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Row format v1:
//
//	magic:         2 bytes, 'Q','R'
//	format:        1 byte, always 1 for this version
//	schemaVersion: 4 bytes, little-endian uint32 — the TableDef.SchemaVersion
//	               in effect when this row was encoded
//	columnCount:   uvarint — the number of columns in effect when this row
//	               was encoded, i.e. len(TableDef.Columns) at that time
//	nullBitmap:    ceil(columnCount/8) bytes; bit i (LSB-first within byte
//	               i/8) set means column i was NULL and contributes no bytes
//	               to the values section
//	values:        for i in [0, columnCount), the columns whose null bit is
//	               clear, encoded in column order per the table below
//
// columnCount is not part of the format description a caller needs to
// reason about directly — RowCodec.Decode reads it once and then treats it
// purely as the null bitmap's bit width, which is what schema evolution
// requires: since TableDef.Columns is append-only, an older row's bitmap
// covers a strict prefix of the current column list, and no version
// history needs to be persisted to know how long that prefix is.
//
// Per-column value encoding:
//
//	TINYINT/BOOL     int8/uint8, 1 byte
//	SMALLINT         int16/uint16, 2 bytes LE
//	MEDIUMINT        int32/uint32, 4 bytes LE (MySQL's 3-byte MEDIUMINT has
//	                  no native Go width; it is widened to 4 bytes)
//	INT              int32/uint32, 4 bytes LE
//	BIGINT           int64/uint64, 8 bytes LE
//	FLOAT            IEEE-754 bits, 4 bytes LE
//	DOUBLE           IEEE-754 bits, 8 bytes LE
//	DECIMAL          uvarint length + apd's canonical decimal text (exact,
//	                  no float round trip; see decimalText)
//	CHAR/VARCHAR/TEXT uvarint length + UTF-8 bytes
//	BINARY/VARBINARY/BLOB uvarint length + raw bytes
//	DATE/DATETIME/TIMESTAMP int64 UTC nanoseconds, 8 bytes LE
//	TIME             int64 microseconds, 8 bytes LE
//	YEAR             2 bytes LE
//	BIT              uint64, 8 bytes LE
//	ENUM             uint16 ordinal, 2 bytes LE
//	SET              uint64 bitmask, 8 bytes LE
//	JSON             uvarint length + canonical JSON bytes
//
// Two properties matter more than the exact byte layout above:
//
//  1. NULL is the bitmap, never absence. An empty VARCHAR is a present
//     value of length 0 with its bit clear; a NULL VARCHAR sets its bit and
//     contributes zero bytes to the values section. There is no way to
//     confuse the two, unlike the JSON encoding this format replaces, which
//     conflated a missing JSON field with an explicit null.
//  2. Decode returns exactly the Go type the column's sql.Type.ValueType()
//     expects (int32, uint64, *apd.Decimal, time.Time, ...), never a type
//     re-inferred from the bytes. In particular, integers are decoded as
//     integers: nothing here round-trips through float64, so an int64 above
//     2^53 decodes exactly. Encode is the same contract in reverse: it
//     accepts only that type, so a go-mysql-server upgrade that changes a
//     column's Go representation fails loudly at the first write rather
//     than storing a coerced approximation of it.
const (
	rowMagic0        byte = 'Q'
	rowMagic1        byte = 'R'
	rowFormatVersion byte = 1

	// rowHeaderLen is the fixed-size prefix of an encoded row: magic (2) +
	// format version (1) + schemaVersion (4), before the variable-length
	// columnCount uvarint begins.
	rowHeaderLen = 2 + 1 + 4
)

// RowSchemaVersionAtLeast reports whether data -- a row previously produced
// by (*RowCodec).Encode -- carries a schema version at or above version,
// without fully decoding the row.
//
// It exists for the DDL rewrite executor (pkg/sql/provider), which streams
// every row of a table being migrated from one schema version to another and
// must be resumable after a crash: since Encode always embeds the schema
// version active when a row was written, a row already re-encoded at the
// target version on an earlier, interrupted pass can be recognized and
// skipped on the resumed pass, rather than being decoded again with the
// pre-rewrite codec -- which would misread bytes laid out under the new
// column list and either fail outright or silently produce garbage values.
func RowSchemaVersionAtLeast(table string, data []byte, version uint32) (bool, error) {
	if len(data) < rowHeaderLen {
		return false, &TruncatedRowError{Table: table}
	}
	if data[0] != rowMagic0 || data[1] != rowMagic1 {
		return false, &InvalidRowMagicError{Table: table}
	}
	if data[2] != rowFormatVersion {
		return false, &UnsupportedRowFormatError{Table: table, Version: data[2]}
	}
	sv := binary.LittleEndian.Uint32(data[3:7])
	return sv >= version, nil
}

// columnCodec is the encode/decode/size triple for one wire representation.
// A single static table, keyed by query.Type, is enough for every column:
// query.Type already distinguishes signed from unsigned and every integer
// width (Int8 vs Int16 vs ... vs Int64), so no per-column parameters are
// needed to pick the right codec.
type columnCodec struct {
	encode func(buf []byte, v any) ([]byte, error)
	decode func(buf []byte) (v any, consumed int, err error)
	size   func(v any) (int, error)
}

// RowCodec encodes and decodes the rows of one relational table to and from
// QRDB's binary row format. A RowCodec is immutable once constructed and
// safe for concurrent use.
type RowCodec struct {
	tableName     string
	columns       []service.ColumnDef
	types         []sqle.Type
	codecs        []*columnCodec
	schemaVersion uint32

	// rowIDSlot records that this codec's table carries the trailing hidden
	// row id column (see rowid.go), so its rows are one slot wider than its
	// declared column list. That slot is never encoded -- the row id is the
	// storage key, so storing it in the value too would be redundant, and
	// leaving it out is what keeps a keyless table's stored bytes identical
	// to a keyed table's.
	rowIDSlot bool
}

// Width returns the number of slots in a row this codec encodes and
// decodes: one per declared column, plus one for the hidden row id of a
// table that declares no PRIMARY KEY.
func (c *RowCodec) Width() int {
	if c.rowIDSlot {
		return len(c.columns) + 1
	}
	return len(c.columns)
}

// NewRowCodec builds a RowCodec for the current definition of a table.
// Constructing a new RowCodec after every schema-version bump (e.g. after
// ALTER TABLE ADD COLUMN) is what lets an existing RowCodec's Decode still
// read rows written before that change: see the package-level format
// comment above for how the encoded columnCount ties the two together.
func NewRowCodec(def *service.TableDef) (*RowCodec, error) {
	if def == nil {
		return nil, &NilTableDefError{}
	}
	if len(def.Columns) == 0 {
		return nil, &EmptyColumnsError{Table: def.Name}
	}

	resolved := make([]sqle.Type, len(def.Columns))
	codecs := make([]*columnCodec, len(def.Columns))
	for i, col := range def.Columns {
		t, err := ColumnTypeOf(col)
		if err != nil {
			return nil, err
		}
		cc, ok := columnCodecs[t.Type()]
		if !ok {
			return nil, &UnsupportedColumnTypeError{Column: col.Name, Type: col.Type}
		}
		resolved[i] = t
		codecs[i] = cc
	}

	return &RowCodec{
		tableName:     def.Name,
		columns:       append([]service.ColumnDef(nil), def.Columns...),
		types:         resolved,
		codecs:        codecs,
		schemaVersion: def.SchemaVersion,
		rowIDSlot:     HasRowID(def),
	}, nil
}

// Encode serializes row, which must be exactly Width() values wide, in the
// format described above. A trailing hidden row id slot, when the table has
// one, is not encoded; see the rowIDSlot field.
func (c *RowCodec) Encode(row sqle.Row) ([]byte, error) {
	if len(row) != c.Width() {
		return nil, &RowColumnCountMismatchError{Table: c.tableName, Expected: c.Width(), Got: len(row)}
	}

	n := len(c.columns)
	bitmapLen := (n + 7) / 8

	buf := make([]byte, 0, c.EncodedSize(row))
	buf = append(buf, rowMagic0, rowMagic1, rowFormatVersion)
	var sv [4]byte
	binary.LittleEndian.PutUint32(sv[:], c.schemaVersion)
	buf = append(buf, sv[:]...)
	buf = binary.AppendUvarint(buf, uint64(n))

	bitmapStart := len(buf)
	buf = append(buf, make([]byte, bitmapLen)...)

	for i := 0; i < n; i++ {
		v := row[i]
		if v == nil {
			buf[bitmapStart+i/8] |= 1 << uint(i%8)
			continue
		}
		var err error
		buf, err = c.codecs[i].encode(buf, v)
		if err != nil {
			return nil, &ColumnEncodeError{Table: c.tableName, Column: c.columns[i].Name, Err: err}
		}
	}
	return buf, nil
}

// Decode reverses Encode, returning a row Width() values wide. When data was
// encoded under an older schema version with fewer columns than this codec's
// current definition, the trailing columns are filled from their declared
// default, or with NULL when nullable and no default is declared.
//
// A table's hidden row id slot is returned nil: the row id is not in the
// encoded bytes at all (it is the storage key), so only a caller that knows
// the key a row was read under can fill it -- see
// RelationalTable.decodeRow.
func (c *RowCodec) Decode(data []byte) (sqle.Row, error) {
	const headerLen = rowHeaderLen
	if len(data) < headerLen {
		return nil, &TruncatedRowError{Table: c.tableName}
	}
	if data[0] != rowMagic0 || data[1] != rowMagic1 {
		return nil, &InvalidRowMagicError{Table: c.tableName}
	}
	if data[2] != rowFormatVersion {
		return nil, &UnsupportedRowFormatError{Table: c.tableName, Version: data[2]}
	}

	pos := headerLen
	colCount, n := binary.Uvarint(data[pos:])
	if n <= 0 {
		return nil, &TruncatedRowError{Table: c.tableName}
	}
	pos += n

	bitmapLen := int((colCount + 7) / 8)
	if bitmapLen < 0 || pos+bitmapLen > len(data) {
		return nil, &TruncatedRowError{Table: c.tableName}
	}
	bitmap := data[pos : pos+bitmapLen]
	pos += bitmapLen

	total := len(c.columns)
	row := make(sqle.Row, c.Width())
	for i := 0; i < total; i++ {
		if uint64(i) >= colCount {
			v, err := c.defaultFor(i)
			if err != nil {
				return nil, err
			}
			row[i] = v
			continue
		}

		byteIdx, bitIdx := i/8, uint(i%8)
		if bitmap[byteIdx]&(1<<bitIdx) != 0 {
			row[i] = nil
			continue
		}

		v, consumed, err := c.codecs[i].decode(data[pos:])
		if err != nil {
			return nil, &ColumnDecodeError{Table: c.tableName, Column: c.columns[i].Name, Err: err}
		}
		row[i] = v
		pos += consumed
	}
	return row, nil
}

// EncodedSize returns the number of bytes Encode(row) would produce,
// without allocating them.
func (c *RowCodec) EncodedSize(row sqle.Row) int {
	n := len(c.columns)
	bitmapLen := (n + 7) / 8
	total := 2 + 1 + 4 + uvarintLen(uint64(n)) + bitmapLen
	for i, v := range row {
		if i >= n || v == nil {
			continue
		}
		if sz, err := c.codecs[i].size(v); err == nil {
			total += sz
		}
	}
	return total
}

// defaultFor computes the value a decoded row should carry for column i
// when the encoded row predates that column's existence. Expression
// defaults (see defaultIsExpression, e.g. CURRENT_TIMESTAMP) cannot be
// evaluated statically here and fall back to NULL (when nullable) or the
// type's zero value, matching the fallback used when no default is declared
// at all.
func (c *RowCodec) defaultFor(i int) (any, error) {
	col := c.columns[i]
	text := strings.TrimSpace(col.Default)
	if text == "" || defaultIsExpression(text) {
		if col.Nullable {
			return nil, nil
		}
		return c.types[i].Zero(), nil
	}
	return parseDefaultLiteral(c.types[i], text)
}

// parseDefaultLiteral converts a column's SQL-text default (e.g. "42",
// "'hello'", "1.50") to the Go value its sql.Type expects, stripping a
// single layer of SQL string-literal quoting when present.
func parseDefaultLiteral(t sqle.Type, lit string) (any, error) {
	lit = strings.TrimSpace(lit)
	if len(lit) >= 2 && lit[0] == '\'' && lit[len(lit)-1] == '\'' {
		lit = strings.ReplaceAll(lit[1:len(lit)-1], "''", "'")
	}
	v, _, err := t.Convert(context.Background(), lit)
	if err != nil {
		return nil, &DefaultValueConvertError{Reason: err.Error()}
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// columnCodecs: the static, per-query.Type wire encoders
// ---------------------------------------------------------------------------

var columnCodecs = map[query.Type]*columnCodec{
	sqltypes.Int8:      {encodeInt8, decodeInt8, constSize(1)},
	sqltypes.Uint8:     {encodeUint8, decodeUint8, constSize(1)},
	sqltypes.Int16:     {encodeInt16, decodeInt16, constSize(2)},
	sqltypes.Uint16:    {encodeUint16, decodeUint16, constSize(2)},
	sqltypes.Int24:     {encodeInt32, decodeInt32, constSize(4)},
	sqltypes.Uint24:    {encodeUint32, decodeUint32, constSize(4)},
	sqltypes.Int32:     {encodeInt32, decodeInt32, constSize(4)},
	sqltypes.Uint32:    {encodeUint32, decodeUint32, constSize(4)},
	sqltypes.Int64:     {encodeInt64, decodeInt64, constSize(8)},
	sqltypes.Uint64:    {encodeUint64, decodeUint64, constSize(8)},
	sqltypes.Float32:   {encodeFloat32, decodeFloat32, constSize(4)},
	sqltypes.Float64:   {encodeFloat64, decodeFloat64, constSize(8)},
	sqltypes.Decimal:   {encodeDecimal, decodeDecimal, sizeDecimal},
	sqltypes.Char:      {encodeString, decodeString, sizeString},
	sqltypes.VarChar:   {encodeString, decodeString, sizeString},
	sqltypes.Text:      {encodeString, decodeString, sizeString},
	sqltypes.Binary:    {encodeBytes, decodeBytes, sizeBytes},
	sqltypes.VarBinary: {encodeBytes, decodeBytes, sizeBytes},
	sqltypes.Blob:      {encodeBytes, decodeBytes, sizeBytes},
	sqltypes.Date:      {encodeTime, decodeTime, constSize(8)},
	sqltypes.Datetime:  {encodeTime, decodeTime, constSize(8)},
	sqltypes.Timestamp: {encodeTime, decodeTime, constSize(8)},
	sqltypes.Time:      {encodeTimespan, decodeTimespan, constSize(8)},
	sqltypes.Year:      {encodeInt16, decodeInt16, constSize(2)},
	sqltypes.Bit:       {encodeUint64, decodeUint64, constSize(8)},
	sqltypes.Enum:      {encodeUint16, decodeUint16, constSize(2)},
	sqltypes.Set:       {encodeUint64, decodeUint64, constSize(8)},
	sqltypes.TypeJSON:  {encodeJSON, decodeJSON, sizeJSON},
}

func constSize(n int) func(any) (int, error) {
	return func(any) (int, error) { return n, nil }
}

// ---------------------------------------------------------------------------
// Fixed-width integer and float codecs
// ---------------------------------------------------------------------------

func encodeInt8(buf []byte, v any) ([]byte, error) {
	x, ok := v.(int8)
	if !ok {
		return nil, typeMismatch("int8", v)
	}
	return append(buf, byte(x)), nil
}

func decodeInt8(buf []byte) (any, int, error) {
	if len(buf) < 1 {
		return nil, 0, &TruncatedValueError{Width: 1}
	}
	return int8(buf[0]), 1, nil
}

func encodeUint8(buf []byte, v any) ([]byte, error) {
	x, ok := v.(uint8)
	if !ok {
		return nil, typeMismatch("uint8", v)
	}
	return append(buf, x), nil
}

func decodeUint8(buf []byte) (any, int, error) {
	if len(buf) < 1 {
		return nil, 0, &TruncatedValueError{Width: 1}
	}
	return buf[0], 1, nil
}

func encodeInt16(buf []byte, v any) ([]byte, error) {
	x, ok := v.(int16)
	if !ok {
		return nil, typeMismatch("int16", v)
	}
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], uint16(x))
	return append(buf, b[:]...), nil
}

func decodeInt16(buf []byte) (any, int, error) {
	if len(buf) < 2 {
		return nil, 0, &TruncatedValueError{Width: 2}
	}
	return int16(binary.LittleEndian.Uint16(buf[:2])), 2, nil
}

func encodeUint16(buf []byte, v any) ([]byte, error) {
	x, ok := v.(uint16)
	if !ok {
		return nil, typeMismatch("uint16", v)
	}
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], x)
	return append(buf, b[:]...), nil
}

func decodeUint16(buf []byte) (any, int, error) {
	if len(buf) < 2 {
		return nil, 0, &TruncatedValueError{Width: 2}
	}
	return binary.LittleEndian.Uint16(buf[:2]), 2, nil
}

func encodeInt32(buf []byte, v any) ([]byte, error) {
	x, ok := v.(int32)
	if !ok {
		return nil, typeMismatch("int32", v)
	}
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(x))
	return append(buf, b[:]...), nil
}

func decodeInt32(buf []byte) (any, int, error) {
	if len(buf) < 4 {
		return nil, 0, &TruncatedValueError{Width: 4}
	}
	return int32(binary.LittleEndian.Uint32(buf[:4])), 4, nil
}

func encodeUint32(buf []byte, v any) ([]byte, error) {
	x, ok := v.(uint32)
	if !ok {
		return nil, typeMismatch("uint32", v)
	}
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], x)
	return append(buf, b[:]...), nil
}

func decodeUint32(buf []byte) (any, int, error) {
	if len(buf) < 4 {
		return nil, 0, &TruncatedValueError{Width: 4}
	}
	return binary.LittleEndian.Uint32(buf[:4]), 4, nil
}

func encodeInt64(buf []byte, v any) ([]byte, error) {
	x, ok := v.(int64)
	if !ok {
		return nil, typeMismatch("int64", v)
	}
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(x))
	return append(buf, b[:]...), nil
}

func decodeInt64(buf []byte) (any, int, error) {
	if len(buf) < 8 {
		return nil, 0, &TruncatedValueError{Width: 8}
	}
	return int64(binary.LittleEndian.Uint64(buf[:8])), 8, nil
}

func encodeUint64(buf []byte, v any) ([]byte, error) {
	x, ok := v.(uint64)
	if !ok {
		return nil, typeMismatch("uint64", v)
	}
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], x)
	return append(buf, b[:]...), nil
}

func decodeUint64(buf []byte) (any, int, error) {
	if len(buf) < 8 {
		return nil, 0, &TruncatedValueError{Width: 8}
	}
	return binary.LittleEndian.Uint64(buf[:8]), 8, nil
}

func encodeFloat32(buf []byte, v any) ([]byte, error) {
	x, ok := v.(float32)
	if !ok {
		return nil, typeMismatch("float32", v)
	}
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], math.Float32bits(x))
	return append(buf, b[:]...), nil
}

func decodeFloat32(buf []byte) (any, int, error) {
	if len(buf) < 4 {
		return nil, 0, &TruncatedValueError{Width: 4}
	}
	return math.Float32frombits(binary.LittleEndian.Uint32(buf[:4])), 4, nil
}

func encodeFloat64(buf []byte, v any) ([]byte, error) {
	x, ok := v.(float64)
	if !ok {
		return nil, typeMismatch("float64", v)
	}
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], math.Float64bits(x))
	return append(buf, b[:]...), nil
}

func decodeFloat64(buf []byte) (any, int, error) {
	if len(buf) < 8 {
		return nil, 0, &TruncatedValueError{Width: 8}
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(buf[:8])), 8, nil
}

// ---------------------------------------------------------------------------
// Variable-length codecs: length-prefixed bytes
// ---------------------------------------------------------------------------

func readVarBytes(buf []byte) ([]byte, int, error) {
	length, n := binary.Uvarint(buf)
	if n <= 0 {
		return nil, 0, &TruncatedValueError{Width: 0}
	}
	end := n + int(length)
	if length > uint64(len(buf)) || end < n || end > len(buf) {
		return nil, 0, &TruncatedValueError{Width: int(length)}
	}
	data := make([]byte, length)
	copy(data, buf[n:end])
	return data, end, nil
}

// decimalText renders a DECIMAL cell as the text this format stores.
//
// (*apd.Decimal).String is apd's canonical scientific form, and it is the
// only rendering apd offers that reproduces a value's coefficient, exponent
// and sign exactly when parsed back: Text('f') collapses a positive exponent
// into its expanded digits (1E+2 becomes "100", exponent 2 becomes exponent
// 0), which would silently change a value's scale across a round trip. Since
// exactness is this format's entire premise, the canonical form is what goes
// on disk; every plain decimal string a previous writer produced still parses
// back through apd.NewFromString unchanged, so stored rows keep decoding.
func decimalText(v any) (string, error) {
	d, ok := v.(*apd.Decimal)
	if !ok || d == nil {
		return "", typeMismatch("*apd.Decimal", v)
	}
	return d.String(), nil
}

func encodeDecimal(buf []byte, v any) ([]byte, error) {
	s, err := decimalText(v)
	if err != nil {
		return nil, err
	}
	buf = binary.AppendUvarint(buf, uint64(len(s)))
	return append(buf, s...), nil
}

func decodeDecimal(buf []byte) (any, int, error) {
	data, consumed, err := readVarBytes(buf)
	if err != nil {
		return nil, 0, err
	}
	d, _, err := apd.NewFromString(string(data))
	if err != nil {
		return nil, 0, &DecimalParseError{Value: string(data)}
	}
	return d, consumed, nil
}

func sizeDecimal(v any) (int, error) {
	s, err := decimalText(v)
	if err != nil {
		return 0, err
	}
	return uvarintLen(uint64(len(s))) + len(s), nil
}

func encodeString(buf []byte, v any) ([]byte, error) {
	s, ok := v.(string)
	if !ok {
		return nil, typeMismatch("string", v)
	}
	buf = binary.AppendUvarint(buf, uint64(len(s)))
	return append(buf, s...), nil
}

func decodeString(buf []byte) (any, int, error) {
	data, consumed, err := readVarBytes(buf)
	if err != nil {
		return nil, 0, err
	}
	return string(data), consumed, nil
}

func sizeString(v any) (int, error) {
	s, ok := v.(string)
	if !ok {
		return 0, typeMismatch("string", v)
	}
	return uvarintLen(uint64(len(s))) + len(s), nil
}

func encodeBytes(buf []byte, v any) ([]byte, error) {
	b, ok := v.([]byte)
	if !ok {
		return nil, typeMismatch("[]byte", v)
	}
	buf = binary.AppendUvarint(buf, uint64(len(b)))
	return append(buf, b...), nil
}

func decodeBytes(buf []byte) (any, int, error) {
	return readVarBytes(buf)
}

func sizeBytes(v any) (int, error) {
	b, ok := v.([]byte)
	if !ok {
		return 0, typeMismatch("[]byte", v)
	}
	return uvarintLen(uint64(len(b))) + len(b), nil
}

func encodeJSON(buf []byte, v any) ([]byte, error) {
	b, err := marshalJSONValue(v)
	if err != nil {
		return nil, err
	}
	buf = binary.AppendUvarint(buf, uint64(len(b)))
	return append(buf, b...), nil
}

func decodeJSON(buf []byte) (any, int, error) {
	data, consumed, err := readVarBytes(buf)
	if err != nil {
		return nil, 0, err
	}
	var doc any
	if len(data) > 0 {
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, 0, &JSONDecodeError{Reason: err.Error()}
		}
	}
	return types.JSONDocument{Val: doc}, consumed, nil
}

func sizeJSON(v any) (int, error) {
	b, err := marshalJSONValue(v)
	if err != nil {
		return 0, err
	}
	return uvarintLen(uint64(len(b))) + len(b), nil
}

func marshalJSONValue(v any) ([]byte, error) {
	jw, ok := v.(sqle.JSONWrapper)
	if !ok {
		return nil, typeMismatch("sql.JSONWrapper", v)
	}
	// ToInterface takes a context so that a lazily-loaded wrapper can honour
	// cancellation. Background is sound here: both go-mysql-server JSON
	// wrappers (JSONDocument and LazyJSONDocument) declare the parameter
	// unnamed and discard it, and the values that reach this encoder are
	// already-materialized cells of a sql.Row the engine handed us, so no I/O
	// occurs. Should qrdb ever store a wrapper that genuinely loads on demand,
	// the context must be threaded through columnCodec instead.
	doc, err := jw.ToInterface(context.Background())
	if err != nil {
		return nil, &JSONEncodeError{Reason: err.Error()}
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, &JSONEncodeError{Reason: err.Error()}
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// Temporal codecs
// ---------------------------------------------------------------------------

func encodeTime(buf []byte, v any) ([]byte, error) {
	t, ok := v.(time.Time)
	if !ok {
		return nil, typeMismatch("time.Time", v)
	}
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(t.UTC().UnixNano()))
	return append(buf, b[:]...), nil
}

func decodeTime(buf []byte) (any, int, error) {
	if len(buf) < 8 {
		return nil, 0, &TruncatedValueError{Width: 8}
	}
	ns := int64(binary.LittleEndian.Uint64(buf[:8]))
	return time.Unix(0, ns).UTC(), 8, nil
}

func encodeTimespan(buf []byte, v any) ([]byte, error) {
	t, ok := v.(types.Timespan)
	if !ok {
		return nil, typeMismatch("types.Timespan", v)
	}
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(int64(t)))
	return append(buf, b[:]...), nil
}

func decodeTimespan(buf []byte) (any, int, error) {
	if len(buf) < 8 {
		return nil, 0, &TruncatedValueError{Width: 8}
	}
	us := int64(binary.LittleEndian.Uint64(buf[:8]))
	return types.Timespan(us), 8, nil
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func typeMismatch(expected string, v any) error {
	return &ValueTypeMismatchError{Expected: expected, Got: goTypeName(v)}
}

func goTypeName(v any) string {
	if v == nil {
		return "nil"
	}
	return fmt.Sprintf("%T", v)
}

// uvarintLen returns the number of bytes binary.PutUvarint would write for
// x, without allocating.
func uvarintLen(x uint64) int {
	n := 1
	for x >= 0x80 {
		x >>= 7
		n++
	}
	return n
}

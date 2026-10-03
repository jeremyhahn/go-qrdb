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
	"encoding/binary"
	"math"
	"time"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/sqltypes"
	"github.com/dolthub/vitess/go/vt/proto/query"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// KeyCodec encodes a table's primary key columns into a single
// order-preserving string, and decodes that string back into typed values.
//
// Order preservation is the entire point: it is what lets a query planner
// turn "WHERE id BETWEEN ? AND ?" into an MVCC range scan instead of a full
// table scan, and what makes a prefix scan over a composite key return rows
// in primary-key order. Each component type below is encoded so that
// lexicographic (byte-wise) order over the encoded string matches the
// natural order of the decoded values:
//
//	unsigned integers  8-byte big-endian magnitude
//	signed integers    8-byte big-endian, sign bit flipped so negative
//	                   values sort below positive ones
//	floats             IEEE-754 bits, sign-magnitude normalized (see
//	                   floatOrderedBits) so that -0 < ... < -epsilon <
//	                   0 <= epsilon < ... regardless of the raw bit
//	                   pattern's own ordering
//	strings/bytes      0x00 escaped as 0x00 0xFF, terminated with 0x00
//	                   0x00 (pkg/index's existing compound-index
//	                   encoding, reused rather than duplicated: see
//	                   index.EncodeCompoundValue)
//
// A composite primary key concatenates its components in declared order,
// so a prefix of the leading components is itself a valid encoded prefix
// key. PKColumn.PrefixLength truncates a string or byte component to its
// first N bytes before encoding, matching MySQL's prefix-index semantics;
// the truncated bytes are permanently lost on decode.
//
// DECIMAL and JSON columns have no encoding defined here: DECIMAL because
// an exact, arbitrary-precision, order-preserving byte encoding is a
// materially different (and separately scoped) problem from the fixed-width
// schemes above, and JSON because MySQL itself does not allow a JSON column
// to participate in a key without an explicit cast. Declaring either as a
// primary key column is rejected by NewKeyCodec with a typed error.
type KeyCodec struct {
	tableName  string
	components []keyComponent
	synthetic  bool

	// columnCount is len(TableDef.Columns), recorded only for a
	// synthetic-row-id codec: it is both the ordinal of the hidden row id
	// column within the schema and the length a row that is missing its row
	// id stops at. See Encode.
	columnCount int
}

// Synthetic reports whether c keys its table by the synthetic hidden row id
// (see rowid.go) rather than by a declared PRIMARY KEY. Callers use it to
// decide whether a decoded row needs its trailing hidden slot filled in from
// the key it was read under.
func (c *KeyCodec) Synthetic() bool { return c.synthetic }

// keyComponent describes one column's contribution to an encoded key.
type keyComponent struct {
	name      string
	colIndex  int // index into the row/TableDef.Columns; -1 for the synthetic row id
	kind      query.Type
	numeric   bool
	prefixLen int
}

// NewKeyCodec builds a KeyCodec for a table's declared primary key, or, for
// a table with no declared primary key, for the synthetic hidden row id
// described in rowid.go.
func NewKeyCodec(def *service.TableDef) (*KeyCodec, error) {
	if def == nil {
		return nil, &NilTableDefError{}
	}

	if len(def.PrimaryKey) == 0 {
		return &KeyCodec{
			tableName:   def.Name,
			synthetic:   true,
			columnCount: len(def.Columns),
			components: []keyComponent{{
				name:     RowIDColumnName,
				colIndex: -1,
				kind:     sqltypes.Uint64,
				numeric:  true,
			}},
		}, nil
	}

	colIndex := make(map[string]int, len(def.Columns))
	for i, c := range def.Columns {
		colIndex[c.Name] = i
	}

	components := make([]keyComponent, 0, len(def.PrimaryKey))
	for _, pk := range def.PrimaryKey {
		idx, ok := colIndex[pk.Name]
		if !ok {
			return nil, &ColumnNotFoundError{Table: def.Name, Column: pk.Name}
		}
		col := def.Columns[idx]
		t, err := ColumnTypeOf(col)
		if err != nil {
			return nil, err
		}
		numeric, isString, ok := keyEncodingKind(t.Type())
		if !ok {
			return nil, &UnsupportedKeyColumnTypeError{Table: def.Name, Column: pk.Name, Type: col.Type}
		}
		if pk.PrefixLength > 0 && !isString {
			return nil, &PrefixLengthNotSupportedError{Table: def.Name, Column: pk.Name, Type: col.Type}
		}
		components = append(components, keyComponent{
			name:      pk.Name,
			colIndex:  idx,
			kind:      t.Type(),
			numeric:   numeric,
			prefixLen: int(pk.PrefixLength),
		})
	}
	return &KeyCodec{tableName: def.Name, components: components}, nil
}

// keyEncodingKind reports how a query.Type is encoded as a key component:
// numeric selects the 8-byte big-endian encoding, isString selects the
// escaped/terminated byte-string encoding, and ok is false for types with
// no defined key encoding (DECIMAL, JSON, spatial types).
func keyEncodingKind(qt query.Type) (numeric, isString, ok bool) {
	switch qt {
	case sqltypes.Uint8, sqltypes.Uint16, sqltypes.Uint24, sqltypes.Uint32, sqltypes.Uint64,
		sqltypes.Year, sqltypes.Bit, sqltypes.Enum, sqltypes.Set,
		sqltypes.Int8, sqltypes.Int16, sqltypes.Int24, sqltypes.Int32, sqltypes.Int64,
		sqltypes.Date, sqltypes.Datetime, sqltypes.Timestamp, sqltypes.Time,
		sqltypes.Float32, sqltypes.Float64:
		return true, false, true
	case sqltypes.Char, sqltypes.VarChar, sqltypes.Text, sqltypes.Binary, sqltypes.VarBinary, sqltypes.Blob:
		return false, true, true
	default:
		return false, false, false
	}
}

// Encode builds the order-preserving key for row, pulling each primary key
// component from its column's declared position. For a synthetic-row-id
// table (see rowid.go), the row id is read from the hidden system column
// SchemaOf appends after all of the table's declared columns; EncodeValues
// is the more direct way to encode a row id on its own.
//
// That trailing-slot convention is checked against the table's column count,
// not merely against a non-empty row: a caller handing over a row that stops
// at the last declared column gets *MissingRowIDError naming the table.
// Without the count, such a row's last declared column would be read as the
// row id, which fails with a confusing type error for most column types and,
// for a trailing BIGINT UNSIGNED/BIT/SET column, silently succeeds: that
// column's value would become the table's physical primary key, and two rows
// sharing it would collide as a duplicate-key violation.
func (c *KeyCodec) Encode(row sqle.Row) (string, error) {
	vals := make([]any, len(c.components))
	for i, comp := range c.components {
		if comp.colIndex < 0 {
			if len(row) <= c.columnCount {
				return "", &MissingRowIDError{Table: c.tableName}
			}
			vals[i] = row[c.columnCount]
			continue
		}
		if comp.colIndex >= len(row) {
			return "", &RowColumnCountMismatchError{Table: c.tableName, Expected: comp.colIndex + 1, Got: len(row)}
		}
		vals[i] = row[comp.colIndex]
	}
	return c.EncodeValues(vals)
}

// EncodeValues builds the order-preserving key from raw component values,
// in primary key column order (or a single uint64 row id for a
// synthetic-row-id table).
func (c *KeyCodec) EncodeValues(vals []any) (string, error) {
	if len(vals) != len(c.components) {
		return "", &KeyValueCountMismatchError{Table: c.tableName, Expected: len(c.components), Got: len(vals)}
	}

	byteVals := make([][]byte, len(c.components))
	valueTypes := make([]index.IndexValueType, len(c.components))
	for i, comp := range c.components {
		// #nosec G602 -- len(vals) == len(c.components) is checked above, and i ranges over c.components.
		numeric, raw, err := encodeKeyComponent(comp, vals[i])
		if err != nil {
			return "", err
		}
		byteVals[i] = raw
		if numeric {
			valueTypes[i] = index.IndexValueTypeNumeric
		} else {
			valueTypes[i] = index.IndexValueTypeString
		}
	}
	return string(index.EncodeCompoundValue(byteVals, valueTypes)), nil
}

// Decode splits an encoded key back into its primary key component values,
// each typed as its column's sql.Type.ValueType() expects. For a
// string/binary component truncated by PKColumn.PrefixLength at encode
// time, the truncated bytes are not recoverable.
func (c *KeyCodec) Decode(key string) ([]any, error) {
	valueTypes := make([]index.IndexValueType, len(c.components))
	for i, comp := range c.components {
		if comp.numeric {
			valueTypes[i] = index.IndexValueTypeNumeric
		} else {
			valueTypes[i] = index.IndexValueTypeString
		}
	}

	parts := index.DecodeCompoundValue([]byte(key), valueTypes)
	if len(parts) != len(c.components) {
		return nil, &InvalidKeyEncodingError{Table: c.tableName, Reason: "component count mismatch"}
	}

	out := make([]any, len(c.components))
	for i, comp := range c.components {
		v, err := decodeKeyComponent(c.tableName, comp, parts[i])
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Component encode/decode
// ---------------------------------------------------------------------------

// encodeKeyComponent encodes one primary key component. Numeric components
// return an 8-byte big-endian, order-preserving encoding; string/byte
// components return the raw (optionally prefix-truncated) bytes for
// index.EncodeCompoundValue to escape and terminate.
func encodeKeyComponent(comp keyComponent, v any) (numeric bool, raw []byte, err error) {
	if v == nil {
		return false, nil, &NullKeyComponentError{Column: comp.name}
	}

	switch comp.kind {
	case sqltypes.Uint8:
		x, ok := v.(uint8)
		if !ok {
			return false, nil, componentTypeMismatch(comp, "uint8", v)
		}
		return true, beUint64(uint64(x)), nil
	case sqltypes.Uint16, sqltypes.Enum:
		x, ok := v.(uint16)
		if !ok {
			return false, nil, componentTypeMismatch(comp, "uint16", v)
		}
		return true, beUint64(uint64(x)), nil
	case sqltypes.Uint24, sqltypes.Uint32:
		x, ok := v.(uint32)
		if !ok {
			return false, nil, componentTypeMismatch(comp, "uint32", v)
		}
		return true, beUint64(uint64(x)), nil
	case sqltypes.Uint64, sqltypes.Bit, sqltypes.Set:
		x, ok := v.(uint64)
		if !ok {
			return false, nil, componentTypeMismatch(comp, "uint64", v)
		}
		return true, beUint64(x), nil
	case sqltypes.Year:
		// Year's Go value is int16 (always non-negative in MySQL's YEAR
		// range), reinterpreted as an unsigned magnitude.
		x, ok := v.(int16)
		if !ok {
			return false, nil, componentTypeMismatch(comp, "int16", v)
		}
		return true, beUint64(uint64(uint16(x))), nil
	case sqltypes.Int8:
		x, ok := v.(int8)
		if !ok {
			return false, nil, componentTypeMismatch(comp, "int8", v)
		}
		return true, beUint64(signedOrderedBits(int64(x))), nil
	case sqltypes.Int16:
		x, ok := v.(int16)
		if !ok {
			return false, nil, componentTypeMismatch(comp, "int16", v)
		}
		return true, beUint64(signedOrderedBits(int64(x))), nil
	case sqltypes.Int24, sqltypes.Int32:
		x, ok := v.(int32)
		if !ok {
			return false, nil, componentTypeMismatch(comp, "int32", v)
		}
		return true, beUint64(signedOrderedBits(int64(x))), nil
	case sqltypes.Int64:
		x, ok := v.(int64)
		if !ok {
			return false, nil, componentTypeMismatch(comp, "int64", v)
		}
		return true, beUint64(signedOrderedBits(x)), nil
	case sqltypes.Float32:
		x, ok := v.(float32)
		if !ok {
			return false, nil, componentTypeMismatch(comp, "float32", v)
		}
		return true, beUint64(floatOrderedBits(float64(x))), nil
	case sqltypes.Float64:
		x, ok := v.(float64)
		if !ok {
			return false, nil, componentTypeMismatch(comp, "float64", v)
		}
		return true, beUint64(floatOrderedBits(x)), nil
	case sqltypes.Date, sqltypes.Datetime, sqltypes.Timestamp:
		x, ok := v.(time.Time)
		if !ok {
			return false, nil, componentTypeMismatch(comp, "time.Time", v)
		}
		return true, beUint64(signedOrderedBits(x.UTC().UnixNano())), nil
	case sqltypes.Time:
		x, ok := v.(types.Timespan)
		if !ok {
			return false, nil, componentTypeMismatch(comp, "types.Timespan", v)
		}
		return true, beUint64(signedOrderedBits(int64(x))), nil
	case sqltypes.Char, sqltypes.VarChar, sqltypes.Text:
		x, ok := v.(string)
		if !ok {
			return false, nil, componentTypeMismatch(comp, "string", v)
		}
		return false, truncatePrefix([]byte(x), comp.prefixLen), nil
	case sqltypes.Binary, sqltypes.VarBinary, sqltypes.Blob:
		x, ok := v.([]byte)
		if !ok {
			return false, nil, componentTypeMismatch(comp, "[]byte", v)
		}
		return false, truncatePrefix(x, comp.prefixLen), nil
	default:
		return false, nil, &UnsupportedKeyColumnTypeError{Column: comp.name}
	}
}

// decodeKeyComponent reverses encodeKeyComponent, returning a value typed
// exactly as the component's sql.Type.ValueType() expects.
func decodeKeyComponent(table string, comp keyComponent, raw []byte) (any, error) {
	switch comp.kind {
	case sqltypes.Char, sqltypes.VarChar, sqltypes.Text:
		return string(raw), nil
	case sqltypes.Binary, sqltypes.VarBinary, sqltypes.Blob:
		out := make([]byte, len(raw))
		copy(out, raw)
		return out, nil
	}

	if len(raw) != 8 {
		return nil, &TruncatedKeyComponentError{Table: table, Column: comp.name}
	}
	bits := binary.BigEndian.Uint64(raw)

	switch comp.kind {
	case sqltypes.Uint8:
		return uint8(bits), nil
	case sqltypes.Uint16, sqltypes.Enum:
		return uint16(bits), nil
	case sqltypes.Uint24, sqltypes.Uint32:
		return uint32(bits), nil
	case sqltypes.Uint64, sqltypes.Bit, sqltypes.Set:
		return bits, nil
	case sqltypes.Year:
		return int16(uint16(bits)), nil
	case sqltypes.Int8:
		return int8(signedFromOrderedBits(bits)), nil
	case sqltypes.Int16:
		return int16(signedFromOrderedBits(bits)), nil
	case sqltypes.Int24, sqltypes.Int32:
		return int32(signedFromOrderedBits(bits)), nil
	case sqltypes.Int64:
		return signedFromOrderedBits(bits), nil
	case sqltypes.Float32:
		return float32(floatOrderedBitsToFloat(bits)), nil
	case sqltypes.Float64:
		return floatOrderedBitsToFloat(bits), nil
	case sqltypes.Date, sqltypes.Datetime, sqltypes.Timestamp:
		return time.Unix(0, signedFromOrderedBits(bits)).UTC(), nil
	case sqltypes.Time:
		return types.Timespan(signedFromOrderedBits(bits)), nil
	default:
		return nil, &UnsupportedKeyColumnTypeError{Table: table, Column: comp.name}
	}
}

func componentTypeMismatch(comp keyComponent, expected string, v any) error {
	return &ValueTypeMismatchError{Column: comp.name, Expected: expected, Got: goTypeName(v)}
}

func truncatePrefix(b []byte, n int) []byte {
	if n > 0 && len(b) > n {
		return b[:n]
	}
	return b
}

// ---------------------------------------------------------------------------
// Order-preserving bit transforms
// ---------------------------------------------------------------------------

// beUint64 renders x as 8 bytes big-endian.
func beUint64(x uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], x)
	return b[:]
}

// signedOrderedBits maps a signed 64-bit magnitude to an unsigned 64-bit
// value whose big-endian byte order matches signed numeric order: flipping
// the sign bit moves every negative value below every non-negative one
// while preserving order within each half.
func signedOrderedBits(v int64) uint64 {
	return uint64(v) ^ (1 << 63)
}

// signedFromOrderedBits reverses signedOrderedBits.
func signedFromOrderedBits(bits uint64) int64 {
	return int64(bits ^ (1 << 63))
}

// floatOrderedBits maps a float64's IEEE-754 bit pattern to an unsigned
// 64-bit value whose big-endian byte order matches float order: for
// non-negative values the sign bit is set (moving them above every negative
// value), and for negative values every bit is flipped (reversing their
// naturally-descending magnitude order into an ascending one).
func floatOrderedBits(f float64) uint64 {
	bits := math.Float64bits(f)
	if bits&(1<<63) != 0 {
		return ^bits
	}
	return bits | (1 << 63)
}

// floatOrderedBitsToFloat reverses floatOrderedBits.
func floatOrderedBitsToFloat(bits uint64) float64 {
	if bits&(1<<63) != 0 {
		bits ^= 1 << 63
	} else {
		bits = ^bits
	}
	return math.Float64frombits(bits)
}

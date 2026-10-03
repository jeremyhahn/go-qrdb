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

// NilTableDefError is returned when a nil *service.TableDef is passed to a
// constructor that requires one.
type NilTableDefError struct{}

// Error implements the error interface.
func (e *NilTableDefError) Error() string {
	return "catalog: table definition is nil"
}

// EmptyColumnsError is returned when a table definition declares no columns.
type EmptyColumnsError struct {
	Table string
}

// Error implements the error interface.
func (e *EmptyColumnsError) Error() string {
	return "catalog: table " + e.Table + " declares no columns"
}

// ColumnNotFoundError is returned when a primary key or index references a
// column name that is not present in the table's column list.
type ColumnNotFoundError struct {
	Table  string
	Column string
}

// Error implements the error interface.
func (e *ColumnNotFoundError) Error() string {
	return "catalog: column not found: " + e.Table + "." + e.Column
}

// UnsupportedColumnTypeError is returned when a column's declared type
// string does not name a type this package can represent.
type UnsupportedColumnTypeError struct {
	Column string
	Type   string
}

// Error implements the error interface.
func (e *UnsupportedColumnTypeError) Error() string {
	return "catalog: unsupported column type for " + e.Column + ": " + e.Type
}

// SpatialTypeUnsupportedError is returned when a column declares a GEOMETRY
// or other spatial type. Spatial types are explicitly out of scope and must
// fail at DDL time rather than at first insert.
type SpatialTypeUnsupportedError struct {
	Column string
	Type   string
}

// Error implements the error interface.
func (e *SpatialTypeUnsupportedError) Error() string {
	return "catalog: spatial types are not supported: " + e.Column + ": " + e.Type
}

// InvalidColumnTypeError is returned when a column's type string names a
// recognized type but its parameters are malformed (e.g. an unparsable
// length or precision).
type InvalidColumnTypeError struct {
	Column string
	Type   string
	Reason string
}

// Error implements the error interface.
func (e *InvalidColumnTypeError) Error() string {
	return "catalog: invalid column type for " + e.Column + " (" + e.Type + "): " + e.Reason
}

// InvalidTypeStringError is returned when a canonical MySQL type string
// cannot be parsed at all, independent of whether the type name is known.
type InvalidTypeStringError struct {
	Type   string
	Reason string
}

// Error implements the error interface.
func (e *InvalidTypeStringError) Error() string {
	return "catalog: invalid type string " + e.Type + ": " + e.Reason
}

// RowColumnCountMismatchError is returned when a row passed to a codec does
// not have the number of values the codec expects.
type RowColumnCountMismatchError struct {
	Table    string
	Expected int
	Got      int
}

// Error implements the error interface.
func (e *RowColumnCountMismatchError) Error() string {
	return "catalog: row column count mismatch for " + e.Table
}

// ColumnEncodeError is returned when a single column's value fails to
// encode. It wraps the underlying cause.
type ColumnEncodeError struct {
	Table  string
	Column string
	Err    error
}

// Error implements the error interface.
func (e *ColumnEncodeError) Error() string {
	return "catalog: encode failed for " + e.Table + "." + e.Column + ": " + e.Err.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ColumnEncodeError) Unwrap() error {
	return e.Err
}

// ColumnDecodeError is returned when a single column's value fails to
// decode. It wraps the underlying cause.
type ColumnDecodeError struct {
	Table  string
	Column string
	Err    error
}

// Error implements the error interface.
func (e *ColumnDecodeError) Error() string {
	return "catalog: decode failed for " + e.Table + "." + e.Column + ": " + e.Err.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ColumnDecodeError) Unwrap() error {
	return e.Err
}

// ValueTypeMismatchError is returned when a value handed to a codec does not
// have the Go type the column's SQL type requires.
//
// Column is optional: the row codec's per-type encoders are shared by every
// column of that type and so do not know which column they were called for,
// but their caller wraps the result in a ColumnEncodeError that names it.
// Only the key codec, whose components each know their own column, fills it
// in.
type ValueTypeMismatchError struct {
	Column   string
	Expected string
	Got      string
}

// Error implements the error interface. The column clause is omitted rather
// than rendered empty when Column is unset, so the message reads as a
// statement about the value in every context the type is used in.
func (e *ValueTypeMismatchError) Error() string {
	if e.Column == "" {
		return "catalog: value has Go type " + e.Got + ", expected " + e.Expected
	}
	return "catalog: value for " + e.Column + " has Go type " + e.Got + ", expected " + e.Expected
}

// TruncatedValueError is returned when a column's encoded value is shorter
// than the fixed or declared width required to decode it.
type TruncatedValueError struct {
	Width int
}

// Error implements the error interface.
func (e *TruncatedValueError) Error() string {
	return "catalog: truncated value, expected at least the declared width"
}

// TruncatedRowError is returned when an encoded row's bytes end before the
// header or values they describe have been fully read.
type TruncatedRowError struct {
	Table string
}

// Error implements the error interface.
func (e *TruncatedRowError) Error() string {
	return "catalog: truncated row for " + e.Table
}

// InvalidRowMagicError is returned when an encoded row does not begin with
// the expected magic bytes.
type InvalidRowMagicError struct {
	Table string
}

// Error implements the error interface.
func (e *InvalidRowMagicError) Error() string {
	return "catalog: invalid row magic for " + e.Table
}

// UnsupportedRowFormatError is returned when an encoded row declares a
// format version this codec does not understand.
type UnsupportedRowFormatError struct {
	Table   string
	Version byte
}

// Error implements the error interface.
func (e *UnsupportedRowFormatError) Error() string {
	return "catalog: unsupported row format version for " + e.Table
}

// DecimalParseError is returned when a decoded DECIMAL value's canonical
// text is not parsable.
type DecimalParseError struct {
	Value string
}

// Error implements the error interface.
func (e *DecimalParseError) Error() string {
	return "catalog: cannot parse decimal value: " + e.Value
}

// DefaultValueConvertError is returned when a column's SQL-text default
// cannot be converted to the column's declared type.
type DefaultValueConvertError struct {
	Column string
	Reason string
}

// Error implements the error interface.
func (e *DefaultValueConvertError) Error() string {
	return "catalog: cannot convert default value for " + e.Column + ": " + e.Reason
}

// JSONEncodeError is returned when a JSON column's value cannot be
// marshaled to canonical JSON.
type JSONEncodeError struct {
	Reason string
}

// Error implements the error interface.
func (e *JSONEncodeError) Error() string {
	return "catalog: json encode failed: " + e.Reason
}

// JSONDecodeError is returned when a JSON column's encoded bytes cannot be
// unmarshaled.
type JSONDecodeError struct {
	Reason string
}

// Error implements the error interface.
func (e *JSONDecodeError) Error() string {
	return "catalog: json decode failed: " + e.Reason
}

// SymbolicTypeMismatchError is returned when a column whose query.Type
// declares it symbolically represented (ENUM, SET) does not implement the
// go-mysql-server interface that carries its value list, leaving no way to
// resolve a stored ordinal or bitmask back to its labels.
type SymbolicTypeMismatchError struct {
	Type     string
	Expected string
}

// Error implements the error interface.
func (e *SymbolicTypeMismatchError) Error() string {
	return "catalog: column type " + e.Type + " does not implement " + e.Expected
}

// SymbolicValueError is returned when an ENUM or SET cell cannot be resolved
// to its labels because the cell does not hold the ordinal or bitmask the
// column type requires.
type SymbolicValueError struct {
	Type   string
	Value  string
	Reason string
}

// Error implements the error interface.
func (e *SymbolicValueError) Error() string {
	return "catalog: cannot resolve " + e.Value + " against column type " + e.Type + ": " + e.Reason
}

// UnsupportedKeyColumnTypeError is returned when a primary key column's type
// has no defined order-preserving key encoding (for example DECIMAL, JSON,
// or a spatial type).
type UnsupportedKeyColumnTypeError struct {
	Table  string
	Column string
	Type   string
}

// Error implements the error interface.
func (e *UnsupportedKeyColumnTypeError) Error() string {
	return "catalog: unsupported primary key column type: " + e.Table + "." + e.Column + ": " + e.Type
}

// PrefixLengthNotSupportedError is returned when a PKColumn declares a
// PrefixLength on a column whose type is not string- or binary-like.
type PrefixLengthNotSupportedError struct {
	Table  string
	Column string
	Type   string
}

// Error implements the error interface.
func (e *PrefixLengthNotSupportedError) Error() string {
	return "catalog: prefix length is not supported for " + e.Table + "." + e.Column + ": " + e.Type
}

// NullKeyComponentError is returned when a NULL value is supplied for a
// primary key component. Primary key columns may not be NULL.
type NullKeyComponentError struct {
	Column string
}

// Error implements the error interface.
func (e *NullKeyComponentError) Error() string {
	return "catalog: primary key component " + e.Column + " must not be NULL"
}

// MissingRowIDError is returned when a row for a synthetic-primary-key table
// does not carry a trailing row id value.
type MissingRowIDError struct {
	Table string
}

// Error implements the error interface.
func (e *MissingRowIDError) Error() string {
	return "catalog: row for " + e.Table + " is missing its synthetic row id"
}

// KeyValueCountMismatchError is returned when the number of values passed to
// KeyCodec.EncodeValues does not match the number of primary key columns.
type KeyValueCountMismatchError struct {
	Table    string
	Expected int
	Got      int
}

// Error implements the error interface.
func (e *KeyValueCountMismatchError) Error() string {
	return "catalog: key value count mismatch for " + e.Table
}

// InvalidKeyEncodingError is returned when an encoded key cannot be split
// back into its declared primary key components.
type InvalidKeyEncodingError struct {
	Table  string
	Reason string
}

// Error implements the error interface.
func (e *InvalidKeyEncodingError) Error() string {
	return "catalog: invalid key encoding for " + e.Table + ": " + e.Reason
}

// TruncatedKeyComponentError is returned when a decoded key component does
// not have the fixed width its type requires.
type TruncatedKeyComponentError struct {
	Table  string
	Column string
}

// Error implements the error interface.
func (e *TruncatedKeyComponentError) Error() string {
	return "catalog: truncated key component " + e.Table + "." + e.Column
}

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

package gorm

import (
	"fmt"
	"strings"

	"gorm.io/gorm/schema"
)

// MySQL's text and blob columns are chosen by declared size against these
// thresholds: up to 65535 bytes is a bounded varchar/varbinary, up to
// 2^24-1 bytes is medium, and above that (or unbounded) is long.
const (
	mediumSizeThreshold = 1 << 16 // 65536
	longSizeThreshold   = 1 << 24 // 16777216
)

// mysqlPrimaryKeySize is the varchar/varbinary width MySQL's utf8mb4 charset
// allows within its 767-byte default index-prefix limit (767 / 4 = 191.75,
// rounded down). A String field with no explicit size that is a primary
// key, indexed, unique, or carries a default value is sized here rather
// than left unbounded, because TEXT and BLOB columns cannot be indexed or
// given a default value in MySQL.
const mysqlIndexableStringSize = 191

// dataTypeBuilders dispatches DataTypeOf on field.DataType. It is a map
// rather than a type switch so that adding a schema.DataType requires
// touching one table instead of every call site that branches on it.
var dataTypeBuilders = map[schema.DataType]func(Dialector, *schema.Field) string{
	schema.Bool:   func(Dialector, *schema.Field) string { return "boolean" },
	schema.Int:    Dialector.integerType,
	schema.Uint:   Dialector.integerType,
	schema.Float:  Dialector.floatType,
	schema.String: Dialector.stringType,
	schema.Time:   Dialector.timeType,
	schema.Bytes:  Dialector.bytesType,
}

// DataTypeOf returns the SQL column type for field, dispatching on its
// schema.DataType:
//
//   - Bool maps to "boolean".
//   - Int and Uint map to tinyint/smallint/mediumint/int/bigint by
//     field.Size (<=8/16/24/32/wider), suffixed " unsigned" for Uint and
//     " AUTO_INCREMENT" when field.AutoIncrement.
//   - Float maps to "decimal(p,s)" when field.Precision is set, else
//     float or double by field.Size.
//   - String maps to "varchar(n)", "mediumtext", or "longtext"; see
//     stringType.
//   - Time maps to "datetime(p)", plus " NULL" unless the field is
//     NotNull or a PrimaryKey.
//   - Bytes maps to "varbinary(n)", "mediumblob", or "longblob".
//
// A field whose DataType matches none of these falls back to the literal
// type from a gorm:"type:..." tag, and to the empty string when it has
// none — see customType for why that is preferred to panicking.
func (dialector Dialector) DataTypeOf(field *schema.Field) string {
	if build, ok := dataTypeBuilders[field.DataType]; ok {
		return build(dialector, field)
	}
	return dialector.customType(field)
}

// integerType returns the SQL integer type for an Int or Uint field, sized
// by field.Size in bits.
func (dialector Dialector) integerType(field *schema.Field) string {
	var sqlType string
	switch {
	case field.Size <= 8:
		sqlType = "tinyint"
	case field.Size <= 16:
		sqlType = "smallint"
	case field.Size <= 24:
		sqlType = "mediumint"
	case field.Size <= 32:
		sqlType = "int"
	default:
		sqlType = "bigint"
	}

	if field.DataType == schema.Uint {
		sqlType += " unsigned"
	}
	if field.AutoIncrement {
		sqlType += " AUTO_INCREMENT"
	}
	return sqlType
}

// floatType returns the SQL floating-point type for a Float field: a fixed
// decimal(p,s) when field.Precision is set, otherwise float or double by
// field.Size.
func (dialector Dialector) floatType(field *schema.Field) string {
	if field.Precision > 0 {
		return fmt.Sprintf("decimal(%d, %d)", field.Precision, field.Scale)
	}
	if field.Size <= 32 {
		return "float"
	}
	return "double"
}

// stringType returns the SQL string type for a String field.
//
// The resolved size is field.Size when the caller set an explicit
// gorm:"size:N" tag; otherwise Config.DefaultStringSize; otherwise
// mysqlIndexableStringSize (191) if the field is a primary key, unique,
// indexed, or carries a default value — TEXT columns cannot be indexed or
// defaulted in MySQL, so those fields must fit in a bounded varchar. A
// field matching none of that is left unbounded (longtext).
//
// A resolved size in [mediumSizeThreshold, longSizeThreshold] returns
// mediumtext; above longSizeThreshold, or when no size could be resolved
// at all, returns longtext.
func (dialector Dialector) stringType(field *schema.Field) string {
	size := field.Size
	if size == 0 {
		if dialector.Config != nil && dialector.Config.DefaultStringSize > 0 {
			size = int(dialector.Config.DefaultStringSize)
		} else if field.PrimaryKey || field.HasDefaultValue || isIndexed(field) {
			size = mysqlIndexableStringSize
		}
	}

	switch {
	case size >= mediumSizeThreshold && size <= longSizeThreshold:
		return "mediumtext"
	case size > longSizeThreshold || size <= 0:
		return "longtext"
	default:
		return fmt.Sprintf("varchar(%d)", size)
	}
}

// isIndexed reports whether field carries a gorm:"index" or gorm:"unique"
// tag.
func isIndexed(field *schema.Field) bool {
	return field.TagSettings["INDEX"] != "" || field.TagSettings["UNIQUE"] != ""
}

// timeType returns the SQL datetime type for a Time field.
//
// field.Precision defaults to Config.DefaultDatetimePrecision (6) when the
// field carries neither an explicit gorm:"precision:N" tag nor
// Config.DisableDatetimePrecision — this mutates field.Precision so that a
// caller inspecting the field afterward (e.g. the Migrator implemented in a
// later phase) sees the resolved value rather than the unset zero. A field
// that is neither NotNull nor a PrimaryKey is suffixed " NULL" so it is
// nullable by default, matching every other GORM-mapped type.
func (dialector Dialector) timeType(field *schema.Field) string {
	if !dialector.DisableDatetimePrecision && field.Precision == 0 && field.TagSettings["PRECISION"] == "" {
		if dialector.Config != nil && dialector.Config.DefaultDatetimePrecision != nil {
			field.Precision = *dialector.Config.DefaultDatetimePrecision
		}
	}

	var precision string
	if field.Precision > 0 {
		precision = fmt.Sprintf("(%d)", field.Precision)
	}

	if field.NotNull || field.PrimaryKey {
		return "datetime" + precision
	}
	return "datetime" + precision + " NULL"
}

// bytesType returns the SQL binary type for a Bytes field: a bounded
// varbinary(n) for 0 < field.Size < mediumSizeThreshold, mediumblob up to
// longSizeThreshold, and longblob above that or when field.Size is 0.
func (dialector Dialector) bytesType(field *schema.Field) string {
	switch {
	case field.Size > 0 && field.Size < mediumSizeThreshold:
		return fmt.Sprintf("varbinary(%d)", field.Size)
	case field.Size >= mediumSizeThreshold && field.Size <= longSizeThreshold:
		return "mediumblob"
	default:
		return "longblob"
	}
}

// customType returns the literal SQL type from a gorm:"type:..." tag,
// suffixed " AUTO_INCREMENT" when field.AutoIncrement and the tag did not
// already say so.
//
// A field with no recognized DataType and no type tag yields the empty
// string, matching gorm.io/driver/mysql. gorm.Dialector.DataTypeOf returns a
// bare string with no error, so the empty type flows into the generated DDL
// and the engine rejects it with a syntax error naming the column — a normal,
// recoverable failure the caller can act on.
//
// This deliberately does not panic. DataTypeOf is called from AutoMigrate
// inside the caller's process; panicking there would take down an application
// over a mis-tagged struct field, which is neither unrecoverable nor the
// dialector's decision to make.
func (dialector Dialector) customType(field *schema.Field) string {
	sqlType := string(field.DataType)

	if sqlType != "" && field.AutoIncrement &&
		!strings.Contains(strings.ToLower(sqlType), " auto_increment") {
		sqlType += " AUTO_INCREMENT"
	}
	return sqlType
}

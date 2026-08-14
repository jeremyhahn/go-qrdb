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
	"sync"
	"testing"
	"time"

	"gorm.io/gorm/schema"
)

// parseField parses dest with schema.Parse and returns its single field
// named "F", failing the test if parsing fails or the field is absent.
func parseField(t *testing.T, dest interface{}) *schema.Field {
	t.Helper()

	s, err := schema.Parse(dest, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("schema.Parse: %v", err)
	}

	f, ok := s.FieldsByName["F"]
	if !ok {
		t.Fatalf("field %q not found in parsed schema", "F")
	}
	return f
}

func TestDataTypeOf_Bool(t *testing.T) {
	type model struct {
		F bool
	}

	dialector := New(Config{})
	got := dialector.DataTypeOf(parseField(t, &model{}))
	if got != "boolean" {
		t.Errorf("DataTypeOf(bool) = %q, want %q", got, "boolean")
	}
}

func TestDataTypeOf_Integer(t *testing.T) {
	tests := []struct {
		name          string
		dest          interface{}
		autoIncrement bool
		want          string
	}{
		{"int8 signed", &struct {
			F int8
		}{}, false, "tinyint"},
		{"uint8 unsigned", &struct {
			F uint8
		}{}, false, "tinyint unsigned"},
		{"int16 signed", &struct {
			F int16
		}{}, false, "smallint"},
		{"uint16 unsigned", &struct {
			F uint16
		}{}, false, "smallint unsigned"},
		{"int32 signed", &struct {
			F int32
		}{}, false, "int"},
		{"uint32 unsigned", &struct {
			F uint32
		}{}, false, "int unsigned"},
		{"int64 signed", &struct {
			F int64
		}{}, false, "bigint"},
		{"uint64 unsigned", &struct {
			F uint64
		}{}, false, "bigint unsigned"},
		{"int64 auto increment", &struct {
			F int64 `gorm:"autoIncrement"`
		}{}, true, "bigint AUTO_INCREMENT"},
		{"uint32 auto increment", &struct {
			F uint32 `gorm:"autoIncrement"`
		}{}, true, "int unsigned AUTO_INCREMENT"},
		{"explicit 24-bit size, signed", &struct {
			F int `gorm:"size:24"`
		}{}, false, "mediumint"},
		{"explicit 24-bit size, unsigned", &struct {
			F uint `gorm:"size:24"`
		}{}, false, "mediumint unsigned"},
	}

	dialector := New(Config{})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			field := parseField(t, tt.dest)
			if field.AutoIncrement != tt.autoIncrement {
				t.Fatalf("parsed AutoIncrement = %v, want %v", field.AutoIncrement, tt.autoIncrement)
			}
			if got := dialector.DataTypeOf(field); got != tt.want {
				t.Errorf("DataTypeOf() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDataTypeOf_IntegerSizeBoundaries(t *testing.T) {
	// Field.Size is set directly (bypassing schema.Parse's Go-kind-derived
	// default) to exercise every documented boundary: <=8/16/24/32/else.
	tests := []struct {
		size   int
		isUint bool
		want   string
	}{
		{8, false, "tinyint"},
		{9, false, "smallint"},
		{16, false, "smallint"},
		{17, false, "mediumint"},
		{24, false, "mediumint"},
		{25, false, "int"},
		{32, false, "int"},
		{33, false, "bigint"},
		{8, true, "tinyint unsigned"},
		{32, true, "int unsigned"},
		{33, true, "bigint unsigned"},
	}

	dialector := New(Config{})
	for _, tt := range tests {
		field := &schema.Field{DataType: schema.Int, Size: tt.size}
		if tt.isUint {
			field.DataType = schema.Uint
		}
		got := dialector.DataTypeOf(field)
		if got != tt.want {
			t.Errorf("size=%d uint=%v: DataTypeOf() = %q, want %q", tt.size, tt.isUint, got, tt.want)
		}
	}
}

func TestDataTypeOf_Float(t *testing.T) {
	tests := []struct {
		name string
		dest interface{}
		want string
	}{
		{"float32", &struct {
			F float32
		}{}, "float"},
		{"float64", &struct {
			F float64
		}{}, "double"},
		{"precision and scale", &struct {
			F float64 `gorm:"precision:10;scale:2"`
		}{}, "decimal(10, 2)"},
	}

	dialector := New(Config{})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dialector.DataTypeOf(parseField(t, tt.dest)); got != tt.want {
				t.Errorf("DataTypeOf() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDataTypeOf_FloatSizeBoundary(t *testing.T) {
	dialector := New(Config{})

	f32 := &schema.Field{DataType: schema.Float, Size: 32}
	if got := dialector.DataTypeOf(f32); got != "float" {
		t.Errorf("size=32: DataTypeOf() = %q, want %q", got, "float")
	}

	f33 := &schema.Field{DataType: schema.Float, Size: 33}
	if got := dialector.DataTypeOf(f33); got != "double" {
		t.Errorf("size=33: DataTypeOf() = %q, want %q", got, "double")
	}
}

func TestDataTypeOf_String(t *testing.T) {
	tests := []struct {
		name              string
		dest              interface{}
		defaultStringSize uint
		want              string
	}{
		{"default size applied", &struct {
			F string
		}{}, 256, "varchar(256)"},
		{"explicit size tag wins over default", &struct {
			F string `gorm:"size:64"`
		}{}, 256, "varchar(64)"},
		{"no default size, unindexed plain field is unbounded", &struct {
			F string
		}{}, 0, "longtext"},
		{"no default size, primary key falls back to 191", &struct {
			F string `gorm:"primaryKey"`
		}{}, 0, "varchar(191)"},
		{"no default size, unique falls back to 191", &struct {
			F string `gorm:"unique"`
		}{}, 0, "varchar(191)"},
		{"no default size, indexed falls back to 191", &struct {
			F string `gorm:"index"`
		}{}, 0, "varchar(191)"},
		{"no default size, has default value falls back to 191", &struct {
			F string `gorm:"default:hello"`
		}{}, 0, "varchar(191)"},
		{"mediumtext lower boundary", &struct {
			F string `gorm:"size:65536"`
		}{}, 256, "mediumtext"},
		{"varchar just below mediumtext boundary", &struct {
			F string `gorm:"size:65535"`
		}{}, 256, "varchar(65535)"},
		{"mediumtext upper boundary", &struct {
			F string `gorm:"size:16777216"`
		}{}, 256, "mediumtext"},
		{"longtext just above mediumtext boundary", &struct {
			F string `gorm:"size:16777217"`
		}{}, 256, "longtext"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Built directly rather than through New, which fills a zero
			// DefaultStringSize in with the 256 default; the "no default
			// size" cases below need to observe stringType's own
			// PK/indexed/has-default-191-else-unbounded fallback instead.
			dialector := Dialector{Config: &Config{DefaultStringSize: tt.defaultStringSize}}
			if got := dialector.DataTypeOf(parseField(t, tt.dest)); got != tt.want {
				t.Errorf("DataTypeOf() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDataTypeOf_Time(t *testing.T) {
	tests := []struct {
		name string
		dest interface{}
		want string
	}{
		{"nullable by default", &struct {
			F time.Time
		}{}, "datetime(6) NULL"},
		{"not null suppresses NULL suffix", &struct {
			F time.Time `gorm:"not null"`
		}{}, "datetime(6)"},
		{"primary key suppresses NULL suffix", &struct {
			F time.Time `gorm:"primaryKey"`
		}{}, "datetime(6)"},
		// Deliberately a precision the default is NOT, so the case still
		// proves the tag wins rather than agreeing with it by accident.
		{"explicit precision tag", &struct {
			F time.Time `gorm:"precision:3"`
		}{}, "datetime(3) NULL"},
		{"explicit zero precision tag disables default", &struct {
			F time.Time `gorm:"precision:0"`
		}{}, "datetime NULL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dialector := New(Config{})
			if got := dialector.DataTypeOf(parseField(t, tt.dest)); got != tt.want {
				t.Errorf("DataTypeOf() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDataTypeOf_TimeDisableDatetimePrecision(t *testing.T) {
	cfg := Config{DisableDatetimePrecision: true}
	dialector := New(cfg)

	type model struct {
		F time.Time
	}
	if got := dialector.DataTypeOf(parseField(t, &model{})); got != "datetime NULL" {
		t.Errorf("DataTypeOf() = %q, want %q", got, "datetime NULL")
	}
}

func TestDataTypeOf_Bytes(t *testing.T) {
	dialector := New(Config{})

	tests := []struct {
		name string
		size int
		want string
	}{
		{"unbounded is longblob", 0, "longblob"},
		{"bounded varbinary", 255, "varbinary(255)"},
		{"just below mediumblob boundary", 65535, "varbinary(65535)"},
		{"mediumblob lower boundary", 65536, "mediumblob"},
		{"mediumblob upper boundary", 16777216, "mediumblob"},
		{"longblob above mediumblob boundary", 16777217, "longblob"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			field := &schema.Field{DataType: schema.Bytes, Size: tt.size}
			if got := dialector.DataTypeOf(field); got != tt.want {
				t.Errorf("DataTypeOf() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDataTypeOf_BytesFromByteSlice(t *testing.T) {
	type model struct {
		F []byte
	}
	dialector := New(Config{})
	if got := dialector.DataTypeOf(parseField(t, &model{})); got != "longblob" {
		t.Errorf("DataTypeOf([]byte) = %q, want %q", got, "longblob")
	}
}

func TestDataTypeOf_CustomTypeTag(t *testing.T) {
	type model struct {
		F string `gorm:"type:enum('a','b')"`
	}
	dialector := New(Config{})
	if got := dialector.DataTypeOf(parseField(t, &model{})); got != "enum('a','b')" {
		t.Errorf("DataTypeOf() = %q, want %q", got, "enum('a','b')")
	}
}

func TestDataTypeOf_CustomTypeTagAutoIncrement(t *testing.T) {
	dialector := New(Config{})
	field := &schema.Field{DataType: "bigint", AutoIncrement: true}
	if got := dialector.DataTypeOf(field); got != "bigint AUTO_INCREMENT" {
		t.Errorf("DataTypeOf() = %q, want %q", got, "bigint AUTO_INCREMENT")
	}

	// A tag that already spells out AUTO_INCREMENT is not suffixed twice.
	field = &schema.Field{DataType: "bigint auto_increment", AutoIncrement: true}
	if got := dialector.DataTypeOf(field); got != "bigint auto_increment" {
		t.Errorf("DataTypeOf() = %q, want %q", got, "bigint auto_increment")
	}
}

func TestDataTypeOf_UnmappableFieldYieldsEmptyType(t *testing.T) {
	dialector := New(Config{})

	// A field with neither a recognized DataType nor a type tag yields the
	// empty string, matching gorm.io/driver/mysql. The empty type flows into
	// the generated DDL and the engine rejects it with a syntax error naming
	// the column — a recoverable failure. Panicking here would take down the
	// caller's application from inside AutoMigrate over a mis-tagged struct.
	field := &schema.Field{Name: "Mystery"}
	if got := dialector.DataTypeOf(field); got != "" {
		t.Errorf("DataTypeOf() = %q, want the empty string", got)
	}
}

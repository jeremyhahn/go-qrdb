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
	"reflect"
	"testing"
	"time"

	"github.com/dolthub/go-mysql-server/sql/types"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestKeyCodec_AllSupportedTypes round trips one representative value for
// every key-encodable column type, covering every branch of
// encodeKeyComponent and decodeKeyComponent.
func TestKeyCodec_AllSupportedTypes(t *testing.T) {
	now := time.Now().UTC().Round(0)
	cases := []struct {
		name    string
		sqlType string
		value   any
	}{
		{"tinyint_unsigned", "TINYINT UNSIGNED", uint8(200)},
		{"smallint_unsigned", "SMALLINT UNSIGNED", uint16(60000)},
		{"mediumint_unsigned", "MEDIUMINT UNSIGNED", uint32(1000000)},
		{"int_unsigned", "INT UNSIGNED", uint32(4000000000)},
		{"bigint_unsigned", "BIGINT UNSIGNED", uint64(1) << 60},
		{"year", "YEAR", int16(2024)},
		{"bit", "BIT(64)", uint64(0xDEADBEEF)},
		{"enum", "ENUM('a','b','c')", uint16(2)},
		{"set", "SET('a','b','c')", uint64(5)},
		{"tinyint", "TINYINT", int8(-42)},
		{"smallint", "SMALLINT", int16(-1234)},
		{"mediumint", "MEDIUMINT", int32(-8000000)},
		{"int", "INT", int32(-70000)},
		{"bigint", "BIGINT", int64(-9000000000)},
		{"date", "DATE", now},
		{"datetime", "DATETIME", now},
		{"timestamp", "TIMESTAMP", now},
		{"time", "TIME", types.Timespan(-123456789)},
		{"float", "FLOAT", float32(-3.5)},
		{"double", "DOUBLE", float64(3.14159)},
		{"char", "CHAR(10)", "hello"},
		{"varchar", "VARCHAR(10)", "world"},
		{"text", "TEXT", "some text"},
		{"binary", "BINARY(4)", []byte{1, 2, 3, 4}},
		{"varbinary", "VARBINARY(10)", []byte{5, 6, 7}},
		{"blob", "BLOB", []byte("blob data")},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kc := singleColumnKeyCodec(t, c.sqlType)
			key, err := kc.EncodeValues([]any{c.value})
			if err != nil {
				t.Fatalf("EncodeValues: %v", err)
			}
			got, err := kc.Decode(key)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			switch want := c.value.(type) {
			case time.Time:
				gt, ok := got[0].(time.Time)
				if !ok || !gt.Equal(want) {
					t.Fatalf("got %v, want %v", got[0], want)
				}
			case []byte:
				gb, ok := got[0].([]byte)
				if !ok || !reflect.DeepEqual(gb, want) {
					t.Fatalf("got %#v, want %#v", got[0], want)
				}
			default:
				if got[0] != c.value {
					t.Fatalf("got %#v, want %#v", got[0], c.value)
				}
			}
		})
	}
}

func TestEncodeKeyComponent_TypeMismatches(t *testing.T) {
	cases := []struct {
		sqlType string
	}{
		{"TINYINT UNSIGNED"}, {"SMALLINT UNSIGNED"}, {"MEDIUMINT UNSIGNED"},
		{"BIGINT UNSIGNED"}, {"YEAR"}, {"TINYINT"}, {"SMALLINT"}, {"MEDIUMINT"},
		{"BIGINT"}, {"FLOAT"}, {"DOUBLE"}, {"DATE"}, {"TIME"},
		{"CHAR(10)"}, {"BINARY(4)"},
	}
	for _, c := range cases {
		t.Run(c.sqlType, func(t *testing.T) {
			kc := singleColumnKeyCodec(t, c.sqlType)
			if _, err := kc.EncodeValues([]any{struct{}{}}); err == nil {
				t.Fatalf("%s: expected type mismatch error", c.sqlType)
			}
		})
	}
}

func TestDecodeKeyComponent_TruncatedNumeric(t *testing.T) {
	kc := singleColumnKeyCodec(t, "BIGINT UNSIGNED")
	_, err := kc.Decode("short")
	if err == nil {
		t.Fatal("expected error decoding a truncated numeric key component")
	}
}

func TestNewKeyCodec_GeometryPrimaryKeyRejected(t *testing.T) {
	def := &service.TableDef{
		Name:       "t",
		Columns:    []service.ColumnDef{{Name: "shape", Type: "VARCHAR(10)"}},
		PrimaryKey: []service.PKColumn{{Name: "shape"}},
	}
	kc, err := NewKeyCodec(def)
	if err != nil {
		t.Fatalf("NewKeyCodec: %v", err)
	}
	if kc.synthetic {
		t.Fatal("expected a declared, non-synthetic key codec")
	}
}

func TestKeyEncodingKind_Unsupported(t *testing.T) {
	_, _, ok := keyEncodingKind(999999)
	if ok {
		t.Fatal("expected ok=false for an unrecognized query.Type")
	}
}

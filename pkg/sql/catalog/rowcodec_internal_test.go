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
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/sqltypes"
)

// TestColumnCodecs_TypeMismatch exercises the wrong-Go-type encode error
// path for every entry in the static columnCodecs table. size() is only
// checked for the variable-length codecs (decimal, string, bytes, json):
// the fixed-width codecs' size is a constant that does not depend on the
// value's Go type, by design.
func TestColumnCodecs_TypeMismatch(t *testing.T) {
	wrong := 12345 // an int, which is not the Go type any codec expects
	for kind, cc := range columnCodecs {
		if _, err := cc.encode(nil, wrong); err == nil {
			t.Fatalf("%v: expected encode error for mismatched Go type", kind)
		}
	}

	variableLength := []*columnCodec{
		{encodeDecimal, decodeDecimal, sizeDecimal},
		{encodeString, decodeString, sizeString},
		{encodeBytes, decodeBytes, sizeBytes},
		{encodeJSON, decodeJSON, sizeJSON},
	}
	for _, cc := range variableLength {
		if _, err := cc.size(wrong); err == nil {
			t.Fatal("expected size error for mismatched Go type")
		}
	}
}

// TestColumnCodecs_TruncatedDecode exercises the too-short-buffer decode
// error path for every entry in the static columnCodecs table.
func TestColumnCodecs_TruncatedDecode(t *testing.T) {
	for kind, cc := range columnCodecs {
		if _, _, err := cc.decode(nil); err == nil {
			t.Fatalf("%v: expected decode error for empty buffer", kind)
		}
	}
}

func TestReadVarBytes_Truncated(t *testing.T) {
	// No uvarint at all.
	if _, _, err := readVarBytes(nil); err == nil {
		t.Fatal("expected error for empty buffer")
	}
	// A length prefix claiming more bytes than are present.
	buf := binaryAppendUvarintForTest(nil, 100)
	if _, _, err := readVarBytes(buf); err == nil {
		t.Fatal("expected error for a length prefix exceeding the buffer")
	}
}

func binaryAppendUvarintForTest(buf []byte, x uint64) []byte {
	var scratch [10]byte
	n := 0
	for x >= 0x80 {
		scratch[n] = byte(x) | 0x80
		x >>= 7
		n++
	}
	scratch[n] = byte(x)
	n++
	return append(buf, scratch[:n]...)
}

func TestMarshalJSONValue_WrongType(t *testing.T) {
	if _, err := marshalJSONValue("not a JSONWrapper"); err == nil {
		t.Fatal("expected error for a non-JSONWrapper value")
	}
}

func TestUvarintLen(t *testing.T) {
	cases := map[uint64]int{
		0:          1,
		1:          1,
		127:        1,
		128:        2,
		16383:      2,
		16384:      3,
		1 << 63:    10,
		^uint64(0): 10,
	}
	for x, want := range cases {
		if got := uvarintLen(x); got != want {
			t.Fatalf("uvarintLen(%d): got %d, want %d", x, got, want)
		}
	}
}

func TestGoTypeName(t *testing.T) {
	if got := goTypeName(nil); got != "nil" {
		t.Fatalf("goTypeName(nil): got %q", got)
	}
	if got := goTypeName(int32(1)); got != "int32" {
		t.Fatalf("goTypeName(int32(1)): got %q", got)
	}
}

func TestParseDefaultLiteral_UnquotedAndQuoted(t *testing.T) {
	var intType sqle.Type = types.Int32
	v, err := parseDefaultLiteral(intType, "42")
	if err != nil {
		t.Fatalf("parseDefaultLiteral(unquoted): %v", err)
	}
	if v != int32(42) {
		t.Fatalf("got %v, want int32(42)", v)
	}

	var strType sqle.Type = types.MustCreateStringWithDefaults(sqltypes.VarChar, 100)
	v, err = parseDefaultLiteral(strType, "'it''s a test'")
	if err != nil {
		t.Fatalf("parseDefaultLiteral(quoted): %v", err)
	}
	if v != "it's a test" {
		t.Fatalf("got %q, want %q", v, "it's a test")
	}
}

func TestParseDefaultLiteral_ConvertError(t *testing.T) {
	var jsonType sqle.Type = types.JSON
	_, err := parseDefaultLiteral(jsonType, "not valid json {")
	if err == nil {
		t.Fatal("expected conversion error")
	}
}

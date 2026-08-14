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
	"errors"
	"math"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"testing/quick"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func singleColumnKeyCodec(t *testing.T, sqlType string) *KeyCodec {
	t.Helper()
	def := &service.TableDef{
		Name:       "t",
		Columns:    []service.ColumnDef{{Name: "id", Type: sqlType}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	}
	kc, err := NewKeyCodec(def)
	if err != nil {
		t.Fatalf("NewKeyCodec(%q): %v", sqlType, err)
	}
	return kc
}

// ---------------------------------------------------------------------------
// Order preservation: the central property
// ---------------------------------------------------------------------------

// assertOrderPreserving asserts that sorting the encoded keys for vals
// yields the same permutation as sorting vals themselves under less.
func assertOrderPreserving(t *testing.T, kc *KeyCodec, vals []any, less func(a, b any) bool) {
	t.Helper()

	type pair struct {
		val any
		key string
	}
	pairs := make([]pair, len(vals))
	for i, v := range vals {
		key, err := kc.EncodeValues([]any{v})
		if err != nil {
			t.Fatalf("EncodeValues(%v): %v", v, err)
		}
		pairs[i] = pair{val: v, key: key}
	}

	byValue := append([]pair(nil), pairs...)
	sort.SliceStable(byValue, func(i, j int) bool { return less(byValue[i].val, byValue[j].val) })

	byKey := append([]pair(nil), pairs...)
	sort.SliceStable(byKey, func(i, j int) bool { return byKey[i].key < byKey[j].key })

	for i := range byValue {
		if !reflect.DeepEqual(byValue[i].val, byKey[i].val) {
			t.Fatalf("order mismatch at position %d: value-sorted=%v key-sorted=%v", i, byValue[i].val, byKey[i].val)
		}
	}
}

func TestKeyCodec_OrderPreserving_Uint64(t *testing.T) {
	kc := singleColumnKeyCodec(t, "BIGINT UNSIGNED")
	prop := func(vals []uint64) bool {
		if len(vals) == 0 {
			return true
		}
		anyVals := make([]any, len(vals))
		for i, v := range vals {
			anyVals[i] = v
		}
		assertOrderPreserving(t, kc, anyVals, func(a, b any) bool { return a.(uint64) < b.(uint64) })
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}

func TestKeyCodec_OrderPreserving_Int64(t *testing.T) {
	kc := singleColumnKeyCodec(t, "BIGINT")
	prop := func(vals []int64) bool {
		if len(vals) == 0 {
			return true
		}
		anyVals := make([]any, len(vals))
		for i, v := range vals {
			anyVals[i] = v
		}
		assertOrderPreserving(t, kc, anyVals, func(a, b any) bool { return a.(int64) < b.(int64) })
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}

func TestKeyCodec_OrderPreserving_Int8(t *testing.T) {
	kc := singleColumnKeyCodec(t, "TINYINT")
	prop := func(vals []int8) bool {
		if len(vals) == 0 {
			return true
		}
		anyVals := make([]any, len(vals))
		for i, v := range vals {
			anyVals[i] = v
		}
		assertOrderPreserving(t, kc, anyVals, func(a, b any) bool { return a.(int8) < b.(int8) })
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}

func TestKeyCodec_OrderPreserving_Float64(t *testing.T) {
	kc := singleColumnKeyCodec(t, "DOUBLE")
	r := rand.New(rand.NewSource(1))
	vals := make([]any, 200)
	for i := range vals {
		f := r.NormFloat64() * 1e12
		vals[i] = f
	}
	assertOrderPreserving(t, kc, vals, func(a, b any) bool { return a.(float64) < b.(float64) })
}

func TestKeyCodec_OrderPreserving_String(t *testing.T) {
	kc := singleColumnKeyCodec(t, "VARCHAR(64)")
	prop := func(vals []string) bool {
		if len(vals) == 0 {
			return true
		}
		anyVals := make([]any, len(vals))
		for i, v := range vals {
			anyVals[i] = v
		}
		assertOrderPreserving(t, kc, anyVals, func(a, b any) bool { return a.(string) < b.(string) })
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}

func TestKeyCodec_OrderPreserving_Bytes(t *testing.T) {
	kc := singleColumnKeyCodec(t, "VARBINARY(64)")
	prop := func(vals [][]byte) bool {
		if len(vals) == 0 {
			return true
		}
		anyVals := make([]any, len(vals))
		for i, v := range vals {
			anyVals[i] = v
		}
		assertOrderPreserving(t, kc, anyVals, func(a, b any) bool {
			return string(a.([]byte)) < string(b.([]byte))
		})
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Round trip (Encode/EncodeValues/Decode)
// ---------------------------------------------------------------------------

func TestKeyCodec_RoundTrip_Uint64(t *testing.T) {
	kc := singleColumnKeyCodec(t, "BIGINT UNSIGNED")
	for _, v := range []uint64{0, 1, 42, 1 << 63, ^uint64(0)} {
		key, err := kc.EncodeValues([]any{v})
		if err != nil {
			t.Fatalf("EncodeValues: %v", err)
		}
		vals, err := kc.Decode(key)
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if vals[0] != v {
			t.Fatalf("got %v, want %v", vals[0], v)
		}
	}
}

func TestKeyCodec_RoundTrip_Int64Negative(t *testing.T) {
	kc := singleColumnKeyCodec(t, "BIGINT")
	for _, v := range []int64{0, -1, 1, -9223372036854775808, 9223372036854775807} {
		key, err := kc.EncodeValues([]any{v})
		if err != nil {
			t.Fatalf("EncodeValues: %v", err)
		}
		vals, err := kc.Decode(key)
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if vals[0] != v {
			t.Fatalf("got %v, want %v", vals[0], v)
		}
	}
}

func TestKeyCodec_Encode_FromRow(t *testing.T) {
	def := &service.TableDef{
		Name: "users",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED"},
			{Name: "email", Type: "VARCHAR(255)"},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	}
	kc, err := NewKeyCodec(def)
	if err != nil {
		t.Fatalf("NewKeyCodec: %v", err)
	}
	row := sqle.Row{uint64(99), "a@example.com"}
	key, err := kc.Encode(row)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	vals, err := kc.Decode(key)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if vals[0] != uint64(99) {
		t.Fatalf("got %v, want 99", vals[0])
	}
}

// ---------------------------------------------------------------------------
// Composite keys and prefix scans
// ---------------------------------------------------------------------------

func TestKeyCodec_CompositeKey(t *testing.T) {
	def := &service.TableDef{
		Name: "orders",
		Columns: []service.ColumnDef{
			{Name: "tenant_id", Type: "BIGINT UNSIGNED"},
			{Name: "order_id", Type: "BIGINT UNSIGNED"},
			{Name: "note", Type: "VARCHAR(50)"},
		},
		PrimaryKey: []service.PKColumn{{Name: "tenant_id"}, {Name: "order_id"}},
	}
	kc, err := NewKeyCodec(def)
	if err != nil {
		t.Fatalf("NewKeyCodec: %v", err)
	}

	row := sqle.Row{uint64(1), uint64(500), "hello"}
	key, err := kc.Encode(row)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	vals, err := kc.Decode(key)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(vals) != 2 || vals[0] != uint64(1) || vals[1] != uint64(500) {
		t.Fatalf("unexpected decoded values: %#v", vals)
	}

	// A composite key's encoding for a shared leading component is a
	// prefix of every key sharing that component. This is what makes
	// prefix scans over the leading column(s) of a composite key work.
	prefixKey, err := kc.EncodeValues([]any{uint64(1), uint64(0)})
	if err != nil {
		t.Fatalf("EncodeValues: %v", err)
	}
	otherKey, err := kc.EncodeValues([]any{uint64(1), uint64(999)})
	if err != nil {
		t.Fatalf("EncodeValues: %v", err)
	}
	if len(prefixKey) == 0 || len(otherKey) == 0 {
		t.Fatal("unexpected empty key")
	}
	// Both keys must share the encoded tenant_id=1 component as a common
	// prefix.
	tenantOnlyKey, err := singleUint64Prefix(kc, uint64(1))
	if err != nil {
		t.Fatalf("singleUint64Prefix: %v", err)
	}
	if len(tenantOnlyKey) > len(prefixKey) || prefixKey[:len(tenantOnlyKey)] != tenantOnlyKey {
		t.Fatalf("prefixKey does not share the tenant_id prefix")
	}
	if len(tenantOnlyKey) > len(otherKey) || otherKey[:len(tenantOnlyKey)] != tenantOnlyKey {
		t.Fatalf("otherKey does not share the tenant_id prefix")
	}
}

// singleUint64Prefix encodes just the first 8 bytes of a BIGINT UNSIGNED
// component the way KeyCodec would, for prefix-sharing assertions.
func singleUint64Prefix(kc *KeyCodec, v uint64) (string, error) {
	return string(beUint64(v)), nil
}

func TestKeyCodec_PrefixLengthTruncation(t *testing.T) {
	def := &service.TableDef{
		Name:       "t",
		Columns:    []service.ColumnDef{{Name: "name", Type: "VARCHAR(100)"}},
		PrimaryKey: []service.PKColumn{{Name: "name", PrefixLength: 3}},
	}
	kc, err := NewKeyCodec(def)
	if err != nil {
		t.Fatalf("NewKeyCodec: %v", err)
	}

	kAbc, err := kc.EncodeValues([]any{"abcdef"})
	if err != nil {
		t.Fatalf("EncodeValues: %v", err)
	}
	kAbcxyz, err := kc.EncodeValues([]any{"abcxyz"})
	if err != nil {
		t.Fatalf("EncodeValues: %v", err)
	}
	if kAbc != kAbcxyz {
		t.Fatalf("expected keys with the same 3-byte prefix to collide, got %q and %q", kAbc, kAbcxyz)
	}

	vals, err := kc.Decode(kAbc)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if vals[0] != "abc" {
		t.Fatalf("expected truncated value 'abc', got %v", vals[0])
	}
}

func TestNewKeyCodec_PrefixLengthOnNonStringRejected(t *testing.T) {
	def := &service.TableDef{
		Name:       "t",
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}},
		PrimaryKey: []service.PKColumn{{Name: "id", PrefixLength: 4}},
	}
	_, err := NewKeyCodec(def)
	var prefixErr *PrefixLengthNotSupportedError
	if !errors.As(err, &prefixErr) {
		t.Fatalf("expected *PrefixLengthNotSupportedError, got %T (%v)", err, err)
	}
}

// ---------------------------------------------------------------------------
// Escape sequences around 0x00
// ---------------------------------------------------------------------------

func TestKeyCodec_EscapesNulBytesInStrings(t *testing.T) {
	kc := singleColumnKeyCodec(t, "VARCHAR(20)")
	values := []string{
		"a\x00b",
		"\x00\x00\x00",
		"trailing\x00",
		"\x00leading",
		"",
	}
	for _, v := range values {
		key, err := kc.EncodeValues([]any{v})
		if err != nil {
			t.Fatalf("EncodeValues(%q): %v", v, err)
		}
		got, err := kc.Decode(key)
		if err != nil {
			t.Fatalf("Decode(%q): %v", v, err)
		}
		if got[0] != v {
			t.Fatalf("round trip mismatch: got %q, want %q", got[0], v)
		}
	}

	// Order preservation must survive escaping: "a" < "a\x00" < "ab".
	assertOrderPreserving(t, kc, []any{"ab", "a\x00", "a"}, func(a, b any) bool {
		return a.(string) < b.(string)
	})
}

// ---------------------------------------------------------------------------
// Synthetic row id
// ---------------------------------------------------------------------------

func TestKeyCodec_SyntheticRowID(t *testing.T) {
	def := &service.TableDef{
		Name:    "log",
		Columns: []service.ColumnDef{{Name: "message", Type: "TEXT"}},
	}
	kc, err := NewKeyCodec(def)
	if err != nil {
		t.Fatalf("NewKeyCodec: %v", err)
	}

	key, err := kc.EncodeValues([]any{uint64(7)})
	if err != nil {
		t.Fatalf("EncodeValues: %v", err)
	}
	vals, err := kc.Decode(key)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if vals[0] != uint64(7) {
		t.Fatalf("got %v, want 7", vals[0])
	}

	// Encode(row) reads the row id from the trailing element, after all
	// declared columns.
	row := sqle.Row{"hello", uint64(7)}
	rowKey, err := kc.Encode(row)
	if err != nil {
		t.Fatalf("Encode(row): %v", err)
	}
	if rowKey != key {
		t.Fatalf("Encode(row) and EncodeValues disagree: %q vs %q", rowKey, key)
	}

	assertOrderPreserving(t, kc, []any{uint64(3), uint64(1), uint64(2)}, func(a, b any) bool {
		return a.(uint64) < b.(uint64)
	})
}

func TestKeyCodec_Encode_MissingRowID(t *testing.T) {
	def := &service.TableDef{
		Name:    "log",
		Columns: []service.ColumnDef{{Name: "message", Type: "TEXT"}},
	}
	kc, err := NewKeyCodec(def)
	if err != nil {
		t.Fatalf("NewKeyCodec: %v", err)
	}
	_, err = kc.Encode(sqle.Row{})
	var missing *MissingRowIDError
	if !errors.As(err, &missing) {
		t.Fatalf("expected *MissingRowIDError, got %T (%v)", err, err)
	}
}

// TestKeyCodec_Encode_SQLVisibleRowIsMissingRowID pins that a row which stops
// at the last declared column -- which is every row the SQL-visible schema
// produces, since SchemaOf never exposes the row id -- is rejected as missing
// its row id rather than having its last column read as one.
//
// The last column's type decides how badly the un-guarded read fails, which
// is exactly why the guard is on the arity and not on the type: a trailing
// TEXT column fails with a confusing type mismatch, while a trailing BIGINT
// UNSIGNED column succeeds and silently promotes that column to the table's
// physical primary key, so two rows sharing its value collide as a duplicate
// key.
func TestKeyCodec_Encode_SQLVisibleRowIsMissingRowID(t *testing.T) {
	cases := []struct {
		name string
		cols []service.ColumnDef
		row  sqle.Row
	}{
		{
			name: "trailing string column",
			cols: []service.ColumnDef{{Name: "id", Type: "BIGINT"}, {Name: "name", Type: "VARCHAR(64)"}},
			row:  sqle.Row{int64(1), "a"},
		},
		{
			name: "trailing uint64 column would otherwise be hijacked as the key",
			cols: []service.ColumnDef{{Name: "name", Type: "VARCHAR(64)"}, {Name: "n", Type: "BIGINT UNSIGNED"}},
			row:  sqle.Row{"a", uint64(7)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kc, err := NewKeyCodec(&service.TableDef{Name: "t", Columns: tc.cols})
			if err != nil {
				t.Fatalf("NewKeyCodec: %v", err)
			}
			_, err = kc.Encode(tc.row)
			var missing *MissingRowIDError
			if !errors.As(err, &missing) {
				t.Fatalf("Encode(%v) = %v (%T), want *MissingRowIDError", tc.row, err, err)
			}
			if missing.Table != "t" {
				t.Errorf("MissingRowIDError.Table = %q, want %q", missing.Table, "t")
			}
		})
	}
}

// TestKeyCodec_Encode_TrailingRowIDIsAccepted pins the other half of the
// arity rule: a row carrying its synthetic row id after every declared
// column still encodes, and encodes identically to EncodeValues.
func TestKeyCodec_Encode_TrailingRowIDIsAccepted(t *testing.T) {
	kc, err := NewKeyCodec(&service.TableDef{
		Name:    "t",
		Columns: []service.ColumnDef{{Name: "id", Type: "BIGINT"}, {Name: "name", Type: "VARCHAR(64)"}},
	})
	if err != nil {
		t.Fatalf("NewKeyCodec: %v", err)
	}
	fromRow, err := kc.Encode(sqle.Row{int64(1), "a", uint64(42)})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	fromValues, err := kc.EncodeValues([]any{uint64(42)})
	if err != nil {
		t.Fatalf("EncodeValues: %v", err)
	}
	if fromRow != fromValues {
		t.Fatalf("Encode(row) = %q, EncodeValues = %q; want identical", fromRow, fromValues)
	}
}

// ---------------------------------------------------------------------------
// Constructor errors
// ---------------------------------------------------------------------------

func TestNewKeyCodec_NilDef(t *testing.T) {
	_, err := NewKeyCodec(nil)
	var nilErr *NilTableDefError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected *NilTableDefError, got %T (%v)", err, err)
	}
}

func TestNewKeyCodec_UnknownPrimaryKeyColumn(t *testing.T) {
	def := &service.TableDef{
		Name:       "t",
		Columns:    []service.ColumnDef{{Name: "a", Type: "INT"}},
		PrimaryKey: []service.PKColumn{{Name: "missing"}},
	}
	_, err := NewKeyCodec(def)
	var notFound *ColumnNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *ColumnNotFoundError, got %T (%v)", err, err)
	}
}

func TestNewKeyCodec_DecimalPrimaryKeyRejected(t *testing.T) {
	def := &service.TableDef{
		Name:       "t",
		Columns:    []service.ColumnDef{{Name: "amount", Type: "DECIMAL(10,2)"}},
		PrimaryKey: []service.PKColumn{{Name: "amount"}},
	}
	_, err := NewKeyCodec(def)
	var unsupported *UnsupportedKeyColumnTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected *UnsupportedKeyColumnTypeError, got %T (%v)", err, err)
	}
}

// TestNewKeyCodec_DecimalIndexColumnRejected covers the secondary-index half
// of the same rule. pkg/sql/provider encodes an index column's values by
// building a synthetic single-column primary key over that column and reusing
// this codec (see indexValueKeyCodec), so an indexed DECIMAL reaches exactly
// the construction below. Pinning it here is what shows the key path never
// has to name a Go decimal type at all: the rejection happens at DDL time
// from the column's SQL type, so no decimal value ever reaches
// encodeKeyComponent to be type-asserted.
func TestNewKeyCodec_DecimalIndexColumnRejected(t *testing.T) {
	def := &service.TableDef{
		Name: "t",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "INT"},
			{Name: "amount", Type: "DECIMAL(20,4)"},
		},
		// The shape indexValueKeyCodec builds for an index on "amount".
		PrimaryKey: []service.PKColumn{{Name: "amount"}},
	}
	_, err := NewKeyCodec(def)
	var unsupported *UnsupportedKeyColumnTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected *UnsupportedKeyColumnTypeError, got %T (%v)", err, err)
	}
	if unsupported.Column != "amount" {
		t.Fatalf("error names column %q, want %q", unsupported.Column, "amount")
	}
}

// TestKeyCodec_Uint64AboveInt64Max asserts a BIGINT UNSIGNED key component
// keeps its exact value and its order across the int64 boundary, where a
// narrowing conversion would both corrupt the value and invert the ordering
// the encoding exists to preserve.
func TestKeyCodec_Uint64AboveInt64Max(t *testing.T) {
	kc := singleColumnKeyCodec(t, "BIGINT UNSIGNED")

	values := []uint64{0, 1, math.MaxInt64, math.MaxInt64 + 1, math.MaxUint64}

	var prev string
	for i, v := range values {
		key, err := kc.EncodeValues([]any{v})
		if err != nil {
			t.Fatalf("EncodeValues(%d): %v", v, err)
		}
		if i > 0 && key <= prev {
			t.Fatalf("key for %d does not sort above the previous value", v)
		}
		prev = key

		got, err := kc.Decode(key)
		if err != nil {
			t.Fatalf("Decode(%d): %v", v, err)
		}
		if got[0] != any(v) {
			t.Fatalf("Decode round trip = %v (%T), want %d (uint64)", got[0], got[0], v)
		}
	}
}

func TestNewKeyCodec_JSONPrimaryKeyRejected(t *testing.T) {
	def := &service.TableDef{
		Name:       "t",
		Columns:    []service.ColumnDef{{Name: "doc", Type: "JSON"}},
		PrimaryKey: []service.PKColumn{{Name: "doc"}},
	}
	_, err := NewKeyCodec(def)
	var unsupported *UnsupportedKeyColumnTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected *UnsupportedKeyColumnTypeError, got %T (%v)", err, err)
	}
}

// ---------------------------------------------------------------------------
// Value and count mismatches
// ---------------------------------------------------------------------------

func TestKeyCodec_EncodeValues_CountMismatch(t *testing.T) {
	kc := singleColumnKeyCodec(t, "INT")
	_, err := kc.EncodeValues([]any{int32(1), int32(2)})
	var mismatch *KeyValueCountMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *KeyValueCountMismatchError, got %T (%v)", err, err)
	}
}

func TestKeyCodec_EncodeValues_NullComponent(t *testing.T) {
	kc := singleColumnKeyCodec(t, "INT")
	_, err := kc.EncodeValues([]any{nil})
	var nullErr *NullKeyComponentError
	if !errors.As(err, &nullErr) {
		t.Fatalf("expected *NullKeyComponentError, got %T (%v)", err, err)
	}
}

func TestKeyCodec_EncodeValues_TypeMismatch(t *testing.T) {
	kc := singleColumnKeyCodec(t, "INT")
	_, err := kc.EncodeValues([]any{"not an int32"})
	var mismatch *ValueTypeMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *ValueTypeMismatchError, got %T (%v)", err, err)
	}
}

func TestKeyCodec_Encode_RowColumnCountMismatch(t *testing.T) {
	def := &service.TableDef{
		Name:       "t",
		Columns:    []service.ColumnDef{{Name: "a", Type: "INT"}, {Name: "b", Type: "INT"}},
		PrimaryKey: []service.PKColumn{{Name: "b"}},
	}
	kc, err := NewKeyCodec(def)
	if err != nil {
		t.Fatalf("NewKeyCodec: %v", err)
	}
	_, err = kc.Encode(sqle.Row{int32(1)})
	var mismatch *RowColumnCountMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *RowColumnCountMismatchError, got %T (%v)", err, err)
	}
}

// ---------------------------------------------------------------------------
// Float ordering sanity: reflect.DeepEqual usage above requires bit-exact
// round trips.
// ---------------------------------------------------------------------------

func TestFloatOrderedBits_Reversible(t *testing.T) {
	prop := func(f float64) bool {
		return floatOrderedBitsToFloat(floatOrderedBits(f)) == f ||
			(f != f) // NaN != NaN; skip
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSignedOrderedBits_Reversible(t *testing.T) {
	prop := func(v int64) bool {
		return signedFromOrderedBits(signedOrderedBits(v)) == v
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

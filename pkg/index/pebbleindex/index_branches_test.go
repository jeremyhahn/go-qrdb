// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pebbleindex

import (
	"encoding/binary"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// ---------------------------------------------------------------------------
// extractEntityKey / parseKey error branches
// ---------------------------------------------------------------------------

// TestExtractEntityKey_LengthLargerThanKey verifies that extractEntityKey
// returns KeyTooShortError when the encoded keyLen says more bytes exist than
// the key actually contains.
func TestExtractEntityKey_LengthLargerThanKey(t *testing.T) {
	// Build a key whose last 4 bytes claim keyLen=100 but there aren't 100
	// bytes before the length suffix.
	key := make([]byte, 8)                   // total 8 bytes
	binary.BigEndian.PutUint32(key[4:], 100) // claim entity key is 100 bytes
	_, err := extractEntityKey(key)
	if err == nil {
		t.Fatal("expected KeyTooShortError, got nil")
	}
	if _, ok := err.(*index.KeyTooShortError); !ok {
		t.Fatalf("expected *index.KeyTooShortError, got %T: %v", err, err)
	}
}

// TestParseKey_PrefixMismatch verifies that parseKey returns KeyPrefixMismatchError
// when the key does not begin with the expected index prefix.
func TestParseKey_PrefixMismatch(t *testing.T) {
	idx, _ := newPebbleIdx(t, "myindex", false)
	// Feed a key that starts with wrong bytes.
	badKey := []byte("WRONGPREFIX:myindex:value:entitykey\x00\x00\x00\x08")
	_, _, err := idx.parseKey(badKey)
	if err == nil {
		t.Fatal("expected KeyPrefixMismatchError, got nil")
	}
	if _, ok := err.(*index.KeyPrefixMismatchError); !ok {
		t.Fatalf("expected *index.KeyPrefixMismatchError, got %T: %v", err, err)
	}
}

// TestParseKey_KeyTooShortAfterPrefix verifies that parseKey returns
// KeyTooShortError when the portion after the index prefix is too short.
func TestParseKey_KeyTooShortAfterPrefix(t *testing.T) {
	idx, _ := newPebbleIdx(t, "myindex", false)

	// Build a key that has the correct prefix but is too short afterwards.
	prefix := idx.buildIndexPrefix()
	// Append only 3 bytes after the prefix — not enough for separator + 4-byte length.
	shortKey := append(prefix, 'a', 'b', 'c')
	_, _, err := idx.parseKey(shortKey)
	if err == nil {
		t.Fatal("expected KeyTooShortError from parseKey, got nil")
	}
}

// TestParseKey_SepIdxNegative verifies the sepIdx < 0 branch in parseKey.
// This occurs when the entity key length consumes all bytes after the prefix
// with no room left for a field-value separator.
func TestParseKey_SepIdxNegative(t *testing.T) {
	idx, _ := newPebbleIdx(t, "myindex", false)
	prefix := idx.buildIndexPrefix()

	// Craft rest = entityKey(5 bytes) + length(4) where ekStart == 0, so
	// sepIdx = ekStart - len(indexkey.Separator) < 0.
	entityKeyBytes := []byte("hello")
	ekLen := uint32(len(entityKeyBytes))

	rest := make([]byte, int(ekLen)+entityKeyLenSize)
	copy(rest[:ekLen], entityKeyBytes)
	binary.BigEndian.PutUint32(rest[ekLen:], ekLen)

	key := append(prefix, rest...)
	_, _, err := idx.parseKey(key)
	if err == nil {
		t.Fatal("expected error from parseKey with no room for separator, got nil")
	}
}

// ---------------------------------------------------------------------------
// Closed-index and unique-idempotent paths
// ---------------------------------------------------------------------------

// TestIndex_Put_Closed verifies Put returns ClosedError after Close.
func TestIndex_Put_Closed(t *testing.T) {
	idx, _ := newPebbleIdx(t, "closed-put", false)
	_ = idx.Close()
	err := idx.Put([]byte("ek"), []byte("fv"))
	if _, ok := err.(*index.ClosedError); !ok {
		t.Fatalf("expected ClosedError after Close, got %T: %v", err, err)
	}
}

// TestIndex_Put_UniqueIdempotent verifies that putting the same
// entity key for the same field value on a unique index is a no-op.
func TestIndex_Put_UniqueIdempotent(t *testing.T) {
	idx, _ := newPebbleIdx(t, "unique-idem", true)
	ek := []byte("entity1")
	fv := []byte("fieldvalue")

	if err := idx.Put(ek, fv); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	// Second put with same entity key should succeed (idempotent).
	if err := idx.Put(ek, fv); err != nil {
		t.Fatalf("idempotent Put should succeed, got: %v", err)
	}
}

// TestIndex_Get_Closed verifies Get returns ClosedError after Close.
func TestIndex_Get_Closed(t *testing.T) {
	idx, _ := newPebbleIdx(t, "closed-get", false)
	_ = idx.Close()
	_, err := idx.Get([]byte("fv"))
	if _, ok := err.(*index.ClosedError); !ok {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// TestIndex_Get_Empty verifies Get returns empty slice for missing key.
func TestIndex_Get_Empty(t *testing.T) {
	idx, _ := newPebbleIdx(t, "get-empty", false)
	keys, err := idx.Get([]byte("nonexistent"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected empty slice, got %d keys", len(keys))
	}
}

// TestIndex_Scan_Closed verifies Scan returns ClosedError after Close.
func TestIndex_Scan_Closed(t *testing.T) {
	idx, _ := newPebbleIdx(t, "closed-scan", false)
	_ = idx.Close()
	_, err := idx.Scan([]byte("a"), []byte("z"))
	if _, ok := err.(*index.ClosedError); !ok {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// TestIndex_GetAll_Closed verifies GetAll returns ClosedError after Close.
func TestIndex_GetAll_Closed(t *testing.T) {
	idx, _ := newPebbleIdx(t, "closed-getall", false)
	_ = idx.Close()
	_, err := idx.GetAll()
	if _, ok := err.(*index.ClosedError); !ok {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// TestIndex_Clear_Closed verifies Clear returns ClosedError after Close.
func TestIndex_Clear_Closed(t *testing.T) {
	idx, _ := newPebbleIdx(t, "closed-clear", false)
	_ = idx.Close()
	err := idx.Clear()
	if _, ok := err.(*index.ClosedError); !ok {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Corrupt stored keys
// ---------------------------------------------------------------------------

// setCorruptKey writes, under fieldValue's prefix, a key whose length suffix
// claims more entity-key bytes than the key holds.
func setCorruptKey(t *testing.T, idx *Index, db *pebble.DB, fieldValue []byte) {
	t.Helper()
	key := append(idx.buildValuePrefix(fieldValue), 'x', 0, 0, 0, 200)
	if err := db.Set(key, nil, pebble.Sync); err != nil {
		t.Fatalf("Set: %v", err)
	}
}

// TestIndex_CorruptKey_ReadsFail verifies every read path surfaces a stored
// key it cannot parse as an error rather than skipping it.
func TestIndex_CorruptKey_ReadsFail(t *testing.T) {
	idx, db := newPebbleIdx(t, "corrupt", true)
	setCorruptKey(t, idx, db, []byte("fv"))

	if _, err := idx.Get([]byte("fv")); err == nil {
		t.Error("Get over a corrupt key: expected error, got nil")
	}
	if err := idx.Put([]byte("ek"), []byte("fv")); err == nil {
		t.Error("unique Put over a corrupt key: expected error, got nil")
	}
	if _, err := idx.GetAll(); err == nil {
		t.Error("GetAll over a corrupt key: expected error, got nil")
	}
	if _, err := idx.Scan(nil, nil); err == nil {
		t.Error("Scan over a corrupt key: expected error, got nil")
	}
	// Clear does not parse keys, so it removes the corrupt one.
	if err := idx.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if keys, err := idx.GetAll(); err != nil || len(keys) != 0 {
		t.Errorf("GetAll after Clear = %d keys, %v; want 0, nil", len(keys), err)
	}
}

// TestIndex_Numeric_UndecodableValueScansRaw verifies a stored numeric value
// that does not decode is reported verbatim rather than dropped.
func TestIndex_Numeric_UndecodableValueScansRaw(t *testing.T) {
	db := openPebble(t)
	idx, err := New(db, "n", false, index.IndexValueTypeNumeric)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := db.Set(idx.buildKey([]byte("abc"), []byte("ek")), nil, pebble.Sync); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := idx.Scan(nil, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if keys := got["abc"]; len(keys) != 1 || string(keys[0]) != "ek" {
		t.Errorf("Scan = %v; want abc -> [ek]", got)
	}
}

// TestIndex_Numeric_DeleteNonNumericIsNoop verifies Delete of a value a
// numeric index could never have stored succeeds without touching the index.
func TestIndex_Numeric_DeleteNonNumericIsNoop(t *testing.T) {
	idx, err := New(openPebble(t), "n", false, index.IndexValueTypeNumeric)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := idx.Put([]byte("ek"), []byte("5")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := idx.Delete([]byte("ek"), []byte("five")); err != nil {
		t.Fatalf("Delete(non-numeric): %v", err)
	}
	if keys, err := idx.Get([]byte("5")); err != nil || len(keys) != 1 {
		t.Errorf("Get(5) after a non-numeric Delete = %d keys, %v; want 1, nil", len(keys), err)
	}
}

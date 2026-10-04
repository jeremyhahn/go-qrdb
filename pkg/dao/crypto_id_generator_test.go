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

package dao

import (
	"errors"
	"testing"
)

type naturalKeyEntity struct {
	ID    uint64
	Email string
}

func (e *naturalKeyEntity) EntityID() uint64      { return e.ID }
func (e *naturalKeyEntity) SetEntityID(id uint64) { e.ID = id }

func TestCryptoFieldHashGenerator_IsDeterministic(t *testing.T) {
	gen := NewCryptoFieldHashGenerator("Email")
	e := &naturalKeyEntity{Email: "alice@example.com"}

	first := gen.NextID(e)
	second := gen.NextID(e)
	if first == 0 {
		t.Fatal("NextID returned 0 for a present field")
	}
	if first != second {
		t.Fatalf("NextID(%v) = %d, then %d; want deterministic", e, first, second)
	}
}

func TestCryptoFieldHashGenerator_DiffersFromFNV1a(t *testing.T) {
	email := "alice@example.com"
	cryptoID := NewCryptoFieldHashGenerator("Email").NextID(&naturalKeyEntity{Email: email})
	fnvID := NewFieldHashGenerator("Email").NextID(&naturalKeyEntity{Email: email})
	if cryptoID == fnvID {
		t.Fatalf("CryptoFieldHashGenerator and FieldHashGenerator produced the same ID (%d) for %q; "+
			"they must use different hash functions", cryptoID, email)
	}
}

func TestCryptoFieldHashGenerator_DistinctInputsDistinctIDs(t *testing.T) {
	gen := NewCryptoFieldHashGenerator("Email")
	a := gen.NextID(&naturalKeyEntity{Email: "alice@example.com"})
	b := gen.NextID(&naturalKeyEntity{Email: "bob@example.com"})
	if a == b {
		t.Fatalf("NextID collided for two different emails: both %d", a)
	}
}

func TestCryptoFieldHashGenerator_MissingFieldReturnsZero(t *testing.T) {
	gen := NewCryptoFieldHashGenerator("DoesNotExist")
	if id := gen.NextID(&naturalKeyEntity{Email: "x"}); id != 0 {
		t.Fatalf("NextID with a missing field = %d, want 0", id)
	}
}

func TestCryptoFieldHashGenerator_FieldName(t *testing.T) {
	gen := NewCryptoFieldHashGenerator("Email")
	if gen.FieldName() != "Email" {
		t.Fatalf("FieldName() = %q, want %q", gen.FieldName(), "Email")
	}
}

func TestCryptoDynamicFieldHashGenerator_IsDeterministicAndDiffers(t *testing.T) {
	gen := NewCryptoDynamicFieldHashGenerator("email")
	e := &DynamicEntity{Fields: map[string]interface{}{"email": "alice@example.com"}}

	first := gen.NextID(e)
	second := gen.NextID(e)
	if first == 0 || first != second {
		t.Fatalf("NextID = %d, then %d; want the same non-zero value", first, second)
	}

	fnvGen := NewDynamicFieldHashGenerator("email")
	if fnvGen.NextID(e) == first {
		t.Fatal("CryptoDynamicFieldHashGenerator and DynamicFieldHashGenerator produced the same ID")
	}

	if gen.FieldName() != "email" {
		t.Fatalf("FieldName() = %q, want %q", gen.FieldName(), "email")
	}
}

func TestCryptoDynamicFieldHashGenerator_NonDynamicEntityReturnsZero(t *testing.T) {
	gen := NewCryptoDynamicFieldHashGenerator("email")
	if id := gen.NextID(&naturalKeyEntity{Email: "x"}); id != 0 {
		t.Fatalf("NextID on a non-DynamicEntity = %d, want 0", id)
	}
}

func TestCryptoDynamicFieldHashGenerator_NilFieldsReturnsZero(t *testing.T) {
	gen := NewCryptoDynamicFieldHashGenerator("email")
	if id := gen.NextID(&DynamicEntity{}); id != 0 {
		t.Fatalf("NextID with nil Fields = %d, want 0", id)
	}
}

func TestCryptoDynamicFieldHashGenerator_MissingFieldReturnsZero(t *testing.T) {
	gen := NewCryptoDynamicFieldHashGenerator("email")
	e := &DynamicEntity{Fields: map[string]interface{}{"other": "x"}}
	if id := gen.NextID(e); id != 0 {
		t.Fatalf("NextID with a missing field = %d, want 0", id)
	}
}

func TestVerifyNaturalKey_MatchIsNil(t *testing.T) {
	e := &naturalKeyEntity{ID: 1, Email: "alice@example.com"}
	if err := VerifyNaturalKey(e, "users", "Email", "alice@example.com"); err != nil {
		t.Fatalf("VerifyNaturalKey (match) = %v, want nil", err)
	}
}

// TestVerifyNaturalKey_MismatchIsTheCollisionSignal is the whole point: a
// caller that looked an entity up by a hash-derived ID and got back a
// DIFFERENT natural key learns about it here rather than silently treating
// the wrong record as its own (go-qrdb#589).
func TestVerifyNaturalKey_MismatchIsTheCollisionSignal(t *testing.T) {
	e := &naturalKeyEntity{ID: 1, Email: "collided-with@example.com"}
	err := VerifyNaturalKey(e, "users", "Email", "alice@example.com")

	var mismatch NaturalKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("VerifyNaturalKey (mismatch) = %v, want NaturalKeyMismatchError", err)
	}
	if mismatch.Expected != "alice@example.com" || mismatch.Actual != "collided-with@example.com" {
		t.Fatalf("mismatch = %+v, want Expected=alice@example.com Actual=collided-with@example.com", mismatch)
	}
	if mismatch.EntityType != "users" || mismatch.FieldName != "Email" {
		t.Fatalf("mismatch = %+v, want EntityType=users FieldName=Email", mismatch)
	}
	if mismatch.Error() == "" {
		t.Fatal("Error() returned an empty string")
	}
}

func TestVerifyNaturalKey_DynamicEntityMatch(t *testing.T) {
	e := &DynamicEntity{Fields: map[string]interface{}{"email": "alice@example.com"}}
	if err := VerifyNaturalKey(e, "users", "email", "alice@example.com"); err != nil {
		t.Fatalf("VerifyNaturalKey (DynamicEntity match) = %v, want nil", err)
	}
}

func TestVerifyNaturalKey_DynamicEntityMismatch(t *testing.T) {
	e := &DynamicEntity{Fields: map[string]interface{}{"email": "collided-with@example.com"}}
	var mismatch NaturalKeyMismatchError
	if err := VerifyNaturalKey(e, "users", "email", "alice@example.com"); !errors.As(err, &mismatch) {
		t.Fatalf("VerifyNaturalKey (DynamicEntity mismatch) = %v, want NaturalKeyMismatchError", err)
	}
}

func TestVerifyNaturalKey_DynamicEntityNilFieldsIsNil(t *testing.T) {
	e := &DynamicEntity{}
	if err := VerifyNaturalKey(e, "users", "email", "alice@example.com"); err != nil {
		t.Fatalf("VerifyNaturalKey (nil Fields) = %v, want nil (nothing to verify)", err)
	}
}

func TestVerifyNaturalKey_DynamicEntityMissingFieldIsNil(t *testing.T) {
	e := &DynamicEntity{Fields: map[string]interface{}{"other": "x"}}
	if err := VerifyNaturalKey(e, "users", "email", "alice@example.com"); err != nil {
		t.Fatalf("VerifyNaturalKey (missing field) = %v, want nil (nothing to verify)", err)
	}
}

func TestVerifyNaturalKey_MissingStructFieldIsNil(t *testing.T) {
	e := &naturalKeyEntity{Email: "alice@example.com"}
	if err := VerifyNaturalKey(e, "users", "DoesNotExist", "anything"); err != nil {
		t.Fatalf("VerifyNaturalKey (missing struct field) = %v, want nil (nothing to verify)", err)
	}
}

// TestVerifyNaturalKey_NilPointerEntityIsNil verifies a nil *naturalKeyEntity
// -- reflect.Value.Elem() on a nil pointer yields the zero Value, not a
// struct -- is treated as "nothing to verify" rather than panicking.
func TestVerifyNaturalKey_NilPointerEntityIsNil(t *testing.T) {
	var e Entity = (*naturalKeyEntity)(nil)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("VerifyNaturalKey panicked on a nil entity: %v", r)
		}
	}()
	if err := VerifyNaturalKey(e, "users", "Email", "anything"); err != nil {
		t.Fatalf("VerifyNaturalKey (nil entity) = %v, want nil", err)
	}
}

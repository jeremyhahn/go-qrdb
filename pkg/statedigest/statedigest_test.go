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

package statedigest

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
)

// The vectors below are the SHA-256 of the literal byte strings spelled out
// beside them, so a change to the encoding fails here rather than silently
// changing every replica's digest.

// emptyVector is sha256("qrdb-state-digest-v1" || 0x00).
func emptyVector() [sha256.Size]byte {
	return sha256.Sum256(append([]byte(Header), 0))
}

// twoRecordVector is the encoding of {"a": "1", "bc": ""} written out by hand.
func twoRecordVector() [sha256.Size]byte {
	enc := append([]byte(Header), 0)
	enc = append(enc, 0, 0, 0, 0, 0, 0, 0, 1, 'a')
	enc = append(enc, 0, 0, 0, 0, 0, 0, 0, 1, '1')
	enc = append(enc, 0, 0, 0, 0, 0, 0, 0, 2, 'b', 'c')
	enc = append(enc, 0, 0, 0, 0, 0, 0, 0, 0)
	return sha256.Sum256(enc)
}

func TestNewBuilder_EmptyMatchesHeaderVector(t *testing.T) {
	d := NewBuilder().Finish(0)
	if d.Sum != emptyVector() {
		t.Fatalf("empty digest = %x, want %x", d.Sum, emptyVector())
	}
	if d.Records != 0 {
		t.Fatalf("Records = %d, want 0", d.Records)
	}
}

func TestNewBuilder_IndependentInstances(t *testing.T) {
	a := NewBuilder()
	b := NewBuilder()
	if err := a.Add([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := b.Finish(0).Sum; got != emptyVector() {
		t.Fatalf("second builder saw the first builder's record: %x", got)
	}
}

func TestBuilderAdd_KnownVector(t *testing.T) {
	b := NewBuilder()
	if err := b.Add([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if err := b.Add([]byte("bc"), nil); err != nil {
		t.Fatalf("Add bc: %v", err)
	}
	d := b.Finish(42)
	if d.Sum != twoRecordVector() {
		t.Fatalf("digest = %x, want %x", d.Sum, twoRecordVector())
	}
	if d.Records != 2 || d.AppliedIndex != 42 {
		t.Fatalf("Records/AppliedIndex = %d/%d, want 2/42", d.Records, d.AppliedIndex)
	}
}

func TestBuilderAdd_LengthPrefixSeparatesFields(t *testing.T) {
	// Without length prefixes {"ab": "c"} and {"a": "bc"} would hash the
	// same bytes.
	x := NewBuilder()
	if err := x.Add([]byte("ab"), []byte("c")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	y := NewBuilder()
	if err := y.Add([]byte("a"), []byte("bc")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if x.Finish(0).Sum == y.Finish(0).Sum {
		t.Fatal("distinct records produced the same digest")
	}
}

func TestBuilderAdd_RejectsDescendingKey(t *testing.T) {
	b := NewBuilder()
	if err := b.Add([]byte("b"), []byte("1")); err != nil {
		t.Fatalf("Add b: %v", err)
	}
	err := b.Add([]byte("a"), []byte("secret"))
	var koe *KeyOrderError
	if !errors.As(err, &koe) {
		t.Fatalf("Add a: err = %v, want *KeyOrderError", err)
	}
	if koe.Position != 1 {
		t.Fatalf("Position = %d, want 1", koe.Position)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), `"a"`) {
		t.Fatalf("error text leaks record content: %q", err.Error())
	}
}

func TestBuilderAdd_RejectsDuplicateKeyAndStaysUnchanged(t *testing.T) {
	b := NewBuilder()
	if err := b.Add([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	var koe *KeyOrderError
	if err := b.Add([]byte("a"), []byte("2")); !errors.As(err, &koe) {
		t.Fatalf("duplicate key: err = %v, want *KeyOrderError", err)
	}
	if err := b.Add([]byte("bc"), nil); err != nil {
		t.Fatalf("Add after rejection: %v", err)
	}
	// The rejected record left no trace, so this is the same two-record digest
	// as the known vector except for the value of "a".
	ref := NewBuilder()
	_ = ref.Add([]byte("a"), []byte("1"))
	_ = ref.Add([]byte("bc"), nil)
	if b.Finish(0).Sum != ref.Finish(0).Sum {
		t.Fatal("rejected record changed the digest")
	}
}

func TestBuilderAdd_EmptyKeyFirst(t *testing.T) {
	b := NewBuilder()
	if err := b.Add(nil, []byte("v")); err != nil {
		t.Fatalf("empty first key: %v", err)
	}
	var koe *KeyOrderError
	if err := b.Add([]byte{}, nil); !errors.As(err, &koe) {
		t.Fatalf("second empty key: err = %v, want *KeyOrderError", err)
	}
}

func TestBuilderAdd_DoesNotRetainCallerBuffer(t *testing.T) {
	b := NewBuilder()
	key := []byte("m")
	if err := b.Add(key, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	key[0] = 'a' // a backend reusing its iterator buffer
	if err := b.Add([]byte("n"), nil); err != nil {
		t.Fatalf("Add after caller reused its buffer: %v", err)
	}
}

func TestBuilderFinish_AppliedIndexNotHashed(t *testing.T) {
	a := NewBuilder().Finish(1)
	b := NewBuilder().Finish(99)
	if a.Sum != b.Sum {
		t.Fatal("applied index changed the sum")
	}
	if a.AppliedIndex != 1 || b.AppliedIndex != 99 {
		t.Fatalf("AppliedIndex = %d/%d, want 1/99", a.AppliedIndex, b.AppliedIndex)
	}
}

func TestBuilderFinish_ValueChangesSum(t *testing.T) {
	x := NewBuilder()
	_ = x.Add([]byte("k"), []byte("v1"))
	y := NewBuilder()
	_ = y.Add([]byte("k"), []byte("v2"))
	if x.Finish(0).Sum == y.Finish(0).Sum {
		t.Fatal("a changed value did not change the sum")
	}
}

func TestDigestHex_EmptyDigestLiteral(t *testing.T) {
	// sha256("qrdb-state-digest-v1\x00"), computed outside Go.
	const want = "bf493fb5a08b61e017a6eb771e4ed6df171271bc3ab6a3a23cffa7346e8031ba"
	if got := NewBuilder().Finish(0).Hex(); got != want {
		t.Fatalf("Hex = %s, want %s", got, want)
	}
}

func TestKeyOrderError_Error(t *testing.T) {
	e := &KeyOrderError{Position: 7}
	if got := e.Error(); got != "statedigest: record 7 is not in strictly ascending key order" {
		t.Fatalf("Error() = %q", got)
	}
	var err error = e
	var koe *KeyOrderError
	if !errors.As(err, &koe) || koe.Position != 7 {
		t.Fatal("errors.As did not recover *KeyOrderError")
	}
}

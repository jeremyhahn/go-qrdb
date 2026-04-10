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

package storage

import (
	"testing"
)

func TestIteratorOptions_Defaults(t *testing.T) {
	opts := &IteratorOptions{}

	if opts.LowerBound != nil {
		t.Fatalf("LowerBound: got %v, want nil", opts.LowerBound)
	}
	if opts.UpperBound != nil {
		t.Fatalf("UpperBound: got %v, want nil", opts.UpperBound)
	}
	if opts.Reverse {
		t.Fatal("Reverse: got true, want false")
	}
	if opts.OnlyKeys {
		t.Fatal("OnlyKeys: got true, want false")
	}
}

func TestIteratorOptions_WithBounds(t *testing.T) {
	lower := []byte("aaa")
	upper := []byte("zzz")

	opts := &IteratorOptions{
		LowerBound: lower,
		UpperBound: upper,
		Reverse:    true,
		OnlyKeys:   true,
	}

	if string(opts.LowerBound) != "aaa" {
		t.Fatalf("LowerBound: got %q, want %q", opts.LowerBound, "aaa")
	}
	if string(opts.UpperBound) != "zzz" {
		t.Fatalf("UpperBound: got %q, want %q", opts.UpperBound, "zzz")
	}
	if !opts.Reverse {
		t.Fatal("Reverse: got false, want true")
	}
	if !opts.OnlyKeys {
		t.Fatal("OnlyKeys: got false, want true")
	}
}

func TestIteratorOptions_NilOptions(t *testing.T) {
	// Verify that a nil *IteratorOptions can be checked without panic.
	var opts *IteratorOptions
	if opts != nil {
		t.Fatal("expected nil IteratorOptions")
	}
}

func TestIteratorOptions_EmptyBounds(t *testing.T) {
	opts := &IteratorOptions{
		LowerBound: []byte{},
		UpperBound: []byte{},
	}

	if len(opts.LowerBound) != 0 {
		t.Fatalf("LowerBound: got len %d, want 0", len(opts.LowerBound))
	}
	if len(opts.UpperBound) != 0 {
		t.Fatalf("UpperBound: got len %d, want 0", len(opts.UpperBound))
	}
}

func TestIteratorOptions_BoundsIsolation(t *testing.T) {
	lower := []byte("abc")
	upper := []byte("xyz")

	opts := &IteratorOptions{
		LowerBound: lower,
		UpperBound: upper,
	}

	// Mutate the original slices.
	lower[0] = 'z'
	upper[0] = 'a'

	// IteratorOptions holds the same slice reference (no copy semantics
	// at the struct level), so callers must not mutate after assignment.
	if opts.LowerBound[0] != 'z' {
		t.Fatal("LowerBound should share the underlying array")
	}
	if opts.UpperBound[0] != 'a' {
		t.Fatal("UpperBound should share the underlying array")
	}
}

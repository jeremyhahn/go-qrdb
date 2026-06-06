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

package seal

import (
	"bytes"
	"context"
	"testing"
)

// TestStorageBarrier_NoneStrategy_PlaintextRoundTrip asserts that a
// StorageBarrier constructed with NoneStrategy stores bytes in the
// underlying backend WITHOUT encryption (Put then raw Get from the
// backend returns the input bytes verbatim) and that the barrier's own
// Get returns the same bytes.
//
// This is the end-to-end check that --barrier-strategy=none truly means
// "no encryption at rest", not "different encryption".
func TestStorageBarrier_NoneStrategy_PlaintextRoundTrip(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()
	cfg := BarrierConfig{RootKeyPath: "root-key"}

	b, err := NewStorageBarrier(testLogger(), backend, cfg, NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	defer func() { _ = b.Close() }()

	// Initialize with empty credentials — NoneStrategy ignores them.
	if err := b.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	key := "kv/example/plaintext"
	plaintext := []byte("the quick brown fox jumps over the lazy dog")

	if err := b.Put(ctx, key, plaintext); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Under NoneStrategy the payload is stored in the clear — nothing is
	// encrypted at rest. The go-quicraft barrier prepends an 8-byte
	// sentinel-zero epoch header to every record so the no-encryption output
	// stays wire-format compatible with epoch-aware WAL/snapshot readers; the
	// payload after that header MUST be the verbatim plaintext (i.e. readable,
	// not ciphertext).
	const epochHeaderSize = 8
	rawCipher, err := backend.Get(ctx, key)
	if err != nil {
		t.Fatalf("raw backend Get: %v", err)
	}
	if len(rawCipher) != len(plaintext)+epochHeaderSize {
		t.Fatalf("raw length = %d, want %d (8-byte zero epoch prefix + plaintext)",
			len(rawCipher), len(plaintext)+epochHeaderSize)
	}
	for i := 0; i < epochHeaderSize; i++ {
		if rawCipher[i] != 0 {
			t.Fatalf("epoch header byte %d = %d, want 0 (sentinel)", i, rawCipher[i])
		}
	}
	if !bytes.Equal(rawCipher[epochHeaderSize:], plaintext) {
		t.Fatalf("raw payload differs from plaintext under NoneStrategy (data must be stored in the clear):\n  raw=%q\n  pt =%q",
			rawCipher[epochHeaderSize:], plaintext)
	}

	// Barrier Get must also return the same plaintext (passthrough decrypt).
	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("barrier Get: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("barrier Get returned %q, want %q", got, plaintext)
	}
}

// TestStorageBarrier_NoneStrategy_StrategyID asserts that the barrier
// reports StrategyNone in its status after initialization.
func TestStorageBarrier_NoneStrategy_StrategyID(t *testing.T) {
	ctx := context.Background()
	cfg := BarrierConfig{RootKeyPath: "root-key"}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	defer func() { _ = b.Close() }()

	if err := b.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	status := b.Status()
	if status.Sealed {
		t.Fatal("barrier should be unsealed after Initialize")
	}
	if status.Strategy != StrategyNone {
		t.Fatalf("status.Strategy = %q, want %q", status.Strategy, StrategyNone)
	}
}

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

package seal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"
)

// --- Helpers -----------------------------------------------------------------

// newLeaderBarrier creates, initializes, and returns an unsealed barrier
// suitable as the "leader" in import round-trip tests.
func newLeaderBarrier(t *testing.T) *StorageBarrier {
	t.Helper()
	backend := NewMemoryBackend()
	cfg := BarrierConfig{
		RootKeyPath:     "test/root-key",
		PreferenceOrder: []StrategyID{StrategyPassphrase},
	}
	b, err := NewStorageBarrier(testLogger(), backend, cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier (leader): %v", err)
	}
	if err := b.Initialize(context.Background(), Credentials{}); err != nil {
		t.Fatalf("Initialize (leader): %v", err)
	}
	return b
}

// newFollowerBarrier creates a sealed, uninitialized barrier suitable as the
// "follower" in import round-trip tests. It uses the same strategy as
// newLeaderBarrier so an imported blob's Unseal succeeds.
func newFollowerBarrier(t *testing.T) *StorageBarrier {
	t.Helper()
	backend := NewMemoryBackend()
	cfg := BarrierConfig{
		RootKeyPath:     "test/root-key",
		PreferenceOrder: []StrategyID{StrategyPassphrase},
	}
	b, err := NewStorageBarrier(testLogger(), backend, cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier (follower): %v", err)
	}
	return b
}

// validSalt returns a 32-byte salt for use in tests.
func validSalt() []byte {
	salt := make([]byte, 32)
	for i := range salt {
		salt[i] = byte(i + 1)
	}
	return salt
}

// --- ImportSealedBlob --------------------------------------------------------

// validSealedBlob returns a JSON-encoded SealedRootKey suitable for
// ImportSealedBlob tests. The ciphertext is a stub since these tests do
// not invoke the Unseal path (or use a NoneStrategy that ignores ciphertext).
func validSealedBlob(t *testing.T) []byte {
	t.Helper()
	sealed := SealedRootKey{
		Strategy:    StrategyNone,
		Version:     1,
		Epoch:       1,
		Salt:        validSalt(),
		KnownEpochs: []uint64{1},
		HKDFVersion: quicraftseal.HKDFV2,
		Ciphertext:  []byte("placeholder"),
	}
	blob, err := json.Marshal(sealed)
	if err != nil {
		t.Fatalf("json.Marshal SealedRootKey: %v", err)
	}
	return blob
}

func TestImportSealedBlob_Success(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()
	cfg := BarrierConfig{
		RootKeyPath: "test/root-key",
	}
	b, err := NewStorageBarrier(testLogger(), backend, cfg, NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	if err := b.ImportSealedBlob(ctx, validSealedBlob(t)); err != nil {
		t.Fatalf("ImportSealedBlob: %v", err)
	}

	// Barrier must remain sealed — caller unseals later.
	if !b.IsSealed() {
		t.Error("barrier must remain sealed after ImportSealedBlob")
	}

	// Root key blob must exist in storage.
	exists, err := backend.Exists(ctx, "test/root-key")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Error("root key blob must be stored after ImportSealedBlob")
	}
}

func TestImportSealedBlob_InitializedAtNonZero(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()
	cfg := BarrierConfig{
		RootKeyPath: "test/root-key",
	}
	b, err := NewStorageBarrier(testLogger(), backend, cfg, NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	if err := b.ImportSealedBlob(ctx, validSealedBlob(t)); err != nil {
		t.Fatalf("ImportSealedBlob: %v", err)
	}

	if b.Status().InitializedAt.IsZero() {
		t.Error("InitializedAt must be non-zero after ImportSealedBlob")
	}
}

func TestImportSealedBlob_CanUnsealAfterImport(t *testing.T) {
	ctx := context.Background()

	// Use a NoneStrategy barrier: ImportSealedBlob stores a NoneStrategy blob,
	// then Unseal loads it without needing a decryption credential.
	backend := NewMemoryBackend()
	cfg := BarrierConfig{
		RootKeyPath: "test/root-key",
	}
	b, err := NewStorageBarrier(testLogger(), backend, cfg, NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	if err := b.ImportSealedBlob(ctx, validSealedBlob(t)); err != nil {
		t.Fatalf("ImportSealedBlob: %v", err)
	}
	if !b.IsSealed() {
		t.Fatal("barrier must be sealed before Unseal")
	}

	if err := b.Unseal(ctx, Credentials{}); err != nil {
		t.Fatalf("Unseal after ImportSealedBlob: %v", err)
	}
	if b.IsSealed() {
		t.Error("barrier must be unsealed after Unseal")
	}
}

func TestImportSealedBlob_Idempotent(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), backend, cfg, NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	blob := validSealedBlob(t)
	if err := b.ImportSealedBlob(ctx, blob); err != nil {
		t.Fatalf("first ImportSealedBlob: %v", err)
	}

	if err := b.ImportSealedBlob(ctx, blob); err != ErrAlreadyInitialized {
		t.Errorf("second ImportSealedBlob: expected ErrAlreadyInitialized, got %v", err)
	}
}

func TestImportSealedBlob_NilBlob(t *testing.T) {
	ctx := context.Background()
	b := newFollowerBarrier(t)

	if err := b.ImportSealedBlob(ctx, nil); err != ErrImportSealedBlobNil {
		t.Errorf("expected ErrImportSealedBlobNil, got %v", err)
	}
}

func TestImportSealedBlob_InvalidJSON(t *testing.T) {
	ctx := context.Background()
	b := newFollowerBarrier(t)

	if err := b.ImportSealedBlob(ctx, []byte("not-valid-json")); err != ErrImportSealedBlobInvalid {
		t.Errorf("expected ErrImportSealedBlobInvalid, got %v", err)
	}
}

func TestImportSealedBlob_InvalidStructure(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		blob []byte
	}{
		{
			name: "epoch zero",
			blob: mustMarshal(t, SealedRootKey{
				Strategy:    StrategyNone,
				Epoch:       0,
				Salt:        validSalt(),
				KnownEpochs: []uint64{0},
				Ciphertext:  []byte("x"),
			}),
		},
		{
			name: "salt wrong length",
			blob: mustMarshal(t, SealedRootKey{
				Strategy:    StrategyNone,
				Epoch:       1,
				Salt:        make([]byte, 16), // must be 32
				KnownEpochs: []uint64{1},
				Ciphertext:  []byte("x"),
			}),
		},
		{
			name: "epoch not in known",
			blob: mustMarshal(t, SealedRootKey{
				Strategy:    StrategyNone,
				Epoch:       5,
				Salt:        validSalt(),
				KnownEpochs: []uint64{1, 2},
				Ciphertext:  []byte("x"),
			}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newFollowerBarrier(t)
			if err := b.ImportSealedBlob(ctx, tc.blob); err != ErrImportSealedBlobInvalid {
				t.Errorf("%s: expected ErrImportSealedBlobInvalid, got %v", tc.name, err)
			}
		})
	}
}

// mustMarshal JSON-encodes v for test helpers.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

// --- ExportSealedBlob --------------------------------------------------------

func TestExportSealedBlob_Uninitialized(t *testing.T) {
	b := newFollowerBarrier(t)
	_, err := b.ExportSealedBlob(context.Background())
	if err != ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized on uninitialized barrier, got %v", err)
	}
}

func TestExportSealedBlob_AfterInitialize(t *testing.T) {
	ctx := context.Background()
	leader := newLeaderBarrier(t)

	blob, err := leader.ExportSealedBlob(ctx)
	if err != nil {
		t.Fatalf("ExportSealedBlob after Initialize: %v", err)
	}
	if len(blob) == 0 {
		t.Error("ExportSealedBlob must return a non-empty blob")
	}

	// The blob must be valid JSON decodable as a SealedRootKey.
	var srk SealedRootKey
	if jsonErr := json.Unmarshal(blob, &srk); jsonErr != nil {
		t.Errorf("ExportSealedBlob returned non-JSON blob: %v", jsonErr)
	}
	if srk.Epoch == 0 {
		t.Error("decoded SealedRootKey must have Epoch > 0")
	}
}

func TestExportSealedBlob_WorksWhenSealed(t *testing.T) {
	ctx := context.Background()
	leader := newLeaderBarrier(t)

	// Seal the barrier to verify export does NOT require unsealed state.
	if err := leader.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	blob, err := leader.ExportSealedBlob(ctx)
	if err != nil {
		t.Fatalf("ExportSealedBlob on sealed barrier: %v", err)
	}
	if len(blob) == 0 {
		t.Error("ExportSealedBlob must return a non-empty blob even when sealed")
	}
}

func TestExportSealedBlob_AfterImportSealedBlob(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), backend, cfg, NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	original := validSealedBlob(t)
	if err := b.ImportSealedBlob(ctx, original); err != nil {
		t.Fatalf("ImportSealedBlob: %v", err)
	}

	exported, err := b.ExportSealedBlob(ctx)
	if err != nil {
		t.Fatalf("ExportSealedBlob after ImportSealedBlob: %v", err)
	}
	if !bytes.Equal(exported, original) {
		t.Error("ExportSealedBlob must return the same bytes that were imported")
	}
}

// TestExportSealedBlob_GetErrorPassthrough verifies that ExportSealedBlob
// propagates a storage-layer Get error that is NOT ErrSecretNotFound. When
// the underlying backend fails with an unexpected error the function must
// return (nil, err) immediately.
func TestExportSealedBlob_GetErrorPassthrough(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Use a backend whose Get returns a non-ErrSecretNotFound error. ErrSealed
	// is a sentinel from this package and is guaranteed != ErrSecretNotFound.
	backend := &errorBackend{getErr: ErrSealed}
	b, err := NewStorageBarrier(testLogger(), backend, BarrierConfig{RootKeyPath: "root"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	_, gotErr := b.ExportSealedBlob(ctx)
	if gotErr == nil {
		t.Fatal("ExportSealedBlob: expected error, got nil")
	}
	if errors.Is(gotErr, ErrNotInitialized) {
		t.Fatalf("ExportSealedBlob: got ErrNotInitialized, want the passthrough storage error")
	}
}

// TestExportSealedBlob_NilBlobReturnsNotInitialized exercises the defensive nil
// guard after a successful Get: when the backend returns (nil, nil) — a valid
// "found but empty" response from some implementations — ExportSealedBlob must
// treat the barrier as uninitialized and return ErrNotInitialized.
func TestExportSealedBlob_NilBlobReturnsNotInitialized(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// errorBackend with no getErr returns (nil, nil) from Get.
	backend := &errorBackend{}
	b, err := NewStorageBarrier(testLogger(), backend, BarrierConfig{RootKeyPath: "root"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	_, gotErr := b.ExportSealedBlob(ctx)
	if !errors.Is(gotErr, ErrNotInitialized) {
		t.Fatalf("ExportSealedBlob nil blob: want ErrNotInitialized, got %v", gotErr)
	}
}

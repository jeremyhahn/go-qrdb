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
	"testing"
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
// newLeaderBarrier so ImportRootKey's SealRootKey → Unseal cycle succeeds.
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

// --- ImportedKey.Validate ----------------------------------------------------

func TestImportedKey_Validate_Success(t *testing.T) {
	k := &ImportedKey{
		RootKey:     make([]byte, 32),
		Salt:        validSalt(),
		Epoch:       1,
		KnownEpochs: []uint64{1},
	}
	if err := k.Validate(); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestImportedKey_Validate_MultipleEpochs(t *testing.T) {
	k := &ImportedKey{
		RootKey:     make([]byte, 32),
		Salt:        validSalt(),
		Epoch:       3,
		KnownEpochs: []uint64{1, 2, 3},
	}
	if err := k.Validate(); err != nil {
		t.Errorf("expected nil for valid multi-epoch key, got %v", err)
	}
}

func TestImportedKey_Validate_Nil(t *testing.T) {
	var k *ImportedKey
	if err := k.Validate(); err != ErrImportedKeyNil {
		t.Errorf("expected ErrImportedKeyNil, got %v", err)
	}
}

func TestImportedKey_Validate_EmptyRootKey(t *testing.T) {
	k := &ImportedKey{
		RootKey:     nil,
		Salt:        validSalt(),
		Epoch:       1,
		KnownEpochs: []uint64{1},
	}
	if err := k.Validate(); err != ErrImportedRootKeyEmpty {
		t.Errorf("expected ErrImportedRootKeyEmpty, got %v", err)
	}
}

func TestImportedKey_Validate_InvalidSaltShort(t *testing.T) {
	k := &ImportedKey{
		RootKey:     make([]byte, 32),
		Salt:        make([]byte, 16), // too short
		Epoch:       1,
		KnownEpochs: []uint64{1},
	}
	if err := k.Validate(); err != ErrImportedSaltInvalid {
		t.Errorf("expected ErrImportedSaltInvalid, got %v", err)
	}
}

func TestImportedKey_Validate_InvalidSaltLong(t *testing.T) {
	k := &ImportedKey{
		RootKey:     make([]byte, 32),
		Salt:        make([]byte, 64), // too long
		Epoch:       1,
		KnownEpochs: []uint64{1},
	}
	if err := k.Validate(); err != ErrImportedSaltInvalid {
		t.Errorf("expected ErrImportedSaltInvalid, got %v", err)
	}
}

func TestImportedKey_Validate_EpochZero(t *testing.T) {
	k := &ImportedKey{
		RootKey:     make([]byte, 32),
		Salt:        validSalt(),
		Epoch:       0,
		KnownEpochs: []uint64{0},
	}
	if err := k.Validate(); err != ErrImportedEpochZero {
		t.Errorf("expected ErrImportedEpochZero, got %v", err)
	}
}

func TestImportedKey_Validate_EpochNotInKnown(t *testing.T) {
	k := &ImportedKey{
		RootKey:     make([]byte, 32),
		Salt:        validSalt(),
		Epoch:       5,
		KnownEpochs: []uint64{1, 2, 3}, // 5 missing
	}
	if err := k.Validate(); err != ErrImportedEpochNotInKnown {
		t.Errorf("expected ErrImportedEpochNotInKnown, got %v", err)
	}
}

func TestImportedKey_Validate_EmptyKnownEpochs(t *testing.T) {
	k := &ImportedKey{
		RootKey:     make([]byte, 32),
		Salt:        validSalt(),
		Epoch:       1,
		KnownEpochs: nil, // empty
	}
	if err := k.Validate(); err != ErrImportedEpochNotInKnown {
		t.Errorf("expected ErrImportedEpochNotInKnown, got %v", err)
	}
}

// --- ExportRootKeyContext -----------------------------------------------------

func TestExportRootKeyContext_Sealed(t *testing.T) {
	backend := NewMemoryBackend()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), backend, cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	// Barrier is sealed (never initialized).
	_, exportErr := b.ExportRootKeyContext(context.Background())
	if exportErr != ErrSealed {
		t.Errorf("expected ErrSealed, got %v", exportErr)
	}
}

func TestExportRootKeyContext_Success(t *testing.T) {
	leader := newLeaderBarrier(t)

	exported, err := leader.ExportRootKeyContext(context.Background())
	if err != nil {
		t.Fatalf("ExportRootKeyContext: %v", err)
	}
	if len(exported.RootKey) == 0 {
		t.Error("exported RootKey must not be empty")
	}
	if len(exported.Salt) != 32 {
		t.Errorf("exported Salt must be 32 bytes, got %d", len(exported.Salt))
	}
	if exported.Epoch == 0 {
		t.Error("exported Epoch must be > 0")
	}
	if len(exported.KnownEpochs) == 0 {
		t.Error("exported KnownEpochs must not be empty")
	}
	epochFound := false
	for _, ep := range exported.KnownEpochs {
		if ep == exported.Epoch {
			epochFound = true
			break
		}
	}
	if !epochFound {
		t.Error("exported KnownEpochs must contain Epoch")
	}
}

func TestExportRootKeyContext_RootKeyMatchesMasterKey(t *testing.T) {
	leader := newLeaderBarrier(t)

	masterKey, err := leader.GetMasterKey(context.Background())
	if err != nil {
		t.Fatalf("GetMasterKey: %v", err)
	}

	exported, err := leader.ExportRootKeyContext(context.Background())
	if err != nil {
		t.Fatalf("ExportRootKeyContext: %v", err)
	}

	if !bytes.Equal(exported.RootKey, masterKey) {
		t.Error("exported RootKey must equal GetMasterKey()")
	}
}

func TestExportRootKeyContext_SealedAfterSeal(t *testing.T) {
	leader := newLeaderBarrier(t)

	if err := leader.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	_, err := leader.ExportRootKeyContext(context.Background())
	if err != ErrSealed {
		t.Errorf("expected ErrSealed after Seal(), got %v", err)
	}
}

// --- ImportRootKey -----------------------------------------------------------

func TestImportRootKey_Success(t *testing.T) {
	ctx := context.Background()
	leader := newLeaderBarrier(t)
	follower := newFollowerBarrier(t)

	exported, err := leader.ExportRootKeyContext(context.Background())
	if err != nil {
		t.Fatalf("ExportRootKeyContext: %v", err)
	}

	if err := follower.ImportRootKey(ctx, exported, Credentials{}); err != nil {
		t.Fatalf("ImportRootKey: %v", err)
	}
	if follower.IsSealed() {
		t.Error("follower must be unsealed after ImportRootKey")
	}
}

func TestImportRootKey_MasterKeyMatchesLeader(t *testing.T) {
	ctx := context.Background()
	leader := newLeaderBarrier(t)
	follower := newFollowerBarrier(t)

	exported, err := leader.ExportRootKeyContext(context.Background())
	if err != nil {
		t.Fatalf("ExportRootKeyContext: %v", err)
	}

	if err := follower.ImportRootKey(ctx, exported, Credentials{}); err != nil {
		t.Fatalf("ImportRootKey: %v", err)
	}

	followerKey, err := follower.GetMasterKey(context.Background())
	if err != nil {
		t.Fatalf("GetMasterKey (follower): %v", err)
	}
	if !bytes.Equal(followerKey, exported.RootKey) {
		t.Error("follower master key must equal the exported leader root key")
	}
}

// TestImportRootKey_CrossNodeDEKParity is the primary correctness proof:
// data encrypted by the leader can be decrypted by the follower after
// ImportRootKey, and vice versa. This holds because both nodes derive the
// same DEK via HKDF(rootKey, salt, epoch).
func TestImportRootKey_CrossNodeDEKParity(t *testing.T) {
	ctx := context.Background()
	leader := newLeaderBarrier(t)

	// Encrypt data on the leader before exporting.
	plaintext := []byte("cross-node-dek-parity-test-vector")
	ciphertext, err := leader.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt (leader): %v", err)
	}

	exported, err := leader.ExportRootKeyContext(context.Background())
	if err != nil {
		t.Fatalf("ExportRootKeyContext: %v", err)
	}

	follower := newFollowerBarrier(t)
	if err := follower.ImportRootKey(ctx, exported, Credentials{}); err != nil {
		t.Fatalf("ImportRootKey: %v", err)
	}

	// Follower must decrypt the leader's ciphertext.
	decrypted, err := follower.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt (follower) leader ciphertext: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("cross-node decrypt = %q, want %q", decrypted, plaintext)
	}

	// Follower must also round-trip its own data.
	followerPlain := []byte("follower-own-data")
	followerCipher, err := follower.Encrypt(followerPlain)
	if err != nil {
		t.Fatalf("Encrypt (follower): %v", err)
	}
	followerOut, err := follower.Decrypt(followerCipher)
	if err != nil {
		t.Fatalf("Decrypt (follower own): %v", err)
	}
	if !bytes.Equal(followerOut, followerPlain) {
		t.Errorf("follower round-trip = %q, want %q", followerOut, followerPlain)
	}
}

func TestImportRootKey_Idempotent(t *testing.T) {
	ctx := context.Background()
	leader := newLeaderBarrier(t)
	follower := newFollowerBarrier(t)

	exported, err := leader.ExportRootKeyContext(context.Background())
	if err != nil {
		t.Fatalf("ExportRootKeyContext: %v", err)
	}

	if err := follower.ImportRootKey(ctx, exported, Credentials{}); err != nil {
		t.Fatalf("first ImportRootKey: %v", err)
	}

	// Second call must return ErrAlreadyInitialized without modifying state.
	if err := follower.ImportRootKey(ctx, exported, Credentials{}); err != ErrAlreadyInitialized {
		t.Errorf("second ImportRootKey: expected ErrAlreadyInitialized, got %v", err)
	}
}

func TestImportRootKey_NilImportedKey(t *testing.T) {
	ctx := context.Background()
	follower := newFollowerBarrier(t)

	err := follower.ImportRootKey(ctx, nil, Credentials{})
	if err != ErrImportedKeyNil {
		t.Errorf("expected ErrImportedKeyNil, got %v", err)
	}
}

func TestImportRootKey_ValidationErrors(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name    string
		key     *ImportedKey
		wantErr error
	}{
		{
			name:    "empty root key",
			key:     &ImportedKey{RootKey: nil, Salt: validSalt(), Epoch: 1, KnownEpochs: []uint64{1}},
			wantErr: ErrImportedRootKeyEmpty,
		},
		{
			name:    "short salt",
			key:     &ImportedKey{RootKey: make([]byte, 32), Salt: make([]byte, 8), Epoch: 1, KnownEpochs: []uint64{1}},
			wantErr: ErrImportedSaltInvalid,
		},
		{
			name:    "epoch zero",
			key:     &ImportedKey{RootKey: make([]byte, 32), Salt: validSalt(), Epoch: 0, KnownEpochs: []uint64{0}},
			wantErr: ErrImportedEpochZero,
		},
		{
			name:    "epoch not in known",
			key:     &ImportedKey{RootKey: make([]byte, 32), Salt: validSalt(), Epoch: 7, KnownEpochs: []uint64{1, 2}},
			wantErr: ErrImportedEpochNotInKnown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			follower := newFollowerBarrier(t)
			err := follower.ImportRootKey(ctx, tc.key, Credentials{})
			if err != tc.wantErr {
				t.Errorf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestImportRootKey_NoAvailableStrategy(t *testing.T) {
	ctx := context.Background()

	backend := NewMemoryBackend()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	follower, err := NewStorageBarrier(testLogger(), backend, cfg, &mockUnavailableStrategy{})
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	key := &ImportedKey{
		RootKey:     make([]byte, 32),
		Salt:        validSalt(),
		Epoch:       1,
		KnownEpochs: []uint64{1},
	}
	if err := follower.ImportRootKey(ctx, key, Credentials{}); err != ErrNoAvailableStrategy {
		t.Errorf("expected ErrNoAvailableStrategy, got %v", err)
	}
}

func TestImportRootKey_MetadataPersistsAcrossWarmBoot(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()
	cfg := BarrierConfig{
		RootKeyPath:     "test/root-key",
		PreferenceOrder: []StrategyID{StrategyPassphrase},
	}

	leader := newLeaderBarrier(t)
	exported, err := leader.ExportRootKeyContext(context.Background())
	if err != nil {
		t.Fatalf("ExportRootKeyContext: %v", err)
	}

	// Import into first follower instance.
	follower1, err := NewStorageBarrier(testLogger(), backend, cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier(follower1): %v", err)
	}
	if err := follower1.ImportRootKey(ctx, exported, Credentials{}); err != nil {
		t.Fatalf("ImportRootKey: %v", err)
	}
	initAt := follower1.Status().InitializedAt
	if initAt.IsZero() {
		t.Fatal("InitializedAt must be non-zero after ImportRootKey")
	}

	// Warm boot: new barrier over same backend, still sealed.
	follower2, err := NewStorageBarrier(testLogger(), backend, cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier(follower2): %v", err)
	}
	warmStatus := follower2.Status()
	if warmStatus.InitializedAt.IsZero() {
		t.Error("warm-boot barrier must report non-zero InitializedAt from persisted metadata")
	}
	if !warmStatus.InitializedAt.Equal(initAt) {
		t.Errorf("warm-boot InitializedAt = %v, want persisted %v", warmStatus.InitializedAt, initAt)
	}
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

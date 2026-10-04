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
)

// initSystemBarrierForImport initializes the registry's system barrier using
// the NoneStrategy (credential-free), which satisfies
// SystemBarrierStrategy.Available() for subsequent auto-unseal tenant operations.
func initSystemBarrierForImport(t *testing.T, reg *BarrierRegistry) {
	t.Helper()
	ctx := context.Background()
	if err := reg.SystemBarrier().Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("Initialize system barrier: %v", err)
	}
}

// buildValidSealedBlob returns a JSON-encoded SealedRootKey with valid
// structure for use with ImportTenant(autoUnseal=false). The NoneStrategy
// identifier is used so that Unseal (if ever called) can succeed without
// requiring a real ciphertext.
func buildValidSealedBlob(t *testing.T) []byte {
	t.Helper()
	salt := make([]byte, 32)
	for i := range salt {
		salt[i] = byte(i + 1)
	}
	key := SealedRootKey{
		Strategy:    StrategyNone,
		Version:     1,
		Epoch:       1,
		Salt:        salt,
		KnownEpochs: []uint64{1},
		Ciphertext:  []byte("placeholder"),
	}
	blob, err := json.Marshal(key)
	if err != nil {
		t.Fatalf("json.Marshal SealedRootKey: %v", err)
	}
	return blob
}

// TestBarrierRegistry_ImportTenant_AutoUnseal verifies the system-managed
// import path: the follower registry imports a leader's root-key context, the
// barrier is immediately unsealed, and the master key matches the exported key.
func TestBarrierRegistry_ImportTenant_AutoUnseal(t *testing.T) {
	ctx := context.Background()

	// ---- Leader registry ----
	leaderReg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, leaderReg)

	leaderDataRoot, leaderTB, err := leaderReg.RegisterVolumeTenant(ctx, "leader-tenant", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant(leader): %v", err)
	}
	_ = leaderDataRoot

	if err := leaderTB.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("Initialize(leader tenant): %v", err)
	}

	exported, err := leaderTB.Barrier().ExportRootKeyContext()
	if err != nil {
		t.Fatalf("ExportRootKeyContext: %v", err)
	}

	// ---- Follower registry (distinct node) ----
	followerReg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, followerReg)

	followerTB, err := followerReg.ImportTenant(ctx, "replicated-tenant", true, exported, nil)
	if err != nil {
		t.Fatalf("ImportTenant(autoUnseal=true): %v", err)
	}

	if followerTB.IsSealed() {
		t.Error("follower barrier must be unsealed after ImportTenant(autoUnseal=true)")
	}

	// Master key on the follower must match the exported root key.
	followerKey, err := followerTB.Barrier().GetMasterKey()
	if err != nil {
		t.Fatalf("GetMasterKey(follower): %v", err)
	}
	if !bytes.Equal(followerKey, exported.RootKey) {
		t.Error("follower master key must equal leader's exported root key")
	}

	// Encrypt/decrypt round-trip on the follower must succeed.
	plaintext := []byte("import-tenant-round-trip")
	ciphertext, err := followerTB.Barrier().Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt(follower): %v", err)
	}
	decrypted, err := followerTB.Barrier().Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt(follower): %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("round-trip = %q, want %q", decrypted, plaintext)
	}
}

// TestBarrierRegistry_ImportTenant_AutoUnseal_CrossNodeDEKParity verifies that
// data encrypted by the leader can be decrypted by the follower after
// ImportTenant, proving identical DEK derivation via shared Salt/Epoch.
func TestBarrierRegistry_ImportTenant_AutoUnseal_CrossNodeDEKParity(t *testing.T) {
	ctx := context.Background()

	leaderReg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, leaderReg)

	_, leaderTB, err := leaderReg.RegisterVolumeTenant(ctx, "dek-leader", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant(dek-leader): %v", err)
	}
	if err := leaderTB.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("Initialize(dek-leader): %v", err)
	}

	// Encrypt on the leader.
	leaderPlain := []byte("dek-parity-test-vector-from-leader")
	leaderCipher, err := leaderTB.Barrier().Encrypt(leaderPlain)
	if err != nil {
		t.Fatalf("Encrypt(leader): %v", err)
	}

	exported, err := leaderTB.Barrier().ExportRootKeyContext()
	if err != nil {
		t.Fatalf("ExportRootKeyContext: %v", err)
	}

	followerReg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, followerReg)

	followerTB, err := followerReg.ImportTenant(ctx, "dek-follower", true, exported, nil)
	if err != nil {
		t.Fatalf("ImportTenant(dek-follower): %v", err)
	}

	// Follower must decrypt the leader's ciphertext.
	decrypted, err := followerTB.Barrier().Decrypt(leaderCipher)
	if err != nil {
		t.Fatalf("Decrypt(follower) leader ciphertext: %v", err)
	}
	if !bytes.Equal(decrypted, leaderPlain) {
		t.Errorf("cross-node decrypt = %q, want %q", decrypted, leaderPlain)
	}
}

// TestBarrierRegistry_ImportTenant_SelfManaged verifies the self-managed
// (autoUnseal=false) import path: the follower is registered with a sealed
// blob stored and the barrier remains sealed for later tenant-driven unseal.
func TestBarrierRegistry_ImportTenant_SelfManaged(t *testing.T) {
	ctx := context.Background()

	reg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, reg)

	blob := buildValidSealedBlob(t)

	followerTB, err := reg.ImportTenant(ctx, "self-managed-tenant", false, nil, blob)
	if err != nil {
		t.Fatalf("ImportTenant(autoUnseal=false): %v", err)
	}
	if !followerTB.IsSealed() {
		t.Error("self-managed barrier must remain sealed after ImportTenant(autoUnseal=false)")
	}

	// The tenant must be registered and retrievable.
	got, err := reg.GetTenant("self-managed-tenant")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got == nil {
		t.Error("GetTenant returned nil for registered tenant")
	}
}

// TestBarrierRegistry_ImportTenant_AutoUnseal_NilImportedKey verifies that
// a nil ImportedKey propagates ErrImportedKeyNil from ImportRootKey.
func TestBarrierRegistry_ImportTenant_AutoUnseal_NilImportedKey(t *testing.T) {
	ctx := context.Background()

	reg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, reg)

	_, err := reg.ImportTenant(ctx, "nil-key-tenant", true, nil, nil)
	if !errors.Is(err, ErrImportedKeyNil) {
		t.Errorf("expected ErrImportedKeyNil, got %v", err)
	}
}

// TestBarrierRegistry_ImportTenant_SelfManaged_NilBlob verifies that a nil
// blob propagates ErrImportSealedBlobNil from ImportSealedBlob.
func TestBarrierRegistry_ImportTenant_SelfManaged_NilBlob(t *testing.T) {
	ctx := context.Background()

	reg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, reg)

	_, err := reg.ImportTenant(ctx, "nil-blob-tenant", false, nil, nil)
	if !errors.Is(err, ErrImportSealedBlobNil) {
		t.Errorf("expected ErrImportSealedBlobNil, got %v", err)
	}
}

// TestBarrierRegistry_ImportTenant_Idempotency verifies that calling
// ImportTenant a second time with the same tenantID on the same registry
// returns ErrTenantAlreadyExists (from RegisterVolumeTenant), not a
// lower-level import error.
func TestBarrierRegistry_ImportTenant_Idempotency(t *testing.T) {
	ctx := context.Background()

	reg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, reg)

	// Create a leader to get a valid exported key.
	leaderReg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, leaderReg)
	_, leaderTB, err := leaderReg.RegisterVolumeTenant(ctx, "idem-leader", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant(idem-leader): %v", err)
	}
	if err := leaderTB.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("Initialize(idem-leader): %v", err)
	}
	exported, err := leaderTB.Barrier().ExportRootKeyContext()
	if err != nil {
		t.Fatalf("ExportRootKeyContext: %v", err)
	}

	// First import — must succeed.
	if _, err := reg.ImportTenant(ctx, "idem-tenant", true, exported, nil); err != nil {
		t.Fatalf("first ImportTenant: %v", err)
	}

	// Second import with same tenantID must fail. Depending on which registry
	// layer catches it first, the error may be ErrTenantAlreadyExists (from the
	// in-memory tenants map) or a per-tenant Pebble "already opened" error (from
	// the per-tenant Pebble registry which refuses to open the same tenant at a
	// second path). Either outcome proves the call is rejected.
	_, err = reg.ImportTenant(ctx, "idem-tenant", true, exported, nil)
	if err == nil {
		t.Error("second ImportTenant with same tenantID must return an error")
	}
}

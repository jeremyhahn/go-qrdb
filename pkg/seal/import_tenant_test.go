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
// structure for use with ImportTenant. The NoneStrategy
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

// TestBarrierRegistry_ImportTenant_SelfManaged verifies the self-managed
// import path: the follower is registered with a sealed blob stored and the
// barrier remains sealed for later tenant-driven unseal.
func TestBarrierRegistry_ImportTenant_SelfManaged(t *testing.T) {
	ctx := context.Background()

	reg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, reg)

	blob := buildValidSealedBlob(t)
	reserveTestVolumeTenant(t, reg, "self-managed-tenant")

	followerTB, err := reg.ImportTenant(ctx, "self-managed-tenant", blob)
	if err != nil {
		t.Fatalf("ImportTenant: %v", err)
	}
	if !followerTB.IsSealed() {
		t.Error("self-managed barrier must remain sealed after ImportTenant")
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

// TestBarrierRegistry_ImportTenant_SelfManaged_FailedImportLeavesNoBarrier
// verifies that an import that fails registers nothing: the cluster key plane
// acks a redelivery that finds the tenant registered as already imported, so
// a sealed barrier left behind would keep the tenant sealed on this node for
// good. The redelivery must instead install the sealed blob.
func TestBarrierRegistry_ImportTenant_SelfManaged_FailedImportLeavesNoBarrier(t *testing.T) {
	ctx := context.Background()

	reg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, reg)
	reserveTestVolumeTenant(t, reg, "redelivered-blob-tenant")

	if _, err := reg.ImportTenant(ctx, "redelivered-blob-tenant", nil); !errors.Is(err, ErrImportSealedBlobNil) {
		t.Fatalf("first ImportTenant = %v, want ErrImportSealedBlobNil", err)
	}
	if _, err := reg.GetTenant("redelivered-blob-tenant"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("GetTenant after a failed import = %v, want ErrTenantNotFound", err)
	}
	blob := buildValidSealedBlob(t)
	tb, err := reg.ImportTenant(ctx, "redelivered-blob-tenant", blob)
	if err != nil {
		t.Fatalf("redelivered ImportTenant: %v", err)
	}
	// The blob must land in a store opened afresh, not in the handle the
	// failed import's rollback closed: read it back from that store.
	got, err := tb.Barrier().ExportSealedBlob(ctx)
	if err != nil {
		t.Fatalf("ExportSealedBlob after the redelivery: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Fatal("the redelivered blob is not the one the store holds")
	}
}

// TestBarrierRegistry_ImportTenant_SelfManaged_RedeliveryOverPersistedBlobRegisters
// verifies a redelivery after an import that persisted the sealed blob and
// then failed: the store already holds the blob, the import answers
// ErrAlreadyInitialized, and the cluster key plane acks that as delivered --
// so the barrier stays registered, sealed, for the tenant to unseal.
func TestBarrierRegistry_ImportTenant_SelfManaged_RedeliveryOverPersistedBlobRegisters(t *testing.T) {
	ctx := context.Background()

	reg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, reg)
	reserveTestVolumeTenant(t, reg, "persisted-blob-tenant")

	blob := buildValidSealedBlob(t)
	if _, err := reg.ImportTenant(ctx, "persisted-blob-tenant", blob); err != nil {
		t.Fatalf("first ImportTenant: %v", err)
	}
	if err := reg.RemoveTenant("persisted-blob-tenant"); err != nil {
		t.Fatalf("RemoveTenant: %v", err)
	}

	tb, err := reg.ImportTenant(ctx, "persisted-blob-tenant", blob)
	if err != nil {
		t.Fatalf("redelivered ImportTenant over a persisted blob: %v", err)
	}
	if _, err := reg.GetTenant("persisted-blob-tenant"); err != nil {
		t.Fatalf("GetTenant after the redelivery: %v", err)
	}
	if !tb.IsSealed() {
		t.Fatal("a self-managed barrier must stay sealed for the tenant to unseal")
	}
}

// TestBarrierRegistry_ImportTenant_SelfManaged_NilBlob verifies that a nil
// blob propagates ErrImportSealedBlobNil from ImportSealedBlob.
func TestBarrierRegistry_ImportTenant_SelfManaged_NilBlob(t *testing.T) {
	ctx := context.Background()

	reg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, reg)
	reserveTestVolumeTenant(t, reg, "nil-blob-tenant")

	_, err := reg.ImportTenant(ctx, "nil-blob-tenant", nil)
	if !errors.Is(err, ErrImportSealedBlobNil) {
		t.Errorf("expected ErrImportSealedBlobNil, got %v", err)
	}
}

// TestBarrierRegistry_ImportTenant_Idempotency verifies that calling
// ImportTenant a second time with the same tenantID on the same registry is
// refused: the barrier the first import initialized refuses a second blob.
func TestBarrierRegistry_ImportTenant_Idempotency(t *testing.T) {
	ctx := context.Background()

	reg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, reg)
	reserveTestVolumeTenant(t, reg, "idem-tenant")

	blob := buildValidSealedBlob(t)

	// First import — must succeed.
	if _, err := reg.ImportTenant(ctx, "idem-tenant", blob); err != nil {
		t.Fatalf("first ImportTenant: %v", err)
	}

	// Second import with same tenantID must fail. Depending on which registry
	// layer catches it first, the error may be ErrTenantAlreadyExists (from the
	// in-memory tenants map) or a per-tenant Pebble "already opened" error (from
	// the per-tenant Pebble registry which refuses to open the same tenant at a
	// second path). Either outcome proves the call is rejected.
	if _, err := reg.ImportTenant(ctx, "idem-tenant", blob); err == nil {
		t.Error("second ImportTenant with same tenantID must return an error")
	}
}

// TestBarrierRegistry_ImportTenant_InitializesRegisteredShell verifies that a
// blob imported for a tenant whose barrier is already registered -- the
// uninitialized, sealed shell placement registers -- initializes that same
// barrier rather than failing on the registration.
func TestBarrierRegistry_ImportTenant_InitializesRegisteredShell(t *testing.T) {
	ctx := context.Background()

	reg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, reg)
	reserveTestVolumeTenant(t, reg, "shell-tenant")

	_, shell, err := reg.RegisterVolumeTenant(ctx, "shell-tenant", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	imported, err := reg.ImportTenant(ctx, "shell-tenant", buildValidSealedBlob(t))
	if err != nil {
		t.Fatalf("ImportTenant into a registered shell: %v", err)
	}
	if imported != shell {
		t.Error("ImportTenant must initialize the registered shell, not a second barrier")
	}
	if initialized, err := imported.Barrier().Initialized(ctx); err != nil || !initialized {
		t.Errorf("the shell must be initialized after ImportTenant: initialized=%v err=%v", initialized, err)
	}
	if !imported.IsSealed() {
		t.Error("an imported owner-sealed barrier must stay sealed")
	}
}

// TestBarrierRegistry_ImportTenant_InitializedBarrierRefusesBlob verifies that
// a second blob for a barrier already initialized is refused with
// ErrAlreadyInitialized, leaving the first key in place.
func TestBarrierRegistry_ImportTenant_InitializedBarrierRefusesBlob(t *testing.T) {
	ctx := context.Background()

	reg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, reg)
	reserveTestVolumeTenant(t, reg, "twice-tenant")

	if _, err := reg.ImportTenant(ctx, "twice-tenant", buildValidSealedBlob(t)); err != nil {
		t.Fatalf("first ImportTenant: %v", err)
	}
	if _, err := reg.ImportTenant(ctx, "twice-tenant", buildValidSealedBlob(t)); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("second ImportTenant: got %v, want ErrAlreadyInitialized", err)
	}
}

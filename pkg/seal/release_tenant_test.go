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
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestReleaseTenant_ThenDropRecords_KeepsTheBytesUntilTheCallerDecides pins
// the two halves DestroyTenant is built from. ReleaseTenant forgets the
// in-memory barrier and changes nothing durable -- the record that names the
// DataRoot survives it, which is what lets a retried reclamation find a
// directory whose removal failed. DropTenantRecords then removes the records
// the boot path restores from.
func TestReleaseTenant_ThenDropRecords_KeepsTheBytesUntilTheCallerDecides(t *testing.T) {
	ctx := context.Background()
	reg, system := newPooledTestRegistry(t)
	dataRoot, _, err := reg.RegisterVolumeTenant(ctx, "acme", "", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}

	if err := reg.ReleaseTenant(ctx, "acme"); err != nil {
		t.Fatalf("ReleaseTenant: %v", err)
	}
	if _, err := reg.GetTenant("acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("barrier still registered: %v", err)
	}
	if _, err := system.Get(ctx, tenantRootStorePrefix+"acme"); err != nil {
		t.Fatalf("ReleaseTenant removed the durable record: %v", err)
	}
	if _, err := os.Stat(dataRoot); err != nil {
		t.Fatalf("ReleaseTenant removed the DataRoot: %v", err)
	}
	if err := reg.ReleaseTenant(ctx, "acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("second ReleaseTenant = %v; want ErrTenantNotFound", err)
	}

	if err := reg.DropTenantRecords(ctx, "acme"); err != nil {
		t.Fatalf("DropTenantRecords: %v", err)
	}
	if _, err := system.Get(ctx, tenantRootStorePrefix+"acme"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("tenant-root record survived DropTenantRecords: %v", err)
	}
	if err := reg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.GetTenant("acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("a tenant with no records was restored at boot: %v", err)
	}
	if err := reg.DropTenantRecords(ctx, "acme"); err != nil {
		t.Fatalf("DropTenantRecords over absent records = %v; want nil", err)
	}

	for name, call := range map[string]func() error{
		"ReleaseTenant":     func() error { return reg.ReleaseTenant(ctx, "") },
		"DropTenantRecords": func() error { return reg.DropTenantRecords(ctx, "") },
	} {
		if err := call(); !errors.Is(err, ErrEmptyTenantID) {
			t.Fatalf("%s(\"\") = %v; want ErrEmptyTenantID", name, err)
		}
	}
}

// TestRegisterVolumeTenant_RefusesARootAnotherRecordClaims: a create may not
// take a directory another tenant's tenant-root record on this node still
// names -- the deleted-tenant reconcile would read that record and remove the
// new tenant's directory.
func TestRegisterVolumeTenant_RefusesARootAnotherRecordClaims(t *testing.T) {
	ctx := context.Background()
	reg, _ := newPooledTestRegistry(t)
	claimed, _, err := reg.RegisterVolumeTenant(ctx, "retired", filepath.Join(t.TempDir(), "disk"), false)
	if err != nil {
		t.Fatal(err)
	}
	// The retired tenant's barrier is gone from memory, its record is not --
	// the state of a node that missed the delete.
	if err := reg.ReleaseTenant(ctx, "retired"); err != nil {
		t.Fatal(err)
	}

	for _, root := range []string{claimed, filepath.Join(claimed, "nested")} {
		_, _, err := reg.RegisterVolumeTenant(ctx, "newcomer", root, false)
		var dc *DataRootClaimedError
		if !errors.As(err, &dc) || dc.ClaimedBy != "retired" {
			t.Fatalf("RegisterVolumeTenant(%s) = %v; want *DataRootClaimedError claimed by retired", root, err)
		}
		if dc.Error() == "" {
			t.Fatal("empty error message")
		}
	}

	// Re-attach of the SAME tenant, and a create at an unrelated root, are fine.
	if _, _, err := reg.RegisterVolumeTenant(ctx, "other", filepath.Join(t.TempDir(), "other"), false); err != nil {
		t.Fatalf("an unrelated root was refused: %v", err)
	}
}

// TestTenantDataRootCandidates_NamesEveryLocalPlace covers the three sources:
// the in-memory registration, the persisted record and the pool-derived path.
func TestTenantDataRootCandidates_NamesEveryLocalPlace(t *testing.T) {
	ctx := context.Background()
	reg, system := newPooledTestRegistry(t)
	bases, err := reg.PoolBasePaths(ctx)
	if err != nil || len(bases) != 1 {
		t.Fatalf("PoolBasePaths = %v, %v", bases, err)
	}

	dataRoot, _, err := reg.RegisterVolumeTenant(ctx, "acme", "", true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reg.TenantDataRootCandidates(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != filepath.Clean(dataRoot) {
		t.Fatalf("candidates = %v; want the one pool DataRoot, de-duplicated", got)
	}

	// A record naming a different directory is reported beside the pool path.
	elsewhere := filepath.Join(t.TempDir(), "moved")
	rec, _ := json.Marshal(tenantRootRecord{DataRoot: elsewhere})
	if err := system.Put(ctx, tenantRootStorePrefix+"ghost", rec); err != nil {
		t.Fatal(err)
	}
	got, err = reg.TenantDataRootCandidates(ctx, "ghost")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{elsewhere: true, filepath.Join(bases[0], "ghost"): true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("candidates = %v; want %v", got, want)
	}

	if _, err := reg.TenantDataRootCandidates(ctx, ""); !errors.Is(err, ErrEmptyTenantID) {
		t.Fatalf("empty id = %v; want ErrEmptyTenantID", err)
	}
}

// TestTenantDataRootCandidates_WithoutAPoolUsesOnlyTheRecords: no pool, no
// pool-derived path; a sealed system barrier contributes no record.
func TestTenantDataRootCandidates_WithoutAPoolUsesOnlyTheRecords(t *testing.T) {
	system, err := NewStorageBarrier(testLogger(), NewMemoryBackend(),
		BarrierConfig{RootKeyPath: "system/root-key"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewBarrierRegistry(testLogger(), system)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reg.TenantDataRootCandidates(context.Background(), "acme")
	if err != nil || len(got) != 0 {
		t.Fatalf("candidates = %v, %v; want none", got, err)
	}
}

// TestTenantDataRootCandidates_ReturnsRecordsAsRecorded: a candidate is
// returned exactly as recorded, never cleaned. "<dir>/a/.." cleans to "<dir>",
// a different directory that exists, so cleaning it here would hand a caller
// the parent to judge instead of the record. Duplicates are still collapsed on
// the cleaned form.
func TestTenantDataRootCandidates_ReturnsRecordsAsRecorded(t *testing.T) {
	ctx := context.Background()
	reg, system := newPooledTestRegistry(t)
	bases, err := reg.PoolBasePaths(ctx)
	if err != nil || len(bases) != 1 {
		t.Fatalf("PoolBasePaths = %v, %v", bases, err)
	}

	dotted := filepath.Join(t.TempDir(), "a") + "/.."
	rec, _ := json.Marshal(tenantRootRecord{DataRoot: dotted})
	if err := system.Put(ctx, tenantRootStorePrefix+"dotted", rec); err != nil {
		t.Fatal(err)
	}
	got, err := reg.TenantDataRootCandidates(ctx, "dotted")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != dotted {
		t.Fatalf("candidates = %v; want the record %q as recorded, then the pool path", got, dotted)
	}

	// A record naming the pool path with a trailing separator is the same
	// directory: it is reported once, in the form the record holds.
	trailing := filepath.Join(bases[0], "twin") + "/"
	rec, _ = json.Marshal(tenantRootRecord{DataRoot: trailing})
	if err := system.Put(ctx, tenantRootStorePrefix+"twin", rec); err != nil {
		t.Fatal(err)
	}
	got, err = reg.TenantDataRootCandidates(ctx, "twin")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != trailing {
		t.Fatalf("candidates = %v; want only %q", got, trailing)
	}
}

// TestHasTenantState_TracksEveryHoldingOfAnId walks one id through each kind
// of state the registry can hold for it -- the in-memory barrier, the
// tenant-root record, the slot assignment -- and back to none.
func TestHasTenantState_TracksEveryHoldingOfAnId(t *testing.T) {
	ctx := context.Background()
	reg, _ := newPooledTestRegistry(t)

	held, err := reg.HasTenantState(ctx, "acme")
	if err != nil || held {
		t.Fatalf("before registration = %v, %v; want false", held, err)
	}
	if _, _, err := reg.RegisterVolumeTenant(ctx, "acme", "", true); err != nil {
		t.Fatal(err)
	}
	if held, err = reg.HasTenantState(ctx, "acme"); err != nil || !held {
		t.Fatalf("registered = %v, %v; want true", held, err)
	}
	if err := reg.ReleaseTenant(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if held, err = reg.HasTenantState(ctx, "acme"); err != nil || !held {
		t.Fatalf("released, records kept = %v, %v; want true", held, err)
	}
	if err := reg.DropTenantRecords(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if held, err = reg.HasTenantState(ctx, "acme"); err != nil || held {
		t.Fatalf("records dropped = %v, %v; want false", held, err)
	}
	if _, err := reg.HasTenantState(ctx, ""); !errors.Is(err, ErrEmptyTenantID) {
		t.Fatalf("empty id = %v; want ErrEmptyTenantID", err)
	}
}

// TestHasTenantState_SealedSystemBarrierStillSeesTheSlot: with the system
// barrier sealed the record is unreadable, so the answer rests on the slot
// assignment, which lives outside it in the replicated slot store.
func TestHasTenantState_SealedSystemBarrierStillSeesTheSlot(t *testing.T) {
	ctx := context.Background()
	reg, system := newPooledTestRegistry(t, WithReplicatedSlotStore(NewMemoryBackend()))
	if _, _, err := reg.RegisterVolumeTenant(ctx, "acme", "", true); err != nil {
		t.Fatal(err)
	}
	if err := reg.ReleaseTenant(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if err := system.Seal(); err != nil {
		t.Fatal(err)
	}
	held, err := reg.HasTenantState(ctx, "acme")
	if err != nil || !held {
		t.Fatalf("sealed system, slot held = %v, %v; want true", held, err)
	}
}

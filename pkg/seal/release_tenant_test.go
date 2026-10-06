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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// TestReleaseTenant_ThenDropRecords_KeepsTheBytesUntilTheCallerDecides pins
// the two halves DestroyTenant is built from. ReleaseTenant forgets the
// in-memory barrier and changes nothing durable -- the catalog Assignment
// naming the DataRoot survives it, which is what lets a retried reclamation
// find a directory whose removal failed. DropTenantRecords then removes the
// records the boot path restores from.
func TestReleaseTenant_ThenDropRecords_KeepsTheBytesUntilTheCallerDecides(t *testing.T) {
	ctx := context.Background()
	reg, _ := newPooledTestRegistry(t)
	allocator, _ := reg.volumeResources()
	reserveTestVolumeTenant(t, reg, "acme")
	dataRoot, _, err := reg.RegisterVolumeTenant(ctx, "acme", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}

	if err := reg.ReleaseTenant(ctx, "acme"); err != nil {
		t.Fatalf("ReleaseTenant: %v", err)
	}
	if _, err := reg.GetTenant("acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("barrier still registered: %v", err)
	}
	if _, err := allocator.Resolve(ctx, "acme"); err != nil {
		t.Fatalf("ReleaseTenant removed the durable catalog assignment: %v", err)
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
	var notFound *volume.AssignmentNotFoundError
	if _, err := allocator.Resolve(ctx, "acme"); !errors.As(err, &notFound) {
		t.Fatalf("catalog assignment survived DropTenantRecords: %v", err)
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

// TestTenantDataRootCandidates_NamesTheResolvedAssignment covers the
// single-candidate contract: a tenant with a catalog Assignment reports
// exactly the one directory RequireInsideLocalPool resolves; a tenant with
// no Assignment reports no candidates (nil, nil) rather than an error.
func TestTenantDataRootCandidates_NamesTheResolvedAssignment(t *testing.T) {
	ctx := context.Background()
	reg, _ := newPooledTestRegistry(t)

	got, err := reg.TenantDataRootCandidates(ctx, "never-registered")
	if err != nil || len(got) != 0 {
		t.Fatalf("candidates for an unassigned tenant = %v, %v; want (nil, nil)", got, err)
	}

	reserveTestVolumeTenant(t, reg, "acme")
	dataRoot, _, err := reg.RegisterVolumeTenant(ctx, "acme", true)
	if err != nil {
		t.Fatal(err)
	}
	got, err = reg.TenantDataRootCandidates(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != filepath.Clean(dataRoot) {
		t.Fatalf("candidates = %v; want the one resolved DataRoot %q", got, dataRoot)
	}

	if _, err := reg.TenantDataRootCandidates(ctx, ""); !errors.Is(err, ErrEmptyTenantID) {
		t.Fatalf("empty id = %v; want ErrEmptyTenantID", err)
	}
}

// TestTenantDataRootCandidates_WithoutAPoolReturnsError verifies that with no
// volume allocator/resolver configured, TenantDataRootCandidates returns the
// underlying ErrPoolNotConfigured rather than silently reporting no
// candidates -- a caller deciding what is safe to reclaim must not read "not
// configured" as "nothing here".
func TestTenantDataRootCandidates_WithoutAPoolReturnsError(t *testing.T) {
	system, err := NewStorageBarrier(testLogger(), NewMemoryBackend(),
		BarrierConfig{RootKeyPath: "system/root-key"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewBarrierRegistry(testLogger(), system)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reg.TenantDataRootCandidates(context.Background(), "acme")
	if !errors.Is(err, ErrPoolNotConfigured) {
		t.Fatalf("candidates without a pool = %v; want ErrPoolNotConfigured", err)
	}
}

// TestHasTenantState_TracksEveryHoldingOfAnId walks one id through each kind
// of state the registry can hold for it -- the in-memory barrier and the
// catalog Assignment -- and back to none.
func TestHasTenantState_TracksEveryHoldingOfAnId(t *testing.T) {
	ctx := context.Background()
	reg, _ := newPooledTestRegistry(t)

	held, err := reg.HasTenantState(ctx, "acme")
	if err != nil || held {
		t.Fatalf("before registration = %v, %v; want false", held, err)
	}
	reserveTestVolumeTenant(t, reg, "acme")
	if _, _, err := reg.RegisterVolumeTenant(ctx, "acme", true); err != nil {
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

// TestHasTenantState_SealedSystemBarrierStillSeesTheAssignment: the catalog
// Assignment lives in the replicated system KV, not the system barrier's own
// store, so sealing the system barrier must not hide it -- the answer rests
// on the catalog, which HasTenantState consults regardless of seal state.
func TestHasTenantState_SealedSystemBarrierStillSeesTheAssignment(t *testing.T) {
	ctx := context.Background()
	reg, _ := newPooledTestRegistry(t)
	reserveTestVolumeTenant(t, reg, "acme")
	if _, _, err := reg.RegisterVolumeTenant(ctx, "acme", true); err != nil {
		t.Fatal(err)
	}
	if err := reg.ReleaseTenant(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if err := reg.SystemBarrier().Seal(); err != nil {
		t.Fatal(err)
	}
	held, err := reg.HasTenantState(ctx, "acme")
	if err != nil || !held {
		t.Fatalf("sealed system, assignment held = %v, %v; want true", held, err)
	}
}

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

package quota

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEntitlements_AllowsIsolation covers membership and absence.
func TestEntitlements_AllowsIsolation(t *testing.T) {
	e := &Entitlements{AllowedIsolationModes: []string{IsolationModePrefix, IsolationModeVolume}}
	if !e.AllowsIsolation(IsolationModePrefix) {
		t.Errorf("AllowsIsolation(prefix) = false, want true")
	}
	if !e.AllowsIsolation(IsolationModeVolume) {
		t.Errorf("AllowsIsolation(volume) = false, want true")
	}
	if e.AllowsIsolation(IsolationModeRaft) {
		t.Errorf("AllowsIsolation(raft) = true, want false")
	}
}

// TestEntitlements_AllowsFeature covers every known feature and the fail-closed
// default for an unknown feature.
func TestEntitlements_AllowsFeature(t *testing.T) {
	e := &Entitlements{
		AllowDedicatedRaft:        true,
		AllowVolumeStorage:        true,
		AllowSystemManagedBarrier: true,
		AllowDedicatedWAL:         true,
	}
	for _, f := range []string{FeatureDedicatedRaft, FeatureVolumeStorage, FeatureSystemManagedBarrier, FeatureDedicatedWAL} {
		if !e.AllowsFeature(f) {
			t.Errorf("AllowsFeature(%q) = false, want true", f)
		}
	}
	if e.AllowsFeature("nonexistent_feature") {
		t.Errorf("AllowsFeature(unknown) = true, want false (fail closed)")
	}

	denied := &Entitlements{}
	for _, f := range []string{FeatureDedicatedRaft, FeatureVolumeStorage, FeatureSystemManagedBarrier, FeatureDedicatedWAL} {
		if denied.AllowsFeature(f) {
			t.Errorf("AllowsFeature(%q) on zero-value = true, want false", f)
		}
	}
}

// TestDefaultEntitlements returns prefix-only entitlements as a fresh value.
func TestDefaultEntitlements(t *testing.T) {
	d := DefaultEntitlements()
	if !d.AllowsIsolation(IsolationModePrefix) {
		t.Fatalf("default must allow prefix isolation")
	}
	if d.AllowsIsolation(IsolationModeVolume) || d.AllowsIsolation(IsolationModeRaft) {
		t.Fatalf("default must deny volume/raft isolation")
	}
	if d.AllowDedicatedRaft || d.AllowVolumeStorage || d.AllowSystemManagedBarrier {
		t.Fatalf("default must deny dedicated features")
	}
	// Each call returns an independent value safe to mutate.
	d.AllowedIsolationModes = append(d.AllowedIsolationModes, IsolationModeRaft)
	if DefaultEntitlements().AllowsIsolation(IsolationModeRaft) {
		t.Fatalf("DefaultEntitlements returned shared state")
	}
}

// TestEntitlementError_Error covers both the feature and mode render branches.
func TestEntitlementError_Error(t *testing.T) {
	feat := &EntitlementError{TenantID: "acme", Feature: FeatureDedicatedRaft}
	if got := feat.Error(); !strings.Contains(got, "feature: "+FeatureDedicatedRaft) || !strings.Contains(got, "acme") {
		t.Fatalf("feature Error() = %q", got)
	}
	mode := &EntitlementError{TenantID: "acme", Mode: IsolationModeVolume}
	if got := mode.Error(); !strings.Contains(got, "isolation mode: "+IsolationModeVolume) || !strings.Contains(got, "acme") {
		t.Fatalf("mode Error() = %q", got)
	}
}

// TestAccountant_LoadBaseToleratesLookupFailure verifies a quota-lookup error is
// treated as a zero base so the write path is never blocked.
func TestAccountant_LoadBaseToleratesLookupFailure(t *testing.T) {
	errQuotas := func(context.Context, string) (*Quotas, error) {
		return nil, errors.New("lookup boom")
	}
	a, err := NewAccountant(AccountantConfig{
		Quotas: errQuotas,
		Flush:  func(context.Context, string, int64) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewAccountant: %v", err)
	}
	// Reserve drives loadBase; the failed lookup yields base 0, so this admits.
	if err := a.Reserve(context.Background(), "t1", 100, 50); err != nil {
		t.Fatalf("Reserve after lookup failure: %v", err)
	}
	if got := a.Usage("t1"); got != 50 {
		t.Fatalf("usage = %d, want 50 (base treated as zero)", got)
	}
}

// TestAccountant_LoadBaseNilQuotas covers the nil-quotas branch of loadBase.
func TestAccountant_LoadBaseNilQuotas(t *testing.T) {
	a, err := NewAccountant(AccountantConfig{
		Quotas: func(context.Context, string) (*Quotas, error) { return nil, nil },
		Flush:  func(context.Context, string, int64) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewAccountant: %v", err)
	}
	if err := a.Reserve(context.Background(), "t1", 0, 10); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if got := a.Usage("t1"); got != 10 {
		t.Fatalf("usage = %d, want 10", got)
	}
}

// TestReconciler_RunOnceExported verifies the exported RunOnce drives a cycle.
func TestReconciler_RunOnceExported(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f"), make([]byte, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	fu := newFakeUsage()
	a := newTestAccountant(t, fu)
	rec, err := NewReconciler(ReconcilerConfig{
		Accountant: a,
		Tenants: func(context.Context) ([]StorageTenant, error) {
			return []StorageTenant{{ID: "vol", DataRoot: root, Volume: true}}, nil
		},
		SetUsage: fu.flushSet,
	})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	rec.RunOnce(context.Background())
	if got := fu.persisted("vol"); got != 64 {
		t.Fatalf("persisted = %d, want 64", got)
	}
}

// TestReconciler_TenantEnumerationError covers the early-return warn path when
// the tenant lister fails.
func TestReconciler_TenantEnumerationError(t *testing.T) {
	fu := newFakeUsage()
	a := newTestAccountant(t, fu)
	rec, err := NewReconciler(ReconcilerConfig{
		Accountant: a,
		Tenants: func(context.Context) ([]StorageTenant, error) {
			return nil, errors.New("enumeration boom")
		},
		SetUsage: fu.flushSet,
	})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	// Must not panic and must return cleanly despite the enumeration error.
	rec.RunOnce(context.Background())
}

// TestReconciler_SetUsageError covers the warn+continue path when persisting a
// volume tenant's corrected usage fails.
func TestReconciler_SetUsageError(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f"), make([]byte, 128), 0o600); err != nil {
		t.Fatal(err)
	}
	fu := newFakeUsage()
	a := newTestAccountant(t, fu)
	rec, err := NewReconciler(ReconcilerConfig{
		Accountant: a,
		Tenants: func(context.Context) ([]StorageTenant, error) {
			return []StorageTenant{{ID: "vol", DataRoot: root, Volume: true}}, nil
		},
		SetUsage: func(context.Context, string, int64) error {
			return errors.New("persist boom")
		},
	})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	rec.RunOnce(context.Background())
	// The base must NOT have been realigned because SetUsage failed.
	if got := a.Usage("vol"); got != 0 {
		t.Fatalf("usage = %d, want 0 (SetUsage failed, base unchanged)", got)
	}
}

// TestReconciler_VolumeTenantWithoutDataRootSkipped covers the volume-but-empty
// DataRoot guard (treated like a prefix tenant).
func TestReconciler_VolumeTenantWithoutDataRootSkipped(t *testing.T) {
	fu := newFakeUsage()
	a := newTestAccountant(t, fu)
	setCalled := false
	rec, err := NewReconciler(ReconcilerConfig{
		Accountant: a,
		Tenants: func(context.Context) ([]StorageTenant, error) {
			return []StorageTenant{{ID: "vol", DataRoot: "", Volume: true}}, nil
		},
		SetUsage: func(ctx context.Context, id string, b int64) error {
			setCalled = true
			return fu.flushSet(ctx, id, b)
		},
	})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	rec.RunOnce(context.Background())
	if setCalled {
		t.Fatal("SetUsage must not be called for a volume tenant without a DataRoot")
	}
}

// TestDirSize_WalkError surfaces a non-root walk error (a directory whose
// children cannot be read) rather than silently under-counting.
func TestDirSize_WalkError(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.MkdirAll(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "f"), make([]byte, 8), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })

	_, err := dirSize(root)
	if os.Geteuid() == 0 {
		// root bypasses the permission bits; the walk succeeds and the error
		// branch is not reachable in this environment.
		return
	}
	if err == nil {
		t.Fatalf("dirSize over an unreadable subdirectory returned nil error")
	}
}

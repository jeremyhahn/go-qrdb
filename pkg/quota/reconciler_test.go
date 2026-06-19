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
	"testing"
	"time"
)

func TestNewReconciler_Validation(t *testing.T) {
	fu := newFakeUsage()
	a := newTestAccountant(t, fu)
	list := func(context.Context) ([]StorageTenant, error) { return nil, nil }
	set := func(context.Context, string, int64) error { return nil }

	if _, err := NewReconciler(ReconcilerConfig{Tenants: list, SetUsage: set}); !errors.Is(err, ErrNilAccountant) {
		t.Fatalf("expected ErrNilAccountant, got %v", err)
	}
	if _, err := NewReconciler(ReconcilerConfig{Accountant: a, SetUsage: set}); !errors.Is(err, ErrNilTenantLister) {
		t.Fatalf("expected ErrNilTenantLister, got %v", err)
	}
	if _, err := NewReconciler(ReconcilerConfig{Accountant: a, Tenants: list}); !errors.Is(err, ErrNilUsageSetter) {
		t.Fatalf("expected ErrNilUsageSetter, got %v", err)
	}
	r, err := NewReconciler(ReconcilerConfig{Accountant: a, Tenants: list, SetUsage: set})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	if r.interval != DefaultReconcileInterval {
		t.Fatalf("interval = %v, want default %v", r.interval, DefaultReconcileInterval)
	}
}

func TestDirSize(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.dat"), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.dat"), make([]byte, 250), 0o600); err != nil {
		t.Fatal(err)
	}
	size, err := dirSize(root)
	if err != nil {
		t.Fatalf("dirSize: %v", err)
	}
	if size != 350 {
		t.Fatalf("size = %d, want 350", size)
	}
}

func TestDirSize_MissingRoot(t *testing.T) {
	size, err := dirSize(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("dirSize missing root: %v", err)
	}
	if size != 0 {
		t.Fatalf("size = %d, want 0", size)
	}
}

func TestReconciler_CorrectsVolumeTenant(t *testing.T) {
	// Lay down 300 bytes of real data under the tenant's DataRoot.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data.bin"), make([]byte, 300), 0o600); err != nil {
		t.Fatal(err)
	}

	fu := newFakeUsage()
	// Deliberately wrong persisted counter that the reconciler must correct.
	fu.current["vol"] = 9999
	a := newTestAccountant(t, fu)

	rec, err := NewReconciler(ReconcilerConfig{
		Accountant: a,
		Interval:   time.Hour, // irrelevant; we drive runOnce directly
		Tenants: func(context.Context) ([]StorageTenant, error) {
			return []StorageTenant{{ID: "vol", DataRoot: root, Volume: true}}, nil
		},
		SetUsage: fu.flushSet,
	})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}

	rec.runOnce(context.Background())

	if got := fu.persisted("vol"); got != 300 {
		t.Fatalf("persisted = %d, want 300 (corrected to true on-disk size)", got)
	}
	if got := a.Usage("vol"); got != 300 {
		t.Fatalf("accountant base = %d, want 300", got)
	}
}

func TestReconciler_SkipsPrefixTenant(t *testing.T) {
	fu := newFakeUsage()
	fu.current["pfx"] = 123
	a := newTestAccountant(t, fu)
	// Accumulate a pending delta for the prefix tenant; the reconciler flushes
	// it (the documented approximation) but does NOT overwrite via SetUsage.
	if err := a.Reserve(context.Background(), "pfx", 0, 40); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	setCalled := false
	rec, err := NewReconciler(ReconcilerConfig{
		Accountant: a,
		Tenants: func(context.Context) ([]StorageTenant, error) {
			return []StorageTenant{{ID: "pfx", Volume: false}}, nil
		},
		SetUsage: func(ctx context.Context, id string, b int64) error {
			setCalled = true
			return fu.flushSet(ctx, id, b)
		},
	})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	rec.runOnce(context.Background())

	if setCalled {
		t.Fatal("SetUsage must not be called for prefix tenants")
	}
	// The pending delta was flushed: 123 + 40 = 163.
	if got := fu.persisted("pfx"); got != 163 {
		t.Fatalf("persisted = %d, want 163 (flushed accumulator)", got)
	}
}

func TestReconciler_RunFinalFlushOnCancel(t *testing.T) {
	fu := newFakeUsage()
	a := newTestAccountant(t, fu)
	if err := a.Reserve(context.Background(), "t1", 0, 77); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	rec, err := NewReconciler(ReconcilerConfig{
		Accountant: a,
		Interval:   time.Hour,
		Tenants:    func(context.Context) ([]StorageTenant, error) { return nil, nil },
		SetUsage:   fu.flushSet,
	})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rec.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if got := fu.persisted("t1"); got != 77 {
		t.Fatalf("persisted after final flush = %d, want 77", got)
	}
}

func TestReconciler_RunTicks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f"), make([]byte, 42), 0o600); err != nil {
		t.Fatal(err)
	}
	fu := newFakeUsage()
	fu.current["vol"] = 0
	a := newTestAccountant(t, fu)
	rec, err := NewReconciler(ReconcilerConfig{
		Accountant: a,
		Interval:   20 * time.Millisecond,
		Tenants: func(context.Context) ([]StorageTenant, error) {
			return []StorageTenant{{ID: "vol", DataRoot: root, Volume: true}}, nil
		},
		SetUsage: fu.flushSet,
	})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rec.Run(ctx)

	deadline := time.After(2 * time.Second)
	for {
		if fu.persisted("vol") == 42 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("reconciler never corrected usage; persisted = %d", fu.persisted("vol"))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// flushSet overwrites the persisted counter (used as the reconciler SetUsage).
func (f *fakeUsage) flushSet(_ context.Context, tenantID string, bytes int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.current[tenantID] = bytes
	return nil
}

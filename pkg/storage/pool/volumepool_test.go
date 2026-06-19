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

package pool_test

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
)

// TestBuildPoolStrategy_Defaults verifies the empty strategy string and the
// explicit "round_robin" name both yield a RoundRobinStrategy bound to the
// supplied state store.
func TestBuildPoolStrategy_Defaults(t *testing.T) {
	state, _, _ := newTempStateStore(t)

	for _, name := range []string{"", "round_robin"} {
		t.Run("name="+name, func(t *testing.T) {
			strategy, err := qrdbpool.BuildPoolStrategy(name, state)
			if err != nil {
				t.Fatalf("BuildPoolStrategy(%q): %v", name, err)
			}
			rr, ok := strategy.(*objpool.RoundRobinStrategy)
			if !ok {
				t.Fatalf("strategy = %T, want *objpool.RoundRobinStrategy", strategy)
			}
			if rr.State != state {
				t.Error("RoundRobinStrategy.State not bound to supplied state store")
			}
		})
	}
}

// TestBuildPoolStrategy_LeastUtilization verifies the stub strategy is
// constructible from config so YAML references survive startup.
func TestBuildPoolStrategy_LeastUtilization(t *testing.T) {
	state, _, _ := newTempStateStore(t)

	strategy, err := qrdbpool.BuildPoolStrategy("least_utilization", state)
	if err != nil {
		t.Fatalf("BuildPoolStrategy(least_utilization): %v", err)
	}
	if _, ok := strategy.(*objpool.LeastUtilizationStrategy); !ok {
		t.Errorf("strategy = %T, want *objpool.LeastUtilizationStrategy", strategy)
	}
}

// TestBuildPoolStrategy_Unknown verifies an unknown name yields a
// VolumePoolStrategyError carrying the offending name.
func TestBuildPoolStrategy_Unknown(t *testing.T) {
	state, _, _ := newTempStateStore(t)

	_, err := qrdbpool.BuildPoolStrategy("quantum_random", state)
	var se *qrdbpool.VolumePoolStrategyError
	if !errors.As(err, &se) {
		t.Fatalf("expected *VolumePoolStrategyError, got %T: %v", err, err)
	}
	if se.Strategy != "quantum_random" {
		t.Errorf("Strategy = %q, want %q", se.Strategy, "quantum_random")
	}
	if !strings.Contains(se.Error(), "unsupported") {
		t.Errorf("Error() = %q, missing %q", se.Error(), "unsupported")
	}
}

// TestBuildPoolStrategy_NilState verifies a nil state store is rejected with
// the dedicated NilStateStoreError regardless of strategy name.
func TestBuildPoolStrategy_NilState(t *testing.T) {
	for _, name := range []string{"", "round_robin", "least_utilization", "bogus"} {
		t.Run("name="+name, func(t *testing.T) {
			_, err := qrdbpool.BuildPoolStrategy(name, nil)
			var ns *qrdbpool.NilStateStoreError
			if !errors.As(err, &ns) {
				t.Fatalf("expected *NilStateStoreError, got %T: %v", err, err)
			}
			if ns.Error() == "" {
				t.Error("NilStateStoreError.Error() must not be empty")
			}
		})
	}
}

// TestBuildLocalCandidates_Success verifies one local Storage per path is
// configured and the volume roots are created with owner-only permissions.
func TestBuildLocalCandidates_Success(t *testing.T) {
	base := t.TempDir()
	paths := []string{
		filepath.Join(base, "vol-a"),
		filepath.Join(base, "vol-b"),
		filepath.Join(base, "nested", "vol-c"),
	}

	candidates, err := qrdbpool.BuildLocalCandidates("default", paths)
	if err != nil {
		t.Fatalf("BuildLocalCandidates: %v", err)
	}
	if len(candidates) != len(paths) {
		t.Fatalf("got %d candidates, want %d", len(candidates), len(paths))
	}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %q: %v", p, err)
		}
		if !info.IsDir() {
			t.Errorf("%q is not a directory", p)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("%q perm = %o, want 0700", p, perm)
		}
	}
}

// TestBuildLocalCandidates_MkdirFailure verifies a path that cannot be
// created (a child of an existing regular file) is reported as a
// VolumePoolPathError carrying the pool and path.
func TestBuildLocalCandidates_MkdirFailure(t *testing.T) {
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	bad := filepath.Join(blocker, "vol")

	_, err := qrdbpool.BuildLocalCandidates("default", []string{bad})
	var pe *qrdbpool.VolumePoolPathError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *VolumePoolPathError, got %T: %v", err, err)
	}
	if pe.Pool != "default" {
		t.Errorf("Pool = %q, want %q", pe.Pool, "default")
	}
	if pe.Path != bad {
		t.Errorf("Path = %q, want %q", pe.Path, bad)
	}
	if pe.Unwrap() == nil {
		t.Error("VolumePoolPathError.Unwrap() must not be nil")
	}
}

// TestBuildLocalCandidates_Empty verifies zero paths yield an empty slice
// without error; objstore's Register rejects the empty candidate list later.
func TestBuildLocalCandidates_Empty(t *testing.T) {
	candidates, err := qrdbpool.BuildLocalCandidates("default", nil)
	if err != nil {
		t.Fatalf("BuildLocalCandidates(nil paths): %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("got %d candidates, want 0", len(candidates))
	}
}

// TestBuildVolumePoolSubsystem_NoPools verifies the documented contract:
// no pools configured means a nil subsystem and nil error so callers run
// unchanged.
func TestBuildVolumePoolSubsystem_NoPools(t *testing.T) {
	for _, pools := range [][]qrdbconfig.VolumePoolConfig{nil, {}} {
		sub, err := qrdbpool.BuildVolumePoolSubsystem(t.TempDir(), pools, nil, false)
		if err != nil {
			t.Fatalf("BuildVolumePoolSubsystem: %v", err)
		}
		if sub != nil {
			t.Errorf("subsystem = %+v, want nil", sub)
		}
	}
}

// TestBuildVolumePoolSubsystem_Success verifies a single round-robin pool
// produces a fully wired subsystem: manager, state store, per-tenant
// registry, default pool name, and a dedicated <dataDir>/volumepool Pebble.
func TestBuildVolumePoolSubsystem_Success(t *testing.T) {
	dataDir := t.TempDir()
	pools := []qrdbconfig.VolumePoolConfig{
		{
			Name:     "default",
			Strategy: "round_robin",
			Paths: []string{
				filepath.Join(dataDir, "vols", "a"),
				filepath.Join(dataDir, "vols", "b"),
			},
		},
	}

	sub, err := qrdbpool.BuildVolumePoolSubsystem(dataDir, pools, slog.New(slog.DiscardHandler), false)
	if err != nil {
		t.Fatalf("BuildVolumePoolSubsystem: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })

	if sub.Manager == nil {
		t.Error("Manager must not be nil")
	}
	if sub.StateStore == nil {
		t.Error("StateStore must not be nil")
	}
	if sub.PerTenantRegistry == nil {
		t.Error("PerTenantRegistry must not be nil")
	}
	if sub.DefaultPoolName != "default" {
		t.Errorf("DefaultPoolName = %q, want %q", sub.DefaultPoolName, "default")
	}
	info, err := os.Stat(filepath.Join(dataDir, "volumepool"))
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if !info.IsDir() {
		t.Error("<dataDir>/volumepool must be a directory")
	}
}

// TestBuildVolumePoolSubsystem_Clustered verifies clustered mode wires a
// ReplicatedState (unbound) for the cursor while still opening the node-local
// Pebble StateStore as a lifetime-pinned handle. Standalone mode leaves
// ReplicatedState nil.
func TestBuildVolumePoolSubsystem_Clustered(t *testing.T) {
	dataDir := t.TempDir()
	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "default", Strategy: "round_robin", Paths: []string{filepath.Join(dataDir, "vols", "a")}},
	}

	clustered, err := qrdbpool.BuildVolumePoolSubsystem(dataDir, pools, nil, true)
	if err != nil {
		t.Fatalf("BuildVolumePoolSubsystem(clustered): %v", err)
	}
	t.Cleanup(func() { _ = clustered.Close() })
	if clustered.ReplicatedState == nil {
		t.Error("clustered subsystem must expose a ReplicatedState")
	}
	if clustered.ReplicatedState != nil && clustered.ReplicatedState.Bound() {
		t.Error("clustered ReplicatedState must start unbound")
	}
	if clustered.StateStore == nil {
		t.Error("clustered subsystem must still open the node-local StateStore")
	}

	standalone, err := qrdbpool.BuildVolumePoolSubsystem(t.TempDir(), pools, nil, false)
	if err != nil {
		t.Fatalf("BuildVolumePoolSubsystem(standalone): %v", err)
	}
	t.Cleanup(func() { _ = standalone.Close() })
	if standalone.ReplicatedState != nil {
		t.Error("standalone subsystem must not expose a ReplicatedState")
	}
}

// TestBuildVolumePoolSubsystem_ManagerPick verifies the wired manager hands
// out path-accessible candidates from the registered pool — the LocalOnly
// wrapper guarantees common.PathAccessor — and the round-robin cursor
// rotates across the configured volumes.
func TestBuildVolumePoolSubsystem_ManagerPick(t *testing.T) {
	dataDir := t.TempDir()
	volA := filepath.Join(dataDir, "vols", "a")
	volB := filepath.Join(dataDir, "vols", "b")
	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "default", Strategy: "round_robin", Paths: []string{volA, volB}},
	}

	sub, err := qrdbpool.BuildVolumePoolSubsystem(dataDir, pools, nil, false)
	if err != nil {
		t.Fatalf("BuildVolumePoolSubsystem: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })

	seen := map[string]bool{}
	for i := range 2 {
		picked, err := sub.Manager.Pick(t.Context(), "default", objpool.Hint{})
		if err != nil {
			t.Fatalf("Pick %d: %v", i, err)
		}
		accessor, ok := picked.(common.PathAccessor)
		if !ok {
			t.Fatalf("Pick %d returned %T, want common.PathAccessor", i, picked)
		}
		seen[accessor.LocalPath()] = true
	}
	if !seen[volA] || !seen[volB] {
		t.Errorf("round robin did not rotate across both volumes: %v", seen)
	}
}

// TestBuildVolumePoolSubsystem_UnknownStrategy verifies an unknown strategy
// fails construction with a VolumePoolStrategyError and does not leak a
// half-built subsystem.
func TestBuildVolumePoolSubsystem_UnknownStrategy(t *testing.T) {
	dataDir := t.TempDir()
	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "default", Strategy: "bogus", Paths: []string{filepath.Join(dataDir, "v")}},
	}

	sub, err := qrdbpool.BuildVolumePoolSubsystem(dataDir, pools, nil, false)
	var se *qrdbpool.VolumePoolStrategyError
	if !errors.As(err, &se) {
		t.Fatalf("expected *VolumePoolStrategyError, got %T: %v", err, err)
	}
	if sub != nil {
		t.Errorf("subsystem = %+v, want nil on error", sub)
	}
}

// TestBuildVolumePoolSubsystem_BadPath verifies a pool path that cannot be
// created surfaces as a VolumePoolPathError and the state Pebble is not
// leaked (a second build at the same dataDir succeeds, proving the lock
// was released).
func TestBuildVolumePoolSubsystem_BadPath(t *testing.T) {
	dataDir := t.TempDir()
	blocker := filepath.Join(dataDir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "default", Strategy: "round_robin", Paths: []string{filepath.Join(blocker, "v")}},
	}

	sub, err := qrdbpool.BuildVolumePoolSubsystem(dataDir, pools, nil, false)
	var pe *qrdbpool.VolumePoolPathError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *VolumePoolPathError, got %T: %v", err, err)
	}
	if sub != nil {
		t.Errorf("subsystem = %+v, want nil on error", sub)
	}

	// The failed build must have closed its state Pebble; a retry with a
	// corrected config at the same dataDir must succeed.
	good := []qrdbconfig.VolumePoolConfig{
		{Name: "default", Strategy: "round_robin", Paths: []string{filepath.Join(dataDir, "v")}},
	}
	sub2, err := qrdbpool.BuildVolumePoolSubsystem(dataDir, good, nil, false)
	if err != nil {
		t.Fatalf("rebuild after failed build: %v", err)
	}
	t.Cleanup(func() { _ = sub2.Close() })
}

// TestBuildVolumePoolSubsystem_DuplicatePool verifies objstore's Register
// rejection (duplicate pool name) is wrapped in a VolumePoolRegisterError
// carrying the pool name.
func TestBuildVolumePoolSubsystem_DuplicatePool(t *testing.T) {
	dataDir := t.TempDir()
	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "dup", Strategy: "round_robin", Paths: []string{filepath.Join(dataDir, "a")}},
		{Name: "dup", Strategy: "round_robin", Paths: []string{filepath.Join(dataDir, "b")}},
	}

	sub, err := qrdbpool.BuildVolumePoolSubsystem(dataDir, pools, nil, false)
	var re *qrdbpool.VolumePoolRegisterError
	if !errors.As(err, &re) {
		t.Fatalf("expected *VolumePoolRegisterError, got %T: %v", err, err)
	}
	if re.Name != "dup" {
		t.Errorf("Name = %q, want %q", re.Name, "dup")
	}
	if re.Unwrap() == nil {
		t.Error("VolumePoolRegisterError.Unwrap() must not be nil")
	}
	if sub != nil {
		t.Errorf("subsystem = %+v, want nil on error", sub)
	}
}

// TestBuildVolumePoolSubsystem_StateDirCreateFailure verifies a dataDir whose
// volumepool state directory cannot be created yields a
// VolumePoolStorageError.
func TestBuildVolumePoolSubsystem_StateDirCreateFailure(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "datadir")
	if err := os.WriteFile(dataDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "default", Strategy: "round_robin", Paths: []string{filepath.Join(base, "v")}},
	}

	sub, err := qrdbpool.BuildVolumePoolSubsystem(dataDir, pools, nil, false)
	var se *qrdbpool.VolumePoolStorageError
	if !errors.As(err, &se) {
		t.Fatalf("expected *VolumePoolStorageError, got %T: %v", err, err)
	}
	if se.Unwrap() == nil {
		t.Error("VolumePoolStorageError.Unwrap() must not be nil")
	}
	if sub != nil {
		t.Errorf("subsystem = %+v, want nil on error", sub)
	}
}

// TestVolumePoolSubsystem_Close verifies Close is nil-safe at every layer:
// nil receiver, zero-value subsystem (nil state storage), and a real
// subsystem closing without error.
func TestVolumePoolSubsystem_Close(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var sub *qrdbpool.VolumePoolSubsystem
		if err := sub.Close(); err != nil {
			t.Errorf("Close on nil receiver: %v", err)
		}
	})

	t.Run("zero value", func(t *testing.T) {
		sub := &qrdbpool.VolumePoolSubsystem{}
		if err := sub.Close(); err != nil {
			t.Errorf("Close on zero value: %v", err)
		}
	})

	t.Run("real subsystem", func(t *testing.T) {
		dataDir := t.TempDir()
		pools := []qrdbconfig.VolumePoolConfig{
			{Name: "default", Strategy: "round_robin", Paths: []string{filepath.Join(dataDir, "v")}},
		}
		sub, err := qrdbpool.BuildVolumePoolSubsystem(dataDir, pools, nil, false)
		if err != nil {
			t.Fatalf("BuildVolumePoolSubsystem: %v", err)
		}
		if err := sub.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		// State store is unusable after Close — the underlying Pebble
		// is gone, proving Close released it.
		_, _, err = sub.StateStore.Get(t.Context(), "pool/default/round_robin/cursor")
		var ge *qrdbpool.GetError
		if !errors.As(err, &ge) {
			t.Errorf("Get after Close = %T (%v), want *GetError", err, err)
		}
	})
}

// TestVolumePoolErrors_Strings verifies the volumepool error types produce
// well-formed messages including their identifying fields.
func TestVolumePoolErrors_Strings(t *testing.T) {
	cause := errors.New("boom")

	t.Run("VolumePoolStorageError", func(t *testing.T) {
		e := &qrdbpool.VolumePoolStorageError{Err: cause}
		if !strings.Contains(e.Error(), "boom") {
			t.Errorf("Error() = %q, missing cause", e.Error())
		}
		if !errors.Is(e, cause) {
			t.Error("errors.Is must reach the cause via Unwrap")
		}
	})

	t.Run("VolumePoolRegisterError", func(t *testing.T) {
		e := &qrdbpool.VolumePoolRegisterError{Name: "p1", Err: cause}
		for _, want := range []string{"p1", "boom"} {
			if !strings.Contains(e.Error(), want) {
				t.Errorf("Error() = %q, missing %q", e.Error(), want)
			}
		}
		if !errors.Is(e, cause) {
			t.Error("errors.Is must reach the cause via Unwrap")
		}
	})

	t.Run("VolumePoolStrategyError wrapped", func(t *testing.T) {
		e := &qrdbpool.VolumePoolStrategyError{Strategy: "s1", Err: cause}
		for _, want := range []string{"s1", "boom"} {
			if !strings.Contains(e.Error(), want) {
				t.Errorf("Error() = %q, missing %q", e.Error(), want)
			}
		}
		if !errors.Is(e, cause) {
			t.Error("errors.Is must reach the cause via Unwrap")
		}
	})

	t.Run("VolumePoolStrategyError bare", func(t *testing.T) {
		e := &qrdbpool.VolumePoolStrategyError{Strategy: "s1"}
		for _, want := range []string{"s1", "unsupported"} {
			if !strings.Contains(e.Error(), want) {
				t.Errorf("Error() = %q, missing %q", e.Error(), want)
			}
		}
		if e.Unwrap() != nil {
			t.Error("Unwrap() must be nil when Err is nil")
		}
	})

	t.Run("VolumePoolPathError", func(t *testing.T) {
		e := &qrdbpool.VolumePoolPathError{Pool: "p1", Path: "/vols/a", Err: cause}
		for _, want := range []string{"p1", "/vols/a", "boom"} {
			if !strings.Contains(e.Error(), want) {
				t.Errorf("Error() = %q, missing %q", e.Error(), want)
			}
		}
		if !errors.Is(e, cause) {
			t.Error("errors.Is must reach the cause via Unwrap")
		}
	})
}

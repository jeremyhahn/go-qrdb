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
	"strings"
	"sync/atomic"
	"testing"

	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// errShardSettling stands in for the server layer's settling class: the
// error a system-shard read returns while no leader is known.
var errShardSettling = errors.New("test: system shard settling")

// settlingAllocator fails ListAll and Resolve with errShardSettling for as
// many calls as each counter holds, then delegates to the real catalog.
type settlingAllocator struct {
	volume.Allocator
	listFails    atomic.Int32
	resolveFails atomic.Int32
}

var _ volume.Allocator = (*settlingAllocator)(nil)

func (a *settlingAllocator) ListAll(ctx context.Context) ([]volume.Assignment, error) {
	if a.listFails.Add(-1) >= 0 {
		return nil, errShardSettling
	}
	return a.Allocator.ListAll(ctx)
}

func (a *settlingAllocator) Resolve(ctx context.Context, tenantID string) (*volume.Assignment, error) {
	if a.resolveFails.Add(-1) >= 0 {
		return nil, errShardSettling
	}
	return a.Allocator.Resolve(ctx, tenantID)
}

// settlingBackend fails a Get of a key ending in suffix with
// errShardSettling for as many calls as fails holds.
type settlingBackend struct {
	StorageBackend
	suffix string
	fails  atomic.Int32
}

func (b *settlingBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if strings.HasSuffix(key, b.suffix) && b.fails.Add(-1) >= 0 {
		return nil, errShardSettling
	}
	return b.StorageBackend.Get(ctx, key)
}

// settlingRetry returns a StartupReadRetry that re-runs a read failing with
// errShardSettling up to attempts times, recording each op it runs.
func settlingRetry(attempts int, ops *[]string) StartupReadRetry {
	return func(ctx context.Context, op string, read func(context.Context) error) error {
		var err error
		for range attempts {
			*ops = append(*ops, op)
			if err = read(ctx); !errors.Is(err, errShardSettling) {
				return err
			}
		}
		return err
	}
}

// restoreFixture is an auto-unseal volume tenant registered and initialized
// by a first registry, whose allocator and system backend a restoring
// registry reads through.
type restoreFixture struct {
	tenantID  string
	system    *StorageBarrier
	allocator *settlingAllocator
	backend   *settlingBackend
	opts      []Option
}

func newRestoreFixture(t *testing.T) *restoreFixture {
	t.Helper()
	ctx := context.Background()
	const tenantID = "vol-settling"

	backend := &settlingBackend{
		StorageBackend: NewMemoryBackend(),
		suffix:         tenantBarrierPostureStorePrefix + tenantID,
	}
	system, err := NewStorageBarrier(poolTestLogger(), backend, BarrierConfig{
		RootKeyPath: "system/root-key",
	}, newPoolSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := system.Initialize(ctx, MustCredentials(ctx, []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	catalog, open, _ := newTestVolumeFixture(t)
	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })
	allocator := &settlingAllocator{Allocator: catalog}
	opts := []Option{
		WithVolumeAllocator(allocator),
		WithVolumeResolver(open),
		WithPerTenantRegistry(perTenantReg),
	}

	reg1, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}
	reserveVolumeTenant(t, reg1, tenantID)
	if _, _, err := reg1.RegisterVolumeTenant(ctx, tenantID, true); err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	if err := reg1.InitializeTenant(ctx, tenantID, Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}
	return &restoreFixture{
		tenantID:  tenantID,
		system:    system,
		allocator: allocator,
		backend:   backend,
		opts:      opts,
	}
}

// restart builds the registry a restarted node constructs, with extra
// options after the fixture's.
func (f *restoreFixture) restart(t *testing.T, extra ...Option) *BarrierRegistry {
	t.Helper()
	reg, err := NewBarrierRegistry(poolTestLogger(), f.system, append(append([]Option(nil), f.opts...), extra...)...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (restart): %v", err)
	}
	return reg
}

// TestRestoreVolumeTenants_WaitsOutSettlingReads verifies that each of the
// restore's system-shard reads -- the catalog listing, the barrier posture
// and the re-attach's Assignment -- is re-run through the StartupReadRetry
// when it fails while the shard settles, so an auto-unseal tenant comes back
// unsealed instead of unrestored or sealed.
func TestRestoreVolumeTenants_WaitsOutSettlingReads(t *testing.T) {
	f := newRestoreFixture(t)
	f.allocator.listFails.Store(1)
	f.allocator.resolveFails.Store(1)
	f.backend.fails.Store(1)

	var ops []string
	reg := f.restart(t, WithStartupReadRetry(settlingRetry(3, &ops)))
	if err := reg.RestoreVolumeTenants(context.Background()); err != nil {
		t.Fatalf("RestoreVolumeTenants: %v", err)
	}

	tb, err := reg.GetTenant(f.tenantID)
	if err != nil {
		t.Fatalf("GetTenant after RestoreVolumeTenants: %v", err)
	}
	if tb.IsSealed() {
		t.Error("auto-unseal tenant must be unsealed once its settling posture read is retried")
	}
	if f.allocator.listFails.Load() >= 0 || f.allocator.resolveFails.Load() >= 0 || f.backend.fails.Load() >= 0 {
		t.Fatalf("a scripted settling failure was never reached: list=%d resolve=%d posture=%d",
			f.allocator.listFails.Load(), f.allocator.resolveFails.Load(), f.backend.fails.Load())
	}
	attempts := map[string]int{}
	for _, op := range ops {
		attempts[op]++
	}
	for _, op := range []string{
		"volume tenant restore: catalog list",
		"volume tenant restore: barrier posture",
		"volume tenant restore: re-attach",
	} {
		if attempts[op] != 2 {
			t.Errorf("%q ran %d times, want 2 (one settling failure, one success)", op, attempts[op])
		}
	}
}

// TestRestoreVolumeTenants_WithoutRetryReadsOnce verifies that a registry
// with no StartupReadRetry attempts the catalog listing once and returns its
// error.
func TestRestoreVolumeTenants_WithoutRetryReadsOnce(t *testing.T) {
	f := newRestoreFixture(t)
	f.allocator.listFails.Store(1)

	err := f.restart(t).RestoreVolumeTenants(context.Background())
	if !errors.Is(err, errShardSettling) {
		t.Fatalf("RestoreVolumeTenants = %v, want the settling listing error", err)
	}
	if got := f.allocator.listFails.Load(); got != 0 {
		t.Fatalf("ListAll ran %d times, want 1", 1-got)
	}
}

// TestRestoreVolumeTenants_SettlingListingOutlastsRetry verifies that a
// listing still settling when the retry gives up is returned.
func TestRestoreVolumeTenants_SettlingListingOutlastsRetry(t *testing.T) {
	f := newRestoreFixture(t)
	f.allocator.listFails.Store(10)

	var ops []string
	err := f.restart(t, WithStartupReadRetry(settlingRetry(3, &ops))).RestoreVolumeTenants(context.Background())
	if !errors.Is(err, errShardSettling) {
		t.Fatalf("RestoreVolumeTenants = %v, want the settling listing error", err)
	}
	if len(ops) != 3 {
		t.Fatalf("retry ran %d attempts, want 3", len(ops))
	}
}

// TestRestoreVolumeTenants_SettlingPostureOutlastsRetryRestoresSealed
// verifies that a posture read still settling when the retry gives up
// restores the tenant under the fail-closed self-managed default: present,
// but sealed.
func TestRestoreVolumeTenants_SettlingPostureOutlastsRetryRestoresSealed(t *testing.T) {
	f := newRestoreFixture(t)
	f.backend.fails.Store(3)

	var ops []string
	reg := f.restart(t, WithStartupReadRetry(settlingRetry(3, &ops)))
	if err := reg.RestoreVolumeTenants(context.Background()); err != nil {
		t.Fatalf("RestoreVolumeTenants: %v", err)
	}
	tb, err := reg.GetTenant(f.tenantID)
	if err != nil {
		t.Fatalf("GetTenant after RestoreVolumeTenants: %v", err)
	}
	if !tb.IsSealed() {
		t.Error("a tenant whose posture could not be read must be restored sealed")
	}
}

// TestRestoreVolumeTenants_SettlingReattachOutlastsRetrySkipsTenant verifies
// that a re-attach whose Assignment is still settling when the retry gives
// up leaves the tenant unrestored without failing the restore.
func TestRestoreVolumeTenants_SettlingReattachOutlastsRetrySkipsTenant(t *testing.T) {
	f := newRestoreFixture(t)
	f.allocator.resolveFails.Store(10)

	var ops []string
	reg := f.restart(t, WithStartupReadRetry(settlingRetry(3, &ops)))
	if err := reg.RestoreVolumeTenants(context.Background()); err != nil {
		t.Fatalf("RestoreVolumeTenants: %v", err)
	}
	if _, err := reg.GetTenant(f.tenantID); err == nil {
		t.Fatal("a tenant whose re-attach never resolved must not be restored")
	}
}

// TestWithStartupReadRetry_NilIsNoOp verifies that passing nil leaves the
// registry reading once.
func TestWithStartupReadRetry_NilIsNoOp(t *testing.T) {
	reg := newUnsealedRegistryWith(t, WithStartupReadRetry(nil))

	calls := 0
	err := reg.startupRead(context.Background(), "op", func(context.Context) error {
		calls++
		return errShardSettling
	})
	if !errors.Is(err, errShardSettling) || calls != 1 {
		t.Fatalf("startupRead = (%v, %d calls), want the read's error after 1 call", err, calls)
	}
}

// TestWithStartupReadRetry_RunsReadsThroughRetry verifies that a configured
// retry receives the op name and the read, and that its result is returned.
func TestWithStartupReadRetry_RunsReadsThroughRetry(t *testing.T) {
	var ops []string
	reg := newUnsealedRegistryWith(t, WithStartupReadRetry(settlingRetry(4, &ops)))

	calls := 0
	err := reg.startupRead(context.Background(), "probe", func(context.Context) error {
		calls++
		if calls < 3 {
			return errShardSettling
		}
		return nil
	})
	if err != nil {
		t.Fatalf("startupRead: %v", err)
	}
	if calls != 3 || len(ops) != 3 || ops[0] != "probe" {
		t.Fatalf("startupRead ran %d reads with ops %v, want 3 reads under op %q", calls, ops, "probe")
	}
}

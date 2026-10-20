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

// Registry barrier-swap tests, plus the migration-journal helpers and the
// error types the migration path reports.
//
// The swaps exist for the encryption migration's commit step, where a live
// none-strategy barrier is replaced by a freshly-unsealed keyed one. Swapping
// the wrong entry -- or reporting success for a tenant that was never
// registered -- would leave the data plane reading through a barrier that
// cannot decrypt what is on disk.

package seal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// newSwapBarrier builds a standalone StorageBarrier for use as a swap target.
func newSwapBarrier(t *testing.T, rootKeyPath string) *StorageBarrier {
	t.Helper()
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(),
		BarrierConfig{RootKeyPath: rootKeyPath}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	return b
}

// TestSetSystemBarrierReplacesTheLiveBarrier covers the system-barrier swap.
func TestSetSystemBarrierReplacesTheLiveBarrier(t *testing.T) {
	reg := newNoPoolRegistry(t)
	original := reg.SystemBarrier()
	if original == nil {
		t.Fatal("expected a system barrier")
	}

	replacement := newSwapBarrier(t, "sys/root-2")
	reg.SetSystemBarrier(replacement)

	if got := reg.SystemBarrier(); got != replacement {
		t.Fatal("SystemBarrier did not return the replacement")
	}
	if reg.SystemBarrier() == original {
		t.Fatal("the original barrier is still installed")
	}
}

// TestSetSystemBarrierMovesTheObserversToTheReplacement covers what other
// subsystems hang on the system barrier: the shard pauser's resume and the
// volume lifecycle's lock and unlock. A swap that left them on the replaced
// barrier would let the live one seal and unseal without them.
func TestSetSystemBarrierMovesTheObserversToTheReplacement(t *testing.T) {
	ctx := context.Background()
	reg := newNoPoolRegistry(t)
	original := reg.SystemBarrier()
	var unsealed, sealing int
	original.OnUnsealed(func(context.Context) { unsealed++ })
	original.OnSealing(func(context.Context) error { sealing++; return nil })

	replacement := newSwapBarrier(t, "sys/root-2")
	reg.SetSystemBarrier(replacement)
	if err := replacement.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := replacement.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := replacement.Unseal(ctx, Credentials{}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if unsealed != 2 || sealing != 1 {
		t.Fatalf("on the replacement: unsealed observer ran %d times, sealing %d; want 2 and 1", unsealed, sealing)
	}

	// The replaced barrier drives them no more.
	if err := original.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("Initialize original: %v", err)
	}
	if err := original.Seal(); err != nil {
		t.Fatalf("Seal original: %v", err)
	}
	if unsealed != 2 || sealing != 1 {
		t.Fatalf("the replaced barrier still ran the observers: unsealed %d, sealing %d", unsealed, sealing)
	}
}

// TestSetSystemBarrierUnsealsSystemManagedTenantsOnce covers the registry's
// own observer: it moves with the others rather than being registered again,
// so the replacement's unseal brings each system-managed tenant back once.
func TestSetSystemBarrierUnsealsSystemManagedTenantsOnce(t *testing.T) {
	ctx := context.Background()
	reg := newNoPoolRegistry(t)
	replacement := newSwapBarrier(t, "sys/root-2")
	reg.SetSystemBarrier(replacement)
	if err := replacement.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := reg.RegisterTenant(ctx, "acme", NewMemoryBackend(),
		BarrierConfig{RootKeyPath: "acme/root"}, NewSystemBarrierStrategy(replacement)); err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}
	if err := reg.InitializeTenant(ctx, "acme", Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}
	if err := reg.SealAll(); err != nil {
		t.Fatalf("SealAll: %v", err)
	}

	if err := replacement.Unseal(ctx, Credentials{}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	tb, err := reg.GetTenant("acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if tb.IsSealed() {
		t.Fatal("the replacement's unseal left the system-managed tenant sealed")
	}
	if n := len(replacement.unsealedObservers); n != 1 {
		t.Fatalf("the replacement carries %d unseal observers, want the registry's one", n)
	}
}

// TestReplaceTenantBarrierRefusesAnUnregisteredTenant covers the guard. A
// shared-barrier tenant has no dedicated barrier to replace, and silently
// registering one would give it a barrier the rest of the system does not
// know it has.
func TestReplaceTenantBarrierRefusesAnUnregisteredTenant(t *testing.T) {
	reg := newNoPoolRegistry(t)

	err := reg.ReplaceTenantBarrier("never-registered", WrapTenantBarrier("never-registered", newSwapBarrier(t, "t/root")))
	if !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("error = %v, want ErrTenantNotFound", err)
	}
}

// TestWrapTenantBarrierCarriesTheTenantIdentity covers the wrapper used by the
// per-tenant migration commit. The tenant id it carries is what the registry
// keys on, so a wrapper that lost it would swap the wrong tenant's barrier.
func TestWrapTenantBarrierCarriesTheTenantIdentity(t *testing.T) {
	barrier := newSwapBarrier(t, "acme/root")

	tb := WrapTenantBarrier("acme", barrier)
	if tb == nil {
		t.Fatal("WrapTenantBarrier returned nil")
	}
	if tb.TenantID() != "acme" {
		t.Errorf("TenantID = %q, want %q", tb.TenantID(), "acme")
	}
	if tb.Barrier() != barrier {
		t.Error("Barrier() did not return the wrapped barrier")
	}
	if !tb.IsSealed() {
		t.Error("a freshly built barrier must report sealed")
	}
}

// TestPoolBasePathsIsEmptyWithoutAPool covers the no-pool branch. The doc
// promises an empty slice and a nil error so callers can range
// unconditionally; returning an error instead would break every
// data-root overlap check on a deployment with no volume pool.
func TestPoolBasePathsIsEmptyWithoutAPool(t *testing.T) {
	reg := newNoPoolRegistry(t)

	paths, err := reg.PoolBasePaths(context.Background())
	if err != nil {
		t.Fatalf("PoolBasePaths: %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("paths = %v, want empty", paths)
	}
	// Ranging over the result must be safe.
	for range paths {
		t.Fatal("unreachable")
	}
}

// TestRootKeyPathReportsTheConfiguredPath covers the accessor the migration
// journal helpers key off.
func TestRootKeyPathReportsTheConfiguredPath(t *testing.T) {
	b := newSwapBarrier(t, "sys/custom-root")
	if got := b.RootKeyPath(); got != "sys/custom-root" {
		t.Fatalf("RootKeyPath = %q, want %q", got, "sys/custom-root")
	}
}

// TestLoadMigrationJournalFromReportsNoJournal covers the absent-journal path.
// Nil-and-nil means "no migration in flight", which is what boot uses to
// decide there is nothing to recover; an error here would make every clean
// start look like a crashed migration.
func TestLoadMigrationJournalFromReportsNoJournal(t *testing.T) {
	backend := NewMemoryBackend()

	j, err := LoadMigrationJournalFrom(context.Background(), backend, "sys/root")
	if err != nil {
		t.Fatalf("LoadMigrationJournalFrom: %v", err)
	}
	if j != nil {
		t.Fatalf("journal = %+v, want nil for a backend with no journal", j)
	}
}

// TestClearMigrationJournalFromIsIdempotent covers the clear path against a
// backend that has no journal. Migration cleanup runs on paths that may have
// already cleaned up, so a second clear must not fail.
func TestClearMigrationJournalFromIsIdempotent(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()

	if err := ClearMigrationJournalFrom(ctx, backend, "sys/root"); err != nil {
		t.Fatalf("first clear: %v", err)
	}
	if err := ClearMigrationJournalFrom(ctx, backend, "sys/root"); err != nil {
		t.Fatalf("second clear: %v", err)
	}
}

// TestPersistedStrategyFromIsEmptyWithoutABlob covers the accessor's
// not-initialized branch, which boot uses before any root key exists.
func TestPersistedStrategyFromIsEmptyWithoutABlob(t *testing.T) {
	if got := PersistedStrategyFrom(context.Background(), NewMemoryBackend(), "sys/root"); got != "" {
		t.Fatalf("PersistedStrategyFrom = %q, want the empty string", got)
	}
}

// TestFinishMigrationIsSafeWithNoJournal covers the exported cleanup wrapper.
func TestFinishMigrationIsSafeWithNoJournal(t *testing.T) {
	b := newSwapBarrier(t, "sys/root")
	if err := b.FinishMigration(context.Background()); err != nil {
		t.Fatalf("FinishMigration: %v", err)
	}
}

// TestBeginMigrationRefusesAnUninitializedBarrier covers the exported
// entrypoint's validation. Starting a migration against a barrier with no
// persisted root key would write a journal describing work that cannot be
// performed, and boot would then try to recover it forever.
func TestBeginMigrationRefusesAnUninitializedBarrier(t *testing.T) {
	b := newSwapBarrier(t, "sys/root")

	j, err := b.BeginMigration(context.Background(), MigrationOptions{})
	if err == nil {
		t.Fatal("BeginMigration must refuse an uninitialized barrier")
	}
	if j != nil {
		t.Error("a refused migration must return no journal")
	}
}

// TestMigrationJournalPhasePredicates covers Committed and Terminal, which
// decide whether a recovered migration rolls forward or back. Getting either
// wrong on a crashed migration re-runs or abandons a re-encryption pass.
func TestMigrationJournalPhasePredicates(t *testing.T) {
	// A zero-value journal is in the initial phase: neither committed nor
	// terminal, so recovery treats it as work still to do.
	var j MigrationJournal
	if j.Committed() {
		t.Error("a zero-value journal must not report committed")
	}
	if j.Terminal() {
		t.Error("a zero-value journal must not report terminal")
	}
}

// TestMigrationErrorMessagesNameTheirSubject covers the error types the
// migration path reports. These surface during boot recovery, where the
// message is often the only record of why a node refused to come up.
func TestMigrationErrorMessagesNameTheirSubject(t *testing.T) {
	sentinel := errors.New("shamir failed")
	cause := errors.New("underlying")

	tests := []struct {
		name     string
		err      error
		contains []string
	}{
		{
			name:     "shamir with op and cause",
			err:      &ShamirError{Sentinel: sentinel, Op: "split", Cause: cause},
			contains: []string{"shamir failed", "split", "underlying"},
		},
		{
			name:     "shamir with sentinel only",
			err:      &ShamirError{Sentinel: sentinel},
			contains: []string{"shamir failed"},
		},
		{
			name:     "migration recovery names the tenant",
			err:      &MigrationRecoveryError{TenantID: "acme", Err: cause},
			contains: []string{"qrdb/seal:", "acme", "underlying"},
		},
		{
			name:     "sentinel length names the bad length",
			err:      &SentinelLengthError{Length: 7},
			contains: []string{"qrdb/seal:", "7", "want 4"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.err.Error()
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("message %q does not contain %q", got, want)
				}
			}
		})
	}
}

// TestShamirErrorUnwrapsToItsSentinel pins the errors.Is behaviour the type
// exists to preserve: callers match on the sentinel, not the wrapper.
func TestShamirErrorUnwrapsToItsSentinel(t *testing.T) {
	sentinel := errors.New("shamir failed")
	err := &ShamirError{Sentinel: sentinel, Op: "combine", Cause: errors.New("underlying")}

	if !errors.Is(err, sentinel) {
		t.Fatal("errors.Is could not reach the sentinel")
	}
}

// TestMigrationRecoveryErrorUnwrapsToItsCause is the same property on the
// recovery wrapper.
func TestMigrationRecoveryErrorUnwrapsToItsCause(t *testing.T) {
	cause := errors.New("underlying")
	err := &MigrationRecoveryError{TenantID: "acme", Err: cause}

	if !errors.Is(err, cause) {
		t.Fatal("errors.Is could not reach the cause")
	}
}

// TestReplaceTenantBarrierSwapsARegisteredTenant covers the success path: the
// tenant exists, so its barrier is replaced in place.
//
// This is the per-tenant migration commit. The replacement must be observable
// through the registry immediately afterwards -- a swap that did not take
// would leave the data plane decrypting with the pre-migration key.
func TestReplaceTenantBarrierSwapsARegisteredTenant(t *testing.T) {
	ctx := context.Background()
	reg := newNoPoolRegistry(t)

	original, err := reg.RegisterTenant(ctx, "acme", NewMemoryBackend(),
		BarrierConfig{RootKeyPath: "acme/root"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}

	replacement := WrapTenantBarrier("acme", newSwapBarrier(t, "acme/root-2"))
	if err := reg.ReplaceTenantBarrier("acme", replacement); err != nil {
		t.Fatalf("ReplaceTenantBarrier: %v", err)
	}

	got, err := reg.GetTenant("acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got != replacement {
		t.Fatal("the registry did not return the replacement barrier")
	}
	if got == original {
		t.Fatal("the original tenant barrier is still installed")
	}
}

// TestTenantBarrierKVStoreRefusesANilBarrier keeps a misconfigured store from
// becoming a nil dereference on the first write.
func TestTenantBarrierKVStoreRefusesANilBarrier(t *testing.T) {
	if _, err := NewTenantBarrierKVStore(nil); err == nil {
		t.Fatal("NewTenantBarrierKVStore(nil) must fail")
	}
}

// TestTenantBarrierKVStorePutIfAbsentIsSingleUse covers the conditional-write
// passthrough. The atomicity belongs to the barrier; what is under test here
// is that the tenant-scoping wrapper does not lose the "created?" answer,
// which callers build single-use semantics on.
func TestTenantBarrierKVStorePutIfAbsentIsSingleUse(t *testing.T) {
	ctx := context.Background()
	reg := newNoPoolRegistry(t)

	tb, err := reg.RegisterTenant(ctx, "acme", NewMemoryBackend(),
		BarrierConfig{RootKeyPath: "acme/root"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}
	if err := tb.Barrier().Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	store, err := NewTenantBarrierKVStore(tb)
	if err != nil {
		t.Fatalf("NewTenantBarrierKVStore: %v", err)
	}
	atomic, ok := store.(kvstore.AtomicKVStore)
	if !ok {
		t.Fatal("the tenant barrier KV store must support conditional writes")
	}

	created, err := atomic.PutIfAbsent(ctx, "k", []byte("first"))
	if err != nil {
		t.Fatalf("first PutIfAbsent: %v", err)
	}
	if !created {
		t.Fatal("the first PutIfAbsent must create the key")
	}

	created, err = atomic.PutIfAbsent(ctx, "k", []byte("second"))
	if err != nil {
		t.Fatalf("second PutIfAbsent: %v", err)
	}
	if created {
		t.Fatal("the second PutIfAbsent must not report creation")
	}

	// The original value must survive the refused write.
	got, err := store.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "first" {
		t.Fatalf("value = %q, want %q", got, "first")
	}
}

// TestMigrationJournalRoundTripsThroughTheBackend covers the write/load pair
// together, which is the only combination that proves both agree on the
// journal's key and encoding. A journal that wrote successfully but loaded as
// absent would make a crashed migration invisible to boot recovery.
func TestMigrationJournalRoundTripsThroughTheBackend(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()
	b, err := NewStorageBarrier(testLogger(), backend,
		BarrierConfig{RootKeyPath: "sys/root"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	journal := &MigrationJournal{TargetStrategy: StrategyPassphrase, StagingRootKeyPath: "sys/staging"}
	if err := b.CheckpointMigration(ctx, journal); err != nil {
		t.Fatalf("CheckpointMigration: %v", err)
	}

	loaded, err := LoadMigrationJournalFrom(ctx, backend, "sys/root")
	if err != nil {
		t.Fatalf("LoadMigrationJournalFrom: %v", err)
	}
	if loaded == nil {
		t.Fatal("a checkpointed journal loaded back as absent")
	}
	if loaded.TargetStrategy != StrategyPassphrase {
		t.Errorf("TargetStrategy = %q, want %q", loaded.TargetStrategy, StrategyPassphrase)
	}
	if loaded.StagingRootKeyPath != "sys/staging" {
		t.Errorf("StagingRootKeyPath = %q, want %q", loaded.StagingRootKeyPath, "sys/staging")
	}

	// And clearing it must make it absent again.
	if err := ClearMigrationJournalFrom(ctx, backend, "sys/root"); err != nil {
		t.Fatalf("ClearMigrationJournalFrom: %v", err)
	}
	cleared, err := LoadMigrationJournalFrom(ctx, backend, "sys/root")
	if err != nil {
		t.Fatalf("LoadMigrationJournalFrom after clear: %v", err)
	}
	if cleared != nil {
		t.Fatal("the journal survived ClearMigrationJournalFrom")
	}
}

// TestLoadMigrationJournalFromRejectsACorruptBlob covers the unmarshal-failure
// branch. A corrupt journal must be an error, not silently "no migration" --
// the latter would abandon a half-finished re-encryption and leave the data
// unreadable.
func TestLoadMigrationJournalFromRejectsACorruptBlob(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()

	if err := backend.Put(ctx, "sys/root"+migrationJournalSuffix, []byte("{not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	j, err := LoadMigrationJournalFrom(ctx, backend, "sys/root")
	if err == nil {
		t.Fatal("a corrupt journal must be reported, not treated as absent")
	}
	if j != nil {
		t.Error("a corrupt journal must yield no value")
	}
}

// TestDeleteRootKeyToleratesAnAbsentBlob covers the cleanup used when a
// migration rolls back or a staging mint fails.
//
// It must succeed when there is nothing to delete: leaving an orphaned staging
// blob behind makes the next mint's Initialize fail with ErrAlreadyInitialized
// and wedges every retry, so cleanup that refused to run on a partially-created
// staging barrier would be worse than no cleanup at all.
func TestDeleteRootKeyToleratesAnAbsentBlob(t *testing.T) {
	ctx := context.Background()
	b := newSwapBarrier(t, "sys/staging")

	if err := b.DeleteRootKey(ctx); err != nil {
		t.Fatalf("DeleteRootKey on an uninitialized barrier: %v", err)
	}
	// Idempotent: a second cleanup pass must also succeed.
	if err := b.DeleteRootKey(ctx); err != nil {
		t.Fatalf("second DeleteRootKey: %v", err)
	}
}

// TestDeleteRootKeyRemovesAPersistedBlob is the other half: after
// initialization there IS a blob, and it must actually go away -- otherwise
// the wedge above is exactly what happens on the next mint.
func TestDeleteRootKeyRemovesAPersistedBlob(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()
	b, err := NewStorageBarrier(testLogger(), backend,
		BarrierConfig{RootKeyPath: "sys/staging"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if blob, err := backend.Get(ctx, "sys/staging"); err != nil || len(blob) == 0 {
		t.Fatalf("expected a persisted root key before deletion (err=%v)", err)
	}

	if err := b.DeleteRootKey(ctx); err != nil {
		t.Fatalf("DeleteRootKey: %v", err)
	}

	if _, err := backend.Get(ctx, "sys/staging"); err == nil {
		t.Fatal("the root key blob survived DeleteRootKey")
	}
}

// TestLoadMigrationJournalReportsNoneAndThenTheJournal covers the barrier's
// own journal accessor across both states, which is what boot consults to
// decide whether a migration needs recovering.
func TestLoadMigrationJournalReportsNoneAndThenTheJournal(t *testing.T) {
	ctx := context.Background()
	b := newSwapBarrier(t, "sys/root")

	j, err := b.LoadMigrationJournal(ctx)
	if err != nil {
		t.Fatalf("LoadMigrationJournal: %v", err)
	}
	if j != nil {
		t.Fatal("a barrier with no journal must report nil")
	}

	if err := b.CheckpointMigration(ctx, &MigrationJournal{TargetStrategy: StrategyPassphrase}); err != nil {
		t.Fatalf("CheckpointMigration: %v", err)
	}

	j, err = b.LoadMigrationJournal(ctx)
	if err != nil {
		t.Fatalf("LoadMigrationJournal after checkpoint: %v", err)
	}
	if j == nil {
		t.Fatal("a checkpointed journal was not loaded back")
	}
	if j.TargetStrategy != StrategyPassphrase {
		t.Errorf("TargetStrategy = %q, want %q", j.TargetStrategy, StrategyPassphrase)
	}

	if err := b.FinishMigration(ctx); err != nil {
		t.Fatalf("FinishMigration: %v", err)
	}
	j, err = b.LoadMigrationJournal(ctx)
	if err != nil {
		t.Fatalf("LoadMigrationJournal after finish: %v", err)
	}
	if j != nil {
		t.Fatal("the journal survived FinishMigration")
	}
}

// TestChangePasswordRefusesBeforeItTouchesKeyMaterial covers the guards that
// run ahead of any re-wrap.
//
// Each one must fail BEFORE the root key is opened. A rejection that happened
// afterwards would leave a plaintext root key resident in memory on a path
// that returns an error and never reaches the deferred wipe.
func TestChangePasswordRefusesBeforeItTouchesKeyMaterial(t *testing.T) {
	ctx := context.Background()

	t.Run("sealed barrier", func(t *testing.T) {
		b := newSwapBarrier(t, "sys/root")
		if err := b.ChangePassword(ctx, MustCredentials(context.Background(), []byte("new-passphrase"))); !errors.Is(err, ErrSealed) {
			t.Fatalf("error = %v, want ErrSealed", err)
		}
	})

	t.Run("empty passphrase", func(t *testing.T) {
		b := newSwapBarrier(t, "sys/root")
		if err := b.Initialize(ctx, Credentials{}); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		if err := b.ChangePassword(ctx, Credentials{}); !errors.Is(err, ErrInvalidPassphrase) {
			t.Fatalf("error = %v, want ErrInvalidPassphrase", err)
		}
	})
}

// TestStartRekeyRefusesWithoutAShamirSetup covers StartRekey's guard chain:
// every precondition is checked before any holder is asked for a share.
func TestStartRekeyRefusesWithoutAShamirSetup(t *testing.T) {
	ctx := context.Background()

	t.Run("sealed barrier", func(t *testing.T) {
		b := newSwapBarrier(t, "sys/root")
		info, err := b.StartRekey(ctx, DomainUnseal, RekeyParams{})
		if !errors.Is(err, ErrSealed) {
			t.Fatalf("error = %v, want ErrSealed", err)
		}
		if info != nil {
			t.Error("a refused rekey must return no round")
		}
	})

	t.Run("no distributor", func(t *testing.T) {
		b := newSwapBarrier(t, "sys/root")
		if err := b.Initialize(ctx, Credentials{}); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		if _, err := b.StartRekey(ctx, DomainUnseal, RekeyParams{}); !errors.Is(err, ErrCeremonyDistributorNotConfigured) {
			t.Fatalf("error = %v, want ErrCeremonyDistributorNotConfigured", err)
		}
	})

	t.Run("no roster", func(t *testing.T) {
		b := newSwapBarrier(t, "sys/root")
		if err := b.Initialize(ctx, Credentials{}); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		b.SetCeremonyDistributor(newCaptureDistributor("h1", "h2"))
		if _, err := b.StartRekey(ctx, DomainUnseal, RekeyParams{}); !errors.Is(err, ErrHolderRosterMissing) {
			t.Fatalf("error = %v, want ErrHolderRosterMissing", err)
		}
	})
}

// TestStartRekeyRejectsAnUnsatisfiableThreshold pins the parameter
// validation: a threshold below two removes the point of splitting, and one
// above the holder count could never be met.
func TestStartRekeyRejectsAnUnsatisfiableThreshold(t *testing.T) {
	for _, threshold := range []int{1, 4, 9} {
		f := newHolderFixture(t, 2, 3)
		if _, err := f.b.StartRekey(context.Background(), DomainUnseal, RekeyParams{Threshold: threshold}); err == nil {
			t.Fatalf("StartRekey(threshold=%d) must refuse", threshold)
		}
	}
}

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

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// errRecoverFault is the injected backend failure of the boot-recovery tests.
var errRecoverFault = errors.New("injected boot-recovery backend fault")

// faultRecoverBackend wraps a seal-store backend and fails chosen calls: the
// next failGets reads, and, while failStagedDelete is set, every delete of a
// staged seal-store record (the last step of the roll-forward copy).
type faultRecoverBackend struct {
	seal.StorageBackend
	failGets         int
	failStagedDelete bool
}

func (f *faultRecoverBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if f.failGets > 0 {
		f.failGets--
		return nil, errRecoverFault
	}
	return f.StorageBackend.Get(ctx, key)
}

func (f *faultRecoverBackend) Delete(ctx context.Context, key string) error {
	if f.failStagedDelete && strings.HasPrefix(key, sealStoreStagingPrefix) {
		return errRecoverFault
	}
	return f.StorageBackend.Delete(ctx, key)
}

// crashRecodeSealStorePastCommit runs a seal-store migration that crashes
// past its commit point, before the staged records are copied, and returns
// the seal-store directory (closed) and the secrets it holds.
func crashRecodeSealStorePastCommit(t *testing.T) (string, map[string]string) {
	t.Helper()
	ctx := context.Background()
	sealDir := filepath.Join(t.TempDir(), "seal")
	store, retirer, err := openSealStore(sealDir)
	if err != nil {
		t.Fatalf("openSealStore: %v", err)
	}
	backend := seal.NewStorageAdapter(store)
	source := newRecodeSourceBarrierOn(t, backend)
	secrets := map[string]string{"alpha": "alpha-recover-secret", "beta": "beta-recover-secret"}
	putRecodeSealStoreSecrets(t, source, secrets)
	walDir := filepath.Join(t.TempDir(), "wal")
	if err := os.MkdirAll(walDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	setRecodeSealStoreCommitFault(t, func() error { return errRecodeSealStoreCrash })
	err = recodeSystemBarrier(ctx, source, service.MigrationStores{}, walDir, validSystemMigrateOpts(), func() error { return retirer.Retire(ctx) })
	if !errors.Is(err, errRecodeSealStoreCrash) {
		t.Fatalf("err = %v, want %v", err, errRecodeSealStoreCrash)
	}
	_ = source.Close()
	_ = store.Close()
	return sealDir, secrets
}

// assertRecoverJournalPending checks the committed journal and its staged
// records are still there: the failed boot wrote nothing past them.
func assertRecoverJournalPending(t *testing.T, backend seal.StorageBackend) {
	t.Helper()
	ctx := context.Background()
	j, err := seal.LoadMigrationJournalFrom(ctx, backend, "system/root-key")
	if err != nil || j == nil || !j.Committed() || !journalNamesSealStore(j) {
		t.Fatalf("journal after the failed boot = %+v (err %v), want the committed seal-store journal kept", j, err)
	}
	staged, err := backend.List(ctx, sealStoreStagingPrefix)
	if err != nil || len(staged) == 0 {
		t.Fatalf("staged records after the failed boot = %v (err %v), want them kept", staged, err)
	}
}

// TestSealSubsystemRecoverInterrupted_JournalReadFailureStopsBoot: a failed
// first journal read is not "no pending migration". The boot returns the
// error before the recovery pass runs, so the recovery's own read cannot
// roll the seal store forward and clear the journal with no retirement
// marker written first.
func TestSealSubsystemRecoverInterrupted_JournalReadFailureStopsBoot(t *testing.T) {
	ctx := context.Background()
	sealDir, _ := crashRecodeSealStorePastCommit(t)
	store, retirer, err := openSealStore(sealDir)
	if err != nil {
		t.Fatalf("reopen the seal store: %v", err)
	}
	defer func() { _ = store.Close() }()
	backend := seal.NewStorageAdapter(store)
	faulty := &faultRecoverBackend{StorageBackend: backend, failGets: 1}
	if err := recoverSystemMigration(ctx, faulty, retirer, "system/root-key", nil); !errors.Is(err, errRecoverFault) {
		t.Fatalf("boot with a failing journal read: err = %v, want %v", err, errRecoverFault)
	}
	assertRecoverJournalPending(t, backend)
	if owed, merr := retirer.marked(); merr != nil || owed {
		t.Fatalf("retirement marker after the failed read = %v (err %v), want none written", owed, merr)
	}
}

// TestSealSubsystemRecoverInterrupted_RollForwardFailureStopsBoot: a failed
// staged-record copy stops the boot (no log-and-continue), the committed
// journal and staged records stay, and the next boot completes the roll
// forward, so a seal-store write after it is not replayed over at the
// restart after that.
func TestSealSubsystemRecoverInterrupted_RollForwardFailureStopsBoot(t *testing.T) {
	ctx := context.Background()
	sealDir, secrets := crashRecodeSealStorePastCommit(t)
	store, retirer, err := openSealStore(sealDir)
	if err != nil {
		t.Fatalf("reopen the seal store: %v", err)
	}
	defer func() { _ = store.Close() }()
	reboot := func() seal.StorageBackend {
		t.Helper()
		_ = store.Close()
		store, retirer, err = openSealStore(sealDir)
		if err != nil {
			t.Fatalf("reopen the seal store: %v", err)
		}
		return seal.NewStorageAdapter(store)
	}

	// Boot 1: the staged-record delete fails; the boot stops with it.
	backend := seal.NewStorageAdapter(store)
	faulty := &faultRecoverBackend{StorageBackend: backend, failStagedDelete: true}
	if err := recoverSystemMigration(ctx, faulty, retirer, "system/root-key", nil); !errors.Is(err, errRecoverFault) {
		t.Fatalf("boot with a failing roll-forward: err = %v, want %v", err, errRecoverFault)
	}
	assertRecoverJournalPending(t, backend)

	// Boot 2: the fault is gone; the roll-forward completes.
	backend = reboot()
	if err := recoverSystemMigration(ctx, backend, retirer, "system/root-key", nil); err != nil {
		t.Fatalf("boot after the failed roll-forward: %v", err)
	}
	assertRecodeSealStoreKeyed(t, backend, secrets)

	// A seal-store write after the recovery survives the next restart. A
	// barrier's Close closes the store under it, so each barrier above and
	// below runs on a freshly opened store, as a real boot does.
	backend = reboot()
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte("daemon-migrate-pw"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	keyed, err := seal.NewStorageBarrier(slog.New(slog.DiscardHandler), backend,
		seal.BarrierConfig{RootKeyPath: "system/root-key"}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := keyed.Unseal(ctx, seal.MustCredentials(ctx, []byte("daemon-migrate-pw"))); err != nil {
		t.Fatalf("Unseal keyed barrier: %v", err)
	}
	secrets["alpha"] = "alpha-rewritten-after-recovery"
	putRecodeSealStoreSecrets(t, keyed, map[string]string{"alpha": secrets["alpha"]})
	_ = keyed.Close()

	// Boot 3: nothing is pending; the rewrite stands.
	backend = reboot()
	if err := recoverSystemMigration(ctx, backend, retirer, "system/root-key", nil); err != nil {
		t.Fatalf("boot after the rewrite: %v", err)
	}
	assertRecodeSealStoreKeyed(t, backend, secrets)
}

// nthGetFaultBackend fails the failOn-th Get (1-based) of key and delegates
// everything else.
type nthGetFaultBackend struct {
	seal.StorageBackend
	key    string
	failOn int
	gets   int
}

func (f *nthGetFaultBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if key == f.key {
		f.gets++
		if f.gets == f.failOn {
			return nil, errRecoverFault
		}
	}
	return f.StorageBackend.Get(ctx, key)
}

// TestSealSubsystemRecoverInterrupted_SecondStagingReadFailureStopsBoot
// covers a staging root key the boot guard reads but the roll-forward cannot:
// the boot stops with the journal and every seal-store secret intact, and the
// next boot rolls the migration forward.
func TestSealSubsystemRecoverInterrupted_SecondStagingReadFailureStopsBoot(t *testing.T) {
	ctx := context.Background()
	// A barrier's Close closes the store under it, so the migration runs on
	// a memory backend; the retirer comes from a seal store of its own.
	store, retirer, err := openSealStore(filepath.Join(t.TempDir(), "seal"))
	if err != nil {
		t.Fatalf("openSealStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	backend := seal.NewMemoryBackend()
	secrets := map[string]string{"alpha": "alpha-second-read-secret", "beta": "beta-second-read-secret"}
	j := recodeAdoptFailure(t, backend, secrets)

	// Boot 1: the guard's read of the staging root passes, the roll-forward's
	// read fails; the boot stops before anything is touched.
	faulty := &nthGetFaultBackend{StorageBackend: backend, key: j.StagingRootKeyPath, failOn: 2}
	if err := recoverSystemMigration(ctx, faulty, retirer, "system/root-key", nil); !errors.Is(err, database.ErrMigrationStagingRootUnreadable) {
		t.Fatalf("boot with a failing staging read: err = %v, want %v", err, database.ErrMigrationStagingRootUnreadable)
	}
	if faulty.gets < 2 {
		t.Fatalf("staging root read %d times, want the guard's and the roll-forward's", faulty.gets)
	}
	assertRecoverJournalPending(t, backend)

	// Boot 2: the fault is gone; the roll-forward completes.
	if err := recoverSystemMigration(ctx, backend, retirer, "system/root-key", nil); err != nil {
		t.Fatalf("boot after the failed staging read: %v", err)
	}
	assertRecodeSealStoreKeyed(t, backend, secrets)
}

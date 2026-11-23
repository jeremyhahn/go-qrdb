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

package database

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

var errBackendFault = errors.New("injected backend fault")

// faultBackend wraps a real seal.StorageBackend and can fail Get/Put for keys
// matching a predicate, delegating everything else. It lets the recovery error
// branches — which the in-memory backend never triggers — be exercised.
type faultBackend struct {
	seal.StorageBackend
	failGet func(key string) bool
	failPut func(key string) bool
	// failGetN, when set, fails the n-th Get (1-based) of key.
	failGetN func(key string, n int) bool
	gets     map[string]int
	// puts records every successful Put key, in order.
	puts []string
}

func (f *faultBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if f.failGet != nil && f.failGet(key) {
		return nil, errBackendFault
	}
	if f.failGetN != nil {
		if f.gets == nil {
			f.gets = map[string]int{}
		}
		f.gets[key]++
		if f.failGetN(key, f.gets[key]) {
			return nil, errBackendFault
		}
	}
	return f.StorageBackend.Get(ctx, key)
}

func (f *faultBackend) Put(ctx context.Context, key string, data []byte) error {
	if f.failPut != nil && f.failPut(key) {
		return errBackendFault
	}
	if err := f.StorageBackend.Put(ctx, key, data); err != nil {
		return err
	}
	f.puts = append(f.puts, key)
	return nil
}

// TestPersistedStrategy_AbsentAndGarbage covers persistedStrategy's two defensive
// early returns: a missing/empty blob and an undecodable blob both yield "".
func TestPersistedStrategy_AbsentAndGarbage(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()

	if got := persistedStrategy(ctx, backend, "missing"); got != "" {
		t.Fatalf("persistedStrategy(missing) = %q, want \"\"", got)
	}

	if err := backend.Put(ctx, "garbage", []byte("not-json")); err != nil {
		t.Fatalf("seed garbage: %v", err)
	}
	if got := persistedStrategy(ctx, backend, "garbage"); got != "" {
		t.Fatalf("persistedStrategy(garbage) = %q, want \"\"", got)
	}
}

// TestRecoverInterruptedMigration_RollBackRestoresBackup covers the roll-back
// branch where a swap had begun before the crash: a pre-migration backup dir and
// a keyed staging copy both exist, and the journal records a staging root-key
// path. Recovery must discard the staging copy, restore the backup over the
// (partial) data dir, delete the staging blob, and clear the journal.
func TestRecoverInterruptedMigration_RollBackRestoresBackup(t *testing.T) {
	ctx := context.Background()
	mem := seal.NewMemoryBackend()
	none := initNoneBarrierBackend(t, mem, "root-key")

	dir := t.TempDir() + "/pebble"
	// Original data lives in the backup (it was moved aside during a partial swap).
	writeStore(t, dir+backupSuffix, atrest.New(none), map[string]string{"x": "orig"})
	// A partial keyed staging copy and a partial swapped-in dir both exist.
	if err := os.MkdirAll(dir+keyedSuffix, 0o700); err != nil {
		t.Fatalf("mkdir keyed: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir partial dir: %v", err)
	}

	// A staging root-key blob exists so the roll-back Delete path is taken.
	if err := mem.Put(ctx, "root-key.migrating", []byte("staging-blob")); err != nil {
		t.Fatalf("seed staging blob: %v", err)
	}

	j := &seal.MigrationJournal{
		Phase: seal.PhaseCopying, TargetStrategy: seal.StrategyPassphrase,
		StagingRootKeyPath: "root-key.migrating",
	}
	jblob, _ := json.Marshal(j)
	if err := mem.Put(ctx, "root-key.migration", jblob); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	if err := RecoverInterruptedMigration(ctx, mem, "root-key", []string{dir}); err != nil {
		t.Fatalf("recover: %v", err)
	}

	// Keyed staging removed, backup restored over dir, staging blob + journal gone.
	if _, err := os.Stat(dir + keyedSuffix); !os.IsNotExist(err) {
		t.Fatal("keyed staging dir must be removed")
	}
	if _, err := os.Stat(dir + backupSuffix); !os.IsNotExist(err) {
		t.Fatal("backup dir must be renamed back into place")
	}
	if got := readAll(t, dir, atrest.New(none)); got["x"] != "orig" {
		t.Fatalf("restored data x=%q, want orig", got["x"])
	}
	if blob, _ := mem.Get(ctx, "root-key.migrating"); len(blob) != 0 {
		t.Fatal("staging root-key blob must be deleted")
	}
	if jj, _ := seal.LoadMigrationJournalFrom(ctx, mem, "root-key"); jj != nil {
		t.Fatal("journal must be cleared after rollback")
	}
}

// TestRecoverInterruptedMigration_LoadJournalError covers the error return when
// the journal cannot be loaded (the backend fails the journal read).
func TestRecoverInterruptedMigration_LoadJournalError(t *testing.T) {
	ctx := context.Background()
	backend := &faultBackend{
		StorageBackend: seal.NewMemoryBackend(),
		failGet:        func(key string) bool { return strings.HasSuffix(key, ".migration") },
	}
	err := RecoverInterruptedMigration(ctx, backend, "root-key", []string{t.TempDir()})
	if !errors.Is(err, errBackendFault) {
		t.Fatalf("Recover err = %v, want %v", err, errBackendFault)
	}
}

// TestRecoverInterruptedMigration_AdoptPutError covers the roll-forward branch
// where the canonical root key is still none and the staging blob must be
// adopted, but the backend Put fails.
func TestRecoverInterruptedMigration_AdoptPutError(t *testing.T) {
	ctx := context.Background()
	const pw = "recover-put-fail"
	mem := seal.NewMemoryBackend()

	// Keyed staging root key present at the staging path.
	keyed, err := seal.NewStorageBarrier(reencTestLogger(), mem,
		seal.BarrierConfig{RootKeyPath: "root-key.migrating"}, seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("keyed barrier: %v", err)
	}
	if err := keyed.Initialize(ctx, seal.MustCredentials(context.Background(), []byte(pw))); err != nil {
		t.Fatalf("init keyed: %v", err)
	}
	_ = atrest.New(keyed)

	// Canonical root key is still the none marker (adopt did not run).
	_ = initNoneBarrierBackend(t, mem, "root-key")

	// Committed journal pointing at the staging path.
	j := &seal.MigrationJournal{
		SourceStrategy: seal.StrategyNone, TargetStrategy: seal.StrategyPassphrase,
		Mechanism: seal.MechanismSwap, Phase: seal.PhaseCommitting,
		StagingRootKeyPath: "root-key.migrating",
	}
	jblob, _ := json.Marshal(j)
	if err := mem.Put(ctx, "root-key.migration", jblob); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	// Wrap so the canonical root-key Put (the adoption write) fails.
	backend := &faultBackend{
		StorageBackend: mem,
		failPut:        func(key string) bool { return key == "root-key" },
	}

	// Use a store dir with no keyed sibling so swapDir is a clean no-op.
	err = RecoverInterruptedMigration(ctx, backend, "root-key", []string{t.TempDir() + "/nope"})
	if !errors.Is(err, errBackendFault) {
		t.Fatalf("Recover err = %v, want %v", err, errBackendFault)
	}
}

// The live record, its staged keyed copy and their values in
// stagedRecoveryFixture.
const (
	recoveryLiveKey    = "store/alpha"
	recoveryStagingKey = "store.migrating/store/alpha"
	recoveryLiveValue  = "none-format-alpha"
	recoveryStaged     = "keyed-alpha"
)

// stagedRecoveryFixture lays out a committed migration interrupted before
// root-key adoption: a keyed staging root key, a canonical none root key, one
// live none-format record and its staged keyed copy, and a committed journal
// naming both.
func stagedRecoveryFixture(t *testing.T) *seal.MemoryBackend {
	t.Helper()
	ctx := context.Background()
	mem := seal.NewMemoryBackend()
	keyed, err := seal.NewStorageBarrier(reencTestLogger(), mem,
		seal.BarrierConfig{RootKeyPath: "root-key.migrating"}, seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("keyed barrier: %v", err)
	}
	if err := keyed.Initialize(ctx, seal.MustCredentials(ctx, []byte("recover-staging"))); err != nil {
		t.Fatalf("init keyed: %v", err)
	}
	_ = initNoneBarrierBackend(t, mem, "root-key")
	if err := mem.Put(ctx, recoveryLiveKey, []byte(recoveryLiveValue)); err != nil {
		t.Fatalf("put live record: %v", err)
	}
	if err := mem.Put(ctx, recoveryStagingKey, []byte(recoveryStaged)); err != nil {
		t.Fatalf("put staged record: %v", err)
	}
	j := &seal.MigrationJournal{
		SourceStrategy: seal.StrategyNone, TargetStrategy: seal.StrategyPassphrase,
		Mechanism: seal.MechanismSwap, Phase: seal.PhaseCommitting,
		StagingRootKeyPath: "root-key.migrating",
		StagedRecords:      []seal.StagedRecords{{Prefix: "store/", StagingPrefix: "store.migrating/"}},
	}
	jblob, _ := json.Marshal(j)
	if err := mem.Put(ctx, "root-key.migration", jblob); err != nil {
		t.Fatalf("write journal: %v", err)
	}
	return mem
}

// assertRecoveryUntouched checks that a refused roll-forward changed nothing:
// the live record still holds its none-format value and the staging root
// key, the staged record and the journal are all still present.
func assertRecoveryUntouched(t *testing.T, mem *seal.MemoryBackend) {
	t.Helper()
	ctx := context.Background()
	if got, err := mem.Get(ctx, recoveryLiveKey); err != nil || string(got) != recoveryLiveValue {
		t.Fatalf("live record = %q (err %v), want %q", got, err, recoveryLiveValue)
	}
	for _, key := range []string{"root-key.migrating", recoveryStagingKey, "root-key.migration"} {
		if blob, err := mem.Get(ctx, key); err != nil || len(blob) == 0 {
			t.Fatalf("%s after the refusal: %d bytes, err %v; want it kept", key, len(blob), err)
		}
	}
	if got := persistedStrategy(ctx, mem, "root-key"); got != seal.StrategyNone {
		t.Fatalf("canonical strategy = %q, want none", got)
	}
}

// TestRecoverInterruptedMigration_StagingRootReadFailsAfterGuard covers a
// staging root key that a caller's guard read but the roll-forward cannot:
// the recovery refuses before any record is copied or anything is cleared.
func TestRecoverInterruptedMigration_StagingRootReadFailsAfterGuard(t *testing.T) {
	mem := stagedRecoveryFixture(t)
	backend := &faultBackend{
		StorageBackend: mem,
		failGet:        func(key string) bool { return key == "root-key.migrating" },
	}
	err := RecoverInterruptedMigration(context.Background(), backend, "root-key", nil)
	if !errors.Is(err, ErrMigrationStagingRootUnreadable) {
		t.Fatalf("Recover err = %v, want %v", err, ErrMigrationStagingRootUnreadable)
	}
	assertRecoveryUntouched(t, mem)
}

// TestRecoverInterruptedMigration_StagingRootEmpty covers an empty staging
// root-key blob: refused like an unreadable one, nothing changed.
func TestRecoverInterruptedMigration_StagingRootEmpty(t *testing.T) {
	ctx := context.Background()
	mem := stagedRecoveryFixture(t)
	if err := mem.Put(ctx, "root-key.migrating", []byte{}); err != nil {
		t.Fatalf("empty staging root: %v", err)
	}
	err := RecoverInterruptedMigration(ctx, mem, "root-key", nil)
	if !errors.Is(err, ErrMigrationStagingRootUnreadable) {
		t.Fatalf("Recover err = %v, want %v", err, ErrMigrationStagingRootUnreadable)
	}
	if got, gerr := mem.Get(ctx, recoveryLiveKey); gerr != nil || string(got) != recoveryLiveValue {
		t.Fatalf("live record = %q (err %v), want %q", got, gerr, recoveryLiveValue)
	}
	for _, key := range []string{recoveryStagingKey, "root-key.migration"} {
		if blob, gerr := mem.Get(ctx, key); gerr != nil || len(blob) == 0 {
			t.Fatalf("%s after the refusal: %d bytes, err %v; want it kept", key, len(blob), gerr)
		}
	}
	if _, gerr := mem.Get(ctx, "root-key.migrating"); gerr != nil {
		t.Fatalf("staging root after the refusal: %v; want it kept", gerr)
	}
}

// TestRecoverInterruptedMigration_StagingWrappedReadFails covers a staging
// wrapped key read that fails with anything but not-found: refused, nothing
// changed.
func TestRecoverInterruptedMigration_StagingWrappedReadFails(t *testing.T) {
	mem := stagedRecoveryFixture(t)
	backend := &faultBackend{
		StorageBackend: mem,
		failGet:        func(key string) bool { return key == seal.WrappedRootKeyPath("root-key.migrating") },
	}
	err := RecoverInterruptedMigration(context.Background(), backend, "root-key", nil)
	if !errors.Is(err, ErrMigrationStagingRootUnreadable) {
		t.Fatalf("Recover err = %v, want %v", err, ErrMigrationStagingRootUnreadable)
	}
	assertRecoveryUntouched(t, mem)
}

// TestRecoverInterruptedMigration_ShamirWrappedKeyAdoptedBeforeEnvelope
// covers a Shamir target interrupted before adoption: the roll-forward
// writes the staged wrapped root key at the canonical wrapped path before the
// canonical envelope, then deletes both staging keys.
func TestRecoverInterruptedMigration_ShamirWrappedKeyAdoptedBeforeEnvelope(t *testing.T) {
	ctx := context.Background()
	mem := stagedRecoveryFixture(t)
	wrapped := []byte("staged-shamir-wrapped-root-key")
	if err := mem.Put(ctx, seal.WrappedRootKeyPath("root-key.migrating"), wrapped); err != nil {
		t.Fatalf("put staging wrapped key: %v", err)
	}
	backend := &faultBackend{StorageBackend: mem}
	if err := RecoverInterruptedMigration(ctx, backend, "root-key", nil); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got, err := mem.Get(ctx, seal.WrappedRootKeyPath("root-key")); err != nil || string(got) != string(wrapped) {
		t.Fatalf("canonical wrapped key = %q (err %v), want %q", got, err, wrapped)
	}
	wrappedAt, envelopeAt := -1, -1
	for i, key := range backend.puts {
		switch key {
		case seal.WrappedRootKeyPath("root-key"):
			wrappedAt = i
		case "root-key":
			envelopeAt = i
		}
	}
	if wrappedAt < 0 || envelopeAt < 0 || wrappedAt > envelopeAt {
		t.Fatalf("put order %v: want the canonical wrapped key before the envelope", backend.puts)
	}
	if got, err := mem.Get(ctx, recoveryLiveKey); err != nil || string(got) != recoveryStaged {
		t.Fatalf("live record = %q (err %v), want %q", got, err, recoveryStaged)
	}
	for _, key := range []string{"root-key.migrating", seal.WrappedRootKeyPath("root-key.migrating")} {
		if _, err := mem.Get(ctx, key); !errors.Is(err, seal.ErrSecretNotFound) {
			t.Fatalf("%s after recovery: err %v, want it deleted", key, err)
		}
	}
}

// TestRecoverInterruptedMigration_ShamirWrappedPutFails covers a failed
// canonical wrapped-key write: the error is returned before the envelope and
// before cleanup, so the journal and staging keys stay for the next boot.
func TestRecoverInterruptedMigration_ShamirWrappedPutFails(t *testing.T) {
	ctx := context.Background()
	mem := stagedRecoveryFixture(t)
	if err := mem.Put(ctx, seal.WrappedRootKeyPath("root-key.migrating"), []byte("wrapped")); err != nil {
		t.Fatalf("put staging wrapped key: %v", err)
	}
	backend := &faultBackend{
		StorageBackend: mem,
		failPut:        func(key string) bool { return key == seal.WrappedRootKeyPath("root-key") },
	}
	if err := RecoverInterruptedMigration(ctx, backend, "root-key", nil); !errors.Is(err, errBackendFault) {
		t.Fatalf("Recover err = %v, want %v", err, errBackendFault)
	}
	for _, key := range []string{"root-key.migration", "root-key.migrating", seal.WrappedRootKeyPath("root-key.migrating")} {
		if blob, err := mem.Get(ctx, key); err != nil || len(blob) == 0 {
			t.Fatalf("%s after the failed adoption: %d bytes, err %v; want it kept", key, len(blob), err)
		}
	}
	if got := persistedStrategy(ctx, mem, "root-key"); got != seal.StrategyNone {
		t.Fatalf("canonical strategy = %q, want none (envelope not written)", got)
	}
}

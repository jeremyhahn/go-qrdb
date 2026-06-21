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
}

func (f *faultBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if f.failGet != nil && f.failGet(key) {
		return nil, errBackendFault
	}
	return f.StorageBackend.Get(ctx, key)
}

func (f *faultBackend) Put(ctx context.Context, key string, data []byte) error {
	if f.failPut != nil && f.failPut(key) {
		return errBackendFault
	}
	return f.StorageBackend.Put(ctx, key, data)
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
	if err := keyed.Initialize(ctx, seal.Credentials{Secret: []byte(pw)}); err != nil {
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

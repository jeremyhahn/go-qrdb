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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// initNoneBarrier returns an initialized none barrier over the shared backend.
func initNoneBarrier(t *testing.T, backend seal.StorageBackend, rootKeyPath string) *seal.StorageBarrier {
	t.Helper()
	b, err := seal.NewStorageBarrier(reencTestLogger(), backend,
		seal.BarrierConfig{RootKeyPath: rootKeyPath, DeploymentID: 1}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier(none): %v", err)
	}
	if err := b.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("Initialize(none): %v", err)
	}
	return b
}

// initKeyedBarrier returns an initialized+unsealed passphrase barrier over the
// shared backend at the given path.
func initKeyedBarrier(t *testing.T, backend seal.StorageBackend, rootKeyPath, passphrase string) *seal.StorageBarrier {
	t.Helper()
	ctx := context.Background()
	strategy, err := seal.NewSoftwareStrategy([]byte(passphrase))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	b, err := seal.NewStorageBarrier(reencTestLogger(), backend,
		seal.BarrierConfig{RootKeyPath: rootKeyPath, DeploymentID: 1}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier(keyed): %v", err)
	}
	creds := seal.Credentials{Secret: []byte(passphrase)}
	if err := b.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize(keyed): %v", err)
	}
	return b
}

// TestNodeStoreMigration_NoneToPassphrase is the standalone end-to-end proof:
// a node with two none-encrypted stores is migrated to a passphrase barrier;
// afterwards the stores hold keyed data, the canonical root key is the keyed
// one, and a fresh barrier unsealed from that root key reads every record back.
func TestNodeStoreMigration_NoneToPassphrase(t *testing.T) {
	ctx := context.Background()
	const passphrase = "enable-encryption-pass"

	// Shared seal backend (models {dataDir}/seal).
	sealBackend := seal.NewMemoryBackend()
	source := initNoneBarrier(t, sealBackend, "root-key")
	target := initKeyedBarrier(t, sealBackend, "root-key.migrating", passphrase)

	noneEnc := atrest.New(source)

	// Two node data stores (e.g. "pebble" + "vector"), written as none/plaintext.
	dirA := t.TempDir() + "/pebble"
	dirB := t.TempDir() + "/vector"
	dataA := map[string]string{}
	dataB := map[string]string{}
	for i := 0; i < 200; i++ {
		dataA[fmt.Sprintf("a/%03d", i)] = fmt.Sprintf("alpha-%d", i)
		dataB[fmt.Sprintf("b/%03d", i)] = fmt.Sprintf("bravo-%d", i)
	}
	writeStore(t, dirA, noneEnc, dataA)
	writeStore(t, dirB, noneEnc, dataB)

	// Quiesced: stores are closed (writeStore closed them). Run the migration.
	mig := &NodeStoreMigration{
		Source:    source,
		Target:    target,
		StoreDirs: []string{dirA, dirB},
		Logger:    reencTestLogger(),
	}
	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.Credentials{Secret: []byte(passphrase)},
	}
	if err := mig.Run(ctx, opts); err != nil {
		t.Fatalf("migration Run: %v", err)
	}

	// Journal cleared after success.
	if j, err := source.LoadMigrationJournal(ctx); err != nil || j != nil {
		t.Fatalf("journal after success = (%v, %v), want (nil, nil)", j, err)
	}

	// Pre-migration backups removed.
	if _, err := os.Stat(dirA + backupSuffix); !os.IsNotExist(err) {
		t.Fatalf("backup dir %s should be removed", dirA+backupSuffix)
	}

	// Reopen the canonical root key as a keyed barrier and read the stores back.
	reopened := initReopenKeyed(t, sealBackend, "root-key", passphrase)
	keyedEnc := atrest.New(reopened)
	gotA := readAll(t, dirA, keyedEnc)
	gotB := readAll(t, dirB, keyedEnc)
	if len(gotA) != len(dataA) || len(gotB) != len(dataB) {
		t.Fatalf("record counts: A=%d/%d B=%d/%d", len(gotA), len(dataA), len(gotB), len(dataB))
	}
	for k, v := range dataA {
		if gotA[k] != v {
			t.Fatalf("store A key %q = %q, want %q", k, gotA[k], v)
		}
	}
	for k, v := range dataB {
		if gotB[k] != v {
			t.Fatalf("store B key %q = %q, want %q", k, gotB[k], v)
		}
	}

	// The stores are genuinely encrypted now: the none encryptor cannot read
	// store A back as plaintext.
	assertNotPlaintext(t, dirA, noneEnc, dataA)
}

// initReopenKeyed constructs a barrier over an EXISTING canonical root-key blob
// (the committed keyed key) and unseals it with the passphrase — modelling a
// node restart after migration.
func initReopenKeyed(t *testing.T, backend seal.StorageBackend, rootKeyPath, passphrase string) *seal.StorageBarrier {
	t.Helper()
	ctx := context.Background()
	strategy, err := seal.NewSoftwareStrategy([]byte(passphrase))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	b, err := seal.NewStorageBarrier(reencTestLogger(), backend,
		seal.BarrierConfig{RootKeyPath: rootKeyPath, DeploymentID: 1}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier(reopen): %v", err)
	}
	// Do NOT Initialize — the canonical root key already exists (committed by the
	// migration). Unseal it directly.
	if err := b.Unseal(ctx, seal.Credentials{Secret: []byte(passphrase)}); err != nil {
		t.Fatalf("Unseal reopened keyed barrier: %v", err)
	}
	return b
}

// assertNotPlaintext fails if the none encryptor can read dir back as the
// expected plaintext (which would mean the data was not actually encrypted).
func assertNotPlaintext(t *testing.T, dir string, noneEnc *atrest.Encryptor, want map[string]string) {
	t.Helper()
	defer func() { _ = recover() }()
	got := tryReadAll(dir, noneEnc)
	matches := 0
	for k, v := range want {
		if got[k] == v {
			matches++
		}
	}
	if matches == len(want) {
		t.Fatalf("none encryptor read %s as plaintext — data was NOT encrypted", dir)
	}
}

// tryReadAll best-effort reads a store, swallowing open/iter errors (expected
// when reading keyed data through the wrong encryptor).
func tryReadAll(dir string, enc *atrest.Encryptor) map[string]string {
	out := map[string]string{}
	defer func() { _ = recover() }()
	opts := &pebble.Options{ReadOnly: true}
	enc.Apply(opts)
	db, err := pebble.Open(dir, opts)
	if err != nil {
		return out
	}
	defer func() { _ = db.Close() }()
	it, err := db.NewIter(nil)
	if err != nil {
		return out
	}
	defer func() { _ = it.Close() }()
	for valid := it.First(); valid; valid = it.Next() {
		out[string(it.Key())] = string(it.Value())
	}
	return out
}

// TestNodeStoreMigration_InPlaceMechanism proves the in-place (raw-file
// recodec) mechanism migrates none->keyed end to end with the data intact —
// the structure-preserving alternative to the swap mechanism.
func TestNodeStoreMigration_InPlaceMechanism(t *testing.T) {
	ctx := context.Background()
	const passphrase = "inplace-pass"
	sealBackend := seal.NewMemoryBackend()
	source := initNoneBarrier(t, sealBackend, "root-key")
	target := initKeyedBarrier(t, sealBackend, "root-key.migrating", passphrase)

	noneEnc := atrest.New(source)
	dir := t.TempDir() + "/pebble"
	data := map[string]string{}
	for i := 0; i < 150; i++ {
		data[fmt.Sprintf("k/%03d", i)] = fmt.Sprintf("v-%d", i)
	}
	writeStore(t, dir, noneEnc, data)

	mig := &NodeStoreMigration{Source: source, Target: target, StoreDirs: []string{dir}, Logger: reencTestLogger()}
	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismInPlace,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.Credentials{Secret: []byte(passphrase)},
	}
	if err := mig.Run(ctx, opts); err != nil {
		t.Fatalf("in-place migration Run: %v", err)
	}

	reopened := initReopenKeyed(t, sealBackend, "root-key", passphrase)
	got := readAll(t, dir, atrest.New(reopened))
	if len(got) != len(data) {
		t.Fatalf("got %d records, want %d", len(got), len(data))
	}
	for k, v := range data {
		if got[k] != v {
			t.Fatalf("key %q = %q, want %q", k, got[k], v)
		}
	}
	assertNotPlaintext(t, dir, noneEnc, data)
}

// TestNodeStoreMigration_ReopenWithDeferredAndNone reproduces the node restart
// conditions: after a none->passphrase migration whose target was minted with
// the deferred-software strategy, a fresh barrier registered with BOTH none
// (config) and deferred-software (persisted-strategy detection) must unseal
// from the canonical key and read the migrated data.
func TestNodeStoreMigration_ReopenWithDeferredAndNone(t *testing.T) {
	ctx := context.Background()
	const pw = "deferred-pass"
	sealBackend := seal.NewMemoryBackend()
	source := initNoneBarrier(t, sealBackend, "root-key")

	// Mint the keyed target with the DEFERRED software strategy (matching the
	// node migration path) at a staging path in the same backend.
	deferred := seal.NewDeferredSoftwareStrategy()
	target, err := seal.NewStorageBarrier(reencTestLogger(), sealBackend,
		seal.BarrierConfig{RootKeyPath: "root-key.migrating", DeploymentID: 1}, deferred)
	if err != nil {
		t.Fatalf("target barrier: %v", err)
	}
	if err := target.Initialize(ctx, seal.Credentials{Secret: []byte(pw)}); err != nil {
		t.Fatalf("init target: %v", err)
	}

	dir := t.TempDir() + "/pebble"
	data := map[string]string{"a": "1", "b": "2", "c": "3"}
	writeStore(t, dir, atrest.New(source), data)

	mig := &NodeStoreMigration{Source: source, Target: target, StoreDirs: []string{dir}, Logger: reencTestLogger()}
	if err := mig.Run(ctx, seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase, Mechanism: seal.MechanismSwap,
		Availability: seal.AvailabilityQuiesced, Credentials: seal.Credentials{Secret: []byte(pw)},
	}); err != nil {
		t.Fatalf("migration: %v", err)
	}

	// Reopen exactly as the node boot does: register [none, deferred-software].
	reopened, err := seal.NewStorageBarrier(reencTestLogger(), sealBackend,
		seal.BarrierConfig{RootKeyPath: "root-key", DeploymentID: 1},
		seal.NewNoneStrategy(), seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("reopen barrier: %v", err)
	}
	if got := reopened.PersistedStrategy(ctx); got != seal.StrategyPassphrase {
		t.Fatalf("PersistedStrategy = %q, want passphrase", got)
	}
	if err := reopened.Unseal(ctx, seal.Credentials{Secret: []byte(pw)}); err != nil {
		t.Fatalf("unseal reopened: %v", err)
	}
	got := readAll(t, dir, atrest.New(reopened))
	for k, v := range data {
		if got[k] != v {
			t.Fatalf("key %q = %q, want %q", k, got[k], v)
		}
	}
}

// TestNodeStoreMigration_DiskSealStoreReopen mirrors a full node restart: the
// seal store is a real on-disk Pebble closed after migration and re-opened as a
// fresh instance over the same directory (as a process restart would), proving
// the canonical keyed root key persists durably and the migrated data decrypts.
func TestNodeStoreMigration_DiskSealStoreReopen(t *testing.T) {
	ctx := context.Background()
	const pw = "disk-reopen-pass"
	sealDir := t.TempDir() + "/seal"
	dir := t.TempDir() + "/pebble"
	data := map[string]string{"a": "1", "b": "2", "c": "3"}

	// --- session 1: none barrier, write data, migrate to passphrase ---------
	store1, err := pebblestore.New(pebblestore.DefaultConfig(sealDir))
	if err != nil {
		t.Fatalf("seal store: %v", err)
	}
	be1 := seal.NewStorageAdapter(store1)
	source := initNoneBarrierBackend(t, be1, "root-key")
	writeStore(t, dir, atrest.New(source), data)

	target, err := seal.NewStorageBarrier(reencTestLogger(), be1,
		seal.BarrierConfig{RootKeyPath: "root-key.migrating", DeploymentID: 1}, seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("target: %v", err)
	}
	if err := target.Initialize(ctx, seal.Credentials{Secret: []byte(pw)}); err != nil {
		t.Fatalf("init target: %v", err)
	}
	mig := &NodeStoreMigration{Source: source, Target: target, StoreDirs: []string{dir}, Logger: reencTestLogger()}
	if err := mig.Run(ctx, seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase, Mechanism: seal.MechanismSwap,
		Availability: seal.AvailabilityQuiesced, Credentials: seal.Credentials{Secret: []byte(pw)},
	}); err != nil {
		t.Fatalf("migration: %v", err)
	}
	if err := store1.Close(); err != nil { // close seal store as a shutdown would
		t.Fatalf("close seal store: %v", err)
	}

	// --- session 2: reopen the seal store from disk, unseal, read -----------
	store2, err := pebblestore.New(pebblestore.DefaultConfig(sealDir))
	if err != nil {
		t.Fatalf("reopen seal store: %v", err)
	}
	defer func() { _ = store2.Close() }()
	be2 := seal.NewStorageAdapter(store2)
	reopened, err := seal.NewStorageBarrier(reencTestLogger(), be2,
		seal.BarrierConfig{RootKeyPath: "root-key", DeploymentID: 1},
		seal.NewNoneStrategy(), seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("reopen barrier: %v", err)
	}
	if got := reopened.PersistedStrategy(ctx); got != seal.StrategyPassphrase {
		t.Fatalf("PersistedStrategy after disk reopen = %q, want passphrase", got)
	}
	if err := reopened.Unseal(ctx, seal.Credentials{Secret: []byte(pw)}); err != nil {
		t.Fatalf("unseal after disk reopen: %v", err)
	}
	got := readAll(t, dir, atrest.New(reopened))
	for k, v := range data {
		if got[k] != v {
			t.Fatalf("key %q = %q after disk reopen, want %q", k, got[k], v)
		}
	}
}

// TestRecoverInterruptedMigration_RollForward simulates a crash in the
// dangerous window — the data dir is already keyed but the canonical root key
// is still "none" (the root-key adopt had not run). Recovery must adopt the
// staging keyed blob so the node boots keyed with data intact.
func TestRecoverInterruptedMigration_RollForward(t *testing.T) {
	ctx := context.Background()
	const pw = "recover-fwd-pass"
	backend := seal.NewMemoryBackend()
	dir := t.TempDir() + "/pebble"
	data := map[string]string{"a": "1", "b": "2"}

	// Keyed barrier at the staging path; its blob is the keyed root key.
	keyed, err := seal.NewStorageBarrier(reencTestLogger(), backend,
		seal.BarrierConfig{RootKeyPath: "root-key.migrating"}, seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("keyed barrier: %v", err)
	}
	if err := keyed.Initialize(ctx, seal.Credentials{Secret: []byte(pw)}); err != nil {
		t.Fatalf("init keyed: %v", err)
	}
	// The data dir is already the keyed copy (swap completed pre-crash).
	writeStore(t, dir, atrest.New(keyed), data)

	// Canonical root key is still the none marker (adopt did NOT run yet).
	none := initNoneBarrierBackend(t, backend, "root-key")
	_ = none

	// Journal records the committing phase + the staging path.
	j := &seal.MigrationJournal{
		SourceStrategy: seal.StrategyNone, TargetStrategy: seal.StrategyPassphrase,
		Mechanism: seal.MechanismSwap, Phase: seal.PhaseCommitting,
		StagingRootKeyPath: "root-key.migrating",
	}
	jblob, _ := json.Marshal(j)
	if err := backend.Put(ctx, "root-key.migration", jblob); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	// Recover.
	if err := RecoverInterruptedMigration(ctx, backend, "root-key", []string{dir}); err != nil {
		t.Fatalf("RecoverInterruptedMigration: %v", err)
	}

	// Canonical root key is now keyed; journal + staging are gone.
	if got := persistedStrategy(ctx, backend, "root-key"); got != seal.StrategyPassphrase {
		t.Fatalf("canonical strategy after recovery = %q, want passphrase", got)
	}
	if jj, _ := seal.LoadMigrationJournalFrom(ctx, backend, "root-key"); jj != nil {
		t.Fatal("journal must be cleared after recovery")
	}

	// Data is readable through a keyed barrier unsealed from the (now keyed)
	// canonical root key.
	reopened, err := seal.NewStorageBarrier(reencTestLogger(), backend,
		seal.BarrierConfig{RootKeyPath: "root-key"}, seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := reopened.Unseal(ctx, seal.Credentials{Secret: []byte(pw)}); err != nil {
		t.Fatalf("unseal recovered: %v", err)
	}
	got := readAll(t, dir, atrest.New(reopened))
	for k, v := range data {
		if got[k] != v {
			t.Fatalf("key %q = %q, want %q", k, got[k], v)
		}
	}
}

// TestRecoverInterruptedMigration_RollBack simulates a crash before commit: a
// keyed staging copy exists but the original is intact and the root key is
// still none. Recovery must discard the staging copy and clear the journal.
func TestRecoverInterruptedMigration_RollBack(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	dir := t.TempDir() + "/pebble"
	data := map[string]string{"x": "1"}

	none := initNoneBarrierBackend(t, backend, "root-key")
	writeStore(t, dir, atrest.New(none), data) // original none store, intact

	// A partial keyed staging copy exists.
	require := func(cond bool, msg string) {
		if !cond {
			t.Fatal(msg)
		}
	}
	if err := os.MkdirAll(dir+keyedSuffix, 0o700); err != nil {
		t.Fatalf("mkdir keyed: %v", err)
	}

	j := &seal.MigrationJournal{Phase: seal.PhaseCopying, TargetStrategy: seal.StrategyPassphrase}
	jblob, _ := json.Marshal(j)
	if err := backend.Put(ctx, "root-key.migration", jblob); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	if err := RecoverInterruptedMigration(ctx, backend, "root-key", []string{dir}); err != nil {
		t.Fatalf("recover: %v", err)
	}

	// Staging copy removed, journal cleared, original none store intact.
	if _, err := os.Stat(dir + keyedSuffix); !os.IsNotExist(err) {
		t.Fatal("keyed staging dir must be removed on rollback")
	}
	if jj, _ := seal.LoadMigrationJournalFrom(ctx, backend, "root-key"); jj != nil {
		t.Fatal("journal must be cleared after rollback")
	}
	if got := persistedStrategy(ctx, backend, "root-key"); got != seal.StrategyNone {
		t.Fatalf("canonical strategy after rollback = %q, want none", got)
	}
	got := readAll(t, dir, atrest.New(none))
	require(got["x"] == "1", "original data must survive rollback")
}

// TestRecoverInterruptedMigration_NoJournal is a no-op when no migration ran.
func TestRecoverInterruptedMigration_NoJournal(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	if err := RecoverInterruptedMigration(ctx, backend, "root-key", []string{t.TempDir()}); err != nil {
		t.Fatalf("recover (no journal) = %v, want nil", err)
	}
}

// initNoneBarrierBackend is initNoneBarrier over a caller-supplied backend.
func initNoneBarrierBackend(t *testing.T, backend seal.StorageBackend, rootKeyPath string) *seal.StorageBarrier {
	t.Helper()
	b, err := seal.NewStorageBarrier(reencTestLogger(), backend,
		seal.BarrierConfig{RootKeyPath: rootKeyPath, DeploymentID: 1}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("none barrier: %v", err)
	}
	if err := b.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("init none: %v", err)
	}
	return b
}

// TestNodeStoreMigration_RejectsUnknownMechanism confirms an unknown mechanism
// is rejected before any work is done.
func TestNodeStoreMigration_RejectsUnknownMechanism(t *testing.T) {
	sealBackend := seal.NewMemoryBackend()
	source := initNoneBarrier(t, sealBackend, "root-key")
	target := initKeyedBarrier(t, sealBackend, "root-key.migrating", "pw")
	mig := &NodeStoreMigration{Source: source, Target: target, StoreDirs: []string{t.TempDir()}}
	opts := seal.MigrationOptions{TargetStrategy: seal.StrategyPassphrase, Mechanism: "bogus", Availability: seal.AvailabilityQuiesced}
	if err := mig.Run(context.Background(), opts); err == nil {
		t.Fatal("orchestrator must reject an unknown mechanism")
	}
}

// TestNodeStoreMigration_RecodeFailureRollsBack proves a copy-phase failure
// (a store dir that is not a valid Pebble store) surfaces a MigrateStoreDirError
// and the rollback defer discards the keyed staging copies, leaving the source
// untouched.
func TestNodeStoreMigration_RecodeFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	sealBackend := seal.NewMemoryBackend()
	source := initNoneBarrier(t, sealBackend, "root-key")
	target := initKeyedBarrier(t, sealBackend, "root-key.migrating", "pw")

	// A path that exists as a FILE (not a directory) makes Pebble open fail.
	badDir := filepath.Join(t.TempDir(), "not-a-store")
	if err := os.WriteFile(badDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed bad store: %v", err)
	}

	mig := &NodeStoreMigration{Source: source, Target: target, StoreDirs: []string{badDir}, Logger: reencTestLogger()}
	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.Credentials{Secret: []byte("pw")},
	}
	err := mig.Run(ctx, opts)
	var dirErr *MigrateStoreDirError
	if !errors.As(err, &dirErr) {
		t.Fatalf("err = %v, want *MigrateStoreDirError", err)
	}
	// Rollback removed the keyed staging copy.
	if _, serr := os.Stat(badDir + KeyedDirSuffix); !os.IsNotExist(serr) {
		t.Fatalf("rollback must remove the keyed staging copy")
	}
	// The source canonical root key is still none (commit never reached).
	if got := source.Status().Strategy; got != seal.StrategyNone {
		t.Fatalf("source strategy = %q, want none after rollback", got)
	}
}

// ---------- Journal-driven crash recovery (tenant-barrier hook path) ----------

// TestRecoverInterruptedMigration_JournalStoreDirs_RollForward simulates the
// tenant-scope crash window: the process died mid-commit BEFORE the directory
// swap, with the store-directory set recorded only in the journal. Recovery is
// invoked with nil storeDirs — exactly how the scope-agnostic tenant-barrier
// hook calls it — and must complete the swap and adopt the keyed root key from
// the journal alone.
func TestRecoverInterruptedMigration_JournalStoreDirs_RollForward(t *testing.T) {
	ctx := context.Background()
	const pw = "journal-dirs-pass"
	backend := seal.NewMemoryBackend()
	dir := t.TempDir() + "/pebble"
	data := map[string]string{"k1": "v1", "k2": "v2"}

	// Keyed barrier at the staging path holds the keyed root key; the keyed
	// data copy is still at the staging sibling (the swap never ran).
	keyed := initKeyedBarrier(t, backend, "root-key.migrating", pw)
	writeStore(t, dir+keyedSuffix, atrest.New(keyed), data)

	// Original none store is still live at dir; canonical root key still none.
	none := initNoneBarrierBackend(t, backend, "root-key")
	writeStore(t, dir, atrest.New(none), data)

	j := &seal.MigrationJournal{
		SourceStrategy: seal.StrategyNone, TargetStrategy: seal.StrategyPassphrase,
		Mechanism: seal.MechanismSwap, Phase: seal.PhaseCommitting,
		StagingRootKeyPath: "root-key.migrating",
		StoreDirs:          []string{dir},
	}
	jblob, _ := json.Marshal(j)
	if err := backend.Put(ctx, "root-key.migration", jblob); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	// nil storeDirs: the recovery must find the dirs in the journal.
	if err := RecoverInterruptedMigration(ctx, backend, "root-key", nil); err != nil {
		t.Fatalf("RecoverInterruptedMigration: %v", err)
	}

	if got := persistedStrategy(ctx, backend, "root-key"); got != seal.StrategyPassphrase {
		t.Fatalf("canonical strategy after recovery = %q, want passphrase", got)
	}
	if jj, _ := seal.LoadMigrationJournalFrom(ctx, backend, "root-key"); jj != nil {
		t.Fatal("journal must be cleared after recovery")
	}
	if _, err := os.Stat(dir + keyedSuffix); !os.IsNotExist(err) {
		t.Fatal("keyed staging dir must be swapped into place")
	}
	if _, err := os.Stat(dir + backupSuffix); !os.IsNotExist(err) {
		t.Fatal("pre-migration backup must be removed after roll-forward")
	}

	// Data is readable through a keyed barrier unsealed from the adopted key.
	// DeploymentID matches the fixtures' (it participates in DEK derivation).
	reopened, err := seal.NewStorageBarrier(reencTestLogger(), backend,
		seal.BarrierConfig{RootKeyPath: "root-key", DeploymentID: 1}, seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := reopened.Unseal(ctx, seal.Credentials{Secret: []byte(pw)}); err != nil {
		t.Fatalf("unseal recovered: %v", err)
	}
	got := readAll(t, dir, atrest.New(reopened))
	for k, v := range data {
		if got[k] != v {
			t.Fatalf("key %q = %q, want %q", k, got[k], v)
		}
	}
}

// TestRecoverInterruptedMigration_StagedSnapshots_RollForward verifies a
// committed journal's staged snapshot data files (the IsolationRaft case,
// whose snapshots live outside every swapped directory) are renamed into
// place on roll-forward.
func TestRecoverInterruptedMigration_StagedSnapshots_RollForward(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	initNoneBarrierBackend(t, backend, "root-key")

	snapDir := t.TempDir()
	final := filepath.Join(snapDir, SnapshotDataFile)
	staged := final + SnapshotKeyedSuffix
	if err := os.WriteFile(final, []byte("source-encrypted"), 0o600); err != nil {
		t.Fatalf("seed final: %v", err)
	}
	if err := os.WriteFile(staged, []byte("keyed"), 0o600); err != nil {
		t.Fatalf("seed staged: %v", err)
	}

	j := &seal.MigrationJournal{
		Phase:           seal.PhaseCommitting,
		TargetStrategy:  seal.StrategyPassphrase,
		StagedSnapshots: []seal.StagedSnapshot{{FinalPath: final, KeyedPath: staged}},
	}
	jblob, _ := json.Marshal(j)
	if err := backend.Put(ctx, "root-key.migration", jblob); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	if err := RecoverInterruptedMigration(ctx, backend, "root-key", nil); err != nil {
		t.Fatalf("recover: %v", err)
	}

	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if string(got) != "keyed" {
		t.Fatalf("final snapshot = %q, want the keyed staged copy", got)
	}
	if _, serr := os.Stat(staged); !os.IsNotExist(serr) {
		t.Fatal("staged sibling must be renamed away on roll-forward")
	}
	if jj, _ := seal.LoadMigrationJournalFrom(ctx, backend, "root-key"); jj != nil {
		t.Fatal("journal must be cleared after recovery")
	}
}

// TestRecoverInterruptedMigration_StagedSnapshots_RollBack verifies an
// uncommitted journal's staged snapshot siblings are discarded on roll-back,
// leaving the live snapshot data untouched.
func TestRecoverInterruptedMigration_StagedSnapshots_RollBack(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	initNoneBarrierBackend(t, backend, "root-key")

	snapDir := t.TempDir()
	final := filepath.Join(snapDir, SnapshotDataFile)
	staged := final + SnapshotKeyedSuffix
	if err := os.WriteFile(final, []byte("live"), 0o600); err != nil {
		t.Fatalf("seed final: %v", err)
	}
	if err := os.WriteFile(staged, []byte("partial-keyed"), 0o600); err != nil {
		t.Fatalf("seed staged: %v", err)
	}

	j := &seal.MigrationJournal{
		Phase:           seal.PhaseCopying,
		TargetStrategy:  seal.StrategyPassphrase,
		StagedSnapshots: []seal.StagedSnapshot{{FinalPath: final, KeyedPath: staged}},
	}
	jblob, _ := json.Marshal(j)
	if err := backend.Put(ctx, "root-key.migration", jblob); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	if err := RecoverInterruptedMigration(ctx, backend, "root-key", nil); err != nil {
		t.Fatalf("recover: %v", err)
	}

	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if string(got) != "live" {
		t.Fatalf("final snapshot = %q, want untouched live copy", got)
	}
	if _, serr := os.Stat(staged); !os.IsNotExist(serr) {
		t.Fatal("staged sibling must be removed on roll-back")
	}
	if jj, _ := seal.LoadMigrationJournalFrom(ctx, backend, "root-key"); jj != nil {
		t.Fatal("journal must be cleared after rollback")
	}
}

// capturingJournal records a deep copy of the journal at every checkpoint so a
// test can assert what crash-recovery state Run persisted mid-flight.
type capturingJournal struct {
	*seal.StorageBarrier
	checkpoints []seal.MigrationJournal
}

func (c *capturingJournal) CheckpointMigration(ctx context.Context, j *seal.MigrationJournal) error {
	cp := *j
	cp.StoreDirs = append([]string(nil), j.StoreDirs...)
	cp.StagedSnapshots = append([]seal.StagedSnapshot(nil), j.StagedSnapshots...)
	c.checkpoints = append(c.checkpoints, cp)
	return c.StorageBarrier.CheckpointMigration(ctx, j)
}

// TestNodeStoreMigration_JournalRecordsStoreDirs asserts Run checkpoints the
// store-directory set into the journal before any phase work, so a crash at
// ANY later point leaves a journal a scope-agnostic recoverer can act on.
func TestNodeStoreMigration_JournalRecordsStoreDirs(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	source := initNoneBarrier(t, backend, "root-key")
	target := initKeyedBarrier(t, backend, "root-key.migrating", "pw")

	dirA := t.TempDir() + "/pebble"
	dirB := t.TempDir() + "/vector"
	noneEnc := atrest.New(source)
	writeStore(t, dirA, noneEnc, map[string]string{"a": "1"})
	writeStore(t, dirB, noneEnc, map[string]string{"b": "2"})

	cap := &capturingJournal{StorageBarrier: source}
	mig := &NodeStoreMigration{
		Source:    source,
		Target:    target,
		StoreDirs: []string{dirA, dirB},
		Logger:    reencTestLogger(),
		journal:   cap,
	}
	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.Credentials{Secret: []byte("pw")},
	}
	if err := mig.Run(ctx, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(cap.checkpoints) == 0 {
		t.Fatal("no checkpoints captured")
	}
	// The VERY FIRST checkpoint (before any phase work) must already carry the
	// full directory set and the staging root-key path.
	first := cap.checkpoints[0]
	if len(first.StoreDirs) != 2 || first.StoreDirs[0] != dirA || first.StoreDirs[1] != dirB {
		t.Fatalf("first checkpoint StoreDirs = %v, want [%s %s]", first.StoreDirs, dirA, dirB)
	}
	if first.StagingRootKeyPath != "root-key.migrating" {
		t.Fatalf("first checkpoint StagingRootKeyPath = %q", first.StagingRootKeyPath)
	}
}

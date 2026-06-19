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
	"fmt"
	"os"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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
	strat, err := seal.NewSoftwareStrategy([]byte(passphrase))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	b, err := seal.NewStorageBarrier(reencTestLogger(), backend,
		seal.BarrierConfig{RootKeyPath: rootKeyPath, DeploymentID: 1}, strat)
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
	strat, err := seal.NewSoftwareStrategy([]byte(passphrase))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	b, err := seal.NewStorageBarrier(reencTestLogger(), backend,
		seal.BarrierConfig{RootKeyPath: rootKeyPath, DeploymentID: 1}, strat)
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

// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/logdb"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb/waldb"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// newNoneBarrier returns an initialized+unsealed none-strategy StorageBarrier.
func newNoneBarrier(t *testing.T, rootKeyPath string) *seal.StorageBarrier {
	t.Helper()
	b, err := seal.NewStorageBarrier(reencTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: rootKeyPath, DeploymentID: 1}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier(none): %v", err)
	}
	if err := b.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("Initialize(none): %v", err)
	}
	return b
}

// newKeyedBarrier returns an initialized+unsealed passphrase StorageBarrier.
func newKeyedBarrier(t *testing.T, rootKeyPath, passphrase string) *seal.StorageBarrier {
	t.Helper()
	strat, err := seal.NewSoftwareStrategy([]byte(passphrase))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	b, err := seal.NewStorageBarrier(reencTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: rootKeyPath, DeploymentID: 1}, strat)
	if err != nil {
		t.Fatalf("NewStorageBarrier(keyed): %v", err)
	}
	if err := b.Initialize(context.Background(), seal.Credentials{Secret: []byte(passphrase)}); err != nil {
		t.Fatalf("Initialize(keyed): %v", err)
	}
	return b
}

// writeWALNode opens a waldb at dir through the barrier and writes one node's
// bootstrap + hard state + a run of log entries.
func writeWALNode(t *testing.T, dir string, b *seal.StorageBarrier, shardID, replicaID uint64, n int) {
	t.Helper()
	db, err := waldb.Open(dir, waldb.WithBarrier(b.EpochBarrier()))
	if err != nil {
		t.Fatalf("waldb.Open(%s): %v", dir, err)
	}
	if err := db.SaveBootstrap(shardID, replicaID, logdb.Bootstrap{
		Addresses: map[uint64]string{replicaID: "127.0.0.1:1"}, Join: false, Type: 7,
	}); err != nil {
		t.Fatalf("SaveBootstrap: %v", err)
	}
	entries := make([]logdb.Entry, 0, n)
	for i := 0; i < n; i++ {
		entries = append(entries, logdb.Entry{Index: uint64(i + 1), Term: 1, Cmd: []byte("wal-payload")})
	}
	if err := db.SaveState([]logdb.Update{{
		ShardID: shardID, ReplicaID: replicaID,
		State:   logdb.State{Term: 1, Vote: replicaID, Commit: uint64(n)},
		Entries: entries,
	}}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("waldb.Close: %v", err)
	}
}

// TestReencryptWALStore_NoneToKeyed proves the WAL re-encryption wrapper
// round-trips a Raft write-ahead log from a none barrier into a keyed barrier
// with every entry preserved, and that the result is genuinely encrypted.
func TestReencryptWALStore_NoneToKeyed(t *testing.T) {
	const shardID, replicaID = uint64(1), uint64(1)
	none := newNoneBarrier(t, "wal-none/root-key")
	keyed := newKeyedBarrier(t, "wal-keyed/root-key", "wal-pass")

	srcDir := filepath.Join(t.TempDir(), "wal")
	dstDir := filepath.Join(t.TempDir(), "wal.keyed")

	writeWALNode(t, srcDir, none, shardID, replicaID, 12)

	count, err := ReencryptWALStore(srcDir, none, dstDir, keyed)
	if err != nil {
		t.Fatalf("ReencryptWALStore: %v", err)
	}
	if count != 1 {
		t.Fatalf("re-encrypted %d nodes, want 1", count)
	}

	// Read back through the keyed barrier — entries must match.
	dst, err := waldb.Open(dstDir, waldb.WithBarrier(keyed.EpochBarrier()))
	if err != nil {
		t.Fatalf("open keyed WAL: %v", err)
	}
	first, length, _ := dst.EntryRange(shardID, replicaID)
	if length != 12 {
		_ = dst.Close()
		t.Fatalf("entry count = %d, want 12", length)
	}
	got, _, eerr := dst.IterateEntries(nil, 0, shardID, replicaID, first, first+length, 1<<30)
	_ = dst.Close()
	if eerr != nil {
		t.Fatalf("IterateEntries: %v", eerr)
	}
	if len(got) != 12 {
		t.Fatalf("iterated %d entries, want 12", len(got))
	}

	// Genuinely encrypted: the none barrier cannot open the keyed WAL.
	if probe, perr := waldb.Open(dstDir, waldb.WithBarrier(none.EpochBarrier())); perr == nil {
		_ = probe.Close()
		t.Fatal("none barrier opened the keyed WAL — not encrypted")
	}
}

// TestReencryptWALStore_PlaintextSides covers the nil-barrier (plaintext) paths
// of the wrapper: a nil source and/or nil destination barrier.
func TestReencryptWALStore_PlaintextSides(t *testing.T) {
	const shardID, replicaID = uint64(2), uint64(1)
	srcDir := filepath.Join(t.TempDir(), "wal")
	dstDir := filepath.Join(t.TempDir(), "wal.plain")

	// Write a plaintext WAL (nil barrier) by opening with no barrier option.
	db, err := waldb.Open(srcDir)
	if err != nil {
		t.Fatalf("waldb.Open(plaintext): %v", err)
	}
	if err := db.SaveState([]logdb.Update{{
		ShardID: shardID, ReplicaID: replicaID,
		State:   logdb.State{Term: 1, Vote: replicaID, Commit: 3},
		Entries: []logdb.Entry{{Index: 1, Term: 1, Cmd: []byte("a")}, {Index: 2, Term: 1, Cmd: []byte("b")}, {Index: 3, Term: 1, Cmd: []byte("c")}},
	}}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	_ = db.Close()

	// nil source + nil destination barrier = plaintext-to-plaintext copy.
	count, err := ReencryptWALStore(srcDir, nil, dstDir, nil)
	if err != nil {
		t.Fatalf("ReencryptWALStore(nil,nil): %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

// TestSwapStoreDir_And_RemoveStoreBackup exercises the exported atomic swap and
// backup-cleanup helpers, including the idempotent recovery no-op.
func TestSwapStoreDir_And_RemoveStoreBackup(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "store")
	keyed := dir + KeyedDirSuffix
	backup := dir + BackupDirSuffix

	mustWriteFile(t, filepath.Join(mustMkdir(t, dir), "marker"), "original")
	mustWriteFile(t, filepath.Join(mustMkdir(t, keyed), "marker"), "keyed")

	// Swap: keyed -> dir, original -> backup.
	if err := SwapStoreDir(dir); err != nil {
		t.Fatalf("SwapStoreDir: %v", err)
	}
	if got := mustReadFile(t, filepath.Join(dir, "marker")); got != "keyed" {
		t.Fatalf("after swap dir/marker = %q, want keyed", got)
	}
	if got := mustReadFile(t, filepath.Join(backup, "marker")); got != "original" {
		t.Fatalf("backup/marker = %q, want original", got)
	}
	if _, err := os.Stat(keyed); !os.IsNotExist(err) {
		t.Fatalf("keyed dir should be gone after swap")
	}

	// Idempotent: a second swap is a no-op (keyed copy absent), data intact.
	if err := SwapStoreDir(dir); err != nil {
		t.Fatalf("SwapStoreDir (idempotent): %v", err)
	}
	if got := mustReadFile(t, filepath.Join(dir, "marker")); got != "keyed" {
		t.Fatalf("after idempotent swap dir/marker = %q, want keyed", got)
	}

	// RemoveStoreBackup clears the backup.
	RemoveStoreBackup(dir)
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("backup dir should be removed")
	}
}

func mustMkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

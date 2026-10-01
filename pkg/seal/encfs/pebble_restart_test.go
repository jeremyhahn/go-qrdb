// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package encfs_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// restartTestPassphrase is the shared passphrase used to auto-unseal the
// barrier across both simulated boots in TestPebble_SurvivesRestart.
var restartTestPassphrase = []byte("correct horse battery staple")

// bootBarrier constructs a *seal.StorageBarrier the same way
// buildNodeSealSubsystem does in production: a fresh *pebblestore.PebbleStorage
// rooted at sealDir backs the root-key blob, wrapped by
// seal.NewStorageAdapter, sealed with the deferred (credentials-at-unseal)
// passphrase strategy. The returned *pebblestore.PebbleStorage is the
// caller's to Close, mirroring the fact that the seal store outlives any
// single StorageBarrier value in the real boot sequence.
func bootBarrier(t *testing.T, sealDir string) (*seal.StorageBarrier, *pebblestore.PebbleStorage) {
	t.Helper()

	// SyncWrites mirrors the production fix in buildNodeSealSubsystem /
	// initSealSubsystem: the seal store is the sole persisted copy of the
	// root-key blob and is NOT protected by Raft's replicated WAL, so its
	// writes must be durable on their own.
	sealCfg := pebblestore.DefaultConfig(sealDir)
	sealCfg.SyncWrites = true
	sealStore, err := pebblestore.New(sealCfg)
	if err != nil {
		t.Fatalf("pebblestore.New(seal): %v", err)
	}

	sealBackend := seal.NewStorageAdapter(sealStore)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	barrier, err := seal.NewStorageBarrier(logger, sealBackend, seal.BarrierConfig{
		RootKeyPath: "root-key",
	}, seal.NewDeferredSoftwareStrategy())
	if err != nil {
		_ = sealStore.Close()
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	ctx := context.Background()
	creds := seal.MustCredentials(bytes.Clone(restartTestPassphrase)) // MustCredentials wipes its argument

	// Mirror attemptAutoUnseal: try Initialize first (first boot), fall back
	// to Unseal when the root key is already persisted (every later boot).
	if err := barrier.Initialize(ctx, creds); err != nil {
		if err != seal.ErrAlreadyInitialized {
			_ = sealStore.Close()
			t.Fatalf("Initialize: %v", err)
		}
		if err := barrier.Unseal(ctx, creds); err != nil {
			_ = sealStore.Close()
			t.Fatalf("Unseal: %v", err)
		}
	}

	return barrier, sealStore
}

// TestPebble_SurvivesRestartWithPassphraseBarrier reproduces the
// production data-loss defect: a Pebble store opened through encfs on top
// of a passphrase-sealed StorageBarrier must remain readable across a
// process restart that reconstructs the barrier from its persisted root
// key, exactly as xkmsd does after a crash/restart of an embedded qrdb
// node. Before the fix this fails with quicraft/seal: decryption failed
// while Pebble opens its MANIFEST during the second boot.
func TestPebble_SurvivesRestartWithPassphraseBarrier(t *testing.T) {
	root := t.TempDir()
	sealDir := filepath.Join(root, "seal")
	dataDir := filepath.Join(root, "pebble")

	// --- Boot 1 -------------------------------------------------------
	barrier1, sealStore1 := bootBarrier(t, sealDir)

	fs1 := encfs.New(vfs.Default, barrier1)
	db, err := pebble.Open(dataDir, &pebble.Options{FS: fs1})
	if err != nil {
		t.Fatalf("boot 1: pebble.Open: %v", err)
	}
	if err := db.Set([]byte("alpha"), []byte("first boot value"), pebble.Sync); err != nil {
		t.Fatalf("boot 1: Set: %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("boot 1: Flush: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("boot 1: db.Close: %v", err)
	}
	if err := barrier1.Seal(); err != nil {
		t.Fatalf("boot 1: barrier.Seal: %v", err)
	}
	if err := sealStore1.Close(); err != nil {
		t.Fatalf("boot 1: sealStore.Close: %v", err)
	}

	// --- Boot 2 (simulated crash/restart: fresh barrier + fresh seal store
	//     rooted at the SAME persisted root-key blob) ---------------------
	barrier2, sealStore2 := bootBarrier(t, sealDir)
	defer func() { _ = sealStore2.Close() }()

	fs2 := encfs.New(vfs.Default, barrier2)
	db2, err := pebble.Open(dataDir, &pebble.Options{FS: fs2})
	if err != nil {
		t.Fatalf("boot 2: pebble.Open: %v", err)
	}
	defer func() { _ = db2.Close() }()

	got, closer, err := db2.Get([]byte("alpha"))
	if err != nil {
		t.Fatalf("boot 2: Get: %v", err)
	}
	defer func() { _ = closer.Close() }()
	if string(got) != "first boot value" {
		t.Errorf("boot 2: Get(alpha) = %q, want %q", got, "first boot value")
	}
}

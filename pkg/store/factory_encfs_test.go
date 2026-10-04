// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package store_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
)

// TestWithEncryptor_NilBarrierIsNoOp confirms that calling
// WithEncryptor with a nil-barrier encryptor leaves the Pebble FS
// untouched — required so existing tests and non-encrypted
// deployments continue to work.
func TestWithEncryptor_NilBarrierIsNoOp(t *testing.T) {
	var opts pebble.Options
	store.WithEncryptor(atrest.New(nil))(&opts)
	if opts.FS != nil {
		t.Errorf("FS = %v, want nil for nil barrier", opts.FS)
	}
}

// TestWithEncryptor_AppliesWrapper confirms the option installs a
// non-nil wrapper FS when given a real barrier.
func TestWithEncryptor_AppliesWrapper(t *testing.T) {
	dir := t.TempDir()
	barrier := newTestBarrier(t, dir)
	var opts pebble.Options
	store.WithEncryptor(atrest.New(barrier))(&opts)
	if opts.FS == nil {
		t.Fatal("FS = nil, want encrypting wrapper")
	}
	if opts.FS == vfs.Default {
		t.Error("FS was not replaced with the encrypting wrapper")
	}
}

// TestWithEncryptor_PebbleRoundTrip verifies that a Pebble database
// constructed with an encrypting FS option produces ciphertext on
// disk and round-trips values correctly.
func TestWithEncryptor_PebbleRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	sealDir := t.TempDir()
	barrier := newTestBarrier(t, sealDir)

	var opts pebble.Options
	store.WithEncryptor(atrest.New(barrier))(&opts)

	db, err := pebble.Open(dataDir, &opts)
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	const sentinel = "ASTRONOMICAL-SENTINEL-PLAINTEXT"
	if err := db.Set([]byte("k"), []byte(sentinel), pebble.Sync); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-open to confirm value comes back.
	db2, err := pebble.Open(dataDir, &opts)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, closer, err := db2.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != sentinel {
		t.Errorf("Get = %q, want %q", got, sentinel)
	}
	_ = closer.Close()
	if err := db2.Close(); err != nil {
		t.Fatalf("Close 2: %v", err)
	}

	// Walk the data dir and assert the sentinel bytes do not appear.
	err = filepath.WalkDir(dataDir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || strings.HasSuffix(p, "LOCK") {
			return nil
		}
		// #nosec G122 -- the tree being walked is a t.TempDir() this test created and no other process can reach, so there is no window between the walk and the read for a path to be substituted.
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if bytes.Contains(body, []byte(sentinel)) {
			t.Errorf("plaintext leaked in %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
}

// newTestBarrier constructs an unsealed *seal.StorageBarrier backed
// by an in-memory store. The barrier is initialized with a software
// strategy and immediately unsealed so callers can use it as a KEK.
func newTestBarrier(t *testing.T, _ string) *seal.StorageBarrier {
	t.Helper()
	ctx := context.Background()
	memBackend := seal.NewMemoryBackend()
	const passphrase = "factory-encfs-test-passphrase"
	strategy, err := seal.NewSoftwareStrategy(context.Background(), []byte(passphrase))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	barrier, err := seal.NewStorageBarrier(logger, memBackend, seal.BarrierConfig{
		RootKeyPath:  "test-root-key",
		DeploymentID: 1,
	}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	creds := seal.MustCredentials(context.Background(), []byte(passphrase))
	if err := barrier.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := barrier.Unseal(ctx, creds); err != nil {
		if !strings.Contains(err.Error(), "already unsealed") {
			t.Fatalf("Unseal: %v", err)
		}
	}
	return barrier
}

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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
)

// fakeKEK mirrors the unexported fakeBarrier from the in-package
// tests but lives in the _test package so it can be reused alongside
// the public encfs.New constructor.
type fakeKEK struct{}

func (fakeKEK) Encrypt(plaintext []byte) ([]byte, error) {
	// Simple obfuscation; the test asserts that user-supplied plaintext
	// does not appear on disk, which works even with a trivial KEK.
	out := make([]byte, len(plaintext)+8)
	copy(out, "fakekek\x00")
	for i := range plaintext {
		out[8+i] = plaintext[i] ^ 0xA5
	}
	return out, nil
}

func (fakeKEK) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < 8 || string(ciphertext[:8]) != "fakekek\x00" {
		return nil, os.ErrInvalid
	}
	body := ciphertext[8:]
	out := make([]byte, len(body))
	for i := range body {
		out[i] = body[i] ^ 0xA5
	}
	return out, nil
}

// TestPebble_RoundTripThroughEncFS opens an actual Pebble database
// against an encfs-wrapped on-disk directory, writes a handful of
// keys, restarts the database, and confirms the values come back
// intact.
func TestPebble_RoundTripThroughEncFS(t *testing.T) {
	dir := t.TempDir()
	enc := encfs.New(vfs.Default, fakeKEK{})

	open := func() *pebble.DB {
		t.Helper()
		db, err := pebble.Open(dir, &pebble.Options{FS: enc})
		if err != nil {
			t.Fatalf("pebble.Open: %v", err)
		}
		return db
	}

	db := open()
	pairs := map[string]string{
		"alpha":   "first value",
		"bravo":   "second value",
		"charlie": "third value",
	}
	for k, v := range pairs {
		if err := db.Set([]byte(k), []byte(v), pebble.Sync); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db = open()
	defer func() { _ = db.Close() }()
	for k, want := range pairs {
		got, closer, err := db.Get([]byte(k))
		if err != nil {
			t.Fatalf("Get %q: %v", k, err)
		}
		if string(got) != want {
			t.Errorf("Get %q = %q, want %q", k, got, want)
		}
		_ = closer.Close()
	}
}

// TestPebble_OnDiskDataIsEncrypted writes a recognizable plaintext
// value through Pebble, flushes to disk, then scans every file in
// the data directory for the plaintext. The assertion fails if the
// plaintext leaks anywhere on disk.
func TestPebble_OnDiskDataIsEncrypted(t *testing.T) {
	dir := t.TempDir()
	enc := encfs.New(vfs.Default, fakeKEK{})
	db, err := pebble.Open(dir, &pebble.Options{FS: enc})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const secret = "RIBOSOME-PLAINTEXT-MUST-NEVER-LEAK"
	if err := db.Set([]byte("k"), []byte(secret), pebble.Sync); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	scanForPlaintext(t, dir, []byte(secret))
}

func scanForPlaintext(t *testing.T, dir string, needle []byte) {
	t.Helper()
	count := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		// Skip the LOCK file — Pebble's lock files go through the base
		// FS via Lock() and are intentionally not encrypted.
		if strings.HasSuffix(path, "LOCK") {
			return nil
		}
		// #nosec G122 -- the tree being walked is a t.TempDir() this test created and no other process can reach, so there is no window between the walk and the read for a path to be substituted.
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(body, needle) {
			t.Errorf("plaintext %q found in %s", needle, path)
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
	if count == 0 {
		t.Error("scan walked zero files")
	}
}

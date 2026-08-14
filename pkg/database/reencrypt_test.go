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
	"io"
	"log/slog"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

func reencTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newNoneEncryptor returns an atrest.Encryptor over an initialized+unsealed
// none-strategy barrier — the on-disk format of a store "initialized without
// encryption" (zero-epoch-tagged plaintext).
func newNoneEncryptor(t *testing.T, rootKeyPath string) *atrest.Encryptor {
	t.Helper()
	ctx := context.Background()
	b, err := seal.NewStorageBarrier(reencTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: rootKeyPath, DeploymentID: 1}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier(none): %v", err)
	}
	if err := b.Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatalf("Initialize(none): %v", err)
	}
	return atrest.New(b)
}

// newKeyedEncryptor returns an atrest.Encryptor over an initialized+unsealed
// passphrase (software) barrier.
func newKeyedEncryptor(t *testing.T, rootKeyPath, passphrase string) *atrest.Encryptor {
	t.Helper()
	ctx := context.Background()
	strategy, err := seal.NewSoftwareStrategy([]byte(passphrase))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	b, err := seal.NewStorageBarrier(reencTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: rootKeyPath, DeploymentID: 1}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier(keyed): %v", err)
	}
	creds := seal.Credentials{Secret: []byte(passphrase)}
	if err := b.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize(keyed): %v", err)
	}
	return atrest.New(b)
}

// writeStore opens a Pebble store at dir through enc and writes the given pairs.
func writeStore(t *testing.T, dir string, enc *atrest.Encryptor, pairs map[string]string) {
	t.Helper()
	opts := &pebble.Options{}
	enc.Apply(opts)
	db, err := pebble.Open(dir, opts)
	if err != nil {
		t.Fatalf("open store %s: %v", dir, err)
	}
	for k, v := range pairs {
		if err := db.Set([]byte(k), []byte(v), pebble.Sync); err != nil {
			t.Fatalf("set %q: %v", k, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store %s: %v", dir, err)
	}
}

// readAll opens a Pebble store at dir through enc and returns all pairs.
func readAll(t *testing.T, dir string, enc *atrest.Encryptor) map[string]string {
	t.Helper()
	opts := &pebble.Options{ReadOnly: true}
	enc.Apply(opts)
	db, err := pebble.Open(dir, opts)
	if err != nil {
		t.Fatalf("open store %s: %v", dir, err)
	}
	defer func() { _ = db.Close() }()
	out := map[string]string{}
	it, err := db.NewIter(nil)
	if err != nil {
		t.Fatalf("iter: %v", err)
	}
	defer func() { _ = it.Close() }()
	for valid := it.First(); valid; valid = it.Next() {
		out[string(it.Key())] = string(it.Value())
	}
	return out
}

// TestReencryptPebbleStore_NoneToKeyed is the core proof of the enable-
// encryption feature: data written to a none (unencrypted) store is faithfully
// re-encrypted into a keyed store with no loss, and the result is genuinely
// encrypted (the none encryptor can no longer read it back).
func TestReencryptPebbleStore_NoneToKeyed(t *testing.T) {
	ctx := context.Background()

	pairs := map[string]string{}
	for i := 0; i < 500; i++ {
		pairs[fmt.Sprintf("key/%04d", i)] = fmt.Sprintf("value-%d-payload", i)
	}

	srcDir := t.TempDir()
	dstDir := t.TempDir()

	noneEnc := newNoneEncryptor(t, "none-root")
	keyedEnc := newKeyedEncryptor(t, "keyed-root", "migrate-pass")

	// 1. Write data to the none store.
	writeStore(t, srcDir, noneEnc, pairs)

	// 2. Re-encrypt none -> keyed.
	n, err := ReencryptPebbleStore(ctx, srcDir, noneEnc, dstDir, keyedEnc)
	if err != nil {
		t.Fatalf("ReencryptPebbleStore: %v", err)
	}
	if n != int64(len(pairs)) {
		t.Fatalf("copied %d records, want %d", n, len(pairs))
	}

	// 3. Verification gate must pass (no data loss).
	if err := VerifyReencryptedStore(ctx, srcDir, noneEnc, dstDir, keyedEnc); err != nil {
		t.Fatalf("VerifyReencryptedStore: %v", err)
	}

	// 4. The keyed store reads back identical plaintext through the keyed encryptor.
	got := readAll(t, dstDir, keyedEnc)
	if len(got) != len(pairs) {
		t.Fatalf("keyed store has %d records, want %d", len(got), len(pairs))
	}
	for k, v := range pairs {
		if got[k] != v {
			t.Fatalf("keyed store key %q = %q, want %q", k, got[k], v)
		}
	}

	// 5. The data is REALLY encrypted now: opening the keyed store through the
	// none encryptor must fail (the none decrypter fails closed on the
	// non-zero DEK epoch written by the keyed strategy) — proving the migration
	// produced ciphertext, not plaintext.
	func() {
		defer func() { _ = recover() }()
		opts := &pebble.Options{ReadOnly: true}
		noneEnc.Apply(opts)
		db, err := pebble.Open(dstDir, opts)
		if err != nil {
			return // expected: cannot open keyed store as none
		}
		defer func() { _ = db.Close() }()
		it, err := db.NewIter(nil)
		if err != nil {
			return
		}
		defer func() { _ = it.Close() }()
		matches := 0
		for valid := it.First(); valid; valid = it.Next() {
			if pairs[string(it.Key())] == string(it.Value()) {
				matches++
			}
		}
		if matches == len(pairs) {
			t.Fatalf("none encryptor read the keyed store as plaintext — data was NOT encrypted")
		}
	}()
}

// TestReencryptPebbleStore_KeyedToNone proves the inverse direction also
// round-trips (used by a future disable path and by symmetry verification):
// keyed -> none yields identical data readable as plaintext.
func TestReencryptPebbleStore_KeyedToNone(t *testing.T) {
	ctx := context.Background()
	pairs := map[string]string{"a": "1", "b": "2", "c": "3"}

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	keyedEnc := newKeyedEncryptor(t, "k", "pw")
	noneEnc := newNoneEncryptor(t, "n")

	writeStore(t, srcDir, keyedEnc, pairs)
	n, err := ReencryptPebbleStore(ctx, srcDir, keyedEnc, dstDir, noneEnc)
	if err != nil {
		t.Fatalf("ReencryptPebbleStore: %v", err)
	}
	if n != int64(len(pairs)) {
		t.Fatalf("copied %d, want %d", n, len(pairs))
	}
	if err := VerifyReencryptedStore(ctx, srcDir, keyedEnc, dstDir, noneEnc); err != nil {
		t.Fatalf("verify: %v", err)
	}
	got := readAll(t, dstDir, noneEnc)
	for k, v := range pairs {
		if got[k] != v {
			t.Fatalf("none store key %q = %q, want %q", k, got[k], v)
		}
	}
}

// TestVerifyReencryptedStore_DetectsMismatch confirms the verification gate
// actually fails when the destination diverges from the source (guards against
// a silently-passing verify that would risk data loss).
func TestVerifyReencryptedStore_DetectsMismatch(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	noneEnc := newNoneEncryptor(t, "n")
	keyedEnc := newKeyedEncryptor(t, "k", "pw")

	writeStore(t, srcDir, noneEnc, map[string]string{"a": "1", "b": "2"})
	// Destination is missing key "b".
	writeStore(t, dstDir, keyedEnc, map[string]string{"a": "1"})

	if err := VerifyReencryptedStore(ctx, srcDir, noneEnc, dstDir, keyedEnc); err == nil {
		t.Fatal("verify must fail when destination is missing records")
	}
}

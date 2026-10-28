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

package file

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	"github.com/jeremyhahn/go-qrdb/pkg/testutil/memdir"
)

// mustRecordPaths returns key's shard directory and record path in e,
// minting an encrypted store's record-name key if it has none, as a write
// would.
func mustRecordPaths(t testing.TB, e *Engine, key []byte) (dir, path string) {
	t.Helper()
	dir, path, err := e.recordPaths(key, true)
	if err != nil {
		t.Fatalf("recordPaths(%q): %v", key, err)
	}
	return dir, path
}

// identityEncryptor puts the engine in the encrypted posture over the bare
// file system, so a test can read the record-name key and recompute names
// without a barrier. It is a double for the posture alone: the barrier's
// ciphertext is proved in encrypted_test.go.
type identityEncryptor struct{}

func (identityEncryptor) Enabled() bool                   { return true }
func (identityEncryptor) Wrap(base filesys.FS) filesys.FS { return base }

var _ Encryptor = identityEncryptor{}

func openIdentityEncrypted(t *testing.T, dir string) *Engine {
	t.Helper()
	e, err := New(dir, WithEncryptor(identityEncryptor{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func readNameKey(t *testing.T, dir string) []byte {
	t.Helper()
	k, err := os.ReadFile(filepath.Join(dir, nameKeyFileName))
	if err != nil {
		t.Fatalf("read record-name key: %v", err)
	}
	return k
}

func TestRecordNames_PlaintextIsBareSHA256(t *testing.T) {
	dir := memdir.TempDir(t)
	e, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer e.Close()
	key := []byte("plain/key")
	if err := e.Set(key, []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	_, path := mustRecordPaths(t, e, key)
	if got, want := filepath.Base(path), hashHex(key); got != want {
		t.Errorf("record name = %s, want SHA-256 %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, nameKeyFileName)); !os.IsNotExist(err) {
		t.Errorf("plaintext store has a record-name key file: stat err = %v", err)
	}
}

func TestRecordNames_EncryptedIsHMACUnderStoreKey(t *testing.T) {
	dir := memdir.TempDir(t)
	e := openIdentityEncrypted(t, dir)
	defer e.Close()
	key := []byte("customer/0001/ssn")
	if err := e.Set(key, []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	nameKey := readNameKey(t, dir)
	if len(nameKey) != nameKeyLen {
		t.Fatalf("record-name key is %d bytes, want %d", len(nameKey), nameKeyLen)
	}
	mac := hmac.New(sha256.New, nameKey)
	mac.Write(key)
	want := hex.EncodeToString(mac.Sum(nil))
	recordPath := filepath.Join(dir, want[:shardHexLen], want[shardHexLen:2*shardHexLen], want)
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("no record at its HMAC name: %v", err)
	}
	unkeyed := hashHex(key)
	if _, err := os.Stat(filepath.Join(e.shardDir(unkeyed), unkeyed)); !os.IsNotExist(err) {
		t.Errorf("record found at its bare SHA-256 name: stat err = %v", err)
	}
}

func TestRecordNames_KeyMintedOnceAndKeptAcrossReopen(t *testing.T) {
	dir := memdir.TempDir(t)
	e := openIdentityEncrypted(t, dir)
	for i := range 3 {
		if err := e.Set([]byte("k"+strconv.Itoa(i)), []byte("v")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	first := readNameKey(t, dir)
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	e = openIdentityEncrypted(t, dir)
	defer e.Close()
	if err := e.Set([]byte("k3"), []byte("v")); err != nil {
		t.Fatalf("Set after reopen: %v", err)
	}
	if !bytes.Equal(readNameKey(t, dir), first) {
		t.Fatal("record-name key changed across a reopen")
	}
	for i := range 4 {
		if v, err := e.Get([]byte("k" + strconv.Itoa(i))); err != nil || string(v) != "v" {
			t.Errorf("Get(k%d) = %q, %v; want \"v\"", i, v, err)
		}
	}
}

// TestRecordNames_ReadsWithoutKeyFindNothing pins that a store which has never
// been written answers every read as empty and mints no key: the key is
// written before the first record, so its absence means no record exists.
func TestRecordNames_ReadsWithoutKeyFindNothing(t *testing.T) {
	dir := memdir.TempDir(t)
	e := openIdentityEncrypted(t, dir)
	defer e.Close()
	key := []byte("absent")
	if v, err := e.Get(key); v != nil || err != nil {
		t.Errorf("Get = %q, %v; want nil, nil", v, err)
	}
	if ok, err := e.Exists(key); ok || err != nil {
		t.Errorf("Exists = %v, %v; want false, nil", ok, err)
	}
	if err := e.Delete(key); err != nil {
		t.Errorf("Delete = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, nameKeyFileName)); !os.IsNotExist(err) {
		t.Errorf("a read minted the record-name key: stat err = %v", err)
	}
}

func TestRecordNames_WrongLengthKeyRefused(t *testing.T) {
	dir := memdir.TempDir(t)
	e := openIdentityEncrypted(t, dir)
	defer e.Close()
	if err := os.WriteFile(filepath.Join(dir, nameKeyFileName), []byte("short"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ops := map[string]func() error{
		"Get": func() error { _, err := e.Get([]byte("k")); return err },
		"Set": func() error { return e.Set([]byte("k"), []byte("v")) },
	}
	for name, op := range ops {
		var lenErr *NameKeyLengthError
		if err := op(); !errors.As(err, &lenErr) {
			t.Errorf("%s = %v, want *NameKeyLengthError", name, err)
			continue
		}
		if lenErr.Len != len("short") || lenErr.Want != nameKeyLen || lenErr.Error() == "" {
			t.Errorf("%s error = %+v", name, lenErr)
		}
	}
}

// TestRecordNames_ParallelBatchMintsOneKey commits a first batch, whose
// point ops are applied in parallel, into a store with no key yet. Were two
// goroutines to mint keys, records named under the losing one would be lost
// after a reopen.
func TestRecordNames_ParallelBatchMintsOneKey(t *testing.T) {
	dir := memdir.TempDir(t)
	e := openIdentityEncrypted(t, dir)
	b := e.Batch()
	const n = 64
	for i := range n {
		if err := b.Set([]byte("batch/"+strconv.Itoa(i)), []byte(strconv.Itoa(i))); err != nil {
			t.Fatalf("batch Set: %v", err)
		}
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	e = openIdentityEncrypted(t, dir)
	defer e.Close()
	for i := range n {
		if v, err := e.Get([]byte("batch/" + strconv.Itoa(i))); err != nil || string(v) != strconv.Itoa(i) {
			t.Errorf("Get(batch/%d) = %q, %v", i, v, err)
		}
	}
}

func TestRecordNames_StrayTempKeyRemovedAtOpen(t *testing.T) {
	dir := memdir.TempDir(t)
	e := openIdentityEncrypted(t, dir)
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	stray := filepath.Join(dir, tmpNamesPrefix+"crashed")
	if err := os.WriteFile(stray, []byte("partial"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	e = openIdentityEncrypted(t, dir)
	defer e.Close()
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Errorf("stray record-name key temp file survived open: stat err = %v", err)
	}
}

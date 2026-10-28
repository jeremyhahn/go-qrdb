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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

func TestReencryptErrorTypes(t *testing.T) {
	base := errors.New("boom")

	openErr := &ReencryptOpenError{Dir: "/d", Err: base}
	if !strings.Contains(openErr.Error(), "/d") || !errors.Is(openErr, base) {
		t.Fatalf("ReencryptOpenError: %v", openErr)
	}

	copyErr := &ReencryptCopyError{Err: base}
	if !strings.Contains(copyErr.Error(), "boom") || !errors.Is(copyErr, base) {
		t.Fatalf("ReencryptCopyError: %v", copyErr)
	}

	verErr := &ReencryptVerifyMismatchError{Reason: "missing key q"}
	if !strings.Contains(verErr.Error(), "missing key q") {
		t.Fatalf("ReencryptVerifyMismatchError: %v", verErr)
	}

	dirErr := &MigrateStoreDirError{Dir: "/store", Err: base}
	if !strings.Contains(dirErr.Error(), "/store") || !errors.Is(dirErr, base) {
		t.Fatalf("MigrateStoreDirError: %v", dirErr)
	}
}

// TestReencryptStoreFiles_RawCopyNonEncfs exercises the raw-copy path
// (copyFileRaw) for a file lacking the encfs magic, alongside the recodec path
// for an encfs file, confirming both land in the destination.
func TestReencryptStoreFiles_RawCopyNonEncfs(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir() + "/store"
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A plain (non-QENC) artifact + a LOCK (must be skipped).
	if err := os.WriteFile(filepath.Join(srcDir, "OPTIONS-000001"), []byte("plaintext-options"), 0o600); err != nil {
		t.Fatalf("write options: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "LOCK"), []byte("lockdata"), 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	// An encfs file written through a none barrier's encrypting FS.
	noneEnc := newNoneEncryptor(t, "n")
	f, err := noneEnc.FS().Create(filepath.Join(srcDir, "000001.sst"))
	if err != nil {
		t.Fatalf("create encfs file: %v", err)
	}
	if _, err := f.Write([]byte("encrypted-payload")); err != nil {
		t.Fatalf("write encfs: %v", err)
	}
	_ = f.Sync()
	_ = f.Close()

	dstDir := t.TempDir() + "/store.keyed"
	keyedEnc := newKeyedEncryptor(t, "k", "pw")
	n, err := ReencryptStoreFiles(ctx, srcDir, noneEnc, dstDir, keyedEnc)
	if err != nil {
		t.Fatalf("ReencryptStoreFiles: %v", err)
	}
	if n < 2 {
		t.Fatalf("copied %d files, want >= 2 (OPTIONS + sst; LOCK skipped)", n)
	}

	// The plain artifact is copied verbatim (raw copy).
	got, err := os.ReadFile(filepath.Join(dstDir, "OPTIONS-000001"))
	if err != nil || string(got) != "plaintext-options" {
		t.Fatalf("raw-copied OPTIONS = %q, %v", got, err)
	}
	// LOCK must NOT be copied.
	if _, err := os.Stat(filepath.Join(dstDir, "LOCK")); !os.IsNotExist(err) {
		t.Fatal("LOCK must be skipped, not copied")
	}
	// The encfs file is readable through the destination keyed FS.
	in, err := keyedEnc.FS().Open(filepath.Join(dstDir, "000001.sst"))
	if err != nil {
		t.Fatalf("open recoded sst: %v", err)
	}
	defer func() { _ = in.Close() }()
	buf := make([]byte, len("encrypted-payload"))
	_, _ = in.ReadAt(buf, 0)
	if string(buf) != "encrypted-payload" {
		t.Fatalf("recoded sst payload = %q", buf)
	}
}

// TestNodeStoreMigration_RollbackOnCopyFailure forces a copy failure (a store
// dir that is not a valid Pebble store) and verifies the migration rolls back:
// the journal reaches the failed phase and no keyed staging dir is left behind.
func TestNodeStoreMigration_RollbackOnCopyFailure(t *testing.T) {
	ctx := context.Background()
	sealBackend := seal.NewMemoryBackend()
	source := initNoneBarrier(t, sealBackend, "root-key")
	target := initKeyedBarrier(t, sealBackend, "root-key.migrating", "pw")

	// A directory that exists but is NOT a Pebble store -> read-only open fails.
	badDir := t.TempDir() + "/not-a-store"
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	mig := &NodeStoreMigration{Source: source, Target: target, StoreDirs: []string{badDir}, Logger: reencTestLogger()}
	err := mig.Run(ctx, seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase, Mechanism: seal.MechanismSwap,
		Availability: seal.AvailabilityQuiesced, Credentials: seal.MustCredentials(context.Background(), []byte("pw")),
	})
	if err == nil {
		t.Fatal("migration must fail when a store dir is not a valid Pebble store")
	}

	// Journal exists in a terminal failed phase (rollback ran).
	j, lerr := source.LoadMigrationJournal(ctx)
	if lerr != nil {
		t.Fatalf("load journal: %v", lerr)
	}
	if j == nil || j.Phase != seal.PhaseFailed {
		t.Fatalf("journal after rollback = %+v, want phase failed", j)
	}
	// No keyed staging dir left behind.
	if _, serr := os.Stat(badDir + keyedSuffix); !os.IsNotExist(serr) {
		t.Fatal("keyed staging dir must be removed on rollback")
	}
	// The canonical root key is untouched (still none): the source never committed.
	if got := persistedStrategy(ctx, sealBackend, "root-key"); got != seal.StrategyNone {
		t.Fatalf("canonical strategy = %q, want none after rollback", got)
	}
}

// TestReencrypt_ContextCancelled covers the ctx-cancellation branches.
func TestReencrypt_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	noneEnc := newNoneEncryptor(t, "n")
	src := t.TempDir() + "/src"
	writeStore(t, src, noneEnc, map[string]string{"a": "1"})
	cancel() // cancel before the copy iterates
	keyedEnc := newKeyedEncryptor(t, "k", "pw")
	_, err := ReencryptPebbleStore(ctx, src, noneEnc, t.TempDir()+"/dst", keyedEnc)
	if err == nil {
		t.Fatal("expected ctx-cancelled copy to error")
	}
}

// TestReencryptStoreFiles_Subdir covers the directory-recursion branch.
func TestReencryptStoreFiles_Subdir(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir() + "/store"
	sub := filepath.Join(srcDir, "nested")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "data.txt"), []byte("nested-plain"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	noneEnc := newNoneEncryptor(t, "n")
	keyedEnc := newKeyedEncryptor(t, "k", "pw")
	if _, err := ReencryptStoreFiles(ctx, srcDir, noneEnc, t.TempDir()+"/dst", keyedEnc); err != nil {
		t.Fatalf("ReencryptStoreFiles (subdir): %v", err)
	}
}

// TestVerifyReencryptedStore_ValueMismatch covers the value-mismatch branch
// (distinct from the count/key-mismatch branch).
func TestVerifyReencryptedStore_ValueMismatch(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir() + "/s"
	dstDir := t.TempDir() + "/d"
	noneEnc := newNoneEncryptor(t, "n")
	keyedEnc := newKeyedEncryptor(t, "k", "pw")
	writeStore(t, srcDir, noneEnc, map[string]string{"k": "original"})
	writeStore(t, dstDir, keyedEnc, map[string]string{"k": "tampered"})
	err := VerifyReencryptedStore(ctx, srcDir, noneEnc, dstDir, keyedEnc)
	var mismatch *ReencryptVerifyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want *ReencryptVerifyMismatchError", err)
	}
}

// TestReencryptPebbleStore_OpenErrors covers the open-error branches.
func TestReencryptPebbleStore_OpenErrors(t *testing.T) {
	ctx := context.Background()
	noneEnc := atrest.New(nil) // disabled encryptor (plaintext) — valid
	// Source does not exist (ReadOnly open fails).
	_, err := ReencryptPebbleStore(ctx, t.TempDir()+"/missing", noneEnc, t.TempDir()+"/dst", noneEnc)
	if err == nil {
		t.Fatal("expected open error for missing source store")
	}
	var openErr *ReencryptOpenError
	if !errors.As(err, &openErr) {
		t.Fatalf("err = %v, want *ReencryptOpenError", err)
	}
}

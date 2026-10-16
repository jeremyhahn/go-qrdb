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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
)

// writeFileStore opens a file store at dir through enc and writes the pairs.
func writeFileStore(t *testing.T, dir string, enc *atrest.Encryptor, pairs map[string]string) {
	t.Helper()
	s, err := file.New(dir, file.WithEncryptor(enc))
	if err != nil {
		t.Fatalf("open file store %s: %v", dir, err)
	}
	for k, v := range pairs {
		if err := s.Set([]byte(k), []byte(v)); err != nil {
			t.Fatalf("set %q: %v", k, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close file store %s: %v", dir, err)
	}
}

// readFileStore opens a file store at dir through enc and returns every pair.
func readFileStore(t *testing.T, dir string, enc *atrest.Encryptor) map[string]string {
	t.Helper()
	s, err := file.New(dir, file.WithEncryptor(enc))
	if err != nil {
		t.Fatalf("open file store %s: %v", dir, err)
	}
	defer func() { _ = s.Close() }()
	it := s.NewIterator(nil)
	defer func() { _ = it.Close() }()
	out := map[string]string{}
	for valid := it.SeekToFirst(); valid; valid = it.Next() {
		out[string(it.Key())] = string(it.Value())
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterate file store %s: %v", dir, err)
	}
	return out
}

// assertNoPlaintextOnDisk fails if any file under dir contains a key or value
// of pairs in the clear.
func assertNoPlaintextOnDisk(t *testing.T, dir string, pairs map[string]string) {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot %s: %v", dir, err)
	}
	defer func() { _ = root.Close() }()
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := root.ReadFile(path)
		if err != nil {
			return err
		}
		for k, v := range pairs {
			if bytes.Contains(raw, []byte(k)) || bytes.Contains(raw, []byte(v)) {
				t.Errorf("%s holds %q=%q in the clear", path, k, v)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

func fileStorePairs(n int) map[string]string {
	pairs := make(map[string]string, n)
	for i := 0; i < n; i++ {
		pairs[fmt.Sprintf("file-key/%04d", i)] = fmt.Sprintf("file-value-%d-payload", i)
	}
	return pairs
}

// TestReencryptFileStore_NoneToKeyed proves a file store written under the
// none key is copied record for record into a keyed store whose files hold no
// key or value in the clear, and that the verify gate accepts the copy.
func TestReencryptFileStore_NoneToKeyed(t *testing.T) {
	ctx := context.Background()
	pairs := fileStorePairs(64)
	srcDir := filepath.Join(t.TempDir(), "file")
	dstDir := filepath.Join(t.TempDir(), "file")
	noneEnc := newNoneEncryptor(t, "none-root")
	keyedEnc := newKeyedEncryptor(t, "keyed-root", "file-migrate-pass")
	writeFileStore(t, srcDir, noneEnc, pairs)

	n, err := ReencryptFileStore(ctx, srcDir, noneEnc, dstDir, keyedEnc)
	if err != nil {
		t.Fatalf("ReencryptFileStore: %v", err)
	}
	if n != int64(len(pairs)) {
		t.Fatalf("copied %d records, want %d", n, len(pairs))
	}
	if err := VerifyReencryptedFileStore(ctx, srcDir, noneEnc, dstDir, keyedEnc); err != nil {
		t.Fatalf("VerifyReencryptedFileStore: %v", err)
	}
	if got := readFileStore(t, dstDir, keyedEnc); !maps.Equal(got, pairs) {
		t.Fatalf("keyed store holds %d records, want the %d written", len(got), len(pairs))
	}
	assertNoPlaintextOnDisk(t, dstDir, pairs)
}

// TestReencryptFileStore_KeyedToKeyed proves a rotation between two keyed
// barriers leaves a store only the new key reads.
func TestReencryptFileStore_KeyedToKeyed(t *testing.T) {
	ctx := context.Background()
	pairs := fileStorePairs(8)
	srcDir := filepath.Join(t.TempDir(), "file")
	dstDir := filepath.Join(t.TempDir(), "file")
	oldEnc := newKeyedEncryptor(t, "old-root", "old-pass")
	newEnc := newKeyedEncryptor(t, "new-root", "new-pass")
	writeFileStore(t, srcDir, oldEnc, pairs)

	if _, err := ReencryptFileStore(ctx, srcDir, oldEnc, dstDir, newEnc); err != nil {
		t.Fatalf("ReencryptFileStore: %v", err)
	}
	if got := readFileStore(t, dstDir, newEnc); !maps.Equal(got, pairs) {
		t.Fatalf("rotated store holds %d records, want %d", len(got), len(pairs))
	}
	s, err := file.New(dstDir, file.WithEncryptor(oldEnc))
	if err != nil {
		return // the old key cannot even open the rotated store
	}
	defer func() { _ = s.Close() }()
	if _, err := s.Get([]byte("file-key/0000")); err == nil {
		t.Fatal("the old key read a record of the rotated store")
	}
}

// TestReencryptFileStore_OpenFailure proves a source path that cannot be a
// file store surfaces a typed *ReencryptOpenError naming it.
func TestReencryptFileStore_OpenFailure(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "not-a-store")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	enc := newNoneEncryptor(t, "n")
	ops := map[string]func() error{
		"copy": func() error {
			_, err := ReencryptFileStore(context.Background(), notADir, enc, t.TempDir(), enc)
			return err
		},
		"verify": func() error {
			return VerifyReencryptedFileStore(context.Background(), notADir, enc, t.TempDir(), enc)
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			var openErr *ReencryptOpenError
			if err := op(); !errors.As(err, &openErr) || openErr.Dir != notADir {
				t.Fatalf("err = %v, want *ReencryptOpenError for %s", err, notADir)
			}
		})
	}
}

// TestReencryptFileStore_Cancelled proves a cancelled context stops the copy
// with a typed *ReencryptCopyError wrapping the cancellation.
func TestReencryptFileStore_Cancelled(t *testing.T) {
	srcDir := filepath.Join(t.TempDir(), "file")
	enc := newNoneEncryptor(t, "n")
	writeFileStore(t, srcDir, enc, fileStorePairs(4))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ReencryptFileStore(ctx, srcDir, enc, filepath.Join(t.TempDir(), "file"), enc)
	var copyErr *ReencryptCopyError
	if !errors.As(err, &copyErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want *ReencryptCopyError wrapping context.Canceled", err)
	}
}

// TestVerifyReencryptedFileStore_DetectsDivergence proves the verify gate
// fails with a typed mismatch for every way a copy can diverge.
func TestVerifyReencryptedFileStore_DetectsDivergence(t *testing.T) {
	src := map[string]string{"a": "1", "b": "2"}
	cases := map[string]map[string]string{
		"missing record": {"a": "1"},
		"extra record":   {"a": "1", "b": "2", "c": "3"},
		"changed value":  {"a": "1", "b": "two"},
		"different key":  {"a": "1", "z": "2"},
	}
	for name, dst := range cases {
		t.Run(name, func(t *testing.T) {
			srcDir := filepath.Join(t.TempDir(), "file")
			dstDir := filepath.Join(t.TempDir(), "file")
			noneEnc := newNoneEncryptor(t, "n")
			keyedEnc := newKeyedEncryptor(t, "k", "pw")
			writeFileStore(t, srcDir, noneEnc, src)
			writeFileStore(t, dstDir, keyedEnc, dst)

			err := VerifyReencryptedFileStore(context.Background(), srcDir, noneEnc, dstDir, keyedEnc)
			var mismatch *ReencryptVerifyMismatchError
			if !errors.As(err, &mismatch) {
				t.Fatalf("err = %v, want *ReencryptVerifyMismatchError", err)
			}
		})
	}
}

// TestNodeStoreMigration_MovesFileStores proves an enable-encryption run moves
// a node's file store alongside its Pebble store under either mechanism: both
// read back under the committed keyed root key and the file store holds no
// record in the clear.
func TestNodeStoreMigration_MovesFileStores(t *testing.T) {
	for _, mechanism := range []seal.MigrationMechanism{seal.MechanismSwap, seal.MechanismInPlace} {
		t.Run(string(mechanism), func(t *testing.T) {
			ctx := context.Background()
			const passphrase = "file-node-pass"
			sealBackend := seal.NewMemoryBackend()
			source := initNoneBarrier(t, sealBackend, "root-key")
			target := initKeyedBarrier(t, sealBackend, "root-key.migrating", passphrase)

			root := t.TempDir()
			pebbleDir := filepath.Join(root, "pebble")
			fileDir := filepath.Join(root, "file")
			pebblePairs := map[string]string{"p/1": "pebble-one", "p/2": "pebble-two"}
			filePairs := fileStorePairs(16)
			noneEnc := atrest.New(source)
			writeStore(t, pebbleDir, noneEnc, pebblePairs)
			writeFileStore(t, fileDir, noneEnc, filePairs)

			mig := &NodeStoreMigration{
				Source:        source,
				Target:        target,
				StoreDirs:     []string{pebbleDir},
				FileStoreDirs: []string{fileDir},
				Logger:        reencTestLogger(),
			}
			if err := mig.Run(ctx, seal.MigrationOptions{
				TargetStrategy: seal.StrategyPassphrase,
				Mechanism:      mechanism,
				Availability:   seal.AvailabilityQuiesced,
				Credentials:    seal.MustCredentials(ctx, []byte(passphrase)),
			}); err != nil {
				t.Fatalf("Run: %v", err)
			}

			keyedEnc := atrest.New(initReopenKeyed(t, sealBackend, "root-key", passphrase))
			if got := readAll(t, pebbleDir, keyedEnc); !maps.Equal(got, pebblePairs) {
				t.Fatalf("pebble store = %v, want %v", got, pebblePairs)
			}
			if got := readFileStore(t, fileDir, keyedEnc); !maps.Equal(got, filePairs) {
				t.Fatalf("file store holds %d records, want %d", len(got), len(filePairs))
			}
			assertNoPlaintextOnDisk(t, fileDir, filePairs)
			if _, err := os.Stat(fileDir + backupSuffix); !os.IsNotExist(err) {
				t.Fatalf("backup %s must be removed after commit", fileDir+backupSuffix)
			}
		})
	}
}

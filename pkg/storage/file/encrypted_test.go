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

package file_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/testutil/memdir"
)

const encTestPassphrase = "file-engine-at-rest-test-passphrase"

// newUnsealedBarrier returns an initialized, unsealed barrier over an
// in-memory backend, and the credentials that unseal it again.
func newUnsealedBarrier(t *testing.T) (*seal.StorageBarrier, seal.Credentials) {
	t.Helper()
	ctx := context.Background()
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte(encTestPassphrase))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	barrier, err := seal.NewStorageBarrier(log, seal.NewMemoryBackend(), seal.BarrierConfig{
		RootKeyPath:  "file-engine-test-root-key",
		DeploymentID: 1,
	}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	creds := seal.MustCredentials(ctx, []byte(encTestPassphrase))
	if err := barrier.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if barrier.IsSealed() {
		if err := barrier.Unseal(ctx, creds); err != nil {
			t.Fatalf("Unseal: %v", err)
		}
	}
	return barrier, creds
}

func openEncrypted(t *testing.T, dir string, barrier *seal.StorageBarrier) *file.Engine {
	t.Helper()
	e, err := file.New(dir, file.WithEncryptor(atrest.New(barrier)))
	if err != nil {
		t.Fatalf("New encrypted: %v", err)
	}
	return e
}

// encRecords are the records the at-rest tests write. Every key and value is
// a distinctive string a plaintext scan of the directory would find.
var encRecords = map[string]string{
	"customer/0001/ssn":   "plaintext-value-ssn-078-05-1120",
	"customer/0002/card":  "plaintext-value-card-4111111111111111",
	"customer/0003/notes": "plaintext-value-notes-confidential",
}

func writeEncRecords(t *testing.T, e *file.Engine) {
	t.Helper()
	for k, v := range encRecords {
		if err := e.Set([]byte(k), []byte(v)); err != nil {
			t.Fatalf("Set %s: %v", k, err)
		}
	}
	// A batch goes through the commit journal, so the journal is covered too.
	b := e.Batch()
	b.Set([]byte("customer/0004/batch"), []byte("plaintext-value-batched-record"))
	if err := b.Commit(); err != nil {
		t.Fatalf("batch Commit: %v", err)
	}
}

// readEveryFile calls fn with the contents of every regular file under dir
// and returns how many it read. The walk and every read go through an
// os.Root on dir, so no path the walk reports can resolve outside it.
func readEveryFile(t *testing.T, dir string, fn func(path string, raw []byte)) int {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer func() { _ = root.Close() }()
	n := 0
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := root.ReadFile(path)
		if err != nil {
			return err
		}
		n++
		fn(path, raw)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
	return n
}

// TestEncrypted_NoPlaintextOnDisk writes records through an encrypted engine
// and scans every file under the data directory: no key and no value appears
// in any of them.
func TestEncrypted_NoPlaintextOnDisk(t *testing.T) {
	barrier, _ := newUnsealedBarrier(t)
	dir := memdir.TempDir(t)
	e := openEncrypted(t, dir, barrier)
	writeEncRecords(t, e)
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	needles := [][]byte{[]byte("customer/0004/batch"), []byte("plaintext-value-batched-record")}
	for k, v := range encRecords {
		needles = append(needles, []byte(k), []byte(v))
	}
	scanned := readEveryFile(t, dir, func(path string, raw []byte) {
		for _, n := range needles {
			if bytes.Contains(raw, n) {
				t.Errorf("%s holds plaintext %q", path, n)
			}
		}
	})
	// Four records plus the format marker: an empty walk would pass vacuously.
	if scanned < len(encRecords)+2 {
		t.Fatalf("scanned %d files, want at least %d", scanned, len(encRecords)+2)
	}
}

// TestEncrypted_ReopenAfterUnsealRoundTrip writes, closes, seals and unseals
// the barrier, reopens the directory and reads every record back.
func TestEncrypted_ReopenAfterUnsealRoundTrip(t *testing.T) {
	barrier, creds := newUnsealedBarrier(t)
	dir := memdir.TempDir(t)
	e := openEncrypted(t, dir, barrier)
	writeEncRecords(t, e)
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := barrier.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := barrier.Unseal(context.Background(), creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	e = openEncrypted(t, dir, barrier)
	defer e.Close()
	for k, v := range encRecords {
		got, err := e.Get([]byte(k))
		if err != nil {
			t.Fatalf("Get %s: %v", k, err)
		}
		if string(got) != v {
			t.Errorf("Get %s = %q, want %q", k, got, v)
		}
	}
	keys, err := e.List([]byte("customer/"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != len(encRecords)+1 {
		t.Errorf("List returned %d keys, want %d", len(keys), len(encRecords)+1)
	}
	it := e.NewIterator(&storage.IteratorOptions{LowerBound: []byte("customer/")})
	defer it.Close()
	n := 0
	for ok := it.SeekToFirst(); ok; ok = it.Next() {
		if want, listed := encRecords[string(it.Key())]; listed && string(it.Value()) != want {
			t.Errorf("iterator %s = %q, want %q", it.Key(), it.Value(), want)
		}
		n++
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterator: %v", err)
	}
	if n != len(encRecords)+1 {
		t.Errorf("iterator yielded %d records, want %d", n, len(encRecords)+1)
	}
}

// TestEncrypted_SealedBarrierFailsClosed seals the barrier under an open
// engine: reads and writes fail with an error that is seal.ErrSealed, before
// and after the directory is reopened.
func TestEncrypted_SealedBarrierFailsClosed(t *testing.T) {
	barrier, _ := newUnsealedBarrier(t)
	dir := memdir.TempDir(t)
	e := openEncrypted(t, dir, barrier)
	writeEncRecords(t, e)
	if err := barrier.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	ops := map[string]func() error{
		"Get": func() error {
			_, err := e.Get([]byte("customer/0001/ssn"))
			return err
		},
		"Set": func() error {
			return e.Set([]byte("customer/0005/new"), []byte("v"))
		},
		"List": func() error {
			_, err := e.List([]byte("customer/"))
			return err
		},
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, seal.ErrSealed) {
			t.Errorf("%s while sealed = %v, want seal.ErrSealed", name, err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The plaintext format marker lets a node open its store before unsealing;
	// nothing in it is readable until then.
	e = openEncrypted(t, dir, barrier)
	defer e.Close()
	if _, err := e.Get([]byte("customer/0001/ssn")); !errors.Is(err, seal.ErrSealed) {
		t.Errorf("Get on a store reopened while sealed = %v, want seal.ErrSealed", err)
	}
}

// TestEncrypted_PostureMismatchRefused refuses to open a directory under the
// other at-rest posture, in both directions, with *file.AtRestMismatchError.
func TestEncrypted_PostureMismatchRefused(t *testing.T) {
	barrier, _ := newUnsealedBarrier(t)
	cases := map[string]struct {
		create, reopen []file.Option
		found, want    string
	}{
		"plaintext-store-opened-encrypted": {
			reopen: []file.Option{file.WithEncryptor(atrest.New(barrier))},
			found:  "plaintext", want: "encrypted",
		},
		"encrypted-store-opened-plaintext": {
			create: []file.Option{file.WithEncryptor(atrest.New(barrier))},
			found:  "encrypted", want: "plaintext",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := memdir.TempDir(t)
			e, err := file.New(dir, tc.create...)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := e.Set([]byte("k"), []byte("v")); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if err := e.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			_, err = file.New(dir, tc.reopen...)
			var mismatch *file.AtRestMismatchError
			if !errors.As(err, &mismatch) {
				t.Fatalf("reopen = %v, want *file.AtRestMismatchError", err)
			}
			if mismatch.Found != tc.found || mismatch.Want != tc.want {
				t.Errorf("mismatch = %+v, want Found=%s Want=%s", mismatch, tc.found, tc.want)
			}
		})
	}
}

// TestWithEncryptor_DisabledIsPlaintext confirms an encryptor with no barrier
// leaves the engine plaintext: the value is readable on disk and the store
// reopens without an encryptor.
func TestWithEncryptor_DisabledIsPlaintext(t *testing.T) {
	dir := memdir.TempDir(t)
	e, err := file.New(dir, file.WithEncryptor(atrest.New(nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := e.Set([]byte("k"), []byte("visible-plaintext-value")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	found := false
	readEveryFile(t, dir, func(_ string, raw []byte) {
		if bytes.Contains(raw, []byte("visible-plaintext-value")) {
			found = true
		}
	})
	if !found {
		t.Error("disabled encryptor: value not found in plaintext on disk")
	}
	e, err = file.New(dir)
	if err != nil {
		t.Fatalf("reopen plaintext: %v", err)
	}
	_ = e.Close()
}

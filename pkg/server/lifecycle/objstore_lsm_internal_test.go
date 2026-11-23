// Copyright (c) 2026 Jeremy Hahn
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

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

// objstoreLSMValue is the distinctive value the round trips write under key
// i. The values differ per key and share no four-byte run with the keys, so
// the SSTable's Snappy compression stores the first one as a literal and the
// SSTable bytes on the shared store can be searched for it.
func objstoreLSMValue(i int) string { return fmt.Sprintf("PLAINTEXT_SENTINEL_VALUE#%03d", i) }

const objstoreLSMPassphrase = "objstore-lsm-at-rest-passphrase"

// newObjstoreLSMBarrier builds an initialized barrier over an in-memory
// backend, unsealed unless sealed is set (the local_test.go newUnsealedBarrier
// shape; that file is package lifecycle_test).
func newObjstoreLSMBarrier(t *testing.T, sealed bool) *seal.StorageBarrier {
	t.Helper()
	ctx := context.Background()
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte(objstoreLSMPassphrase))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	barrier, err := seal.NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)),
		seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "objstore-lsm-root-key", DeploymentID: 1}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	creds := seal.MustCredentials(ctx, []byte(objstoreLSMPassphrase))
	if err := barrier.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	switch {
	case sealed && !barrier.IsSealed():
		if err := barrier.Seal(); err != nil {
			t.Fatalf("Seal: %v", err)
		}
	case !sealed && barrier.IsSealed():
		if err := barrier.Unseal(ctx, creds); err != nil {
			t.Fatalf("Unseal: %v", err)
		}
	}
	if barrier.IsSealed() != sealed {
		t.Fatalf("barrier sealed = %v, want %v", barrier.IsSealed(), sealed)
	}
	return barrier
}

func objstoreLSMKey(i int) []byte { return []byte(fmt.Sprintf("objstore-lsm-key-%03d", i)) }

// roundTripObjstoreLSM opens the LSM store, writes and flushes 50 keys, closes
// it, reopens it on the same data dir and reads every key back. It returns
// the concatenated bytes of every SSTable on the shared store.
func roundTripObjstoreLSM(t *testing.T, dataDir string, enc *atrest.Encryptor) (sharedTables, cacheFiles []byte) {
	t.Helper()
	const n = 50
	stg, err := newObjstoreLSMStorage(dataDir, enc, qrdbconfig.StorageConfig{})
	if err != nil {
		t.Fatalf("newObjstoreLSMStorage: %v", err)
	}
	for i := 0; i < n; i++ {
		if err := stg.Set(objstoreLSMKey(i), []byte(objstoreLSMValue(i))); err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
	}
	if err := stg.DB().Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := stg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := newObjstoreLSMStorage(dataDir, enc, qrdbconfig.StorageConfig{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	for i := 0; i < n; i++ {
		got, err := reopened.Get(objstoreLSMKey(i))
		if err != nil {
			t.Fatalf("Get %d after reopen: %v", i, err)
		}
		if string(got) != objstoreLSMValue(i) {
			t.Fatalf("Get %d after reopen = %q, want %q", i, got, objstoreLSMValue(i))
		}
	}

	if _, err := os.Stat(filepath.Join(dataDir, objstoreLSMDir, "QRDB-CREATORID")); err != nil {
		t.Fatalf("QRDB-CREATORID in the local Pebble dir: %v", err)
	}
	var tables bytes.Buffer
	sharedDir := filepath.Join(dataDir, "objstore", objstoreLSMPrefix)
	root, err := os.OpenRoot(sharedDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer func() { _ = root.Close() }()
	walkErr := fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".sst") {
			return nil
		}
		raw, err := root.ReadFile(path)
		if err != nil {
			return err
		}
		tables.Write(raw)
		tables.WriteByte(0)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", sharedDir, walkErr)
	}
	if tables.Len() == 0 {
		t.Fatalf("no .sst object under %s; the flush did not reach the shared store", sharedDir)
	}

	cacheDir := filepath.Join(dataDir, objstoreLSMCacheDir)
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatalf("read the local object cache %s: %v", cacheDir, err)
	}
	var cached bytes.Buffer
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(cacheDir, e.Name()))
		if err != nil {
			t.Fatalf("read cache file %s: %v", e.Name(), err)
		}
		cached.Write(raw)
		cached.WriteByte(0)
	}
	if len(entries) == 0 {
		t.Fatalf("no file in the local object cache %s after the reopen read-back", cacheDir)
	}
	return tables.Bytes(), cached.Bytes()
}

func TestObjstoreLSM_PlaintextRoundTrip(t *testing.T) {
	tables, cached := roundTripObjstoreLSM(t, t.TempDir(), atrest.New(nil))
	if !bytes.Contains(tables, []byte(objstoreLSMValue(0))) {
		t.Fatal("plaintext SSTables on the shared store do not hold the written value")
	}
	if !bytes.Contains(cached, []byte(objstoreLSMValue(0))) {
		t.Fatal("plaintext SSTables in the local object cache do not hold the written value")
	}
}

func TestObjstoreLSM_EncryptedRoundTrip(t *testing.T) {
	enc := atrest.New(newObjstoreLSMBarrier(t, false))
	tables, cached := roundTripObjstoreLSM(t, t.TempDir(), enc)
	if bytes.Contains(tables, []byte(objstoreLSMValue(0))) {
		t.Fatal("SSTables on the shared store hold the plaintext value; the barrier did not encrypt them")
	}
	if bytes.Contains(cached, []byte(objstoreLSMValue(0))) {
		t.Fatal("the local object cache holds the plaintext value; it must hold only sealed objects")
	}
}

func TestObjstoreLSM_SealedBarrierRefused(t *testing.T) {
	dataDir := t.TempDir()
	enc := atrest.New(newObjstoreLSMBarrier(t, true))
	_, err := newObjstoreLSMStorage(dataDir, enc, qrdbconfig.StorageConfig{})
	var sealedErr *ObjstoreBarrierSealedError
	if !errors.As(err, &sealedErr) {
		t.Fatalf("err = %v; want *ObjstoreBarrierSealedError", err)
	}
	for _, dir := range []string{objstoreLSMDir, objstoreLSMCacheDir, "objstore"} {
		if _, err := os.Stat(filepath.Join(dataDir, dir)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s after a sealed refusal: stat err = %v; want it absent", dir, err)
		}
	}
}

func TestObjstoreLSM_FlatStoreRefused(t *testing.T) {
	dataDir := t.TempDir()
	if _, err := objstore.New("local", map[string]string{"path": filepath.Join(dataDir, "objstore")}); err != nil {
		t.Fatalf("flat New: %v", err)
	}
	_, err := newObjstoreLSMStorage(dataDir, atrest.New(nil), qrdbconfig.StorageConfig{})
	var mismatch *objstore.FormatVersionMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v; want *objstore.FormatVersionMismatchError", err)
	}
	if mismatch.Found != "2" {
		t.Fatalf("Found = %q; want \"2\"", mismatch.Found)
	}
}

func TestObjstoreLSM_ArchiveTierRefused(t *testing.T) {
	cfg := qrdbconfig.StorageConfig{}
	cfg.Objstore.Provider = "glacier"
	_, err := newObjstoreLSMStorage(t.TempDir(), atrest.New(nil), cfg)
	var archive *objstore.ArchiveTierUnsupportedError
	if !errors.As(err, &archive) {
		t.Fatalf("err = %v; want *objstore.ArchiveTierUnsupportedError", err)
	}
}

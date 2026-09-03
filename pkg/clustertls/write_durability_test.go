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

// go-qrdb#241: atomic visibility and crash durability are different
// properties, and WriteFileAtomic promises both.
package clustertls

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteFileAtomic_DoesNotSucceedUntilTheDirectoryIsSynced.
//
// rename gives a concurrent reader the old file or the new one and never a
// mix. It says nothing about whether the new DIRECTORY ENTRY survives a power
// loss: that entry is directory metadata and is not on stable storage until
// the directory is fsynced. Without the sync, a crash can recover a set of
// files in an order the writer never wrote them in -- which is what breaks
// callers whose ORDERING is load-bearing, like clusterenroll writing the
// enrollment role before the CA material.
//
// The post-state is byte-identical whether or not the directory was synced, so
// this observes the OPERATION rather than the result: the sync is failed at
// its boundary, and the write must report that failure instead of returning
// success. A test that inspected the file afterwards would pass either way.
func TestWriteFileAtomic_DoesNotSucceedUntilTheDirectoryIsSynced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "durable.pem")

	sentinel := errors.New("directory sync refused")
	var syncedDirs []string
	orig := syncDirFn
	syncDirFn = func(d string) error {
		syncedDirs = append(syncedDirs, d)
		return sentinel
	}
	t.Cleanup(func() { syncDirFn = orig })

	err := WriteFileAtomic(path, []byte("payload"), 0o600)
	if !errors.Is(err, sentinel) {
		t.Fatalf("WriteFileAtomic = %v; want the directory-sync failure. Returning success "+
			"before the directory entry is durable is what lets a crash recover files in an "+
			"order they were never written in", err)
	}
	if len(syncedDirs) != 1 || syncedDirs[0] != dir {
		t.Errorf("synced dirs = %v; want exactly [%s] — the containing directory", syncedDirs, dir)
	}
}

// TestWriteFileAtomic_SyncsTheDirectoryAfterEverySuccessfulWrite: the sync is
// not conditional on the file being new. An overwrite replaces the directory
// entry too, so it needs the same durability.
func TestWriteFileAtomic_SyncsTheDirectoryAfterEverySuccessfulWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "durable.pem")

	var count int
	orig := syncDirFn
	syncDirFn = func(d string) error { count++; return orig(d) }
	t.Cleanup(func() { syncDirFn = orig })

	for i := 0; i < 3; i++ {
		if err := WriteFileAtomic(path, []byte{byte(i)}, 0o600); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if count != 3 {
		t.Errorf("directory synced %d times over 3 writes; want 3", count)
	}
	if got, err := os.ReadFile(path); err != nil || len(got) != 1 || got[0] != 2 {
		t.Fatalf("final content = %v err=%v; want the last write", got, err)
	}
}

// TestPersistMaterial_IsCrashDurable records that fixing the shared helper
// fixed callers this change never touched. PersistMaterial and PersistCA have
// always written through WriteFileAtomic, so their material was atomic but not
// crash-durable, before go-qrdb#241 and independently of it.
func TestPersistMaterial_IsCrashDurable(t *testing.T) {
	dir := t.TempDir()
	sentinel := errors.New("directory sync refused")
	orig := syncDirFn
	syncDirFn = func(string) error { return sentinel }
	t.Cleanup(func() { syncDirFn = orig })

	mat, err := GenerateCABundle("spiffe://test.cluster/node/127.0.0.1:63001")
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	if err := PersistMaterial(mat, dir); !errors.Is(err, sentinel) {
		t.Errorf("PersistMaterial = %v; want the directory-sync failure propagated. "+
			"TLS material that reports persisted before its directory entries are durable "+
			"can come back partially after a power loss", err)
	}
	if err := PersistCA(mat.CACertPEM, mat.CAKeyPEM, dir); !errors.Is(err, sentinel) {
		t.Errorf("PersistCA = %v; want the directory-sync failure propagated", err)
	}
}

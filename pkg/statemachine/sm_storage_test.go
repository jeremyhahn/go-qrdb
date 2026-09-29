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

package statemachine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	filestore "github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	memstore "github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// TestNewStorageFactory_SelectsBackend pins the raft.sm_storage seam: the
// storage factory composes the selected storage backend in the anchored
// per-replica directory.
func TestNewStorageFactory_SelectsBackend(t *testing.T) {
	root := t.TempDir()

	if _, ok := newStorageFactory(StorageConfig{Root: root, Kind: "memory"})(1, 1).(*memstore.MemoryStorage); !ok {
		t.Fatal("sm_storage memory must compose the memory backend")
	}

	stg := newStorageFactory(StorageConfig{Root: root, Kind: "file"})(2, 3)
	if _, ok := stg.(*filestore.Engine); !ok {
		t.Fatalf("sm_storage file must compose the file backend, got %T", stg)
	}
	if err := stg.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	_ = stg.Close()
	if _, err := os.Stat(filepath.Join(root, "sm", "shard-2-replica-3", "k")); err != nil {
		t.Fatalf("file SM storage not rooted at the anchored per-replica path: %v", err)
	}
}

// TestDiskAdapter_RestartResumesOverFileStorage extends the P1 restart
// contract to the file backend selected via raft.sm_storage: a restarted
// replica over the SAME file-backed path resumes from the persisted
// applied index with its data intact.
func TestDiskAdapter_RestartResumesOverFileStorage(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	open := func() (*DiskAdapter, func()) {
		st, err := filestore.New(dir)
		if err != nil {
			t.Fatalf("open file storage: %v", err)
		}
		return newDiskAdapter(st), func() { _ = st.Close() }
	}

	a, closeStore := open()
	idx, err := a.Open(ctx, "", nil)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if idx != 0 {
		t.Fatalf("fresh store Open = %d, want 0", idx)
	}
	entries := []sm.Entry{
		kvPutEntry(1, "a", "1"),
		kvPutEntry(2, "b", "2"),
	}
	if err := a.Update(ctx, entries, make([]sm.Result, len(entries))); err != nil {
		t.Fatalf("Update: %v", err)
	}
	closeStore()

	a2, closeStore2 := open()
	defer closeStore2()
	idx, err = a2.Open(ctx, "", nil)
	if err != nil {
		t.Fatalf("restart Open: %v", err)
	}
	if idx != 2 {
		t.Fatalf("restart Open = %d, want 2 (the persisted applied index)", idx)
	}
	val, err := a2.BytesLookup([]byte("b"))
	if err != nil {
		t.Fatalf("BytesLookup after restart: %v", err)
	}
	if string(val) != "2" {
		t.Fatalf("value after restart = %q, want %q", val, "2")
	}
}

// TestNewStorageFactory_RestartReusesFilePath proves the seam end to end at
// the factory level: two consecutive invocations for the same (shard,
// replica) under sm_storage file open the SAME durable directory, so a
// restarted replica sees its data.
func TestNewStorageFactory_RestartReusesFilePath(t *testing.T) {
	sc := StorageConfig{Root: t.TempDir(), Kind: "file"}

	s1 := newStorageFactory(sc)(7, 1)
	if err := s1.Set([]byte("survivor"), []byte("yes")); err != nil {
		t.Fatal(err)
	}
	_ = s1.Close()

	s2 := newStorageFactory(sc)(7, 1)
	defer func() { _ = s2.Close() }()
	got, err := s2.Get([]byte("survivor"))
	if err != nil || string(got) != "yes" {
		t.Fatalf("restarted factory storage Get = %q, %v; want \"yes\"", got, err)
	}
}

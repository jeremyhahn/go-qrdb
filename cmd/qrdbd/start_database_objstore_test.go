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

package main

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// objstoreObjectFiles returns the regular files beneath dir; a missing dir
// holds none.
func objstoreObjectFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return files
}

// A standalone node's database manager opens a backend: objstore database on
// the node's storage.objstore provider, under the database's own object
// prefix, and a drop deletes its objects.
func TestDatabaseManagerOptions_StandaloneOpensObjstoreDatabase(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bucket")
	objCfg := qrdbconfig.ObjstoreConfig{Provider: "local", Settings: map[string]string{"path": root}}
	mgr := databasetest.NewManager(nil, t.TempDir(), databaseManagerOptions(capabilities.ModeStandalone, objCfg)...)
	t.Cleanup(func() { _ = mgr.Close() })

	db, err := mgr.Create(database.DatabaseConfig{Name: "blobs", Backend: database.BackendObjstore})
	if err != nil {
		t.Fatalf("Create(objstore): %v", err)
	}
	ctx := context.Background()
	if err := db.Backend().Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := db.Backend().Get(ctx, []byte("k")); err != nil || string(got) != "v" {
		t.Fatalf("Get = %q, %v; want v", got, err)
	}

	prefix := filepath.Join(root, filepath.FromSlash(lifecycle.DatabaseObjectPrefix("", "blobs")))
	if len(objstoreObjectFiles(t, prefix)) == 0 {
		t.Fatalf("no object file under %q: the database is not on the node's provider", prefix)
	}
	if err := mgr.Drop(ctx, "blobs"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if left := objstoreObjectFiles(t, prefix); len(left) != 0 {
		t.Fatalf("the drop left objects under %q: %q", prefix, left)
	}
}

// A Raft-mode node's database manager refuses a backend: objstore database
// and names the reason.
func TestDatabaseManagerOptions_RaftModeRefusesObjstoreDatabase(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bucket")
	objCfg := qrdbconfig.ObjstoreConfig{Provider: "local", Settings: map[string]string{"path": root}}
	mgr := databasetest.NewManager(nil, t.TempDir(), databaseManagerOptions(capabilities.ModeRaft, objCfg)...)
	t.Cleanup(func() { _ = mgr.Close() })

	_, err := mgr.Create(database.DatabaseConfig{Name: "blobs", Backend: database.BackendObjstore})
	var raftErr *database.ObjstoreRaftModeError
	if !errors.As(err, &raftErr) || raftErr.Name != "blobs" {
		t.Fatalf("Create(objstore) in raft mode = %v, want *database.ObjstoreRaftModeError", err)
	}
	if files := objstoreObjectFiles(t, root); len(files) != 0 {
		t.Fatalf("the refused database left objects: %q", files)
	}
}

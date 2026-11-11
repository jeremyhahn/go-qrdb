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

package lifecycle_test

import (
	"errors"
	"io/fs"
	"path/filepath"
	"regexp"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// regularFilesUnder returns the regular files beneath dir; a missing dir
// holds none.
func regularFilesUnder(t *testing.T, dir string) []string {
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

// A database no tenant owns and a system-tenant database share one prefix; a
// tenant's database lies under a digest of the tenant ID, so the ID is never
// an object-name element.
func TestDatabaseObjectPrefix(t *testing.T) {
	for _, tenant := range []string{"", tenantid.System} {
		if got := lifecycle.DatabaseObjectPrefix(tenant, "blobs"); got != "databases/blobs/" {
			t.Errorf("DatabaseObjectPrefix(%q) = %q, want databases/blobs/", tenant, got)
		}
	}
	got := lifecycle.DatabaseObjectPrefix("acme", "blobs")
	if !regexp.MustCompile(`^tenants/[0-9a-f]{64}/databases/blobs/$`).MatchString(got) {
		t.Errorf("DatabaseObjectPrefix(acme) = %q, want tenants/<sha256>/databases/blobs/", got)
	}
	if other := lifecycle.DatabaseObjectPrefix("globex", "blobs"); other == got {
		t.Errorf("two tenants share the prefix %q", got)
	}
}

// Databases opened through the opener share the node's provider (one bucket,
// here one local path) without seeing each other's keys, each lays its
// objects out under its own prefix, and the reclaim of one deletes its
// objects and leaves the other's alone.
func TestDatabaseObjstoreOpener_DatabasesShareTheProviderIsolated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bucket")
	open := lifecycle.DatabaseObjstoreOpener(qrdbconfig.ObjstoreConfig{Provider: "local", Settings: map[string]string{"path": root}})

	a, reclaimA, err := open("", "blobs", t.TempDir())
	if err != nil {
		t.Fatalf("open(blobs): %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, reclaimB, err := open("acme", "blobs", t.TempDir())
	if err != nil {
		t.Fatalf("open(acme/blobs): %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if reclaimA == nil || reclaimB == nil {
		t.Fatal("the opener returned no reclaim: such a database could never be dropped")
	}

	if err := a.Set([]byte("k"), []byte("system")); err != nil {
		t.Fatalf("Set(blobs): %v", err)
	}
	if err := b.Set([]byte("k"), []byte("acme")); err != nil {
		t.Fatalf("Set(acme/blobs): %v", err)
	}
	if got, err := a.Get([]byte("k")); err != nil || string(got) != "system" {
		t.Errorf("blobs.Get = %q, %v; want system", got, err)
	}
	if got, err := b.Get([]byte("k")); err != nil || string(got) != "acme" {
		t.Errorf("acme/blobs.Get = %q, %v; want acme", got, err)
	}
	prefixA := filepath.Join(root, filepath.FromSlash(lifecycle.DatabaseObjectPrefix("", "blobs")))
	prefixB := filepath.Join(root, filepath.FromSlash(lifecycle.DatabaseObjectPrefix("acme", "blobs")))
	for _, dir := range []string{prefixA, prefixB} {
		if _, err := statDir(dir); err != nil {
			t.Errorf("no objects under %q: %v", dir, err)
		}
		if len(regularFilesUnder(t, dir)) == 0 {
			t.Errorf("no object file under %q", dir)
		}
	}

	if err := a.Close(); err != nil {
		t.Fatalf("Close(blobs): %v", err)
	}
	if err := reclaimA(); err != nil {
		t.Fatalf("reclaim(blobs): %v", err)
	}
	if left := regularFilesUnder(t, prefixA); len(left) != 0 {
		t.Errorf("the reclaim left objects under %q: %q", prefixA, left)
	}
	if got, err := b.Get([]byte("k")); err != nil || string(got) != "acme" {
		t.Errorf("acme/blobs.Get after the other database's reclaim = %q, %v; want acme", got, err)
	}
}

func TestDatabaseObjstoreOpener_UnknownProviderFails(t *testing.T) {
	open := lifecycle.DatabaseObjstoreOpener(qrdbconfig.ObjstoreConfig{Provider: "no-such-provider"})
	store, reclaim, err := open("", "blobs", t.TempDir())
	if err == nil {
		t.Fatal("open with an unknown provider succeeded")
	}
	if store != nil || reclaim != nil {
		t.Errorf("a failed open returned a store or a reclaim: %v, %v", store, reclaim != nil)
	}
}

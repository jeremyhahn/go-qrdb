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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ledgerConfig is an object-storage database configuration for the release
// ledger tests.
func ledgerConfig(name string) DatabaseConfig {
	return DatabaseConfig{Name: name, Backend: BackendObjstore,
		Objstore: &DatabaseObjstoreConfig{Provider: "memory"}}
}

func TestReleaseEntryFile_Forms(t *testing.T) {
	file, err := releaseEntryFile("")
	if err != nil {
		t.Fatalf("releaseEntryFile(\"\") = %v", err)
	}
	if !strings.HasPrefix(file, "-") || len(file) < 2 {
		t.Fatalf("releaseEntryFile(\"\") = %q, want a \"-\"-prefixed token", file)
	}
	again, err := releaseEntryFile("")
	if err != nil {
		t.Fatalf("releaseEntryFile(\"\") = %v", err)
	}
	if again == file {
		t.Fatalf("releaseEntryFile(\"\") twice = %q, want distinct tokens", file)
	}

	sum := sha256.Sum256([]byte("Tenant/x"))
	for _, tc := range []struct{ incarnation, want string }{
		{"0a1b", "0a1b"},
		{"Tenant/x", "sha256-" + hex.EncodeToString(sum[:])},
	} {
		got, err := releaseEntryFile(tc.incarnation)
		if err != nil {
			t.Fatalf("releaseEntryFile(%q) = %v", tc.incarnation, err)
		}
		if got != tc.want {
			t.Errorf("releaseEntryFile(%q) = %q, want %q", tc.incarnation, got, tc.want)
		}
	}
}

func TestReleaseEntries_NoLedger(t *testing.T) {
	m := newTestManager(t)
	entries, err := m.releaseEntries()
	if err != nil || entries != nil {
		t.Fatalf("releaseEntries = %v, %v; want nil, nil", entries, err)
	}
}

func TestReleaseEntries_SkipsUnreadableAndHidden(t *testing.T) {
	m := newTestManager(t)
	key := dbKey{tenant: "", name: "blobs"}
	file, err := m.recordRelease(key, ledgerConfig("blobs"), "0a1b", true)
	if err != nil {
		t.Fatalf("recordRelease = %v", err)
	}
	dir := filepath.Dir(m.releaseEntryPath(key.tenant, key.name, file))
	for name, data := range map[string]string{
		"garbage": "not json",
		"noname":  `{"name":""}`,
		".tmp":    `{"name":"blobs","backend":"objstore"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatalf("WriteFile %s = %v", name, err)
		}
	}
	entries, err := m.releaseEntries()
	if err != nil {
		t.Fatalf("releaseEntries = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("releaseEntries = %+v, want only the recorded entry", entries)
	}
	got := entries[0]
	if got.Name != "blobs" || got.Backend != BackendObjstore || got.Incarnation != "0a1b" || !got.ClaimOnly || got.file != file {
		t.Fatalf("releaseEntries[0] = %+v, want the recorded entry with file %q", got, file)
	}
}

func TestForgetRelease_Rules(t *testing.T) {
	m := newTestManager(t)
	if err := m.forgetRelease("", "blobs", ""); err != nil {
		t.Fatalf("forgetRelease with no file = %v, want nil", err)
	}
	if err := m.forgetRelease("", "missing", "0a1b"); err != nil {
		t.Fatalf("forgetRelease of a missing entry = %v, want nil", err)
	}

	key := dbKey{tenant: "", name: "blobs"}
	first, err := m.recordRelease(key, ledgerConfig("blobs"), "0a1b", false)
	if err != nil {
		t.Fatalf("recordRelease = %v", err)
	}
	second, err := m.recordRelease(key, ledgerConfig("blobs"), "0c2d", false)
	if err != nil {
		t.Fatalf("recordRelease = %v", err)
	}
	dir := filepath.Dir(m.releaseEntryPath(key.tenant, key.name, first))
	if err := m.forgetRelease(key.tenant, key.name, first); err != nil {
		t.Fatalf("forgetRelease = %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("name directory after forgetting one of two entries: %v, want it kept", err)
	}
	if err := m.forgetRelease(key.tenant, key.name, second); err != nil {
		t.Fatalf("forgetRelease = %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("name directory after forgetting its last entry: %v, want it removed", err)
	}
}

func TestRecordRelease_MkdirFailure(t *testing.T) {
	m := newTestManager(t)
	if err := os.WriteFile(filepath.Join(m.dataDir, releasingDir), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile = %v", err)
	}
	_, err := m.recordRelease(dbKey{name: "blobs"}, ledgerConfig("blobs"), "0a1b", false)
	var writeErr *MetaWriteError
	if !errors.As(err, &writeErr) {
		t.Fatalf("recordRelease = %v, want *MetaWriteError", err)
	}
}

func TestSameObjectStorageNamespace_Rules(t *testing.T) {
	m := newTestManager(t)
	m.factory = NewEmbeddedBackendFactory(WithObjectStorageNamespace(func(cfg DatabaseConfig) (string, error) {
		return cfg.Objstore.Provider + "/" + cfg.Objstore.Settings["bucket"], nil
	}))
	dropped := ledgerConfig("blobs")
	dropped.Objstore.Settings = map[string]string{"bucket": "a"}

	same, err := m.sameObjectStorageNamespace(dropped, DatabaseConfig{Name: "blobs", Backend: BackendMemory})
	if err != nil || same {
		t.Fatalf("sameObjectStorageNamespace(live memory) = %v, %v; want false, nil", same, err)
	}
	live := ledgerConfig("blobs")
	live.Objstore.Settings = map[string]string{"bucket": "a"}
	if same, err := m.sameObjectStorageNamespace(dropped, live); err != nil || !same {
		t.Fatalf("sameObjectStorageNamespace(same bucket) = %v, %v; want true, nil", same, err)
	}
	live.Objstore.Settings = map[string]string{"bucket": "b"}
	if same, err := m.sameObjectStorageNamespace(dropped, live); err != nil || same {
		t.Fatalf("sameObjectStorageNamespace(other bucket) = %v, %v; want false, nil", same, err)
	}

	m.factory = NewEmbeddedBackendFactory()
	if _, err := m.sameObjectStorageNamespace(dropped, live); !errors.Is(err, ErrObjectStorageNamespaceUnknown) {
		t.Fatalf("sameObjectStorageNamespace with no namespacer = %v, want ErrObjectStorageNamespaceUnknown", err)
	}
}

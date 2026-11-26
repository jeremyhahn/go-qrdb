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

package objstoredb_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/objstoredb"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore/objstoretest"
)

func writeCredential(t *testing.T, name, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// create opens cfg through the wired factory, so errors carry the factory's
// *database.StorageCreateError wrapping.
func create(t *testing.T, cfg database.DatabaseConfig) (database.Backend, error) {
	t.Helper()
	backend, err := objstoredb.NewBackendFactory().Create(cfg, "")
	if err == nil {
		t.Cleanup(func() { _ = backend.Close() })
	}
	return backend, err
}

func TestOpen_RoundTrip(t *testing.T) {
	p := objstoretest.NewProvider(t)
	cfg := database.DatabaseConfig{Name: "blobs", Backend: database.BackendObjstore, Objstore: &database.DatabaseObjstoreConfig{
		Provider:   p.Name(),
		KeyFile:    writeCredential(t, "key", "access-key-id"),
		SecretFile: writeCredential(t, "secret", "secret-access-key"),
	}}
	store, err := objstoredb.Open(cfg)
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, ok := store.(*objstore.Engine); !ok {
		t.Fatalf("Open: got %T, want *objstore.Engine", store)
	}
	if err := store.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get([]byte("k"))
	if err != nil || string(got) != "v" {
		t.Fatalf("Get = %q, %v; want \"v\"", got, err)
	}
	deliveries := p.Deliveries()
	if len(deliveries) == 0 || deliveries[0].Key != "access-key-id" {
		t.Fatalf("provider deliveries = %+v, want the key file's handle", deliveries)
	}
}

func TestFactory_OpensObjstoreDatabase(t *testing.T) {
	p := objstoretest.NewProvider(t)
	cfg := database.DatabaseConfig{Name: "blobs", Backend: database.BackendObjstore, Objstore: &database.DatabaseObjstoreConfig{Provider: p.Name()}}
	backend, err := create(t, cfg)
	if err != nil || backend == nil {
		t.Fatalf("Create objstore = %v, %v; want a backend", backend, err)
	}
}

func TestFactory_MissingCredentialFile(t *testing.T) {
	p := objstoretest.NewProvider(t)
	missing := filepath.Join(t.TempDir(), "absent")
	cfg := database.DatabaseConfig{Name: "blobs", Backend: database.BackendObjstore, Objstore: &database.DatabaseObjstoreConfig{
		Provider:   p.Name(),
		KeyFile:    writeCredential(t, "key", "access-key-id"),
		SecretFile: missing,
	}}
	backend, err := create(t, cfg)
	if backend != nil {
		t.Fatal("Create objstore: got a backend with a missing credential file")
	}
	var createErr *database.StorageCreateError
	if !errors.As(err, &createErr) || createErr.Backend != string(database.BackendObjstore) {
		t.Fatalf("Create objstore = %v, want *database.StorageCreateError", err)
	}
	var fileErr *objstore.CredentialFileError
	if !errors.As(err, &fileErr) || fileErr.File != missing {
		t.Fatalf("Create objstore = %v, want *objstore.CredentialFileError naming %s", err, missing)
	}
	if len(p.Deliveries()) != 0 {
		t.Fatal("a failed credential read reached the provider")
	}
}

func TestFactory_UnknownProvider(t *testing.T) {
	cfg := database.DatabaseConfig{Name: "blobs", Backend: database.BackendObjstore, Objstore: &database.DatabaseObjstoreConfig{Provider: "no-such-provider"}}
	_, err := create(t, cfg)
	var createErr *database.StorageCreateError
	if !errors.As(err, &createErr) {
		t.Fatalf("Create objstore = %v, want *database.StorageCreateError", err)
	}
	var unsupported *objstore.UnsupportedProviderError
	if !errors.As(err, &unsupported) || unsupported.Provider != "no-such-provider" {
		t.Fatalf("Create objstore = %v, want *objstore.UnsupportedProviderError", err)
	}
}

// TestValidateSettings_RefusesInlineSecret checks the seam's validator
// refuses an inline secret with *database.ObjstoreCredentialSettingError,
// naming the setting, never echoing the value, and keeping go-objstore's
// refusal reachable.
func TestValidateSettings_RefusesInlineSecret(t *testing.T) {
	const secret = "inline-secret-must-not-leak"
	err := objstoredb.ValidateSettings(map[string]string{"bucket": "b", "secret_key": secret})
	var settingErr *database.ObjstoreCredentialSettingError
	if !errors.As(err, &settingErr) {
		t.Fatalf("ValidateSettings = %v, want *database.ObjstoreCredentialSettingError", err)
	}
	if settingErr.Setting != "secret_key" {
		t.Fatalf("Setting = %q, want secret_key", settingErr.Setting)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("the refusal echoes the secret")
	}
	var cause *objstore.CredentialSettingError
	if !errors.As(err, &cause) {
		t.Fatalf("cause %v is not *objstore.CredentialSettingError", errors.Unwrap(err))
	}
}

// TestValidateSettings_AcceptsNonSecretSettings checks settings without a
// credential pass.
func TestValidateSettings_AcceptsNonSecretSettings(t *testing.T) {
	for _, settings := range []map[string]string{nil, {"bucket": "b", "region": "r"}} {
		if err := objstoredb.ValidateSettings(settings); err != nil {
			t.Fatalf("ValidateSettings(%v) = %v", settings, err)
		}
	}
}

// TestNewBackendFactory_WiresTheValidator checks the composed factory
// validates an Objstore block through ValidateSettings.
func TestNewBackendFactory_WiresTheValidator(t *testing.T) {
	f := objstoredb.NewBackendFactory()
	refused := database.DatabaseConfig{Name: "blobs", Backend: database.BackendObjstore,
		Objstore: &database.DatabaseObjstoreConfig{Provider: "memory", Settings: map[string]string{"secret_key": "x"}}}
	var settingErr *database.ObjstoreCredentialSettingError
	if err := f.ValidateObjectStorage(refused); !errors.As(err, &settingErr) {
		t.Fatalf("ValidateObjectStorage = %v, want *database.ObjstoreCredentialSettingError", err)
	}
	accepted := database.DatabaseConfig{Name: "blobs", Backend: database.BackendObjstore,
		Objstore: &database.DatabaseObjstoreConfig{Provider: "memory"}}
	if err := f.ValidateObjectStorage(accepted); err != nil {
		t.Fatalf("ValidateObjectStorage = %v", err)
	}
}

// TestNamespace_KeysTheProviderNamespace checks that Namespace keys a
// database's provider namespace as objstore.NamespaceKey does and refuses a
// config with no object-storage block.
func TestNamespace_KeysTheProviderNamespace(t *testing.T) {
	settings := map[string]string{"bucket": "b"}
	cfg := database.DatabaseConfig{Name: "blobs", Backend: database.BackendObjstore,
		Objstore: &database.DatabaseObjstoreConfig{Provider: "memory", Settings: settings}}
	got, err := objstoredb.Namespace(cfg)
	if err != nil {
		t.Fatalf("Namespace = %v", err)
	}
	want, err := objstore.NamespaceKey("memory", settings)
	if err != nil {
		t.Fatalf("NamespaceKey = %v", err)
	}
	if got != want {
		t.Fatalf("Namespace = %q, want %q", got, want)
	}
	if _, err := objstoredb.Namespace(database.DatabaseConfig{Name: "blobs"}); err == nil {
		t.Fatal("Namespace with no objstore config = nil, want an error")
	}
}

// TestNilObjstoreConfig_Refused pins the branches a database record with no
// object-storage configuration takes: Open builds no engine, and Namespace
// and ReleaseOwnerMarker refuse with the missing-configuration error.
func TestNilObjstoreConfig_Refused(t *testing.T) {
	cfg := database.DatabaseConfig{Name: "blobs", Backend: database.BackendObjstore}
	store, err := objstoredb.Open(cfg)
	if err == nil || store != nil {
		t.Fatalf("Open with no objstore config = %v, %v; want an error and no engine", store, err)
	}
	if _, err := objstoredb.Namespace(cfg); !errors.Is(err, objstoredb.ErrNoObjstoreConfig) {
		t.Fatalf("Namespace with no objstore config = %v, want ErrNoObjstoreConfig", err)
	}
	if err := objstoredb.ReleaseOwnerMarker(cfg); !errors.Is(err, objstoredb.ErrNoObjstoreConfig) {
		t.Fatalf("ReleaseOwnerMarker with no objstore config = %v, want ErrNoObjstoreConfig", err)
	}
}

// TestReleaseOwnerMarker_FreesTheNamespace opens a database on a local-provider
// namespace, closes it, and releases its owner marker: before the release a
// second incarnation is refused the namespace, after it the second opens.
func TestReleaseOwnerMarker_FreesTheNamespace(t *testing.T) {
	settings := map[string]string{"path": filepath.Join(t.TempDir(), "objects")}
	first := database.DatabaseConfig{Name: "blobs", Incarnation: "one", Backend: database.BackendObjstore,
		Objstore: &database.DatabaseObjstoreConfig{Provider: "local", Settings: settings}}
	second := first
	second.Incarnation = "two"

	store, err := objstoredb.Open(first)
	if err != nil {
		t.Fatalf("Open first: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close first: %v", err)
	}
	if refused, err := objstoredb.Open(second); err == nil {
		_ = refused.Close()
		t.Fatal("Open second before the release = nil, want the namespace refused")
	}

	if err := objstoredb.ReleaseOwnerMarker(first); err != nil {
		t.Fatalf("ReleaseOwnerMarker: %v", err)
	}
	reopened, err := objstoredb.Open(second)
	if err != nil {
		t.Fatalf("Open second after the release: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close second: %v", err)
	}
}

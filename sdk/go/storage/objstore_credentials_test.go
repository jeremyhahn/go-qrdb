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

package storage_test

// These tests are the external composer's view of object-storage
// credentials: they import go-qrdb only through sdk/go/storage, so an
// embedder can supply credentials by file reference without reaching into
// pkg/storage/objstore.

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objmemory "github.com/jeremyhahn/go-objstore/pkg/memory"

	"github.com/jeremyhahn/go-qrdb/sdk/go/storage"
)

var composerProviderSeq atomic.Uint64

// composerProvider is a credential-reading provider an embedder registers
// through the SDK; it records the credential each open received.
type composerProvider struct {
	name   string
	key    atomic.Pointer[string]
	secret atomic.Pointer[string]
}

func registerComposerProvider(t *testing.T) *composerProvider {
	t.Helper()
	p := &composerProvider{name: "sdk-composer-" + strconv.FormatUint(composerProviderSeq.Add(1), 10)}
	reads := storage.ObjStoreProviderCredentials{Key: true, Secret: true}
	if err := storage.RegisterObjStoreProvider(p.name, reads, p.open); err != nil {
		t.Fatalf("RegisterObjStoreProvider(%q): %v", p.name, err)
	}
	return p
}

func (p *composerProvider) open(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
	if err := creds.Open(func(key, secret []byte) error {
		k, s := string(key), string(secret)
		p.key.Store(&k)
		p.secret.Store(&s)
		return nil
	}); err != nil {
		return nil, err
	}
	store := objmemory.New()
	if err := store.Configure(settings, creds); err != nil {
		return nil, err
	}
	return store, nil
}

func writeCredentialFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestNewObjStoreStorage_OpensWithCredentialFileReferences(t *testing.T) {
	p := registerComposerProvider(t)
	files := storage.ObjStoreCredentialFiles{
		KeyFile:    writeCredentialFile(t, "key", "AKIACOMPOSER"),
		SecretFile: writeCredentialFile(t, "secret", "composer-secret"),
	}
	var opt storage.ObjStoreOption = storage.WithObjStoreCredentialFiles(files)

	engine, err := storage.NewObjStoreStorage(p.name, map[string]string{}, opt)
	if err != nil {
		t.Fatalf("NewObjStoreStorage: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	if got := p.key.Load(); got == nil || *got != "AKIACOMPOSER" {
		t.Fatalf("provider key = %v, want the key file's contents", got)
	}
	if got := p.secret.Load(); got == nil || *got != "composer-secret" {
		t.Fatalf("provider secret = %v, want the secret file's contents", got)
	}
	var store storage.Storage = engine
	if err := store.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, err := store.Get([]byte("k")); err != nil || string(v) != "v" {
		t.Fatalf("Get = %q, %v; want v", v, err)
	}
}

func TestNewObjStoreStorage_CredentialErrorsAreSDKTypes(t *testing.T) {
	p := registerComposerProvider(t)
	secret := writeCredentialFile(t, "secret", "s")
	missing := filepath.Join(t.TempDir(), "absent")

	_, err := storage.NewObjStoreStorage(p.name, nil, storage.WithObjStoreCredentialFiles(storage.ObjStoreCredentialFiles{KeyFile: missing, SecretFile: secret}))
	var fileErr *storage.ObjStoreCredentialFileError
	if !errors.As(err, &fileErr) || fileErr.File != missing || fileErr.Credential != storage.ObjStoreCredentialKey {
		t.Fatalf("missing key file: err = %v, want ObjStoreCredentialFileError naming %s", err, missing)
	}

	_, err = storage.NewObjStoreStorage(p.name, nil, storage.WithObjStoreCredentialFiles(storage.ObjStoreCredentialFiles{KeyFile: missing}))
	if !errors.Is(err, storage.ErrObjStoreCredentialKeyWithoutSecret) {
		t.Fatalf("key without secret: err = %v, want ErrObjStoreCredentialKeyWithoutSecret", err)
	}

	_, err = storage.NewObjStoreStorage("memory", nil, storage.WithObjStoreCredentialFiles(storage.ObjStoreCredentialFiles{SecretFile: secret}))
	var ignored *storage.ObjStoreCredentialIgnoredError
	if !errors.As(err, &ignored) || ignored.Credential != storage.ObjStoreCredentialSecret {
		t.Fatalf("memory provider: err = %v, want ObjStoreCredentialIgnoredError for the secret", err)
	}

	settings := map[string]string{"secret_key": "inline"}
	var settingErr *storage.ObjStoreCredentialSettingError
	if err := storage.RejectObjStoreCredentialSettings(settings); !errors.As(err, &settingErr) {
		t.Fatalf("RejectObjStoreCredentialSettings = %v, want ObjStoreCredentialSettingError", err)
	}
	if _, err := storage.NewObjStoreStorage(p.name, settings); !errors.As(err, &settingErr) {
		t.Fatalf("inline credential: err = %v, want ObjStoreCredentialSettingError", err)
	}
}

func TestRegisterObjStoreProvider_RefusesShippedAndInvalidRegistrations(t *testing.T) {
	p := registerComposerProvider(t)
	reads := storage.ObjStoreProviderCredentials{Secret: true}

	var registered *storage.ObjStoreProviderRegisteredError
	if err := storage.RegisterObjStoreProvider(p.name, reads, p.open); !errors.As(err, &registered) {
		t.Fatalf("duplicate registration: err = %v, want ObjStoreProviderRegisteredError", err)
	}
	if err := storage.RegisterObjStoreProvider("s3", reads, p.open); !errors.As(err, &registered) {
		t.Fatalf("shipped name: err = %v, want ObjStoreProviderRegisteredError", err)
	}
	if err := storage.RegisterObjStoreProvider("", reads, p.open); !errors.Is(err, storage.ErrInvalidObjStoreProviderRegistration) {
		t.Fatalf("empty name: err = %v, want ErrInvalidObjStoreProviderRegistration", err)
	}
	if err := storage.RegisterObjStoreProvider(p.name+"-nil", reads, nil); !errors.Is(err, storage.ErrInvalidObjStoreProviderRegistration) {
		t.Fatalf("nil creator: err = %v, want ErrInvalidObjStoreProviderRegistration", err)
	}
}

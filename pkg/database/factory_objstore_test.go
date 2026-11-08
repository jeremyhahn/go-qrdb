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
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

func memoryObjstoreOpener(opened *[]string) ObjstoreOpener {
	return func(name, _ string) (storage.Storage, error) {
		*opened = append(*opened, name)
		return objstore.New("memory", map[string]string{})
	}
}

// A backend: objstore database opens on the objstore engine the node's opener
// returns and round-trips a key through it.
func TestEmbeddedBackendFactory_ObjstoreOpensThroughOpener(t *testing.T) {
	var opened []string
	f := NewEmbeddedBackendFactory(WithObjstoreOpener(memoryObjstoreOpener(&opened)))
	b, err := f.Create(DatabaseConfig{Name: "blobs", Backend: BackendObjstore}, t.TempDir())
	if err != nil {
		t.Fatalf("Create(objstore): %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if len(opened) != 1 || opened[0] != "blobs" {
		t.Fatalf("opener calls = %q, want [blobs]", opened)
	}
	ctx := context.Background()
	if err := b.Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := b.Get(ctx, []byte("k"))
	if err != nil || string(got) != "v" {
		t.Fatalf("Get = %q, %v; want v", got, err)
	}
}

// Without an opener the factory cannot reach the node's provider, and says so
// with a typed error rather than InvalidBackendError.
func TestEmbeddedBackendFactory_ObjstoreWithoutOpener(t *testing.T) {
	_, err := NewEmbeddedBackendFactory().Create(DatabaseConfig{Name: "blobs", Backend: BackendObjstore}, t.TempDir())
	var notConfigured *ObjstoreNotConfiguredError
	if !errors.As(err, &notConfigured) {
		t.Fatalf("Create(objstore) = %v, want *ObjstoreNotConfiguredError", err)
	}
	if notConfigured.Name != "blobs" || notConfigured.Error() == "" {
		t.Fatalf("ObjstoreNotConfiguredError = %+v", notConfigured)
	}
	var invalid *InvalidBackendError
	if errors.As(err, &invalid) {
		t.Fatal("objstore is a known backend, not InvalidBackendError")
	}
}

// An opener that fails is reported as a StorageCreateError naming the backend.
func TestEmbeddedBackendFactory_ObjstoreOpenerFails(t *testing.T) {
	boom := errors.New("bucket unreachable")
	f := NewEmbeddedBackendFactory(WithObjstoreOpener(func(string, string) (storage.Storage, error) {
		return nil, boom
	}))
	_, err := f.Create(DatabaseConfig{Name: "blobs", Backend: BackendObjstore}, t.TempDir())
	var createErr *StorageCreateError
	if !errors.As(err, &createErr) || createErr.Backend != string(BackendObjstore) || !errors.Is(err, boom) {
		t.Fatalf("Create = %v, want StorageCreateError wrapping the opener's error", err)
	}
}

func TestObjstoreRaftModeError_NamesTheReason(t *testing.T) {
	err := &ObjstoreRaftModeError{Name: "blobs"}
	if msg := err.Error(); msg == "" || !strings.Contains(msg, ObjstoreRaftModeReason) {
		t.Fatalf("Error() = %q, want the hot-apply-path reason", msg)
	}
}

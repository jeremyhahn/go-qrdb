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

package index

import (
	"context"
	"reflect"
	"testing"

	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// ---------------------------------------------------------------------------
// Local test helpers
// ---------------------------------------------------------------------------

// deletefailIndex is an Index whose Delete always returns deleteErr.
type deletefailIndex struct {
	deleteErr error
}

func (f *deletefailIndex) Put(_ []byte, _ []byte) error                  { return nil }
func (f *deletefailIndex) Get(_ []byte) ([][]byte, error)                { return nil, nil }
func (f *deletefailIndex) Delete(_ []byte, _ []byte) error               { return f.deleteErr }
func (f *deletefailIndex) Scan(_, _ []byte) (map[string][][]byte, error) { return nil, nil }
func (f *deletefailIndex) GetAll() ([][]byte, error)                     { return nil, nil }
func (f *deletefailIndex) Clear() error                                  { return nil }
func (f *deletefailIndex) Close() error                                  { return nil }

// putfailIndex is an Index whose Put always returns putErr.
// Delete succeeds so the old-value removal succeeds; Put then fails for the new value.
type putfailIndex struct {
	putErr error
}

func (f *putfailIndex) Put(_ []byte, _ []byte) error                  { return f.putErr }
func (f *putfailIndex) Get(_ []byte) ([][]byte, error)                { return nil, nil }
func (f *putfailIndex) Delete(_ []byte, _ []byte) error               { return nil }
func (f *putfailIndex) Scan(_, _ []byte) (map[string][][]byte, error) { return nil, nil }
func (f *putfailIndex) GetAll() ([][]byte, error)                     { return nil, nil }
func (f *putfailIndex) Clear() error                                  { return nil }
func (f *putfailIndex) Close() error                                  { return nil }

// ---------------------------------------------------------------------------
// manager.go — RegisterIndexes on closed manager (94.4%)
// ---------------------------------------------------------------------------

// TestIndexManager_RegisterIndexes_Closed verifies RegisterIndexes returns an
// error when the manager is closed.
func TestIndexManager_RegisterIndexes_Closed(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	mgr, err := NewIndexManager(s, newTestLogger(t))
	if err != nil {
		t.Fatalf("NewIndexManager: %v", err)
	}
	_ = mgr.Close()

	type User struct {
		Name string `index:"true"`
	}
	err = mgr.RegisterIndexes(reflect.TypeOf(User{}))
	if err == nil {
		t.Fatal("expected error from RegisterIndexes on closed manager")
	}
}

// TestIndexManager_UpdateIndexes_Closed verifies UpdateIndexes returns an
// error when the manager is closed.
func TestIndexManager_UpdateIndexes_Closed(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	mgr, err := NewIndexManager(s, newTestLogger(t))
	if err != nil {
		t.Fatalf("NewIndexManager: %v", err)
	}
	_ = mgr.Close()

	type User struct{ Name string }
	err = mgr.UpdateIndexes(nil, User{Name: "alice"}, []byte("ek"))
	if err == nil {
		t.Fatal("expected error from UpdateIndexes on closed manager")
	}
}

// TestIndexManager_DeleteIndexes_Closed verifies DeleteIndexes returns an
// error when the manager is closed.
func TestIndexManager_DeleteIndexes_Closed(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	mgr, err := NewIndexManager(s, newTestLogger(t))
	if err != nil {
		t.Fatalf("NewIndexManager: %v", err)
	}
	_ = mgr.Close()

	type User struct{ Name string }
	err = mgr.DeleteIndexes(User{Name: "alice"}, []byte("ek"))
	if err == nil {
		t.Fatal("expected error from DeleteIndexes on closed manager")
	}
}

// TestIndexManager_QueryIndexWithContext_Closed verifies QueryIndexWithContext
// returns an error when the manager is closed.
func TestIndexManager_QueryIndexWithContext_Closed(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	mgr, err := NewIndexManager(s, newTestLogger(t))
	if err != nil {
		t.Fatalf("NewIndexManager: %v", err)
	}
	_ = mgr.Close()

	_, err = mgr.QueryIndexWithContext(context.Background(), "Name", "alice")
	if err == nil {
		t.Fatal("expected error from QueryIndexWithContext on closed manager")
	}
}

// TestIndexManager_RebuildIndex_PopulateErrorExtra verifies RebuildIndex
// propagates errors from the populate callback (extra coverage test).
func TestIndexManager_RebuildIndex_PopulateErrorExtra(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	mgr, err := NewIndexManager(s, newTestLogger(t))
	if err != nil {
		t.Fatalf("NewIndexManager: %v", err)
	}

	// Register an index.
	type User struct {
		Name string `index:"true"`
	}
	if err := mgr.RegisterIndexes(reflect.TypeOf(User{})); err != nil {
		t.Fatalf("RegisterIndexes: %v", err)
	}

	populateErr := &ClosedError{Name: "populate-fail"}
	err = mgr.RebuildIndex("Name", func(_ Index) error {
		return populateErr
	})
	if err == nil {
		t.Fatal("expected error from RebuildIndex when populate fails")
	}
}

// TestIndexManager_UpdateIndexes_DeleteError verifies UpdateIndexes collects
// and returns the first error when Delete fails on an index.
func TestIndexManager_UpdateIndexes_DeleteError(t *testing.T) {
	log := newTestLogger(t)
	store := newMockStorage()
	mgr := &IndexManager{
		storage:  store,
		logger:   log.WithField("component", "test"),
		indexes:  make(map[string]Index),
		metadata: make(map[string]*IndexMetadata),
	}

	type User struct {
		Name string `index:"true"`
	}

	deleteErr := &ClosedError{Name: "delete-error"}
	// failingIndex.Delete returns its deleteErr which is nil (we use putErr instead).
	// Use a customFailingIndex that fails Delete specifically.
	mgr.indexes["Name"] = &deletefailIndex{deleteErr: deleteErr}
	mgr.metadata["Name"] = &IndexMetadata{
		FieldName: "Name", FieldType: "string",
		Config: IndexConfig{Name: "Name", Type: IndexTypeNormal, CaseSensitive: true},
	}

	// oldEntity has the Name field; newEntity has a different value so oldValue != newValue.
	old := User{Name: "alice"}
	newEntity := User{Name: "bob"}
	err := mgr.UpdateIndexes(old, newEntity, []byte("ek1"))
	if err == nil {
		t.Fatal("expected error when Delete fails, got nil")
	}
}

// TestIndexManager_UpdateIndexes_PutError verifies UpdateIndexes returns an
// error when Put fails on the index after successful Delete.
func TestIndexManager_UpdateIndexes_PutError(t *testing.T) {
	log := newTestLogger(t)
	store := newMockStorage()
	mgr := &IndexManager{
		storage:  store,
		logger:   log.WithField("component", "test"),
		indexes:  make(map[string]Index),
		metadata: make(map[string]*IndexMetadata),
	}

	type User struct {
		Name string `index:"true"`
	}

	putErr := &ClosedError{Name: "put-error"}
	mgr.indexes["Name"] = &putfailIndex{putErr: putErr}
	mgr.metadata["Name"] = &IndexMetadata{
		FieldName: "Name", FieldType: "string",
		Config: IndexConfig{Name: "Name", Type: IndexTypeNormal, CaseSensitive: true},
	}

	// oldEntity and newEntity have different values so the index update is attempted.
	old := User{Name: "alice"}
	newEntity := User{Name: "bob"}
	err := mgr.UpdateIndexes(old, newEntity, []byte("ek1"))
	if err == nil {
		t.Fatal("expected error when Put fails, got nil")
	}
}

// TestIndexManager_UpdateIndexes_OldEntityGetError verifies UpdateIndexes
// collects and returns the first error when GetFieldValue fails for an old entity.
func TestIndexManager_UpdateIndexes_OldEntityGetError(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	mgr, err := NewIndexManager(s, newTestLogger(t))
	if err != nil {
		t.Fatalf("NewIndexManager: %v", err)
	}

	type User struct {
		Name string `index:"true"`
	}
	if err := mgr.RegisterIndexes(reflect.TypeOf(User{})); err != nil {
		t.Fatalf("RegisterIndexes: %v", err)
	}

	// Use a different type for oldEntity so GetFieldValue fails (field not found).
	type Other struct{ X int }
	err = mgr.UpdateIndexes(Other{X: 1}, User{Name: "new"}, []byte("ek"))
	if err == nil {
		t.Fatal("expected error when old entity type doesn't match, got nil")
	}
}

// TestIndexManager_QueryIndexWithContext_NotFound verifies QueryIndexWithContext
// returns an error when the index does not exist.
func TestIndexManager_QueryIndexWithContext_NotFound(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	mgr, err := NewIndexManager(s, newTestLogger(t))
	if err != nil {
		t.Fatalf("NewIndexManager: %v", err)
	}

	_, err = mgr.QueryIndexWithContext(context.Background(), "missing", "value")
	if err == nil {
		t.Fatal("expected error for missing index, got nil")
	}
}

// TestIndexManager_QueryIndexWithContext_ConvertError verifies
// QueryIndexWithContext returns an error when value conversion fails.
func TestIndexManager_QueryIndexWithContext_ConvertError(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	mgr, err := NewIndexManager(s, newTestLogger(t))
	if err != nil {
		t.Fatalf("NewIndexManager: %v", err)
	}

	type User struct {
		Name string `index:"true"`
	}
	if err := mgr.RegisterIndexes(reflect.TypeOf(User{})); err != nil {
		t.Fatalf("RegisterIndexes: %v", err)
	}

	// Pass nil to trigger interfaceToBytes nil error.
	_, err = mgr.QueryIndexWithContext(context.Background(), "Name", nil)
	if err == nil {
		t.Fatal("expected error for nil value, got nil")
	}
}

// TestIndexManager_DeleteIndexes_GetFieldError verifies DeleteIndexes collects
// and returns the first error when GetFieldValue fails.
func TestIndexManager_DeleteIndexes_GetFieldError(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	mgr, err := NewIndexManager(s, newTestLogger(t))
	if err != nil {
		t.Fatalf("NewIndexManager: %v", err)
	}

	type User struct {
		Name string `index:"true"`
	}
	if err := mgr.RegisterIndexes(reflect.TypeOf(User{})); err != nil {
		t.Fatalf("RegisterIndexes: %v", err)
	}

	// Pass an entity that doesn't have a "Name" field.
	type Other struct{ X int }
	err = mgr.DeleteIndexes(Other{X: 1}, []byte("ek"))
	if err == nil {
		t.Fatal("expected error when entity type doesn't match index fields, got nil")
	}
}

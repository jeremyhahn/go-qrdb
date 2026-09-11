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

package objstore_test

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

// A closed engine's Batch must be the shared inert handle, not a live batch
// over state the engine no longer serves. Batch returns a value, so this is
// the only way it can refuse.
func TestClosedEngineBatchRefusesEveryOperation(t *testing.T) {
	s := mustNewObjstoreEngine(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	b := s.Batch()
	t.Cleanup(func() { _ = b.Close() })

	for name, call := range map[string]func() error{
		"Set":      func() error { return b.Set([]byte("k"), []byte("v")) },
		"Delete":   func() error { return b.Delete([]byte("k")) },
		"Get":      func() error { _, err := b.Get([]byte("k")); return err },
		"Commit":   func() error { return b.Commit() },
		"Rollback": func() error { return b.Rollback() },
	} {
		if err := call(); !errors.Is(err, storage.ErrStorageClosed) {
			t.Errorf("%s on a closed engine's batch = %v, want ErrStorageClosed", name, err)
		}
	}
}

// A closed engine's iterator must report the closure rather than reading as
// an empty range, which would be a wrong answer instead of a loud one.
func TestClosedEngineIteratorReportsTheClosure(t *testing.T) {
	s := mustNewObjstoreEngine(t)
	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	it := s.NewIterator(nil)
	t.Cleanup(func() { _ = it.Close() })

	if it.SeekToFirst() {
		t.Error("SeekToFirst on a closed engine positioned the iterator")
	}
	if !errors.Is(it.Error(), storage.ErrStorageClosed) {
		t.Errorf("Error() = %v, want ErrStorageClosed", it.Error())
	}
}

// mustNewObjstoreEngine opens a local-provider objstore engine rooted in a
// per-test temp dir.
func mustNewObjstoreEngine(t *testing.T) *objstore.Engine {
	t.Helper()
	e, err := objstore.New("local", map[string]string{"path": t.TempDir()})
	if err != nil {
		t.Fatalf("objstore.New: %v", err)
	}
	return e
}

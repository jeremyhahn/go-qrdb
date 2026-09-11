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

package sdk

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// The sentinel crosses the SDK boundary as a VALUE alias, not a copy. A
// redeclaration would look identical and never match, which is the failure
// mode the errors convention warns about (handbook errors.md §4).
func TestErrStorageClosedIsTheSameValue(t *testing.T) {
	if ErrStorageClosed != error(storage.ErrStorageClosed) {
		t.Fatal("sdk.ErrStorageClosed is not pkg/storage's sentinel value")
	}
	if !errors.Is(&storage.ClosedError{}, ErrStorageClosed) {
		t.Fatal("a backend's ClosedError does not match sdk.ErrStorageClosed")
	}
}

// A consumer of the SDK never imports pkg/storage (00 L3), so every closed
// condition it can meet must be reachable from the SDK surface alone.
func TestIsStorageClosedCoversEveryClosedProducer(t *testing.T) {
	closed := map[string]error{
		"storage ClosedError": &StorageClosedError{},
		"storage sentinel":    ErrStorageClosed,
		"wrapped in OpError":  &StorageOpError{Op: "set", Err: &StorageClosedError{}},
		"wrapped in Batch":    &StorageBatchError{Op: "commit", Err: &StorageClosedError{}},
		"local backend":       &backend.BackendClosedError{},
	}
	for name, err := range closed {
		t.Run(name, func(t *testing.T) {
			if !IsStorageClosed(err) {
				t.Fatalf("IsStorageClosed(%v) = false, want true", err)
			}
			if !errors.Is(err, ErrStorageClosed) {
				t.Fatalf("errors.Is(%v, ErrStorageClosed) = false, want true", err)
			}
		})
	}
}

// The predicate must stay narrow: a closed CLIENT is the caller's own handle,
// a different fact from the store going away underneath it, and collapsing the
// two would let a genuine misuse read as an orderly shutdown.
func TestIsStorageClosedRejectsOtherClosures(t *testing.T) {
	others := map[string]error{
		"nil":            nil,
		"client closed":  &ClientClosedError{},
		"key not found":  storage.ErrKeyNotFound,
		"unrelated":      errors.New("disk on fire"),
		"wrapped unrel.": &StorageOpError{Op: "set", Err: errors.New("disk on fire")},
	}
	for name, err := range others {
		t.Run(name, func(t *testing.T) {
			if IsStorageClosed(err) {
				t.Fatalf("IsStorageClosed(%v) = true, want false", err)
			}
		})
	}
}

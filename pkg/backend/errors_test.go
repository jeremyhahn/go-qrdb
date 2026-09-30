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

package backend

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

func TestClusterOperationError_Message(t *testing.T) {
	err := &ClusterOperationError{Op: "AddNode"}
	if !strings.Contains(err.Error(), "AddNode") {
		t.Fatalf("expected error to contain op name, got %q", err.Error())
	}
}

func TestClusterOperationError_ImplementsError(t *testing.T) {
	var err error = &ClusterOperationError{Op: "RemoveNode"}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestUnsupportedOperationError_Message(t *testing.T) {
	err := &UnsupportedOperationError{Op: 0xff}
	if !strings.Contains(err.Error(), "0xff") {
		t.Fatalf("expected error to contain hex op code, got %q", err.Error())
	}
}

func TestUnsupportedOperationError_ImplementsError(t *testing.T) {
	var err error = &UnsupportedOperationError{Op: 42}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestBackendClosedError_Message(t *testing.T) {
	err := &BackendClosedError{}
	if !strings.Contains(err.Error(), "closed") {
		t.Fatalf("expected error to contain 'closed', got %q", err.Error())
	}
}

func TestBackendClosedError_ImplementsError(t *testing.T) {
	var err error = &BackendClosedError{}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

// Closing a LocalBackend closes the state machine and with it the store, so
// its refusal reports the same fact as the storage engine's own. A caller must
// match ONE sentinel, not one per layer that can lose a shutdown race
// (go-qrdb#371).
func TestBackendClosedError_MatchesTheStorageSentinel(t *testing.T) {
	err := error(&BackendClosedError{})
	if !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatal("errors.Is(&BackendClosedError{}, storage.ErrStorageClosed) = false, want true")
	}
	if !storage.IsClosed(err) {
		t.Fatal("storage.IsClosed(&BackendClosedError{}) = false, want true")
	}
}

// The sentinel must survive a wrapper, since a caller receives this error
// through the service and transport layers rather than bare.
func TestBackendClosedError_MatchesThroughAWrapper(t *testing.T) {
	err := &storage.OpError{Op: "propose", Err: &BackendClosedError{}}
	if !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatal("a wrapped BackendClosedError did not match storage.ErrStorageClosed")
	}
	if errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("a BackendClosedError matched ErrKeyNotFound; the sentinels are not distinct")
	}
}

func TestLookupTypeError_Message(t *testing.T) {
	err := &LookupTypeError{Type: "int"}
	if !strings.Contains(err.Error(), "int") {
		t.Fatalf("expected error to contain type name, got %q", err.Error())
	}
}

func TestLookupTypeError_ImplementsError(t *testing.T) {
	var err error = &LookupTypeError{Type: "map[string]string"}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestUndecodableCommandError(t *testing.T) {
	inner := errors.New("short")
	err := &UndecodableCommandError{Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("UndecodableCommandError must unwrap to the decode error")
	}
	if err.Error() == "" {
		t.Fatal("UndecodableCommandError must describe itself")
	}
}

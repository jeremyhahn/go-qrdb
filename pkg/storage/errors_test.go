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

package storage

import (
	"errors"
	"io"
	"testing"
)

// ---------------------------------------------------------------------------
// StorageError (sentinels)
// ---------------------------------------------------------------------------

func TestStorageError_Error(t *testing.T) {
	err := &StorageError{Code: "test_code", Message: "test message"}
	want := "qrdb/storage: test_code: test message"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestStorageError_Sentinel(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"ErrKeyNotFound", ErrKeyNotFound, "qrdb/storage: key_not_found: key does not exist"},
		{"ErrStorageClosed", ErrStorageClosed, "qrdb/storage: storage_closed: storage engine is closed"},
		{"ErrEmptyKey", ErrEmptyKey, "qrdb/storage: empty_key: key must not be empty"},
		{"ErrInvalidPath", ErrInvalidPath, "qrdb/storage: invalid_path: storage path is invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Error() != tt.want {
				t.Fatalf("got %q, want %q", tt.err.Error(), tt.want)
			}
		})
	}
}

func TestStorageError_Is(t *testing.T) {
	// Verify errors.Is works with pointer identity.
	if !errors.Is(ErrKeyNotFound, ErrKeyNotFound) {
		t.Fatal("errors.Is(ErrKeyNotFound, ErrKeyNotFound) should be true")
	}

	// Different instances with same code are not equal via errors.Is.
	other := &StorageError{Code: "key_not_found", Message: "key does not exist"}
	if errors.Is(other, ErrKeyNotFound) {
		t.Fatal("different instances should not be equal via errors.Is without Is method")
	}
}

// ---------------------------------------------------------------------------
// OpError
// ---------------------------------------------------------------------------

func TestOpError_Error(t *testing.T) {
	err := &OpError{Op: "get", Err: io.ErrUnexpectedEOF}
	want := "storage get: unexpected EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// KeyNotFoundError
// ---------------------------------------------------------------------------

func TestKeyNotFoundError_Error(t *testing.T) {
	err := &KeyNotFoundError{Key: []byte("test-key")}
	want := "key not found: test-key"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestKeyNotFoundError_EmptyKey(t *testing.T) {
	err := &KeyNotFoundError{Key: []byte{}}
	want := "key not found: "
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// ClosedError
// ---------------------------------------------------------------------------

func TestClosedError_Error(t *testing.T) {
	err := &ClosedError{}
	want := "storage is closed"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// BatchError
// ---------------------------------------------------------------------------

func TestBatchError_Error(t *testing.T) {
	err := &BatchError{Op: "commit", Err: io.ErrShortWrite}
	want := "storage batch commit: short write"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// IteratorError
// ---------------------------------------------------------------------------

func TestIteratorError_Error(t *testing.T) {
	err := &IteratorError{Err: io.EOF}
	want := "storage iterator: EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// SnapshotError
// ---------------------------------------------------------------------------

func TestSnapshotError_Error(t *testing.T) {
	err := &SnapshotError{Op: "create", Err: io.ErrUnexpectedEOF}
	want := "storage snapshot create: unexpected EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

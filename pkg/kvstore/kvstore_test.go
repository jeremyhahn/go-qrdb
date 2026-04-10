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

package kvstore

import (
	"errors"
	"io"
	"testing"
)

// ---------------------------------------------------------------------------
// SealedError
// ---------------------------------------------------------------------------

func TestSealedError_Message(t *testing.T) {
	err := &SealedError{}
	want := "kv store is sealed"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestSealedError_As(t *testing.T) {
	var err error = &SealedError{}
	var target *SealedError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *SealedError")
	}
}

// ---------------------------------------------------------------------------
// PutError
// ---------------------------------------------------------------------------

func TestPutError_Message(t *testing.T) {
	err := &PutError{Key: "mykey", Err: io.ErrShortWrite}
	want := "kvstore put mykey: short write"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestPutError_Unwrap(t *testing.T) {
	inner := io.ErrClosedPipe
	err := &PutError{Key: "k", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("Unwrap should allow errors.Is to match the inner error")
	}

	var target *PutError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *PutError")
	}
	if target.Key != "k" {
		t.Fatalf("Key: got %q, want %q", target.Key, "k")
	}
}

func TestPutError_EmptyKey(t *testing.T) {
	err := &PutError{Key: "", Err: io.ErrUnexpectedEOF}
	want := "kvstore put : unexpected EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// GetError
// ---------------------------------------------------------------------------

func TestGetError_Message(t *testing.T) {
	err := &GetError{Key: "fetch-key", Err: io.EOF}
	want := "kvstore get fetch-key: EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestGetError_Unwrap(t *testing.T) {
	inner := io.ErrNoProgress
	err := &GetError{Key: "k", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("Unwrap should allow errors.Is to match the inner error")
	}

	var target *GetError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *GetError")
	}
	if target.Key != "k" {
		t.Fatalf("Key: got %q, want %q", target.Key, "k")
	}
}

func TestGetError_EmptyKey(t *testing.T) {
	err := &GetError{Key: "", Err: io.ErrUnexpectedEOF}
	want := "kvstore get : unexpected EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// DeleteError
// ---------------------------------------------------------------------------

func TestDeleteError_Message(t *testing.T) {
	err := &DeleteError{Key: "del-key", Err: io.ErrShortBuffer}
	want := "kvstore delete del-key: short buffer"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestDeleteError_Unwrap(t *testing.T) {
	inner := io.ErrClosedPipe
	err := &DeleteError{Key: "k", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("Unwrap should allow errors.Is to match the inner error")
	}

	var target *DeleteError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *DeleteError")
	}
	if target.Key != "k" {
		t.Fatalf("Key: got %q, want %q", target.Key, "k")
	}
}

func TestDeleteError_EmptyKey(t *testing.T) {
	err := &DeleteError{Key: "", Err: io.ErrUnexpectedEOF}
	want := "kvstore delete : unexpected EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// ScanError
// ---------------------------------------------------------------------------

func TestScanError_Message(t *testing.T) {
	err := &ScanError{Prefix: "prefix/", Err: io.ErrUnexpectedEOF}
	want := "kvstore scan prefix/: unexpected EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestScanError_Unwrap(t *testing.T) {
	inner := io.ErrShortWrite
	err := &ScanError{Prefix: "p", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("Unwrap should allow errors.Is to match the inner error")
	}

	var target *ScanError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *ScanError")
	}
	if target.Prefix != "p" {
		t.Fatalf("Prefix: got %q, want %q", target.Prefix, "p")
	}
}

func TestScanError_EmptyPrefix(t *testing.T) {
	err := &ScanError{Prefix: "", Err: io.EOF}
	want := "kvstore scan : EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// ListError
// ---------------------------------------------------------------------------

func TestListError_Message(t *testing.T) {
	err := &ListError{Prefix: "list/", Err: io.ErrClosedPipe}
	want := "kvstore list list/: io: read/write on closed pipe"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestListError_Unwrap(t *testing.T) {
	inner := io.ErrNoProgress
	err := &ListError{Prefix: "l", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("Unwrap should allow errors.Is to match the inner error")
	}

	var target *ListError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *ListError")
	}
	if target.Prefix != "l" {
		t.Fatalf("Prefix: got %q, want %q", target.Prefix, "l")
	}
}

func TestListError_EmptyPrefix(t *testing.T) {
	err := &ListError{Prefix: "", Err: io.ErrUnexpectedEOF}
	want := "kvstore list : unexpected EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// ExistsError
// ---------------------------------------------------------------------------

func TestExistsError_Message(t *testing.T) {
	err := &ExistsError{Key: "check-key", Err: io.ErrClosedPipe}
	want := "kvstore exists check-key: io: read/write on closed pipe"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestExistsError_Unwrap(t *testing.T) {
	inner := io.ErrNoProgress
	err := &ExistsError{Key: "k", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("Unwrap should allow errors.Is to match the inner error")
	}

	var target *ExistsError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *ExistsError")
	}
	if target.Key != "k" {
		t.Fatalf("Key: got %q, want %q", target.Key, "k")
	}
}

func TestExistsError_EmptyKey(t *testing.T) {
	err := &ExistsError{Key: "", Err: io.ErrUnexpectedEOF}
	want := "kvstore exists : unexpected EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

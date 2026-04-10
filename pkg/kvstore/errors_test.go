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

package kvstore

import (
	"errors"
	"io"
	"testing"
)

// ---------------------------------------------------------------------------
// IndexQueryError
// ---------------------------------------------------------------------------

func TestIndexQueryError_Error(t *testing.T) {
	err := &IndexQueryError{IndexName: "by_name", Reason: "no results"}
	want := "kvstore index query by_name: no results"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestIndexQueryError_Error_EmptyFields(t *testing.T) {
	err := &IndexQueryError{IndexName: "", Reason: ""}
	want := "kvstore index query : "
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestIndexQueryError_As(t *testing.T) {
	var err error = &IndexQueryError{IndexName: "idx", Reason: "timeout"}
	var target *IndexQueryError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *IndexQueryError")
	}
	if target.IndexName != "idx" {
		t.Fatalf("IndexName: got %q, want %q", target.IndexName, "idx")
	}
	if target.Reason != "timeout" {
		t.Fatalf("Reason: got %q, want %q", target.Reason, "timeout")
	}
}

// ---------------------------------------------------------------------------
// IndexScanError
// ---------------------------------------------------------------------------

func TestIndexScanError_Error(t *testing.T) {
	err := &IndexScanError{IndexName: "by_age", Reason: "range invalid"}
	want := "kvstore index scan by_age: range invalid"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestIndexScanError_Error_EmptyFields(t *testing.T) {
	err := &IndexScanError{IndexName: "", Reason: ""}
	want := "kvstore index scan : "
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestIndexScanError_As(t *testing.T) {
	var err error = &IndexScanError{IndexName: "idx", Reason: "scan failed"}
	var target *IndexScanError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *IndexScanError")
	}
	if target.IndexName != "idx" {
		t.Fatalf("IndexName: got %q, want %q", target.IndexName, "idx")
	}
}

// ---------------------------------------------------------------------------
// IndexRegistrationError
// ---------------------------------------------------------------------------

func TestIndexRegistrationError_Error(t *testing.T) {
	err := &IndexRegistrationError{EntityType: "users", Err: io.ErrClosedPipe}
	want := "kvstore index registration users: io: read/write on closed pipe"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestIndexRegistrationError_Error_EmptyEntityType(t *testing.T) {
	err := &IndexRegistrationError{EntityType: "", Err: io.EOF}
	want := "kvstore index registration : EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestIndexRegistrationError_Unwrap(t *testing.T) {
	inner := io.ErrShortWrite
	err := &IndexRegistrationError{EntityType: "orders", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("Unwrap should allow errors.Is to match the inner error")
	}

	var target *IndexRegistrationError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *IndexRegistrationError")
	}
	if target.EntityType != "orders" {
		t.Fatalf("EntityType: got %q, want %q", target.EntityType, "orders")
	}
}

func TestIndexRegistrationError_Unwrap_NilSafe(t *testing.T) {
	sentinel := errors.New("custom error")
	err := &IndexRegistrationError{EntityType: "products", Err: sentinel}
	if err.Unwrap() != sentinel {
		t.Fatal("Unwrap should return the exact inner error")
	}
}

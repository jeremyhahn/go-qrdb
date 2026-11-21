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

func TestConstructionAndIndexErrors_Error(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"nil inner":       {&NilInnerStoreError{}, "kvstore: namespaced store requires a non-nil inner KVStore"},
		"empty namespace": {&EmptyNamespaceError{}, "kvstore: namespaced store requires a non-empty key prefix"},
		"unindexable": {
			&UnindexableValueError{Key: "user/1", IndexName: "by_email"},
			"kvstore: value at user/1 cannot be decoded to maintain index by_email; this store indexes JSON-encoded values only",
		},
		"conflicting index": {
			&ConflictingIndexError{IndexName: "by_email"},
			"kvstore: index by_email is already registered with a different definition",
		},
		"empty guard key": {&EmptyKeyError{Field: "guard", Index: 0}, "kvstore: guard 0 names an empty key"},
		"empty write key": {&EmptyKeyError{Field: "write", Index: 3}, "kvstore: write 3 names an empty key"},
	}
	for name, tc := range cases {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("%s: Error(): got %q, want %q", name, got, tc.want)
		}
	}
}

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

package index

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ClosedError
// ---------------------------------------------------------------------------

func TestClosedError_Error_ContainsName(t *testing.T) {
	e := &ClosedError{Name: "my-index"}
	got := e.Error()
	if !strings.Contains(got, "my-index") {
		t.Errorf("Error: expected name in message, got %q", got)
	}
	if !strings.Contains(got, "index: closed") {
		t.Errorf("Error: expected prefix, got %q", got)
	}
}

func TestClosedError_Error_EmptyName(t *testing.T) {
	e := &ClosedError{Name: ""}
	got := e.Error()
	if !strings.Contains(got, "index: closed:") {
		t.Errorf("Error: expected prefix for empty name, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// NilDBError
// ---------------------------------------------------------------------------

func TestNilDBError_Error_ContainsMessage(t *testing.T) {
	e := &NilDBError{}
	got := e.Error()
	if !strings.Contains(got, "pebble db is nil") {
		t.Errorf("Error: expected nil db message, got %q", got)
	}
}

func TestNilDBError_Error_HasPrefix(t *testing.T) {
	e := &NilDBError{}
	got := e.Error()
	if !strings.HasPrefix(got, "index:") {
		t.Errorf("Error: expected 'index:' prefix, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// EmptyNameError
// ---------------------------------------------------------------------------

func TestEmptyNameError_Error_ContainsMessage(t *testing.T) {
	e := &EmptyNameError{}
	got := e.Error()
	if !strings.Contains(got, "name must not be empty") {
		t.Errorf("Error: expected empty name message, got %q", got)
	}
}

func TestEmptyNameError_Error_HasPrefix(t *testing.T) {
	e := &EmptyNameError{}
	got := e.Error()
	if !strings.HasPrefix(got, "index:") {
		t.Errorf("Error: expected 'index:' prefix, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// NilFieldValueError
// ---------------------------------------------------------------------------

func TestNilFieldValueError_Error_ContainsOp(t *testing.T) {
	e := &NilFieldValueError{Op: "put"}
	got := e.Error()
	if !strings.Contains(got, "put") {
		t.Errorf("Error: expected op in message, got %q", got)
	}
	if !strings.Contains(got, "nil field value") {
		t.Errorf("Error: expected 'nil field value', got %q", got)
	}
}

func TestNilFieldValueError_Error_DifferentOp(t *testing.T) {
	e := &NilFieldValueError{Op: "scan start"}
	got := e.Error()
	if !strings.Contains(got, "scan start") {
		t.Errorf("Error: expected 'scan start' op, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// UniqueViolationError
// ---------------------------------------------------------------------------

func TestUniqueViolationError_Error_ContainsFields(t *testing.T) {
	e := &UniqueViolationError{
		IndexName:   "email-idx",
		FieldValue:  []byte("test@example.com"),
		ExistingKey: []byte("entity-100"),
		NewKey:      []byte("entity-200"),
	}
	got := e.Error()
	if !strings.Contains(got, "email-idx") {
		t.Errorf("Error: expected index name, got %q", got)
	}
	if !strings.Contains(got, "unique constraint violation") {
		t.Errorf("Error: expected 'unique violation', got %q", got)
	}
}

func TestUniqueViolationError_Error_DifferentKeys(t *testing.T) {
	e := &UniqueViolationError{
		IndexName:   "name-idx",
		FieldValue:  []byte("alice"),
		ExistingKey: []byte("key-1"),
		NewKey:      []byte("key-2"),
	}
	got := e.Error()
	if !strings.Contains(got, "name-idx") {
		t.Errorf("Error: expected 'name-idx', got %q", got)
	}
	if !strings.Contains(got, "unique constraint violation") {
		t.Errorf("Error: expected 'unique violation', got %q", got)
	}
}

// ---------------------------------------------------------------------------
// KeyTooShortError
// ---------------------------------------------------------------------------

func TestKeyTooShortError_Error_ContainsMessage(t *testing.T) {
	e := &KeyTooShortError{}
	got := e.Error()
	if !strings.Contains(got, "key too short") {
		t.Errorf("Error: expected 'key too short', got %q", got)
	}
}

func TestKeyTooShortError_Error_HasPrefix(t *testing.T) {
	e := &KeyTooShortError{}
	got := e.Error()
	if !strings.HasPrefix(got, "index:") {
		t.Errorf("Error: expected 'index:' prefix, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// KeyPrefixMismatchError
// ---------------------------------------------------------------------------

func TestKeyPrefixMismatchError_Error_ContainsMessage(t *testing.T) {
	e := &KeyPrefixMismatchError{}
	got := e.Error()
	if !strings.Contains(got, "key prefix does not match") {
		t.Errorf("Error: expected 'key prefix does not match', got %q", got)
	}
}

func TestKeyPrefixMismatchError_Error_HasPrefix(t *testing.T) {
	e := &KeyPrefixMismatchError{}
	got := e.Error()
	if !strings.HasPrefix(got, "index:") {
		t.Errorf("Error: expected 'index:' prefix, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// errors.As compatibility
// ---------------------------------------------------------------------------

func TestClosedError_ErrorsAs(t *testing.T) {
	var err error = &ClosedError{Name: "test"}
	var target *ClosedError
	if !errors.As(err, &target) {
		t.Error("errors.As: failed for ClosedError")
	}
	if target.Name != "test" {
		t.Errorf("errors.As: Name got %q, want %q", target.Name, "test")
	}
}

func TestUniqueViolationError_ErrorsAs(t *testing.T) {
	var err error = &UniqueViolationError{
		IndexName:   "idx",
		FieldValue:  []byte("val"),
		ExistingKey: []byte("key-10"),
		NewKey:      []byte("key-20"),
	}
	var target *UniqueViolationError
	if !errors.As(err, &target) {
		t.Error("errors.As: failed for UniqueViolationError")
	}
	if !bytes.Equal(target.ExistingKey, []byte("key-10")) {
		t.Errorf("errors.As: ExistingKey got %v, want %v", target.ExistingKey, []byte("key-10"))
	}
}

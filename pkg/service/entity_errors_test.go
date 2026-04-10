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

package service

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// InvalidEntityOpTypeError
// ---------------------------------------------------------------------------

func TestInvalidEntityOpTypeError_Message(t *testing.T) {
	err := &InvalidEntityOpTypeError{OpType: "update"}
	want := "service: invalid entity operation type: update"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestInvalidEntityOpTypeError_EmptyOpType(t *testing.T) {
	err := &InvalidEntityOpTypeError{OpType: ""}
	want := "service: invalid entity operation type: "
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestInvalidEntityOpTypeError_ImplementsError(t *testing.T) {
	err := &InvalidEntityOpTypeError{OpType: "patch"}
	var _ error = err
}

func TestInvalidEntityOpTypeError_ErrorsAs(t *testing.T) {
	var err error = &InvalidEntityOpTypeError{OpType: "merge"}

	var target *InvalidEntityOpTypeError
	if !errors.As(err, &target) {
		t.Fatalf("expected errors.As to match *InvalidEntityOpTypeError")
	}
	if target.OpType != "merge" {
		t.Fatalf("expected OpType 'merge', got %q", target.OpType)
	}
}

// ---------------------------------------------------------------------------
// BatchEntityEmptyOpsError
// ---------------------------------------------------------------------------

func TestBatchEntityEmptyOpsError_Message(t *testing.T) {
	err := &BatchEntityEmptyOpsError{}
	want := "service: batch entity request must contain at least one operation"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestBatchEntityEmptyOpsError_ImplementsError(t *testing.T) {
	err := &BatchEntityEmptyOpsError{}
	var _ error = err
}

func TestBatchEntityEmptyOpsError_ErrorsAs(t *testing.T) {
	var err error = &BatchEntityEmptyOpsError{}

	var target *BatchEntityEmptyOpsError
	if !errors.As(err, &target) {
		t.Fatalf("expected errors.As to match *BatchEntityEmptyOpsError")
	}
}

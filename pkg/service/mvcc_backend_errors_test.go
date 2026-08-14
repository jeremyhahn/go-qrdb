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
	"strings"
	"testing"
)

// fakeUnsupportedQueryTypeError is a minimal stand-in for
// pkg/engine.InvalidQueryError used to test translateReadError's duck-typed
// match without importing pkg/engine (which itself imports pkg/service).
type fakeUnsupportedQueryTypeError struct {
	unsupported bool
}

func (e *fakeUnsupportedQueryTypeError) Error() string {
	return "engine: invalid query: unsupported query type"
}

func (e *fakeUnsupportedQueryTypeError) UnsupportedQueryType() bool {
	return e.unsupported
}

func TestMVCCBackendRequiredError_Error(t *testing.T) {
	err := &MVCCBackendRequiredError{Err: errors.New("engine: invalid query: unsupported query type")}
	msg := err.Error()
	if !strings.Contains(msg, "raft.sm_type: mvcc") {
		t.Errorf("Error() = %q, want it to name the fix (raft.sm_type: mvcc)", msg)
	}
	if !strings.Contains(msg, "unsupported query type") {
		t.Errorf("Error() = %q, want it to include the underlying refusal", msg)
	}
}

func TestMVCCBackendRequiredError_ErrorNilUnderlying(t *testing.T) {
	err := &MVCCBackendRequiredError{}
	if err.Error() == "" {
		t.Error("Error() should not be empty even with a nil underlying error")
	}
}

func TestMVCCBackendRequiredError_Unwrap(t *testing.T) {
	inner := errors.New("sentinel")
	err := &MVCCBackendRequiredError{Err: inner}
	if got := err.Unwrap(); got != inner {
		t.Errorf("Unwrap() = %v, want %v", got, inner)
	}
}

func TestTranslateReadError_UnsupportedQueryType_MapsToMVCCBackendRequired(t *testing.T) {
	svc := &MVCCKVService{}
	src := &fakeUnsupportedQueryTypeError{unsupported: true}

	got := svc.translateReadError(src)

	var mbr *MVCCBackendRequiredError
	if !errors.As(got, &mbr) {
		t.Fatalf("translateReadError(%T) = %T, want *MVCCBackendRequiredError", src, got)
	}
	if mbr.Unwrap() != error(src) {
		t.Errorf("MVCCBackendRequiredError.Err = %v, want the original error", mbr.Unwrap())
	}
}

func TestTranslateReadError_OtherInvalidQuery_DoesNotMapToMVCCBackendRequired(t *testing.T) {
	svc := &MVCCKVService{}
	// A refusal that satisfies the interface but reports false — e.g. an
	// unknown index name — must fall through to the generic *ReadError, not
	// be reinterpreted as "wrong state machine".
	src := &fakeUnsupportedQueryTypeError{unsupported: false}

	got := svc.translateReadError(src)

	var mbr *MVCCBackendRequiredError
	if errors.As(got, &mbr) {
		t.Fatalf("translateReadError(%T) = %T, want it NOT to match *MVCCBackendRequiredError", src, got)
	}
	var re *ReadError
	if !errors.As(got, &re) {
		t.Fatalf("translateReadError(%T) = %T, want *ReadError", src, got)
	}
}

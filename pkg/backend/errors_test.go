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
	"strings"
	"testing"
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

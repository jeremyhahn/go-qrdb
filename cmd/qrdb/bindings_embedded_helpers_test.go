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

//go:build qrdb_embedded && !integration

// bindings_embedded_helpers_test.go covers binding helper functions that are
// gated behind the qrdb_embedded build tag (bindings_server.go).
package main

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// isPebbleLockError
// ---------------------------------------------------------------------------

func TestIsPebbleLockError_True(t *testing.T) {
	err := errors.New("open database: lock held by current process")
	if !isPebbleLockError(err) {
		t.Error("expected true for error containing 'lock held by current process'")
	}
}

func TestIsPebbleLockError_False(t *testing.T) {
	err := errors.New("some other error")
	if isPebbleLockError(err) {
		t.Error("expected false for unrelated error")
	}
}

func TestIsPebbleLockError_Nil(t *testing.T) {
	if isPebbleLockError(nil) {
		t.Error("expected false for nil error")
	}
}

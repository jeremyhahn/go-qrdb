//go:build !qrdb_sql && !integration

// Copyright 2026 Jeremy Hahn
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

package main

import (
	"errors"
	"testing"
)

// newSQLStubBinding creates a SQLBinding backed by a no-connection
// ConnectionBinding (the stub always returns FeatureDisabledError).
func newSQLStubBinding(t *testing.T) *SQLBinding {
	t.Helper()
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	return NewSQLBinding(log, conn)
}

func TestSQLStub_NewSQLBinding(t *testing.T) {
	b := newSQLStubBinding(t)
	if b == nil {
		t.Fatal("expected non-nil SQLBinding")
	}
}

func TestSQLStub_Execute(t *testing.T) {
	b := newSQLStubBinding(t)

	result, err := b.Execute("SELECT 1", "testdb")
	if result != nil {
		t.Fatalf("expected nil result, got %v", result)
	}
	if err == nil {
		t.Fatal("expected FeatureDisabledError, got nil")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "sql" {
		t.Fatalf("expected feature %q, got %q", "sql", fde.Feature)
	}
}

//go:build !integration

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

package main

import (
	"testing"
)

// TestSetManager_NilManager verifies that SetManager(nil) sets the manager
// field without panicking.
func TestSetManager_NilManager(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	// Should not panic.
	conn.SetManager(nil)
	if conn.manager != nil {
		t.Fatal("expected manager to be nil after SetManager(nil)")
	}
}

// TestSetManager_NonNilManager verifies that SetManager with a real manager
// stores the reference.
func TestSetManager_NonNilManager(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	mgr := NewConnectionManagerBinding(testLogger(t))

	conn.SetManager(mgr)

	if conn.manager == nil {
		t.Fatal("expected non-nil manager after SetManager")
	}
	if conn.manager != mgr {
		t.Fatal("expected manager field to equal the provided manager")
	}
}

// TestSetManager_ReplacesExisting verifies that a subsequent SetManager call
// replaces the previous one.
func TestSetManager_ReplacesExisting(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))

	mgr1 := NewConnectionManagerBinding(testLogger(t))
	conn.SetManager(mgr1)

	mgr2 := NewConnectionManagerBinding(testLogger(t))
	conn.SetManager(mgr2)

	if conn.manager != mgr2 {
		t.Fatal("expected manager to be replaced by second SetManager call")
	}
}

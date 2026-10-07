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

package database

import (
	"context"
	"testing"
)

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Unserved closes when the database is sealed and is replaced by an open
// signal when it is unsealed: a stream opened before the seal ends, and one
// opened after the unseal is not ended by the earlier seal.
func TestDatabase_UnservedFollowsSealAndUnseal(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = m.Close() })
	db, err := m.Create(DatabaseConfig{Name: "app", Backend: BackendMemory})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	before := db.Unserved()
	if closed(before) {
		t.Fatal("an open database reports itself unserved")
	}
	db.SetStatus(StatusSealed)
	if !closed(before) {
		t.Fatal("sealing did not end the signal a stream holds")
	}
	if !closed(db.Unserved()) {
		t.Fatal("a sealed database hands out an open signal")
	}
	db.SetStatus(StatusSealed) // idempotent: closing twice must not panic
	db.SetStatus(StatusOpen)
	after := db.Unserved()
	if closed(after) {
		t.Fatal("an unsealed database hands out a closed signal")
	}
	if !closed(before) {
		t.Fatal("unsealing reopened a signal a stream already saw end")
	}
}

// A drop closes the database, which ends every stream on it.
func TestDatabase_UnservedClosesOnDrop(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = m.Close() })
	db, err := m.Create(DatabaseConfig{Name: "app", Backend: BackendMemory})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	signal := db.Unserved()
	if err := m.Drop(context.Background(), "app"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if !closed(signal) {
		t.Fatal("dropping the database did not end the signal a stream holds")
	}
}

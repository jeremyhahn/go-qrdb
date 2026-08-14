// Copyright (c) 2026 Jeremy Hahn
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

package pebble

import (
	"testing"

	rawpebble "github.com/cockroachdb/pebble/v2"
)

// ---------------------------------------------------------------------------
// NewFromDB / DB
// ---------------------------------------------------------------------------

// TestNewFromDB_RoundTrip verifies that NewFromDB wraps the given *pebble.DB
// and that DB() returns the same instance.
func TestNewFromDB_RoundTrip(t *testing.T) {
	// Open a real pebble.DB in a temp directory so the test stays fully
	// in-memory from the OS perspective (temp dir is cleaned up).
	dir := t.TempDir()
	db, err := rawpebble.Open(dir, &rawpebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s := NewFromDB(db, true)
	if s == nil {
		t.Fatal("NewFromDB returned nil")
	}

	got := s.DB()
	if got != db {
		t.Fatalf("DB(): got %p, want %p", got, db)
	}
}

// TestNewFromDB_SyncWritesFlag verifies that the syncWrites value is reflected
// in the resulting config so callers that depend on the flag get consistent behaviour.
func TestNewFromDB_SyncWritesFlag(t *testing.T) {
	dir := t.TempDir()
	db, err := rawpebble.Open(dir, &rawpebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	sSync := NewFromDB(db, true)
	if !sSync.config.SyncWrites {
		t.Fatal("SyncWrites should be true")
	}

	sNoSync := NewFromDB(db, false)
	if sNoSync.config.SyncWrites {
		t.Fatal("SyncWrites should be false")
	}
}

// ---------------------------------------------------------------------------
// Iterator.Error — it.err branch (57.1%)
// ---------------------------------------------------------------------------

// TestIterator_Error_WithStoredErr exercises the it.err != nil branch of
// pebbleIterator.Error by directly setting the internal error field on a
// freshly constructed iterator before calling Error().
func TestIterator_Error_WithStoredErr(t *testing.T) {
	s := openTestStorage(t)

	// Obtain a real iterator so we have a properly initialised pebbleIterator.
	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	// Cast to the concrete type so we can inject an error (white-box test
	// within the same package).
	pi, ok := iter.(*pebbleIterator)
	if !ok {
		t.Fatalf("expected *pebbleIterator, got %T", iter)
	}

	sentinel := &testSentinelError{}
	pi.err = sentinel

	err := pi.Error()
	if err == nil {
		t.Fatal("Error() should return non-nil when it.err is set")
	}
}

// testSentinelError is a minimal error used as an injection target.
type testSentinelError struct{}

func (e *testSentinelError) Error() string { return "sentinel iterator error" }

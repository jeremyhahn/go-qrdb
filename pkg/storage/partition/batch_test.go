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

package partition

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

func TestBatch_SetAndCommit(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	batch := m.Batch()

	// Write multiple keys that will be spread across partitions.
	keys := []string{"alpha", "bravo", "charlie", "delta"}
	for _, k := range keys {
		if err := batch.Set([]byte(k), []byte("val-"+k)); err != nil {
			t.Fatalf("batch.Set(%q) failed: %v", k, err)
		}
	}

	// Before commit, keys should not be visible.
	for _, k := range keys {
		got, err := m.Get([]byte(k))
		if err != nil {
			t.Fatalf("Get(%q) failed: %v", k, err)
		}
		if got != nil {
			t.Errorf("Get(%q) before commit = %q, want nil", k, got)
		}
	}

	// Commit
	if err := batch.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// After commit, keys should be visible.
	for _, k := range keys {
		got, err := m.Get([]byte(k))
		if err != nil {
			t.Fatalf("Get(%q) failed: %v", k, err)
		}
		expected := "val-" + k
		if !bytes.Equal(got, []byte(expected)) {
			t.Errorf("Get(%q) = %q, want %q", k, got, expected)
		}
	}
}

func TestBatch_DeleteAndCommit(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	// Pre-populate.
	if err := m.Set([]byte("key"), []byte("val")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	batch := m.Batch()
	if err := batch.Delete([]byte("key")); err != nil {
		t.Fatalf("batch.Delete failed: %v", err)
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	got, err := m.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got != nil {
		t.Errorf("Get after batch delete = %q, want nil", got)
	}
}

func TestBatch_Rollback(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	batch := m.Batch()
	if err := batch.Set([]byte("key"), []byte("val")); err != nil {
		t.Fatalf("batch.Set failed: %v", err)
	}

	if err := batch.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	// Key should not exist after rollback.
	got, err := m.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got != nil {
		t.Errorf("Get after rollback = %q, want nil", got)
	}
}

func TestBatch_Close(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	batch := m.Batch()
	if err := batch.Set([]byte("key"), []byte("val")); err != nil {
		t.Fatalf("batch.Set failed: %v", err)
	}

	if err := batch.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Operations after close should error.
	var closedErr *storage.ClosedError
	if err := batch.Set([]byte("x"), []byte("y")); !errors.As(err, &closedErr) {
		t.Errorf("Set after close: got %v, want ClosedError", err)
	}
}

func TestBatch_DoubleClose(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	batch := m.Batch()
	if err := batch.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if err := batch.Close(); err != nil {
		t.Fatalf("second Close should be no-op: %v", err)
	}
}

func TestBatch_ClosedOperations(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	batch := m.Batch()
	if err := batch.Close(); err != nil {
		t.Errorf("batch.Close: %v", err)
	}

	var batchErr *storage.BatchError

	if err := batch.Set([]byte("k"), []byte("v")); !errors.As(err, &batchErr) {
		t.Errorf("Set on closed batch: got %v, want BatchError", err)
	}
	if err := batch.Delete([]byte("k")); !errors.As(err, &batchErr) {
		t.Errorf("Delete on closed batch: got %v, want BatchError", err)
	}
	if err := batch.Commit(); !errors.As(err, &batchErr) {
		t.Errorf("Commit on closed batch: got %v, want BatchError", err)
	}
	if err := batch.Rollback(); !errors.As(err, &batchErr) {
		t.Errorf("Rollback on closed batch: got %v, want BatchError", err)
	}
}

func TestBatch_ManyKeys(t *testing.T) {
	m := newTestManager(t, 8)
	defer func() { _ = m.Close() }()

	const count = 500
	batch := m.Batch()

	for i := 0; i < count; i++ {
		key := []byte(fmt.Sprintf("key-%04d", i))
		val := []byte(fmt.Sprintf("val-%04d", i))
		if err := batch.Set(key, val); err != nil {
			t.Fatalf("batch.Set(%q) failed: %v", key, err)
		}
	}

	if err := batch.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Verify all keys.
	for i := 0; i < count; i++ {
		key := []byte(fmt.Sprintf("key-%04d", i))
		expected := []byte(fmt.Sprintf("val-%04d", i))
		got, err := m.Get(key)
		if err != nil {
			t.Fatalf("Get(%q) failed: %v", key, err)
		}
		if !bytes.Equal(got, expected) {
			t.Errorf("Get(%q) = %q, want %q", key, got, expected)
		}
	}
}

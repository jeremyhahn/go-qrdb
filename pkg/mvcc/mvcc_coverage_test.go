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

package mvcc

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ---------------------------------------------------------------------------
// NewRaftStorage — compacted revision loaded from storage (71.4%)
// ---------------------------------------------------------------------------

// TestNewRaftStorage_WithPersistedCompactedRevision verifies the else-branch
// in NewRaftStorage that decodes a previously persisted compacted revision.
func TestNewRaftStorage_WithPersistedCompactedRevision(t *testing.T) {
	store := newMemStorage()

	// Pre-seed a valid compacted revision so NewRaftStorage decodes it.
	rev := int64(42)
	if err := store.Set(CompactedKey(), EncodeRevision(rev)); err != nil {
		t.Fatalf("seed CompactedKey: %v", err)
	}

	s, err := NewRaftStorage(&RaftStorageConfig{
		Store:  store,
		Logger: newTestLogger(t),
	})
	if err != nil {
		t.Fatalf("NewRaftStorage: %v", err)
	}
	if got := s.CompactedRevision(); got != rev {
		t.Errorf("CompactedRevision: got %d, want %d", got, rev)
	}
}

// TestNewRaftStorage_CorruptCompactedRevision verifies the error path when
// the stored compacted revision bytes are invalid (cannot be decoded).
func TestNewRaftStorage_CorruptCompactedRevision(t *testing.T) {
	store := newMemStorage()

	// Store garbage bytes at the compacted key.
	if err := store.Set(CompactedKey(), []byte("not-a-revision")); err != nil {
		t.Fatalf("seed garbage: %v", err)
	}

	_, err := NewRaftStorage(&RaftStorageConfig{
		Store:  store,
		Logger: newTestLogger(t),
	})
	if err == nil {
		t.Fatal("expected error for corrupt compacted revision")
	}
}

// ---------------------------------------------------------------------------
// RaftStorage.Close — storage-error path (71.4%)
// ---------------------------------------------------------------------------

// errOnCloseStorage wraps memStorage and returns an error from Close.
type errOnCloseStorage struct {
	*memStorage
	closeErr error
}

func (e *errOnCloseStorage) Close() error {
	return e.closeErr
}

// TestRaftStorage_Close_StorageError verifies that Close surfaces the error
// returned by the underlying storage when it fails to close.
func TestRaftStorage_Close_StorageError(t *testing.T) {
	inner := newMemStorage()
	cs := &errOnCloseStorage{
		memStorage: inner,
		closeErr:   errors.New(errors.ErrStorage, "errOnCloseStorage.Close", "close failed"),
	}

	s, err := NewRaftStorage(&RaftStorageConfig{
		Store:  cs,
		Logger: newTestLogger(t),
	})
	if err != nil {
		t.Fatalf("NewRaftStorage: %v", err)
	}

	// RaftStorage.Close should propagate the storage error.
	if err := s.Close(); err == nil {
		t.Fatal("expected error when underlying storage Close fails")
	}
}

// ---------------------------------------------------------------------------
// executeDelete — key not found branch (73.7%)
// ---------------------------------------------------------------------------

// TestRaftTransaction_ExecuteDelete_KeyNotFound exercises executeDelete when
// the key being deleted does not exist, which returns nil, nil (no-op).
func TestRaftTransaction_ExecuteDelete_KeyNotFound(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	// Use SetSuccess to stage a delete for a non-existent key.
	if err := txn.SetSuccess([]RaftTxnOp{
		{Type: RaftTxnOpDelete, Key: []byte("nonexistent")},
	}); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	// Delete of a non-existent key is a successful no-op.
	if !result.Succeeded {
		t.Fatal("expected transaction to succeed")
	}
}

// ---------------------------------------------------------------------------
// newRaftTransaction — snapshot failure (83.3%)
// ---------------------------------------------------------------------------

// closedSnapshotStorage wraps memStorage and forces Snapshot() to fail.
type closedSnapshotStorage struct {
	*memStorage
}

func (c *closedSnapshotStorage) Snapshot() (storage.Snapshot, error) {
	return nil, errors.New(errors.ErrStorage, "closedSnapshotStorage.Snapshot", "closed")
}

// TestNewRaftTransaction_SnapshotError verifies that BeginRaftTxn propagates a
// snapshot failure from the underlying storage.
func TestNewRaftTransaction_SnapshotError(t *testing.T) {
	inner := newMemStorage()
	cs := &closedSnapshotStorage{inner}

	s, err := NewRaftStorage(&RaftStorageConfig{
		Store:  cs,
		Logger: newTestLogger(t),
	})
	if err != nil {
		t.Fatalf("NewRaftStorage: %v", err)
	}

	_, err = s.BeginRaftTxn(context.Background())
	if err == nil {
		t.Fatal("expected error when snapshot fails")
	}
}

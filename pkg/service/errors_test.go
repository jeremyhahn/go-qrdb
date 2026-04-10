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
// RevisionCompactedError
// ---------------------------------------------------------------------------

func TestRevisionCompactedError_Message(t *testing.T) {
	err := &RevisionCompactedError{Requested: 5, Compacted: 10}
	want := "service: revision 5 has been compacted (earliest available: 10)"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestRevisionCompactedError_ErrorsAs(t *testing.T) {
	var err error = &RevisionCompactedError{Requested: 3, Compacted: 7}

	var target *RevisionCompactedError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *RevisionCompactedError")
	}
	if target.Requested != 3 || target.Compacted != 7 {
		t.Fatalf("expected Requested=3, Compacted=7, got %d, %d", target.Requested, target.Compacted)
	}
}

// ---------------------------------------------------------------------------
// RevisionNotFoundError
// ---------------------------------------------------------------------------

func TestRevisionNotFoundError_Message(t *testing.T) {
	err := &RevisionNotFoundError{Revision: 99}
	want := "service: revision not found: 99"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestRevisionNotFoundError_ErrorsAs(t *testing.T) {
	var err error = &RevisionNotFoundError{Revision: 42}

	var target *RevisionNotFoundError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *RevisionNotFoundError")
	}
	if target.Revision != 42 {
		t.Fatalf("expected Revision=42, got %d", target.Revision)
	}
}

// ---------------------------------------------------------------------------
// TransactionConflictError
// ---------------------------------------------------------------------------

func TestTransactionConflictError_Message(t *testing.T) {
	err := &TransactionConflictError{Key: "config/db"}
	want := "service: transaction conflict on key: config/db"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestTransactionConflictError_ErrorsAs(t *testing.T) {
	var err error = &TransactionConflictError{Key: "x"}

	var target *TransactionConflictError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *TransactionConflictError")
	}
	if target.Key != "x" {
		t.Fatalf("expected Key 'x', got %q", target.Key)
	}
}

// ---------------------------------------------------------------------------
// WatchCanceledError
// ---------------------------------------------------------------------------

func TestWatchCanceledError_Message(t *testing.T) {
	err := &WatchCanceledError{Reason: "compaction past watcher revision"}
	want := "service: watch canceled: compaction past watcher revision"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestWatchCanceledError_ErrorsAs(t *testing.T) {
	var err error = &WatchCanceledError{Reason: "shutdown"}

	var target *WatchCanceledError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *WatchCanceledError")
	}
	if target.Reason != "shutdown" {
		t.Fatalf("expected Reason 'shutdown', got %q", target.Reason)
	}
}

// ---------------------------------------------------------------------------
// LeaseNotFoundError
// ---------------------------------------------------------------------------

func TestLeaseNotFoundError_Message(t *testing.T) {
	err := &LeaseNotFoundError{LeaseID: 100}
	want := "service: lease not found: 100"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestLeaseNotFoundError_ErrorsAs(t *testing.T) {
	var err error = &LeaseNotFoundError{LeaseID: 55}

	var target *LeaseNotFoundError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *LeaseNotFoundError")
	}
	if target.LeaseID != 55 {
		t.Fatalf("expected LeaseID=55, got %d", target.LeaseID)
	}
}

// ---------------------------------------------------------------------------
// LeaseExpiredError
// ---------------------------------------------------------------------------

func TestLeaseExpiredError_Message(t *testing.T) {
	err := &LeaseExpiredError{LeaseID: 200}
	want := "service: lease expired: 200"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestLeaseExpiredError_ErrorsAs(t *testing.T) {
	var err error = &LeaseExpiredError{LeaseID: 88}

	var target *LeaseExpiredError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *LeaseExpiredError")
	}
	if target.LeaseID != 88 {
		t.Fatalf("expected LeaseID=88, got %d", target.LeaseID)
	}
}

// ---------------------------------------------------------------------------
// LeaseExistsError
// ---------------------------------------------------------------------------

func TestLeaseExistsError_Message(t *testing.T) {
	err := &LeaseExistsError{LeaseID: 300}
	want := "service: lease already exists: 300"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestLeaseExistsError_ErrorsAs(t *testing.T) {
	var err error = &LeaseExistsError{LeaseID: 77}

	var target *LeaseExistsError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *LeaseExistsError")
	}
	if target.LeaseID != 77 {
		t.Fatalf("expected LeaseID=77, got %d", target.LeaseID)
	}
}

// ---------------------------------------------------------------------------
// BatchError
// ---------------------------------------------------------------------------

func TestBatchError_Message(t *testing.T) {
	inner := &KeyNotFoundError{Key: "missing-key"}
	err := &BatchError{Index: 3, Err: inner}
	want := "service: batch operation 3 failed: service: key not found: missing-key"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestBatchError_Unwrap(t *testing.T) {
	inner := &KeyNotFoundError{Key: "k1"}
	err := &BatchError{Index: 0, Err: inner}

	unwrapped := err.Unwrap()
	if unwrapped != inner {
		t.Fatalf("expected Unwrap to return inner error, got %v", unwrapped)
	}

	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatal("expected errors.As to traverse through BatchError.Unwrap")
	}
	if knf.Key != "k1" {
		t.Fatalf("expected Key 'k1', got %q", knf.Key)
	}
}

// ---------------------------------------------------------------------------
// IndexNotFoundError
// ---------------------------------------------------------------------------

func TestIndexNotFoundError_Message(t *testing.T) {
	err := &IndexNotFoundError{Name: "idx_users_email"}
	want := "service: index not found: idx_users_email"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestIndexNotFoundError_ErrorsAs(t *testing.T) {
	var err error = &IndexNotFoundError{Name: "idx"}

	var target *IndexNotFoundError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *IndexNotFoundError")
	}
	if target.Name != "idx" {
		t.Fatalf("expected Name 'idx', got %q", target.Name)
	}
}

// ---------------------------------------------------------------------------
// IndexExistsError
// ---------------------------------------------------------------------------

func TestIndexExistsError_Message(t *testing.T) {
	err := &IndexExistsError{Name: "idx_products_sku"}
	want := "service: index already exists: idx_products_sku"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestIndexExistsError_ErrorsAs(t *testing.T) {
	var err error = &IndexExistsError{Name: "dup"}

	var target *IndexExistsError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *IndexExistsError")
	}
	if target.Name != "dup" {
		t.Fatalf("expected Name 'dup', got %q", target.Name)
	}
}

// ---------------------------------------------------------------------------
// TimeSeriesError
// ---------------------------------------------------------------------------

func TestTimeSeriesError_Message(t *testing.T) {
	inner := errors.New("bucket overflow")
	err := &TimeSeriesError{Op: "insert", Err: inner}
	want := "service: timeseries insert failed: bucket overflow"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestTimeSeriesError_Unwrap(t *testing.T) {
	inner := errors.New("retention expired")
	err := &TimeSeriesError{Op: "query", Err: inner}

	unwrapped := err.Unwrap()
	if unwrapped != inner {
		t.Fatalf("expected Unwrap to return inner error, got %v", unwrapped)
	}
	if !errors.Is(err, inner) {
		t.Fatal("expected errors.Is to match inner error through Unwrap")
	}
}

// ---------------------------------------------------------------------------
// ConsistencyError
// ---------------------------------------------------------------------------

func TestConsistencyError_Message(t *testing.T) {
	err := &ConsistencyError{Level: "linearizable", Reason: "leader unavailable"}
	want := "service: consistency linearizable: leader unavailable"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestConsistencyError_ErrorsAs(t *testing.T) {
	var err error = &ConsistencyError{Level: "serializable", Reason: "timeout"}

	var target *ConsistencyError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *ConsistencyError")
	}
	if target.Level != "serializable" {
		t.Fatalf("expected Level 'serializable', got %q", target.Level)
	}
	if target.Reason != "timeout" {
		t.Fatalf("expected Reason 'timeout', got %q", target.Reason)
	}
}

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
	"errors"
	"io"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// KeyNotFoundError
// ---------------------------------------------------------------------------

func TestKeyNotFoundError_Error_ContainsKey(t *testing.T) {
	e := &KeyNotFoundError{Key: "mykey"}
	got := e.Error()
	if !strings.Contains(got, "mykey") {
		t.Errorf("Error: expected key in message, got %q", got)
	}
	if !strings.Contains(got, "mvcc: key not found") {
		t.Errorf("Error: expected prefix, got %q", got)
	}
}

func TestKeyNotFoundError_Error_EmptyKey(t *testing.T) {
	e := &KeyNotFoundError{Key: ""}
	got := e.Error()
	if !strings.Contains(got, "mvcc: key not found:") {
		t.Errorf("Error: expected prefix for empty key, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// RevisionCompactedError
// ---------------------------------------------------------------------------

func TestRevisionCompactedError_Error_ContainsRevisions(t *testing.T) {
	e := &RevisionCompactedError{Requested: 5, Compacted: 10}
	got := e.Error()
	if !strings.Contains(got, "5") {
		t.Errorf("Error: expected requested revision, got %q", got)
	}
	if !strings.Contains(got, "10") {
		t.Errorf("Error: expected compacted revision, got %q", got)
	}
	if !strings.Contains(got, "compacted") {
		t.Errorf("Error: expected 'compacted' in message, got %q", got)
	}
}

func TestRevisionCompactedError_Error_ZeroRevisions(t *testing.T) {
	e := &RevisionCompactedError{Requested: 0, Compacted: 0}
	got := e.Error()
	if !strings.Contains(got, "0") {
		t.Errorf("Error: expected 0 in message, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// RevisionNotFoundError
// ---------------------------------------------------------------------------

func TestRevisionNotFoundError_Error_ContainsFields(t *testing.T) {
	e := &RevisionNotFoundError{Key: "foo", Revision: 42}
	got := e.Error()
	if !strings.Contains(got, "foo") {
		t.Errorf("Error: expected key, got %q", got)
	}
	if !strings.Contains(got, "42") {
		t.Errorf("Error: expected revision, got %q", got)
	}
	if !strings.Contains(got, "not found") {
		t.Errorf("Error: expected 'not found', got %q", got)
	}
}

func TestRevisionNotFoundError_Error_EmptyKeyZeroRevision(t *testing.T) {
	e := &RevisionNotFoundError{Key: "", Revision: 0}
	got := e.Error()
	if !strings.Contains(got, "revision 0") {
		t.Errorf("Error: expected 'revision 0', got %q", got)
	}
}

// ---------------------------------------------------------------------------
// StoreError
// ---------------------------------------------------------------------------

func TestStoreError_Error_ContainsOpAndErr(t *testing.T) {
	inner := errors.New("disk full")
	e := &StoreError{Op: "put data", Err: inner}
	got := e.Error()
	if !strings.Contains(got, "put data") {
		t.Errorf("Error: expected op, got %q", got)
	}
	if !strings.Contains(got, "disk full") {
		t.Errorf("Error: expected inner error, got %q", got)
	}
	if !strings.Contains(got, "failed") {
		t.Errorf("Error: expected 'failed', got %q", got)
	}
}

func TestStoreError_Error_DifferentOp(t *testing.T) {
	inner := errors.New("timeout")
	e := &StoreError{Op: "get meta", Err: inner}
	got := e.Error()
	if !strings.Contains(got, "get meta") {
		t.Errorf("Error: expected op 'get meta', got %q", got)
	}
	if !strings.Contains(got, "timeout") {
		t.Errorf("Error: expected inner error 'timeout', got %q", got)
	}
}

func TestStoreError_Unwrap_ReturnsInnerError(t *testing.T) {
	inner := io.ErrUnexpectedEOF
	e := &StoreError{Op: "load revision", Err: inner}
	if !errors.Is(e, io.ErrUnexpectedEOF) {
		t.Error("Unwrap: errors.Is failed for inner error")
	}
}

func TestStoreError_Unwrap_MatchesAs(t *testing.T) {
	inner := errors.New("wrapped")
	e := &StoreError{Op: "test", Err: inner}
	var target *StoreError
	if !errors.As(e, &target) {
		t.Error("Unwrap: errors.As failed for StoreError")
	}
	if target.Op != "test" {
		t.Errorf("Unwrap: Op got %q, want %q", target.Op, "test")
	}
}

// ---------------------------------------------------------------------------
// EncodingError
// ---------------------------------------------------------------------------

func TestEncodingError_Error_ContainsReason(t *testing.T) {
	e := &EncodingError{Reason: "data key too short"}
	got := e.Error()
	if !strings.Contains(got, "data key too short") {
		t.Errorf("Error: expected reason, got %q", got)
	}
	if !strings.Contains(got, "encoding error") {
		t.Errorf("Error: expected prefix, got %q", got)
	}
}

func TestEncodingError_Error_EmptyReason(t *testing.T) {
	e := &EncodingError{Reason: ""}
	got := e.Error()
	if !strings.Contains(got, "mvcc: encoding error:") {
		t.Errorf("Error: expected prefix for empty reason, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// CompactionError
// ---------------------------------------------------------------------------

func TestCompactionError_Error_ContainsInnerErr(t *testing.T) {
	inner := errors.New("batch too large")
	e := &CompactionError{Err: inner}
	got := e.Error()
	if !strings.Contains(got, "batch too large") {
		t.Errorf("Error: expected inner error, got %q", got)
	}
	if !strings.Contains(got, "compaction failed") {
		t.Errorf("Error: expected prefix, got %q", got)
	}
}

func TestCompactionError_Error_DifferentInnerErr(t *testing.T) {
	inner := errors.New("permission denied")
	e := &CompactionError{Err: inner}
	got := e.Error()
	if !strings.Contains(got, "permission denied") {
		t.Errorf("Error: expected 'permission denied', got %q", got)
	}
}

func TestCompactionError_Unwrap_ReturnsInnerError(t *testing.T) {
	inner := io.EOF
	e := &CompactionError{Err: inner}
	if !errors.Is(e, io.EOF) {
		t.Error("Unwrap: errors.Is failed for inner error")
	}
}

func TestCompactionError_Unwrap_MatchesAs(t *testing.T) {
	inner := errors.New("inner")
	e := &CompactionError{Err: inner}
	var target *CompactionError
	if !errors.As(e, &target) {
		t.Error("Unwrap: errors.As failed for CompactionError")
	}
}

// ---------------------------------------------------------------------------
// Cross-package error interface methods
// ---------------------------------------------------------------------------

func TestKeyNotFoundError_KeyNotFound_ReturnsKey(t *testing.T) {
	e := &KeyNotFoundError{Key: "missing-key"}
	if got := e.KeyNotFound(); got != "missing-key" {
		t.Errorf("KeyNotFound: got %q, want %q", got, "missing-key")
	}
}

func TestKeyNotFoundError_KeyNotFound_EmptyKey(t *testing.T) {
	e := &KeyNotFoundError{Key: ""}
	if got := e.KeyNotFound(); got != "" {
		t.Errorf("KeyNotFound: got %q, want empty", got)
	}
}

func TestRevisionCompactedError_RevisionCompacted_ReturnsFields(t *testing.T) {
	e := &RevisionCompactedError{Requested: 3, Compacted: 10}
	requested, compacted := e.RevisionCompacted()
	if requested != 3 {
		t.Errorf("RevisionCompacted: requested got %d, want 3", requested)
	}
	if compacted != 10 {
		t.Errorf("RevisionCompacted: compacted got %d, want 10", compacted)
	}
}

func TestRevisionCompactedError_RevisionCompacted_ZeroValues(t *testing.T) {
	e := &RevisionCompactedError{Requested: 0, Compacted: 0}
	requested, compacted := e.RevisionCompacted()
	if requested != 0 || compacted != 0 {
		t.Errorf("RevisionCompacted: got (%d, %d), want (0, 0)", requested, compacted)
	}
}

func TestRevisionNotFoundError_RevisionNotFound_ReturnsFields(t *testing.T) {
	e := &RevisionNotFoundError{Key: "mykey", Revision: 42}
	key, revision := e.RevisionNotFound()
	if key != "mykey" {
		t.Errorf("RevisionNotFound: key got %q, want %q", key, "mykey")
	}
	if revision != 42 {
		t.Errorf("RevisionNotFound: revision got %d, want 42", revision)
	}
}

func TestRevisionNotFoundError_RevisionNotFound_EmptyKeyZeroRevision(t *testing.T) {
	e := &RevisionNotFoundError{Key: "", Revision: 0}
	key, revision := e.RevisionNotFound()
	if key != "" {
		t.Errorf("RevisionNotFound: key got %q, want empty", key)
	}
	if revision != 0 {
		t.Errorf("RevisionNotFound: revision got %d, want 0", revision)
	}
}

func TestCompactRevisionInvalidError_Error_ContainsRevision(t *testing.T) {
	got := (&CompactRevisionInvalidError{Revision: -7}).Error()
	if want := "mvcc: compaction revision -7 is invalid (must be at least 1)"; got != want {
		t.Errorf("Error = %q, want %q", got, want)
	}
}

func TestCompactRevisionInvalidError_ReturnedForRevisionZero(t *testing.T) {
	s := newTestStore(t)
	err := s.CompactPrefix(nil, 0)
	var invalid *CompactRevisionInvalidError
	if !errors.As(err, &invalid) || invalid.Revision != 0 {
		t.Fatalf("CompactPrefix(0) err = %v, want *CompactRevisionInvalidError{Revision: 0}", err)
	}
	if !strings.Contains(err.Error(), "revision 0 is invalid") {
		t.Errorf("Error = %q, want it to name revision 0", err.Error())
	}
}

func TestCompactRevisionFutureError_Error_ContainsBothRevisions(t *testing.T) {
	got := (&CompactRevisionFutureError{Requested: 12, Current: 5}).Error()
	if want := "mvcc: compaction revision 12 is a future revision (current: 5)"; got != want {
		t.Errorf("Error = %q, want %q", got, want)
	}
}

func TestCompactRevisionFutureError_ReturnedPastCurrent(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Put([]byte("k"), []byte("v"), 0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	batch := s.Storage().Batch()
	defer func() { _ = batch.Close() }()
	err := s.CompactPrefixInBatch(batch, nil, 9)
	var future *CompactRevisionFutureError
	if !errors.As(err, &future) || future.Requested != 9 || future.Current != 1 {
		t.Fatalf("CompactPrefixInBatch(9) err = %v, want *CompactRevisionFutureError{Requested: 9, Current: 1}", err)
	}
	if !strings.Contains(err.Error(), "(current: 1)") {
		t.Errorf("Error = %q, want it to name the current revision", err.Error())
	}
}

func TestUnsupportedSortTargetError_Error_ContainsTarget(t *testing.T) {
	got := (&UnsupportedSortTargetError{Target: 42}).Error()
	if want := "mvcc: unsupported sort target: 42"; got != want {
		t.Errorf("Error = %q, want %q", got, want)
	}
}

func TestUnsupportedSortTargetError_Error_ZeroTarget(t *testing.T) {
	got := (&UnsupportedSortTargetError{}).Error()
	if !strings.HasSuffix(got, ": 0") {
		t.Errorf("Error = %q, want the zero target named", got)
	}
}

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
	"testing"
)

// TestCommittedRevision_TrailsAnOpenBatch: the apply loop advances the
// revision counter entry by entry inside an open batch, and those revisions
// are not readable until the batch commits. CommittedRevision must not name
// them until the apply loop marks the batch committed.
func TestCommittedRevision_TrailsAnOpenBatch(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Put([]byte("a"), []byte("committed"), 0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	batch := s.Storage().Batch()
	defer func() { _ = batch.Close() }()
	if _, err := s.PutInBatch(batch, []byte("b"), []byte("staged"), 0, 2); err != nil {
		t.Fatalf("PutInBatch: %v", err)
	}
	s.SetRevision(2) // as the apply loop advances it entry by entry

	if got := s.CurrentRevision(); got != 2 {
		t.Fatalf("CurrentRevision = %d, want 2", got)
	}
	if got := s.CommittedRevision(); got != 1 {
		t.Fatalf("CommittedRevision with revision 2 uncommitted = %d, want 1", got)
	}

	if err := batch.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	s.MarkCommitted(2)
	if got := s.CommittedRevision(); got != 2 {
		t.Fatalf("CommittedRevision after MarkCommitted(2) = %d, want 2", got)
	}
}

// TestRange_AnswersAtTheCommittedRevision is the #746 defect: Range read
// committed storage but reported, and defaulted its read revision to, the
// apply counter an open batch had already advanced. A watch resumed from
// that revision + 1 skipped the batch's changes once they committed.
func TestRange_AnswersAtTheCommittedRevision(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Put([]byte("a"), []byte("committed"), 0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	batch := s.Storage().Batch()
	defer func() { _ = batch.Close() }()
	if _, err := s.PutInBatch(batch, []byte("b"), []byte("staged"), 0, 2); err != nil {
		t.Fatalf("PutInBatch: %v", err)
	}
	s.SetRevision(2)

	kvs, count, rev, err := s.Range([]byte("a"), []byte("c"))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if rev != 1 {
		t.Fatalf("Range revision with revision 2 uncommitted = %d, want 1", rev)
	}
	if count != 1 || len(kvs) != 1 || string(kvs[0].Key) != "a" {
		t.Fatalf("Range = %d keys (count %d), want only the committed key a", len(kvs), count)
	}

	if err := batch.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	s.MarkCommitted(2)
	kvs, _, rev, err = s.Range([]byte("a"), []byte("c"))
	if err != nil {
		t.Fatalf("Range after commit: %v", err)
	}
	if rev != 2 || len(kvs) != 2 {
		t.Fatalf("Range after commit = rev %d, %d keys; want rev 2, 2 keys", rev, len(kvs))
	}
}

// TestRangeInBatch_AnswersAtTheApplyCounter pins the other half: a read inside
// the batch sees the batch's own writes and reports the revision they carry.
func TestRangeInBatch_AnswersAtTheApplyCounter(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Put([]byte("a"), []byte("committed"), 0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	batch := s.Storage().Batch()
	defer func() { _ = batch.Close() }()
	if _, err := s.PutInBatch(batch, []byte("b"), []byte("staged"), 0, 2); err != nil {
		t.Fatalf("PutInBatch: %v", err)
	}
	s.SetRevision(2)

	kvs, _, rev, err := s.RangeInBatch(batch, []byte("a"), []byte("c"))
	if err != nil {
		t.Fatalf("RangeInBatch: %v", err)
	}
	if rev != 2 || len(kvs) != 2 {
		t.Fatalf("RangeInBatch = rev %d, %d keys; want rev 2, 2 keys", rev, len(kvs))
	}
}

// TestCommittedRevision_DirectWritesAdvanceIt: Put and Delete commit their
// own batch, so each leaves CommittedRevision at the revision it wrote.
func TestCommittedRevision_DirectWritesAdvanceIt(t *testing.T) {
	s := newTestStore(t)
	kv, err := s.Put([]byte("k"), []byte("v"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := s.CommittedRevision(); got != kv.ModRevision {
		t.Fatalf("CommittedRevision after Put = %d, want %d", got, kv.ModRevision)
	}
	if _, err := s.Delete([]byte("k")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, want := s.CommittedRevision(), s.CurrentRevision(); got != want || got != kv.ModRevision+1 {
		t.Fatalf("CommittedRevision after Delete = %d, want %d", got, kv.ModRevision+1)
	}
}

// TestCommittedRevision_LoadedOnOpen: a store opened over existing storage
// starts with every persisted revision committed, since the revision counter
// is only ever persisted inside a batch that committed.
func TestCommittedRevision_LoadedOnOpen(t *testing.T) {
	s := newTestStore(t)
	for _, v := range []string{"1", "2", "3"} {
		if _, err := s.Put([]byte("k"), []byte(v), 0); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	reopened, err := New(s.Storage())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := reopened.CommittedRevision(); got != 3 {
		t.Fatalf("CommittedRevision on reopen = %d, want 3", got)
	}
}

// TestCommittedRevision_ZeroOnAFreshStore: nothing has committed yet.
func TestCommittedRevision_ZeroOnAFreshStore(t *testing.T) {
	if got := newTestStore(t).CommittedRevision(); got != 0 {
		t.Fatalf("CommittedRevision on a fresh store = %d, want 0", got)
	}
}

// TestMarkCommitted_NeverMovesBackward: a stale mark cannot regress the
// watermark below a revision already reported committed.
func TestMarkCommitted_NeverMovesBackward(t *testing.T) {
	s := newTestStore(t)
	s.MarkCommitted(5)
	s.MarkCommitted(3)
	if got := s.CommittedRevision(); got != 5 {
		t.Fatalf("CommittedRevision after MarkCommitted(5), MarkCommitted(3) = %d, want 5", got)
	}
}

// TestMarkCommitted_AdvancesMonotonically: successive marks raise the
// watermark to each newer revision.
func TestMarkCommitted_AdvancesMonotonically(t *testing.T) {
	s := newTestStore(t)
	for _, rev := range []int64{1, 2, 7} {
		s.MarkCommitted(rev)
		if got := s.CommittedRevision(); got != rev {
			t.Fatalf("CommittedRevision after MarkCommitted(%d) = %d", rev, got)
		}
	}
}

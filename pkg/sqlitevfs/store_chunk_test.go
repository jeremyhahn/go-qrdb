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

package sqlitevfs

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// liveKeys is the live keys of app.db under prefix at the backend's newest
// revision.
func liveKeys(t *testing.T, b *MVCCBackend, prefix []byte) [][]byte {
	t.Helper()
	keys, err := b.Keys(t.Context(), prefix, revOf(t, b))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// A removal of a database with more pages than one request can carry
// deletes them in requests under the transport cap and leaves no key of
// the database.
func TestRemovalChunksDeletesUnderTheCap(t *testing.T) {
	b := newTestBackend(t)
	if _, err := b.Commit(t.Context(), nil, map[string][]byte{"other/1": []byte("x")}, nil); err != nil {
		t.Fatal(err)
	}
	absent := revOf(t, b)
	rec := &requestRecorder{MVCCBackend: b}
	s := seedTruncatable(t, rec)
	*rec = requestRecorder{MVCCBackend: b}

	if _, err := s.RestoreToRevision(t.Context(), absent); err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if rec.maxEst >= truncCap || rec.maxJSON >= truncCap {
		t.Fatalf("a request of %d bytes (JSON %d) reached the cap %d", rec.maxEst, rec.maxJSON, truncCap)
	}
	if rec.commits < 3 {
		t.Fatalf("%d commits: the removal's deletes were not split", rec.commits)
	}
	if keys := liveKeys(t, b, []byte(keyRoot+"app.db/")); len(keys) != 0 {
		t.Fatalf("%d keys of app.db left after its removal", len(keys))
	}
}

// An optimistic transaction whose read checks alone would reach the
// transport cap is refused with ErrReadSetTooLarge before anything is sent,
// since its checks must travel with its write; a smaller read set commits.
func TestOptimisticReadSetOverTheCapIsRefused(t *testing.T) {
	b := newTestBackend(t)
	rec := &requestRecorder{MVCCBackend: b}
	s := newOptimisticStore(t, rec)
	fills := make([]byte, truncPages)
	for i := range fills {
		fills[i] = byte(i%250 + 1)
	}
	seed(t, s, fills...)
	s.requestBytes = truncCap
	*rec = requestRecorder{MVCCBackend: b}
	before := revOf(t, b)

	txn := begin(t, s)
	for pgno := int64(1); pgno <= truncPages; pgno++ {
		readPage(t, txn, pgno)
	}
	writePage(t, txn, 1, 0x7f)
	if _, err := txn.Commit(t.Context()); !errors.Is(err, ErrReadSetTooLarge) {
		t.Fatalf("Commit = %v, want ErrReadSetTooLarge", err)
	}
	if rec.commits != 0 || rec.publishes != 0 {
		t.Fatalf("%d commits, %d publishes sent for a refused read set, want none", rec.commits, rec.publishes)
	}
	if rev := revOf(t, b); rev != before {
		t.Fatalf("backend at revision %d after the refusal, want %d", rev, before)
	}

	small := begin(t, s)
	for pgno := int64(1); pgno <= 16; pgno++ {
		readPage(t, small, pgno)
	}
	writePage(t, small, 1, 0x7f)
	if _, err := small.Commit(t.Context()); err != nil {
		t.Fatalf("Commit of a read set under the cap = %v", err)
	}
	if rec.maxEst >= truncCap || rec.maxJSON >= truncCap {
		t.Fatalf("a request of %d bytes (JSON %d) reached the cap %d", rec.maxEst, rec.maxJSON, truncCap)
	}
	if got := readPage(t, begin(t, s), 1); got[0] != 0x7f {
		t.Fatalf("page 1 begins %#x after the commit, want 0x7f", got[0])
	}
}

// failPublish refuses every publish, so a staged commit fails after its
// staging commits and cleans its stage keys up.
type failPublish struct {
	*requestRecorder
}

var errPublishRefused = errors.New("publish refused")

func (f failPublish) Publish(context.Context, []byte, []byte, int64, []ReadCheck, map[string][]byte, [][]byte) (int64, error) {
	f.publishes++
	return 0, errPublishRefused
}

// A staged commit that fails at publish deletes more stage keys than one
// request can carry in requests under the transport cap, and leaves none.
func TestFailedStageCleanupChunksDeletesUnderTheCap(t *testing.T) {
	b := newTestBackend(t)
	rec := &requestRecorder{MVCCBackend: b}
	s := seedTruncatable(t, failPublish{rec})
	*rec = requestRecorder{MVCCBackend: b}
	s.stageThreshold = 0

	txn := beginReserved(t, s)
	fills := make([]byte, truncPages)
	for i := range fills {
		fills[i] = 0x7f
	}
	writePages(t, txn, fills...)
	if _, err := txn.Commit(t.Context()); !errors.Is(err, errPublishRefused) {
		t.Fatalf("Commit = %v, want the refused publish", err)
	}
	if rec.publishes != 1 {
		t.Fatalf("%d publishes, want 1", rec.publishes)
	}
	if rec.maxEst >= truncCap || rec.maxJSON >= truncCap {
		t.Fatalf("a request of %d bytes (JSON %d) reached the cap %d", rec.maxEst, rec.maxJSON, truncCap)
	}
	root, err := StageRoot("app.db")
	if err != nil {
		t.Fatal(err)
	}
	if keys := liveKeys(t, b, root); len(keys) != 0 {
		t.Fatalf("%d stage keys left after the failed commit", len(keys))
	}
}

// sweepStageKeys is more abandoned stage keys than one delete request under
// truncCap carries.
const sweepStageKeys = 600

// abandonStage writes n stage keys of one commit of app.db straight to b,
// as a writer that crashed after staging leaves them.
func abandonStage(t *testing.T, b *MVCCBackend, n int) {
	t.Helper()
	commitID, err := newCommitID(false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	stageUnder(t, b, commitID, n)
}

// stageUnder writes n stage keys of commit commitID of app.db straight to b.
func stageUnder(t *testing.T, b *MVCCBackend, commitID string, n int) {
	t.Helper()
	puts := make(map[string][]byte, n)
	for pgno := 1; pgno <= n; pgno++ {
		key, err := StageKey("app.db", commitID, uint32(pgno))
		if err != nil {
			t.Fatal(err)
		}
		puts[string(key)] = []byte{0xEE}
	}
	if _, err := b.Commit(t.Context(), nil, puts, nil); err != nil {
		t.Fatal(err)
	}
}

// hookAfterStage runs hook once, after the first staging Commit (stage-key
// puts only) it is sent succeeds.
type hookAfterStage struct {
	*MVCCBackend
	hook func()
	ran  bool
}

func (b *hookAfterStage) Commit(ctx context.Context, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (int64, error) {
	rev, err := b.MVCCBackend.Commit(ctx, reads, puts, deletes)
	if err != nil || b.ran || len(puts) == 0 {
		return rev, err
	}
	for key := range puts {
		if !strings.HasPrefix(key, keyRoot+"app.db"+stageSegment) {
			return rev, err
		}
	}
	b.ran = true
	b.hook()
	return rev, err
}

// TestSerializedSweepKeepsLiveOptimisticStage checks that a serialized
// writer's Reserve between an optimistic commit's staging chunks leaves that
// commit's stage keys, so it publishes; and that once the stage is past the
// optimistic stage age the sweep takes it and the commit is the retryable
// ErrSnapshotChanged.
func TestSerializedSweepKeepsLiveOptimisticStage(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "in flight", true: "expired"}[expired], func(t *testing.T) {
			b := newTestBackend(t)
			hb := &hookAfterStage{MVCCBackend: b}
			opt := newOptimisticStore(t, hb)
			ser := newLockedStore(t, b, newMemLocker(), "s")
			if expired {
				ser.now = func() time.Time { return time.Now().Add(time.Hour) }
			}
			seeded := beginReserved(t, ser)
			writePages(t, seeded, 0x01, 0x02)
			commit(t, seeded)
			root, err := StageRoot("app.db")
			if err != nil {
				t.Fatal(err)
			}
			hb.hook = func() {
				staged := len(liveKeys(t, b, root))
				txn := beginReserved(t, ser)
				left := len(liveKeys(t, b, root))
				if err := txn.Rollback(t.Context()); err != nil {
					t.Fatalf("Rollback: %v", err)
				}
				if want := map[bool]int{false: staged, true: 0}[expired]; staged == 0 || left != want {
					t.Fatalf("%d stage keys before the serialized Reserve, %d after, want %d", staged, left, want)
				}
			}
			w := begin(t, opt)
			writePages(t, w, 0x11, 0x12, 0x13)
			_, err = w.Commit(t.Context())
			if !hb.ran {
				t.Fatal("the optimistic commit did not stage")
			}
			if expired {
				if !errors.Is(err, ErrSnapshotChanged) || !errors.Is(err, ErrPublishConflict) {
					t.Fatalf("Commit after its stage was swept = %v, want ErrSnapshotChanged wrapping ErrPublishConflict", err)
				}
				if got := readAll(t, begin(t, opt)); !bytes.Equal(got, pagesOf(0x01, 0x02)) {
					t.Fatal("a refused optimistic commit changed the pages")
				}
				return
			}
			if err != nil {
				t.Fatalf("Commit: %v", err)
			}
			if got := readAll(t, begin(t, opt)); !bytes.Equal(got, pagesOf(0x11, 0x12, 0x13)) {
				t.Fatal("the optimistic commit is not visible")
			}
		})
	}
}

// TestSweepSelectsStagesByModeAndAge checks which stages a serialized
// Reserve sweeps: every serialized stage, an optimistic stage past the
// optimistic stage age or dated past it ahead, and a stage whose id records
// no mode (an older random id read as far-future); never an optimistic
// stage within the age.
func TestSweepSelectsStagesByModeAndAge(t *testing.T) {
	b := newTestBackend(t)
	ser := newLockedStore(t, b, newMemLocker(), "s")
	now := time.Now()
	ser.now = func() time.Time { return now }
	id := func(optimistic bool, created time.Time) string {
		t.Helper()
		id, err := newCommitID(optimistic, created)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	live := id(true, now.Add(-time.Minute))
	for _, swept := range []string{
		id(false, now),
		id(true, now.Add(-DefaultOptimisticStageAge-time.Second)),
		id(true, now.Add(DefaultOptimisticStageAge+time.Second)),
		"ffffffffffffffff0123456789abcdef",
	} {
		stageUnder(t, b, swept, 2)
	}
	stageUnder(t, b, live, 2)
	root, err := StageRoot("app.db")
	if err != nil {
		t.Fatal(err)
	}
	// A key under the stage root too short to carry a commit id.
	if _, err := b.Commit(t.Context(), nil, map[string][]byte{string(root) + "junk": {1}}, nil); err != nil {
		t.Fatal(err)
	}
	txn := beginReserved(t, ser)
	if err := txn.Rollback(t.Context()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	prefix, err := StagePrefix("app.db", live)
	if err != nil {
		t.Fatal(err)
	}
	keys := liveKeys(t, b, root)
	if len(keys) != 2 {
		t.Fatalf("stage keys after the sweep = %q, want the 2 of the live optimistic stage", keys)
	}
	for _, key := range keys {
		if !bytes.HasPrefix(key, prefix) {
			t.Fatalf("stage key %q left after the sweep is not of the live optimistic stage", key)
		}
	}
	if _, err := NewStore(b, "app.db", WithOptimisticStageAge(0)); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("WithOptimisticStageAge(0) err = %v, want ErrInvalidOption", err)
	}
}

// failNthDelete fails the n-th delete-only Commit it is sent.
type failNthDelete struct {
	*requestRecorder
	n, deletes int
}

func (b *failNthDelete) Commit(ctx context.Context, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (int64, error) {
	if len(puts) == 0 && len(deletes) > 0 {
		b.deletes++
		if b.deletes == b.n {
			return 0, errInjected
		}
	}
	return b.requestRecorder.Commit(ctx, reads, puts, deletes)
}

// TestReserveSweepsAbandonedStagesInChunks checks that a stage with more
// keys than one request carries is swept by Reserve in delete requests each
// under the cap, and that a failed chunk fails Reserve and releases the lock.
func TestReserveSweepsAbandonedStagesInChunks(t *testing.T) {
	b := newTestBackend(t)
	rec := &requestRecorder{MVCCBackend: b}
	s := newLockedStore(t, rec, newMemLocker(), "a")
	s.requestBytes = truncCap
	abandonStage(t, b, sweepStageKeys)
	root, err := StageRoot("app.db")
	if err != nil {
		t.Fatal(err)
	}

	txn := beginReserved(t, s)
	if rec.commits < 2 {
		t.Fatalf("%d commits: the sweep was not split", rec.commits)
	}
	if rec.maxEst >= truncCap || rec.maxJSON >= truncCap {
		t.Fatalf("a request of %d bytes (JSON %d) reached the cap %d", rec.maxEst, rec.maxJSON, truncCap)
	}
	if keys := liveKeys(t, b, root); len(keys) != 0 {
		t.Fatalf("%d stage keys left after the sweep", len(keys))
	}
	writePages(t, txn, 0x21)
	commit(t, txn)

	t.Run("failed chunk", func(t *testing.T) {
		b := newTestBackend(t)
		fb := &failNthDelete{requestRecorder: &requestRecorder{MVCCBackend: b}, n: 2}
		locker := newMemLocker()
		s := newLockedStore(t, fb, locker, "a")
		s.requestBytes = truncCap
		abandonStage(t, b, sweepStageKeys)
		txn := begin(t, s)
		if err := txn.Reserve(t.Context()); !errors.Is(err, errInjected) {
			t.Fatalf("Reserve err = %v, want errInjected", err)
		}
		if l, err := locker.Status(t.Context(), s.lockKey); err != nil || l != nil {
			t.Fatalf("lock after a failed sweep = %v, %v, want released", l, err)
		}
		if keys := liveKeys(t, b, root); len(keys) == 0 || len(keys) == sweepStageKeys {
			t.Fatalf("%d stage keys left, want the first chunk swept and the rest live", len(keys))
		}
	})
}

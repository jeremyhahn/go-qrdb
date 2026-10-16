// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package lease

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// raftLog numbers the entries a test applies, the way a Raft group's log does:
// every replica applies each entry at the same index, and the indexes only
// grow.
type raftLog struct {
	last atomic.Uint64
}

// next appends an entry and tells every lessor in lrs it is applying it,
// returning its index.
func (g *raftLog) next(lrs ...*Lessor) uint64 {
	index := g.last.Add(1)
	for _, lr := range lrs {
		lr.Applying(index)
	}
	return index
}

// keepAlive applies a replicated renewal of lease id on every lessor in lrs as
// the log's next entry, returning its index.
func (g *raftLog) keepAlive(t *testing.T, w Writer, id int64, lrs ...*Lessor) uint64 {
	t.Helper()
	index := g.next(lrs...)
	for _, lr := range lrs {
		if _, err := lr.KeepAlive(w, id); err != nil {
			t.Fatalf("KeepAlive(%d) at %d: %v", id, index, err)
		}
	}
	return index
}

// startRecording applies, as the log's next entry, the empty checkpoint that
// sets the group's renewal marker on every lessor in lrs -- what the first
// checkpoint a group applies does -- so a test's next checkpoint is judged by
// the renewal rule alone.
func (g *raftLog) startRecording(t *testing.T, w Writer, lrs ...*Lessor) uint64 {
	t.Helper()
	index := g.next(lrs...)
	for _, lr := range lrs {
		if err := lr.ApplyCheckpoints(w, 0, 0, nil); err != nil {
			t.Fatalf("ApplyCheckpoints(marker) at %d: %v", index, err)
		}
	}
	return index
}

// checkpointNow applies cps on lr as a checkpoint no renewal superseded: as the
// log's next entry after lr's last, sampled at the entry before it, and after
// the entry that starts recording renewals when lr has not applied one yet.
func checkpointNow(lr *Lessor, w Writer, cps []Checkpoint) error {
	index := lr.applying.Load() + 1
	if lr.recordFrom.Load() == 0 {
		lr.Applying(index)
		if err := lr.ApplyCheckpoints(w, 0, 0, nil); err != nil {
			return err
		}
		index++
	}
	lr.Applying(index)
	return lr.ApplyCheckpoints(w, 0, lr.sampledIndex(), cps)
}

// applyingCheckpointer is a Checkpointer that does what the log does with a
// checkpoint: every lessor in apply applies it, in order, as the log's next
// entry, before Checkpoint returns. beforeApply runs ahead of the first apply, which is how a test
// lands a replicated renewal between a checkpoint's proposal and its apply.
// await, when set, is AwaitApplied: what the new leader's log still has to
// apply, and whether it can yet.
type applyingCheckpointer struct {
	mu          sync.Mutex
	apply       []*Lessor
	writer      Writer
	disabled    atomic.Bool
	err         error
	rounds      []recordedCheckpoint
	beforeApply func()
	proposed    chan []Checkpoint
	await       func() error
	log         *raftLog
}

// recordedCheckpoint is one Checkpoint call.
type recordedCheckpoint struct {
	token uint64
	index uint64
	cps   []Checkpoint
}

var _ Checkpointer = (*applyingCheckpointer)(nil)

func newApplyingCheckpointer(apply ...*Lessor) *applyingCheckpointer {
	return &applyingCheckpointer{apply: apply, writer: nopWriter{}, log: &raftLog{}}
}

func (c *applyingCheckpointer) CheckpointEnabled() bool { return !c.disabled.Load() }

func (c *applyingCheckpointer) Checkpoint(_ context.Context, token, index uint64, cps []Checkpoint) error {
	c.mu.Lock()
	c.rounds = append(c.rounds, recordedCheckpoint{token: token, index: index, cps: slices.Clone(cps)})
	err, before := c.err, c.beforeApply
	c.beforeApply = nil
	c.mu.Unlock()
	if c.proposed != nil {
		c.proposed <- slices.Clone(cps)
	}
	if err != nil {
		return err
	}
	if before != nil {
		before()
	}
	c.log.next(c.apply...)
	for _, lr := range c.apply {
		if aerr := lr.ApplyCheckpoints(c.writer, token, index, cps); aerr != nil {
			return aerr
		}
	}
	return nil
}

// startRecording applies the checkpoint that starts the group recording
// renewals on every lessor the double applies to (see raftLog.startRecording).
func (c *applyingCheckpointer) startRecording(t *testing.T) {
	t.Helper()
	c.log.startRecording(t, c.writer, c.apply...)
}

func (c *applyingCheckpointer) AwaitApplied(context.Context) error {
	c.mu.Lock()
	await := c.await
	c.mu.Unlock()
	if await == nil {
		return nil
	}
	return await()
}

func (c *applyingCheckpointer) recorded() []recordedCheckpoint {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.rounds)
}

// checkpointTTLOf returns lease id's replicated checkpoint.
func checkpointTTLOf(t *testing.T, lr *Lessor, id int64) int64 {
	t.Helper()
	lr.mu.RLock()
	l, ok := lr.leases[id]
	lr.mu.RUnlock()
	if !ok {
		t.Fatalf("lease %d is not held", id)
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.checkpointTTL
}

// runRound runs one checkpoint round on lr the way RunExpiry does.
func runRound(lr *Lessor, cp Checkpointer, interval time.Duration, all bool) error {
	return lr.checkpointRound(context.Background(), expiryConfig{checkpointInterval: interval, checkpointer: cp}, all, nil)
}

func TestCheckpointRound_ProposesOnlyLeasesThatOutliveTheInterval(t *testing.T) {
	clk := newFakeClock()
	var leading atomic.Bool
	leading.Store(true)
	lr := leaderLessor(t, clk, &leading)
	short, _ := lr.Grant(nopWriter{}, 300)
	long, _ := lr.Grant(nopWriter{}, 3600)
	cp := newApplyingCheckpointer(lr)
	cp.startRecording(t)

	clk.Advance(100 * time.Second)
	if err := runRound(lr, cp, 5*time.Minute, false); err != nil {
		t.Fatal(err)
	}
	rounds := cp.recorded()
	if len(rounds) != 1 || !slices.Equal(rounds[0].cps, []Checkpoint{{ID: long.ID, RemainingTTL: 3500}}) {
		t.Fatalf("proposed %v, want only lease %d at 3500s (lease %d cannot outlive 5m)", rounds, long.ID, short.ID)
	}
	if rounds[0].token == 0 {
		t.Fatal("a checkpoint round carried the zero token")
	}
	if got := checkpointTTLOf(t, lr, long.ID); got != 3500 {
		t.Fatalf("checkpoint applied = %d, want 3500", got)
	}
}

func TestCheckpointRound_StopsOnceTheCheckpointIsInsideTheInterval(t *testing.T) {
	clk := newFakeClock()
	var leading atomic.Bool
	leading.Store(true)
	lr := leaderLessor(t, clk, &leading)
	l, _ := lr.Grant(nopWriter{}, 400)
	cp := newApplyingCheckpointer(lr)
	cp.startRecording(t)

	clk.Advance(200 * time.Second)
	if err := runRound(lr, cp, 5*time.Minute, false); err != nil {
		t.Fatal(err)
	}
	if err := runRound(lr, cp, 5*time.Minute, false); err != nil {
		t.Fatal(err)
	}
	if rounds := cp.recorded(); len(rounds) != 1 {
		t.Fatalf("rounds = %v, want one: a 200s checkpoint cannot outlive 5m", rounds)
	}
	if got := checkpointTTLOf(t, lr, l.ID); got != 200 {
		t.Fatalf("checkpoint = %d, want 200", got)
	}
}

func TestCheckpointRound_AllCoversEveryLiveLease(t *testing.T) {
	clk := newFakeClock()
	var leading atomic.Bool
	leading.Store(true)
	lr := leaderLessor(t, clk, &leading)
	a, _ := lr.Grant(nopWriter{}, 10)
	b, _ := lr.Grant(nopWriter{}, 20)
	expired, _ := lr.Grant(nopWriter{}, 1)
	cp := newApplyingCheckpointer(lr)
	cp.startRecording(t)

	clk.Advance(2500 * time.Millisecond)
	if err := runRound(lr, cp, 5*time.Minute, true); err != nil {
		t.Fatal(err)
	}
	rounds := cp.recorded()
	if len(rounds) != 1 {
		t.Fatalf("rounds = %v, want one", rounds)
	}
	got := slices.Clone(rounds[0].cps)
	slices.SortFunc(got, func(x, y Checkpoint) int { return int(x.ID - y.ID) })
	want := []Checkpoint{{ID: a.ID, RemainingTTL: 8}, {ID: b.ID, RemainingTTL: 18}} // 7.5s and 17.5s, rounded up
	if !slices.Equal(got, want) {
		t.Fatalf("proposed %v, want %v (lease %d has expired)", got, want, expired.ID)
	}
}

func TestCheckpointRound_DisabledProposesAndMarksNothing(t *testing.T) {
	clk := newFakeClock()
	var leading atomic.Bool
	leading.Store(true)
	lr := leaderLessor(t, clk, &leading)
	l, _ := lr.Grant(nopWriter{}, 3600)
	cp := newApplyingCheckpointer(lr)
	cp.disabled.Store(true)

	var cde *CheckpointsDisabledError
	if err := runRound(lr, cp, time.Minute, true); !errors.As(err, &cde) {
		t.Fatalf("round with checkpoints disabled = %v, want *CheckpointsDisabledError", err)
	}
	if rounds := cp.recorded(); len(rounds) != 0 {
		t.Fatalf("proposed %v while disabled", rounds)
	}
	if _, err := lr.RenewLocal(l.ID); err != nil {
		t.Fatalf("RenewLocal after a disabled round: %v", err)
	}
}

func TestCheckpointRound_FailedProposalKeepsTheLeasePendingAndRetries(t *testing.T) {
	clk := newFakeClock()
	var leading atomic.Bool
	leading.Store(true)
	lr := leaderLessor(t, clk, &leading)
	l, _ := lr.Grant(nopWriter{}, 600)
	cp := newApplyingCheckpointer(lr)
	cp.startRecording(t)
	cp.err = errors.New("not leader")

	if err := runRound(lr, cp, 5*time.Minute, false); !errors.Is(err, cp.err) {
		t.Fatalf("round error = %v, want the proposal's", err)
	}
	var ce *CheckpointedError
	if _, err := lr.RenewLocal(l.ID); !errors.As(err, &ce) || ce.ID != l.ID {
		t.Fatalf("RenewLocal with a checkpoint unapplied = %v, want *CheckpointedError{%d}", err, l.ID)
	}

	// The retry proposes it again even though, at 100s left, the lease would
	// no longer be due on its own.
	cp.err = nil
	clk.Advance(500 * time.Second)
	if err := runRound(lr, cp, 5*time.Minute, false); err != nil {
		t.Fatal(err)
	}
	rounds := cp.recorded()
	if len(rounds) != 2 || !slices.Equal(rounds[1].cps, []Checkpoint{{ID: l.ID, RemainingTTL: 100}}) {
		t.Fatalf("rounds = %v, want the retry to carry lease %d at 100s", rounds, l.ID)
	}
	if got := checkpointTTLOf(t, lr, l.ID); got != 100 {
		t.Fatalf("checkpoint = %d, want 100", got)
	}
}

// A replicated renewal that applies after a checkpoint is sampled and before
// it applies supersedes it: the checkpoint records the TTL from before the
// renewal, so every replica rejects it and the renewal's TTL stands (#681).
func TestCheckpointRound_ARenewalAfterTheSampleRejectsTheCheckpoint(t *testing.T) {
	clk := newFakeClock()
	var leading atomic.Bool
	leading.Store(true)
	lr := leaderLessor(t, clk, &leading)
	follower := NewLessor(WithClock(clk))
	l, _ := lr.Grant(nopWriter{}, 600)
	if _, err := follower.Grant(nopWriter{}, 600); err != nil {
		t.Fatal(err)
	}
	cp := newApplyingCheckpointer(lr, follower)
	cp.startRecording(t)

	clk.Advance(500 * time.Second)
	var renewal uint64
	cp.beforeApply = func() { renewal = cp.log.keepAlive(t, nopWriter{}, l.ID, lr, follower) }
	if err := runRound(lr, cp, 5*time.Minute, true); err != nil {
		t.Fatal(err)
	}
	rounds := cp.recorded()
	if len(rounds) != 1 || !slices.Equal(rounds[0].cps, []Checkpoint{{ID: l.ID, RemainingTTL: 100}}) {
		t.Fatalf("rounds = %v, want the one 100s checkpoint of lease %d", rounds, l.ID)
	}
	if rounds[0].index >= renewal {
		t.Fatalf("checkpoint sampled at %d, want below the renewal at %d", rounds[0].index, renewal)
	}
	for _, r := range []*Lessor{lr, follower} {
		if got := checkpointTTLOf(t, r, l.ID); got != 0 {
			t.Fatalf("checkpoint after the superseding renewal = %d, want it rejected", got)
		}
	}
	follower.Promote(0)
	if got := follower.Lookup(l.ID).RemainingTTL; got != 600 {
		t.Fatalf("promoted follower remaining = %d, want the renewal's 600", got)
	}
	if _, err := lr.RenewLocal(l.ID); err != nil {
		t.Fatalf("RenewLocal after the rejected checkpoint: %v", err)
	}
}

// The group's first checkpoint only starts it recording renewals, so the
// round that proposes it samples its leases again once it has applied and
// proposes them a second time: that one applies on every replica.
func TestCheckpointRound_TheFirstRoundProposesAgainOnceRecording(t *testing.T) {
	clk := newFakeClock()
	var leading atomic.Bool
	leading.Store(true)
	lr := leaderLessor(t, clk, &leading)
	follower := NewLessor(WithClock(clk))
	l, _ := lr.Grant(nopWriter{}, 600)
	if _, err := follower.Grant(nopWriter{}, 600); err != nil {
		t.Fatal(err)
	}
	cp := newApplyingCheckpointer(lr, follower)

	clk.Advance(500 * time.Second)
	if err := runRound(lr, cp, 5*time.Minute, true); err != nil {
		t.Fatal(err)
	}
	rounds := cp.recorded()
	if len(rounds) != 2 {
		t.Fatalf("proposed %d checkpoints, want the marker's and its re-sample", len(rounds))
	}
	if rounds[1].index <= rounds[0].index {
		t.Fatalf("re-sample at %d, want after the first at %d applied", rounds[1].index, rounds[0].index)
	}
	for _, r := range []*Lessor{lr, follower} {
		if got := checkpointTTLOf(t, r, l.ID); got != 100 {
			t.Fatalf("checkpoint after the first round = %d, want 100", got)
		}
	}
	if err := runRound(lr, cp, 5*time.Minute, true); err != nil {
		t.Fatal(err)
	}
	if got := len(cp.recorded()); got != 3 {
		t.Fatalf("a recording group's round proposed %d more, want 1", got-2)
	}
}

// supersededLog is the log of the #681 finding as entries 1..4: a 600s grant,
// the checkpoint that starts the group recording renewals, a replicated
// renewal, and a 100s checkpoint the leader sampled before that renewal.
var supersededLog = []func(lr *Lessor, w Writer) error{
	func(lr *Lessor, w Writer) error { _, err := lr.Grant(w, 600); return err },
	func(lr *Lessor, w Writer) error { return lr.ApplyCheckpoints(w, 0, 0, nil) },
	func(lr *Lessor, w Writer) error { _, err := lr.KeepAlive(w, 1); return err },
	func(lr *Lessor, w Writer) error {
		return lr.ApplyCheckpoints(w, 0, 2, []Checkpoint{{ID: 1, RemainingTTL: 100}})
	},
}

// replaySuperseded applies supersededLog's entries from..to-1 to lr through w,
// each at its own index.
func replaySuperseded(t *testing.T, lr *Lessor, w Writer, from, to int) {
	t.Helper()
	for i := from; i < to; i++ {
		lr.Applying(uint64(i + 1))
		if err := supersededLog[i](lr, w); err != nil {
			t.Fatalf("entry %d: %v", i+1, err)
		}
	}
}

// A replica that restarts, recovers from a snapshot taken before the
// checkpoint and replays it, or replays the whole log counts the lease from
// the renewal's 600s, never the superseded checkpoint's 100s.
func TestCheckpoint_ARestartKeepsTheRenewalsTTL(t *testing.T) {
	restartRemaining := func(t *testing.T, st *memory.MemoryStorage) int64 {
		t.Helper()
		restarted := NewLessor(WithClock(newFakeClock()))
		if err := restarted.Restore(st); err != nil {
			t.Fatalf("Restore: %v", err)
		}
		return restarted.Lookup(1).RemainingTTL
	}

	t.Run("restart after the checkpoint", func(t *testing.T) {
		st := memory.NewStorage()
		replaySuperseded(t, NewLessor(WithClock(newFakeClock())), st, 0, len(supersededLog))
		if v, _ := st.Get(checkpointRecordKey(1)); v != nil {
			t.Fatal("the superseded checkpoint was recorded")
		}
		if v, _ := st.Get(renewalRecordKey(1)); !bytes.Equal(v, encodeIndex(3)) {
			t.Fatalf("renewal record = %x, want index 3", v)
		}
		if v, _ := st.Get([]byte(recordFromKey)); !bytes.Equal(v, encodeIndex(2)) {
			t.Fatalf("renewal marker = %x, want index 2", v)
		}
		if got := restartRemaining(t, st); got != 600 {
			t.Fatalf("restarted remaining = %d, want 600", got)
		}
	})

	t.Run("snapshot before the checkpoint then replay", func(t *testing.T) {
		st := memory.NewStorage()
		replaySuperseded(t, NewLessor(WithClock(newFakeClock())), st, 0, 3)
		recovered := NewLessor(WithClock(newFakeClock()))
		if err := recovered.Restore(st); err != nil {
			t.Fatal(err)
		}
		replaySuperseded(t, recovered, st, 3, len(supersededLog))
		if got := checkpointTTLOf(t, recovered, 1); got != 0 {
			t.Fatalf("recovered replica applied the superseded checkpoint: %d", got)
		}
		if got := restartRemaining(t, st); got != 600 {
			t.Fatalf("restarted remaining = %d, want 600", got)
		}
	})

	t.Run("replay of the whole log", func(t *testing.T) {
		live, replayed := memory.NewStorage(), memory.NewStorage()
		replaySuperseded(t, NewLessor(WithClock(newFakeClock())), live, 0, len(supersededLog))
		replaySuperseded(t, NewLessor(WithClock(newFakeClock())), replayed, 0, len(supersededLog))
		for _, k := range [][]byte{renewalRecordKey(1), []byte(recordFromKey), checkpointRecordKey(1)} {
			a, _ := live.Get(k)
			b, _ := replayed.Get(k)
			if !bytes.Equal(a, b) {
				t.Fatalf("record %q: live %x, replayed %x", k, a, b)
			}
		}
		if got := restartRemaining(t, replayed); got != 600 {
			t.Fatalf("replayed remaining = %d, want 600", got)
		}
	})
}

// A checkpoint sampled at or after the lease's last renewal applies; one
// sampled before the group's renewal marker does not, and neither does any
// entry of the checkpoint that sets the marker.
func TestApplyCheckpoints_JudgesTheSampleAgainstRenewalAndMarker(t *testing.T) {
	st := memory.NewStorage()
	lr := NewLessor()
	g := &raftLog{}
	g.next(lr)
	l, _ := lr.Grant(st, 600)

	sampled := lr.sampledIndex()
	marker := g.next(lr)
	if err := lr.ApplyCheckpoints(st, 0, sampled, []Checkpoint{{ID: l.ID, RemainingTTL: 50}}); err != nil {
		t.Fatal(err)
	}
	if got := checkpointTTLOf(t, lr, l.ID); got != 0 {
		t.Fatalf("the marker's own checkpoint applied: %d", got)
	}

	renewal := g.keepAlive(t, st, l.ID, lr)
	if got := lr.sampledIndex(); got != renewal {
		t.Fatalf("sampled index after the renewal = %d, want %d", got, renewal)
	}
	for _, tc := range []struct {
		index uint64
		want  int64
	}{
		{marker - 1, 0},
		{renewal - 1, 0},
		{renewal, 100},
		{renewal + 1, 90},
	} {
		g.next(lr)
		if err := lr.ApplyCheckpoints(st, 0, tc.index, []Checkpoint{{ID: l.ID, RemainingTTL: tc.want}}); err != nil {
			t.Fatal(err)
		}
		if tc.want == 0 {
			if got := checkpointTTLOf(t, lr, l.ID); got != 0 {
				t.Fatalf("checkpoint sampled at %d applied: %d", tc.index, got)
			}
			continue
		}
		if got := checkpointTTLOf(t, lr, l.ID); got != tc.want {
			t.Fatalf("checkpoint sampled at %d = %d, want %d", tc.index, got, tc.want)
		}
	}
}

// Revoking a renewed lease deletes its renewal record with it, so a restart
// finds no record naming an unheld lease.
func TestRevoke_DeletesTheRenewalRecord(t *testing.T) {
	st := memory.NewStorage()
	replaySuperseded(t, NewLessor(WithClock(newFakeClock())), st, 0, 3)
	lr := NewLessor(WithClock(newFakeClock()))
	if err := lr.Restore(st); err != nil {
		t.Fatal(err)
	}
	lr.Applying(4)
	if _, err := lr.Revoke(st, 1); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.Get(renewalRecordKey(1)); v != nil {
		t.Fatalf("renewal record after Revoke = %x, want deleted", v)
	}
	if err := NewLessor().Restore(st); err != nil {
		t.Fatalf("Restore after Revoke: %v", err)
	}
}

// A state machine that never told the lessor what it is applying cannot have
// a checkpoint judged, so the checkpoint is refused rather than judged
// against index zero.
func TestApplyCheckpoints_RefusedWithoutAnApplyIndex(t *testing.T) {
	lr := NewLessor()
	l, _ := lr.Grant(nopWriter{}, 60)
	var unset *ApplyIndexUnsetError
	if err := lr.ApplyCheckpoints(nopWriter{}, 0, 0, []Checkpoint{{ID: l.ID, RemainingTTL: 10}}); !errors.As(err, &unset) {
		t.Fatalf("ApplyCheckpoints with no apply index = %v, want *ApplyIndexUnsetError", err)
	}
	if unset.Error() == "" {
		t.Fatal("ApplyIndexUnsetError has an empty message")
	}
	if lr.recordFrom.Load() != 0 {
		t.Fatal("a refused checkpoint set the renewal marker")
	}
}

func TestRenewLocal_RefusedWhileACheckpointIsRecorded(t *testing.T) {
	clk := newFakeClock()
	var leading atomic.Bool
	leading.Store(true)
	lr := leaderLessor(t, clk, &leading)
	l, _ := lr.Grant(nopWriter{}, 600)
	if err := checkpointNow(lr, nopWriter{}, []Checkpoint{{ID: l.ID, RemainingTTL: 50}}); err != nil {
		t.Fatal(err)
	}

	var ce *CheckpointedError
	if _, err := lr.RenewLocal(l.ID); !errors.As(err, &ce) {
		t.Fatalf("RenewLocal with a checkpoint recorded = %v, want *CheckpointedError", err)
	}
	if _, err := lr.KeepAlive(nopWriter{}, l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := lr.RenewLocal(l.ID); err != nil {
		t.Fatalf("RenewLocal after a replicated renewal cleared the checkpoint: %v", err)
	}
}

func TestKeepAlive_DeletesTheCheckpointRecord(t *testing.T) {
	clk := newFakeClock()
	st := memory.NewStorage()
	lr := NewLessor(WithClock(clk))
	l, _ := lr.Grant(st, 600)
	if err := checkpointNow(lr, st, []Checkpoint{{ID: l.ID, RemainingTTL: 50}}); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.Get(checkpointRecordKey(l.ID)); v == nil {
		t.Fatal("ApplyCheckpoints wrote no record")
	}

	var twe *TableWriteError
	if _, err := lr.KeepAlive(faultWriter{}, l.ID); !errors.As(err, &twe) {
		t.Fatalf("KeepAlive through a failing writer = %v, want *TableWriteError", err)
	}
	if got := checkpointTTLOf(t, lr, l.ID); got != 50 {
		t.Fatalf("a failed KeepAlive changed the checkpoint to %d", got)
	}

	if _, err := lr.KeepAlive(st, l.ID); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.Get(checkpointRecordKey(l.ID)); v != nil {
		t.Fatal("KeepAlive left the checkpoint record behind")
	}

	// Once the group records renewals, a keep-alive of a lease with no
	// checkpoint writes its renewal record and nothing else.
	w := &countingWriter{}
	if _, err := lr.KeepAlive(w, l.ID); err != nil {
		t.Fatal(err)
	}
	if w.writes != 1 {
		t.Fatalf("KeepAlive with no checkpoint wrote %d records, want only the renewal record", w.writes)
	}
}

// Until the group applies its first checkpoint -- which it never does while
// featuregate.LeaseCheckpoint is off, because no checkpoint is proposed -- a
// replicated keep-alive writes nothing, exactly as before checkpoints existed,
// so a member that does not know the renewal record computes the same state.
func TestKeepAlive_WritesNothingBeforeTheFirstCheckpoint(t *testing.T) {
	st := memory.NewStorage()
	lr := NewLessor()
	l, _ := lr.Grant(st, 60)
	g := &raftLog{}

	w := &countingWriter{}
	g.keepAlive(t, w, l.ID, lr)
	if w.writes != 0 {
		t.Fatalf("KeepAlive before any checkpoint wrote %d records, want none", w.writes)
	}
	for _, k := range [][]byte{renewalRecordKey(l.ID), []byte(recordFromKey)} {
		if v, _ := st.Get(k); v != nil {
			t.Fatalf("record %q exists before any checkpoint", k)
		}
	}
	lr.mu.RLock()
	renewIndex := lr.leases[l.ID].renewIndex
	lr.mu.RUnlock()
	if renewIndex != 0 {
		t.Fatalf("renewal index before any checkpoint = %d, want 0", renewIndex)
	}
}

func TestApplyCheckpoints_ClampsSkipsAndClears(t *testing.T) {
	st := memory.NewStorage()
	lr := NewLessor()
	l, _ := lr.Grant(st, 60)

	if err := checkpointNow(lr, st, []Checkpoint{{ID: l.ID, RemainingTTL: 999}, {ID: 404, RemainingTTL: 5}}); err != nil {
		t.Fatal(err)
	}
	if got := checkpointTTLOf(t, lr, l.ID); got != 60 {
		t.Fatalf("checkpoint = %d, want it clamped to the 60s TTL", got)
	}
	if v, _ := st.Get(checkpointRecordKey(404)); v != nil {
		t.Fatal("a checkpoint of a lease not held was recorded")
	}

	if err := checkpointNow(lr, st, []Checkpoint{{ID: l.ID, RemainingTTL: -3}}); err != nil {
		t.Fatal(err)
	}
	if got := checkpointTTLOf(t, lr, l.ID); got != 0 {
		t.Fatalf("checkpoint after a negative entry = %d, want it cleared", got)
	}
	if v, _ := st.Get(checkpointRecordKey(l.ID)); v != nil {
		t.Fatal("a clearing checkpoint left the record behind")
	}
}

func TestApplyCheckpoints_WriteFaultChangesNothing(t *testing.T) {
	lr := NewLessor()
	a, _ := lr.Grant(nopWriter{}, 60)
	b, _ := lr.Grant(nopWriter{}, 60)

	var twe *TableWriteError
	err := checkpointNow(lr, &nthFaultWriter{n: 2}, []Checkpoint{{ID: a.ID, RemainingTTL: 10}, {ID: b.ID, RemainingTTL: 10}})
	if !errors.As(err, &twe) {
		t.Fatalf("ApplyCheckpoints through a failing writer = %v, want *TableWriteError", err)
	}
	if checkpointTTLOf(t, lr, a.ID) != 0 || checkpointTTLOf(t, lr, b.ID) != 0 {
		t.Fatal("a failed checkpoint changed the in-memory state")
	}

	lr.Close()
	var closed *LessorClosedError
	if err := lr.ApplyCheckpoints(nopWriter{}, 0, 0, nil); !errors.As(err, &closed) {
		t.Fatalf("closed lessor = %v, want *LessorClosedError", err)
	}
}

func TestRevoke_DeletesTheCheckpointRecord(t *testing.T) {
	st := memory.NewStorage()
	lr := NewLessor()
	l, _ := lr.Grant(st, 60)
	if err := checkpointNow(lr, st, []Checkpoint{{ID: l.ID, RemainingTTL: 10}}); err != nil {
		t.Fatal(err)
	}
	if _, err := lr.Revoke(st, l.ID); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.Get(checkpointRecordKey(l.ID)); v != nil {
		t.Fatal("Revoke left the checkpoint record behind")
	}
	if err := NewLessor().Restore(st); err != nil {
		t.Fatalf("Restore after the revoke: %v", err)
	}
}

func TestRestore_CountsFromTheCheckpoint(t *testing.T) {
	clk := newFakeClock()
	st := memory.NewStorage()
	lr := NewLessor(WithClock(clk))
	checkpointed, _ := lr.Grant(st, 600)
	plain, _ := lr.Grant(st, 600)
	if err := checkpointNow(lr, st, []Checkpoint{{ID: checkpointed.ID, RemainingTTL: 40}}); err != nil {
		t.Fatal(err)
	}

	clk.Advance(time.Hour)
	restored := NewLessor(WithClock(clk))
	if err := restored.Restore(st); err != nil {
		t.Fatal(err)
	}
	if got := restored.Lookup(checkpointed.ID).RemainingTTL; got != 40 {
		t.Fatalf("checkpointed lease remaining = %d, want 40", got)
	}
	if got := restored.Lookup(plain.ID).RemainingTTL; got != 600 {
		t.Fatalf("lease with no checkpoint remaining = %d, want the full 600", got)
	}
}

func TestRequestCheckpoint_RefusedOffThePrimary(t *testing.T) {
	var np *NotPrimaryError
	if _, err := NewLessor().RequestCheckpoint(); !errors.As(err, &np) {
		t.Fatalf("RequestCheckpoint without RunExpiry = %v, want *NotPrimaryError", err)
	}

	clk := newFakeClock()
	var leading atomic.Bool
	leading.Store(true)
	lr := leaderLessor(t, clk, &leading) // leading, but no checkpoint loop
	if _, err := lr.RequestCheckpoint(); !errors.As(err, &np) {
		t.Fatalf("RequestCheckpoint without a checkpointer = %v, want *NotPrimaryError", err)
	}
}

func TestRequestCheckpoint_CheckpointsEveryLeaseNow(t *testing.T) {
	clk := newFakeClock()
	lr := NewLessor(WithClock(clk))
	short, _ := lr.Grant(nopWriter{}, 10)

	var leading atomic.Bool
	leading.Store(true)
	cp := newApplyingCheckpointer(lr)
	cp.startRecording(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		lr.RunExpiry(ctx, time.Hour, leading.Load,
			func(context.Context, int64) error { return nil }, nil,
			WithCheckpoint(time.Hour, cp))
	}()

	armCtx, cancelArm := context.WithTimeout(ctx, guard)
	defer cancelArm()
	if err := clk.BlockUntil(armCtx, 2); err != nil {
		t.Fatalf("the sweeper never armed its tickers: %v", err)
	}
	clk.Advance(4 * time.Second) // not a tick of either ticker
	answer, err := lr.RequestCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-answer:
		if err != nil {
			t.Fatalf("on-demand checkpoint: %v", err)
		}
	case <-time.After(guard):
		t.Fatal("the on-demand checkpoint was never answered")
	}
	if got := checkpointTTLOf(t, lr, short.ID); got != 6 {
		t.Fatalf("checkpoint = %d, want 6", got)
	}

	// A request the loop has not answered when it stops is refused, not lost.
	lr.waitersMu.Lock()
	orphan := make(chan error, 1)
	lr.checkpointWaiters = append(lr.checkpointWaiters, orphan)
	lr.waitersMu.Unlock()
	cancel()
	<-done
	var np *NotPrimaryError
	if err := <-orphan; !errors.As(err, &np) {
		t.Fatalf("request outstanding at shutdown = %v, want *NotPrimaryError", err)
	}
}

func TestLeases_AFollowerListsLeasesPastItsOwnDeadline(t *testing.T) {
	clk := newFakeClock()
	var leading atomic.Bool
	lr := leaderLessor(t, clk, &leading)
	l, _ := lr.Grant(nopWriter{}, 10)
	clk.Advance(time.Minute)

	if got := lr.Leases(); len(got) != 1 || got[0].ID != l.ID {
		t.Fatalf("follower Leases = %v, want lease %d: only the leader decides expiry", got, l.ID)
	}
	leading.Store(true)
	if got := lr.Leases(); len(got) != 0 {
		t.Fatalf("leader Leases = %v, want none past their deadline", got)
	}
}

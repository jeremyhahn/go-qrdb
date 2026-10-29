// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package clustermig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// fakeProgress is a ReplicationProgress whose answers the test sets. It
// records the shards it was asked about, so a test can tell the gate asked
// about the migration's own shard.
type fakeProgress struct {
	mu        sync.Mutex
	follower  bool
	commit    uint64
	commitOK  bool
	commitErr error
	match     map[string]uint64
	matchErr  error
	asked     []uint64
}

var _ ReplicationProgress = (*fakeProgress)(nil)

func newFakeProgress(commit uint64) *fakeProgress {
	return &fakeProgress{commit: commit, commitOK: true, match: map[string]uint64{}}
}

func (p *fakeProgress) Leads(shardID uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, shardID)
	return !p.follower
}

func (p *fakeProgress) CommitIndex(shardID uint64) (uint64, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, shardID)
	return p.commit, p.commitOK, p.commitErr
}

func (p *fakeProgress) MatchIndex(shardID uint64, nodeID string) (uint64, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, shardID)
	if p.matchErr != nil {
		return 0, false, p.matchErr
	}
	m, ok := p.match[nodeID]
	return m, ok, nil
}

func (p *fakeProgress) set(nodeID string, match uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.match[nodeID] = match
}

// catchUpCoordinator is rollingCoordinator gated on p: owners n1..n3 on shard
// 7, self n1, so the order is n2, n3, n1.
func catchUpCoordinator(s *Store, p ReplicationProgress) *Coordinator {
	return NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1", "n2", "n3"}},
		func() bool { return true }, nil, WithRollingSteps(rollingOn, "n1"), WithCatchUp(p))
}

func requestRolling(t *testing.T, c *Coordinator) {
	t.Helper()
	if _, err := c.Request(context.Background(), tid, db, validOpts()); err != nil {
		t.Fatalf("Request: %v", err)
	}
}

func advance(t *testing.T, c *Coordinator, wantWrote bool, why string) {
	t.Helper()
	wrote, err := c.Advance(context.Background(), tid, db)
	if err != nil || wrote != wantWrote {
		t.Fatalf("Advance (%s) = %v, %v; want %v, nil", why, wrote, err, wantWrote)
	}
}

func catchUpStep(nodeID string, state StepState, target uint64) NodeStep {
	return NodeStep{NodeID: nodeID, State: state, CatchUpIndex: target}
}

func TestCoordinator_Advance_HandsOnOnlyOnceTheOwnerCaughtUp(t *testing.T) {
	s := NewStore(newMemKV())
	p := newFakeProgress(40)
	c := catchUpCoordinator(s, p)
	requestRolling(t, c)
	ack(t, s, "n2", 1, seal.PhaseComplete)

	// The ack records the leader's commit index as n2's target; n2 has not
	// reported a match yet, so the turn stays with it.
	advance(t, c, true, "ack seen, target recorded")
	requireSteps(t, s, catchUpStep("n2", StepRecycling, 40), step("n3", StepPending), step("n1", StepPending))

	p.set("n2", 39)
	advance(t, c, false, "one entry behind")
	requireSteps(t, s, catchUpStep("n2", StepRecycling, 40), step("n3", StepPending), step("n1", StepPending))

	p.set("n2", 40)
	advance(t, c, true, "caught up")
	requireSteps(t, s, catchUpStep("n2", StepServing, 40), step("n3", StepRecycling), step("n1", StepPending))
	for _, sh := range p.asked {
		if sh != 7 {
			t.Fatalf("progress asked about shard %d, want the migration's shard 7", sh)
		}
	}
}

func TestCoordinator_Advance_TheTargetDoesNotMoveWithTheLog(t *testing.T) {
	s := NewStore(newMemKV())
	p := newFakeProgress(40)
	c := catchUpCoordinator(s, p)
	requestRolling(t, c)
	ack(t, s, "n2", 1, seal.PhaseComplete)
	advance(t, c, true, "target recorded")

	// Writes keep committing; the owner need only reach the index recorded
	// when it acked, or a busy cluster would never hand the turn on.
	p.commit = 90
	p.set("n2", 41)
	advance(t, c, true, "past the recorded target")
	requireSteps(t, s, catchUpStep("n2", StepServing, 40), step("n3", StepRecycling), step("n1", StepPending))
}

func TestCoordinator_Advance_ANewLeaderJudgesTheRecordedTarget(t *testing.T) {
	s := NewStore(newMemKV())
	requestRolling(t, catchUpCoordinator(s, newFakeProgress(40)))
	ack(t, s, "n2", 1, seal.PhaseComplete)
	advance(t, catchUpCoordinator(s, newFakeProgress(40)), true, "first leader records the target")

	next := newFakeProgress(70)
	next.set("n2", 40)
	advance(t, catchUpCoordinator(s, next), true, "second leader hands on at the recorded target")
	requireSteps(t, s, catchUpStep("n2", StepServing, 40), step("n3", StepRecycling), step("n1", StepPending))
}

func TestCoordinator_Advance_UnknownProgressWaits(t *testing.T) {
	s := NewStore(newMemKV())
	p := newFakeProgress(0)
	p.commitOK = false
	c := catchUpCoordinator(s, p)
	requestRolling(t, c)
	ack(t, s, "n2", 1, seal.PhaseComplete)

	advance(t, c, false, "commit index not known yet")
	requireSteps(t, s, step("n2", StepRecycling), step("n3", StepPending), step("n1", StepPending))

	p.commit, p.commitOK = 12, true
	advance(t, c, true, "target recorded")
	advance(t, c, false, "match not known yet")
	requireSteps(t, s, catchUpStep("n2", StepRecycling, 12), step("n3", StepPending), step("n1", StepPending))
}

func TestCoordinator_Advance_ProgressErrorsLeaveTheRecord(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		set  func(*fakeProgress)
		want error
	}{
		{"not the shard leader", func(p *fakeProgress) { p.commitErr = &NotShardLeaderError{ShardID: 7} }, &NotShardLeaderError{}},
		{"not a member", func(p *fakeProgress) { p.matchErr = &NotShardMemberError{ShardID: 7, NodeID: "n2"} }, &NotShardMemberError{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore(newMemKV())
			p := newFakeProgress(40)
			tc.set(p)
			c := catchUpCoordinator(s, p)
			requestRolling(t, c)
			ack(t, s, "n2", 1, seal.PhaseComplete)
			wrote, err := c.Advance(ctx, tid, db)
			if wrote || err == nil {
				t.Fatalf("Advance = %v, %v; want false and an error", wrote, err)
			}
			switch tc.want.(type) {
			case *NotShardLeaderError:
				var e *NotShardLeaderError
				if !errors.As(err, &e) || e.ShardID != 7 {
					t.Fatalf("Advance error = %v, want NotShardLeaderError for shard 7", err)
				}
			case *NotShardMemberError:
				var e *NotShardMemberError
				if !errors.As(err, &e) || e.NodeID != "n2" {
					t.Fatalf("Advance error = %v, want NotShardMemberError for n2", err)
				}
			}
			requireSteps(t, s, step("n2", StepRecycling), step("n3", StepPending), step("n1", StepPending))
		})
	}
}

func TestCoordinator_Advance_NoShardHandsOnAtTheAck(t *testing.T) {
	s := NewStore(newMemKV())
	p := newFakeProgress(40)
	c := NewCoordinator(s, fixedOwners{owners: []string{"n1", "n2"}},
		func() bool { return true }, nil, WithRollingSteps(rollingOn, "n1"), WithCatchUp(p))
	requestRolling(t, c)
	ack(t, s, "n2", 1, seal.PhaseComplete)
	advance(t, c, true, "a scope with no single shard")
	requireSteps(t, s, step("n2", StepServing), step("n1", StepRecycling))
	if len(p.asked) != 0 {
		t.Fatalf("progress asked about shards %v for a scope with no shard", p.asked)
	}
}

// TestCoordinator_Advance_TheShardLeaderCoordinates proves a migration on a
// database's own shard is advanced by the node leading that shard, the one
// node that knows its peers' progress, even though it does not lead the
// coordination store; the store's leader, not leading the shard, leaves it.
func TestCoordinator_Advance_TheShardLeaderCoordinates(t *testing.T) {
	s := NewStore(newMemKV())
	requestRolling(t, catchUpCoordinator(s, newFakeProgress(40)))
	ack(t, s, "n2", 1, seal.PhaseComplete)

	systemLeader := newFakeProgress(40)
	systemLeader.follower = true
	systemLeader.set("n2", 40)
	advance(t, catchUpCoordinator(s, systemLeader), false, "the store's leader does not lead shard 7")
	requireSteps(t, s, catchUpStep("n2", StepRecycling, 0), step("n3", StepPending), step("n1", StepPending))

	shardLeader := newFakeProgress(40)
	shardLeader.set("n2", 40)
	c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1", "n2", "n3"}},
		func() bool { return false }, nil, WithRollingSteps(rollingOn, "n3"), WithCatchUp(shardLeader))
	advance(t, c, true, "the shard's leader records the target and n2 holds it")
	requireSteps(t, s, catchUpStep("n2", StepServing, 40), step("n3", StepRecycling), step("n1", StepPending))
	for _, sh := range shardLeader.asked {
		if sh != 7 {
			t.Fatalf("progress asked about shard %d, want only the migration's shard 7", sh)
		}
	}
}

// TestCoordinator_Advance_NoShardLeaderHandsNothingOn proves the rollout
// fails closed while the shard has no leader: no node, the coordination
// store's leader included, hands a turn on unchecked, and the record stays
// as it was.
func TestCoordinator_Advance_NoShardLeaderHandsNothingOn(t *testing.T) {
	s := NewStore(newMemKV())
	requestRolling(t, catchUpCoordinator(s, newFakeProgress(40)))
	ack(t, s, "n2", 1, seal.PhaseComplete)
	for _, systemLeader := range []bool{true, false} {
		p := newFakeProgress(40)
		p.follower = true
		p.set("n2", 40)
		c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1", "n2", "n3"}},
			func() bool { return systemLeader }, nil, WithRollingSteps(rollingOn, "n1"), WithCatchUp(p))
		advance(t, c, false, "no node leads shard 7")
	}
	requireSteps(t, s, catchUpStep("n2", StepRecycling, 0), step("n3", StepPending), step("n1", StepPending))
}

func TestCoordinator_Advance_WithoutCatchUpHandsOnAtTheAck(t *testing.T) {
	s := NewStore(newMemKV())
	c := rollingCoordinator(s, rollingOn)
	requestRolling(t, c)
	ack(t, s, "n2", 1, seal.PhaseComplete)
	advance(t, c, true, "no catch-up gate")
	requireSteps(t, s, step("n2", StepServing), step("n3", StepRecycling), step("n1", StepPending))
}

// TestCoordinator_Advance_LogsEachHandOn proves WithLogger records one line per
// step handed on, carrying the target its owner caught up to, and none while
// the turn stays put.
func TestCoordinator_Advance_LogsEachHandOn(t *testing.T) {
	s := NewStore(newMemKV())
	p := newFakeProgress(40)
	var logs bytes.Buffer
	c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1", "n2", "n3"}},
		func() bool { return true }, nil, WithRollingSteps(rollingOn, "n1"), WithCatchUp(p),
		WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))))
	requestRolling(t, c)
	ack(t, s, "n2", 1, seal.PhaseComplete)
	advance(t, c, true, "ack seen, target recorded")
	if logs.Len() != 0 {
		t.Fatalf("logged before any hand-on: %s", logs.String())
	}

	p.set("n2", 40)
	advance(t, c, true, "caught up")
	var line struct {
		Msg          string `json:"msg"`
		Scope        string `json:"scope"`
		Generation   uint64 `json:"generation"`
		Node         string `json:"node"`
		CatchUpIndex uint64 `json:"catch_up_index"`
	}
	if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
		t.Fatalf("hand-on log %q: %v", logs.String(), err)
	}
	if line.Msg != "cluster-migration step handed on" || line.Node != "n2" || line.CatchUpIndex != 40 || line.Generation != 1 {
		t.Fatalf("hand-on log = %+v, want n2 handed on at 40 in generation 1", line)
	}
}

// loggingCatchUpCoordinator is catchUpCoordinator logging its hand-ons to
// logs, so a test can tell which coordinator handed a turn on.
func loggingCatchUpCoordinator(s *Store, p ReplicationProgress, logs *bytes.Buffer) *Coordinator {
	return NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1", "n2", "n3"}},
		func() bool { return true }, nil, WithRollingSteps(rollingOn, "n1"), WithCatchUp(p),
		WithLogger(slog.New(slog.NewJSONHandler(logs, nil))))
}

// TestCoordinator_Advance_ASteppedDownCoordinatorLeavesTheHandOn is the
// restarted coordinator that loses its leadership while a turn is held: it
// recorded the target, but once it no longer leads the shard it neither
// writes nor logs the hand-on, and the node elected in its place hands the
// turn on at that target and logs it.
func TestCoordinator_Advance_ASteppedDownCoordinatorLeavesTheHandOn(t *testing.T) {
	s := NewStore(newMemKV())
	var formerLogs, nextLogs bytes.Buffer
	former := newFakeProgress(40)
	restarted := loggingCatchUpCoordinator(s, former, &formerLogs)
	requestRolling(t, restarted)
	ack(t, s, "n2", 1, seal.PhaseComplete)
	advance(t, restarted, true, "restarted coordinator records the target")

	former.follower = true
	former.set("n2", 40)
	advance(t, restarted, false, "stepped-down coordinator, owner caught up")
	requireSteps(t, s, catchUpStep("n2", StepRecycling, 40), step("n3", StepPending), step("n1", StepPending))

	next := newFakeProgress(55)
	next.set("n2", 40)
	advance(t, loggingCatchUpCoordinator(s, next, &nextLogs), true, "the new leader hands on at the recorded target")
	requireSteps(t, s, catchUpStep("n2", StepServing, 40), step("n3", StepRecycling), step("n1", StepPending))
	if formerLogs.Len() != 0 {
		t.Fatalf("the stepped-down coordinator logged %s", formerLogs.String())
	}
	if !strings.Contains(nextLogs.String(), `"msg":"cluster-migration step handed on"`) || !strings.Contains(nextLogs.String(), `"node":"n2"`) {
		t.Fatalf("the new leader's log = %q, want n2's hand-on", nextLogs.String())
	}
}

// TestCoordinator_Advance_ACoordinatorLeadingAgainHandsOn is the other
// branch: the restarted coordinator that steps down and is elected again
// before the owner catches up hands the turn on itself, from the record.
func TestCoordinator_Advance_ACoordinatorLeadingAgainHandsOn(t *testing.T) {
	s := NewStore(newMemKV())
	var logs bytes.Buffer
	p := newFakeProgress(40)
	c := loggingCatchUpCoordinator(s, p, &logs)
	requestRolling(t, c)
	ack(t, s, "n2", 1, seal.PhaseComplete)
	advance(t, c, true, "target recorded")

	p.follower = true
	advance(t, c, false, "stepped down")
	p.follower = false
	p.set("n2", 40)
	advance(t, c, true, "leading again, owner caught up")
	requireSteps(t, s, catchUpStep("n2", StepServing, 40), step("n3", StepRecycling), step("n1", StepPending))
	if !strings.Contains(logs.String(), `"node":"n2"`) {
		t.Fatalf("log = %q, want n2's hand-on", logs.String())
	}
}

// interleavedKV runs between, once, right after the first read of the
// migration record: another leader's write landing between a former leader's
// read and its write.
type interleavedKV struct {
	*memKV
	between func()
}

func (k *interleavedKV) Get(ctx context.Context, key string) ([]byte, error) {
	v, err := k.memKV.Get(ctx, key)
	if f := k.between; f != nil && key == migrationKey(tid, db) {
		k.between = nil
		f()
	}
	return v, err
}

func TestCoordinator_Advance_RefusesAFormerLeadersWrite(t *testing.T) {
	ctx := context.Background()
	kv := &interleavedKV{memKV: newMemKV()}
	s := NewStore(kv)
	former, current := rollingCoordinator(s, rollingOn), rollingCoordinator(s, rollingOn)
	requestRolling(t, current)
	ack(t, s, "n2", 1, seal.PhaseComplete)

	// The current leader re-requests (generation 2) while the former one is
	// between reading generation 1 and handing n2's turn on. The former's
	// conditional write loses, and the record it reads again is a newer
	// request's, not its own to act on.
	kv.between = func() { requestRolling(t, current) }
	wrote, err := former.Advance(ctx, tid, db)
	if wrote || err != nil {
		t.Fatalf("former Advance = %v, %v; want false, nil", wrote, err)
	}
	m, _ := s.GetMigration(ctx, tid, db)
	if m.Generation != 2 {
		t.Fatalf("Generation = %d, want the current leader's 2", m.Generation)
	}
	requireSteps(t, s, step("n2", StepRecycling), step("n3", StepPending), step("n1", StepPending))
}

func TestCoordinator_Advance_RefusesAFormerLeadersCatchUpTarget(t *testing.T) {
	ctx := context.Background()
	kv := &interleavedKV{memKV: newMemKV()}
	s := NewStore(kv)
	current := catchUpCoordinator(s, newFakeProgress(40))
	requestRolling(t, current)
	ack(t, s, "n2", 1, seal.PhaseComplete)

	// A former leader, whose view of the log is older, read the record
	// before the current leader recorded n2's target; the lower target it
	// would record must not replace the current one. Its write loses, and on
	// the record it reads again the target is already set, so it waits.
	kv.between = func() { advance(t, current, true, "current leader records the target") }
	wrote, err := catchUpCoordinator(s, newFakeProgress(10)).Advance(ctx, tid, db)
	if wrote || err != nil {
		t.Fatalf("former Advance = %v, %v; want false, nil", wrote, err)
	}
	requireSteps(t, s, catchUpStep("n2", StepRecycling, 40), step("n3", StepPending), step("n1", StepPending))
}

func TestCoordinator_Request_SupersedesARequestThatLandedFirst(t *testing.T) {
	ctx := context.Background()
	kv := &interleavedKV{memKV: newMemKV()}
	s := NewStore(kv)
	former, current := rollingCoordinator(s, rollingOn), rollingCoordinator(s, rollingOn)
	undone := false
	kv.between = func() { requestRolling(t, current) }
	m, err := former.Request(ctx, tid, db, validOpts(), WithPrepare(func(*ClusterMigration) (func(), error) {
		return func() { undone = true }, nil
	}))
	if err != nil {
		t.Fatalf("former Request = %v, want nil", err)
	}
	if !undone {
		t.Fatal("the lost write's prepare was not undone")
	}
	if m.Generation != 2 {
		t.Fatalf("returned Generation = %d, want 2, past the request that landed first", m.Generation)
	}
	if got, _ := s.GetMigration(ctx, tid, db); got.Generation != 2 {
		t.Fatalf("Generation = %d, want 2", got.Generation)
	}
}

func TestCoordinator_Reap_RefusesAFormerLeadersDelete(t *testing.T) {
	ctx := context.Background()
	kv := &interleavedKV{memKV: newMemKV()}
	s := NewStore(kv)
	owners := fixedOwners{shardID: 7, owners: []string{"n1"}}
	leader := func() bool { return true }
	former, current := NewCoordinator(s, owners, leader, nil), NewCoordinator(s, owners, leader, nil)
	if _, err := current.Request(ctx, tid, db, validOpts()); err != nil {
		t.Fatalf("Request: %v", err)
	}
	ack(t, s, "n1", 1, seal.PhaseComplete)

	// A new request lands after the former leader read the complete record.
	kv.between = func() {
		if _, err := current.Request(ctx, tid, db, validOpts()); err != nil {
			t.Errorf("Request: %v", err)
		}
	}
	reaped, err := former.Reap(ctx, tid, db)
	if reaped || err != nil {
		t.Fatalf("Reap = %v, %v; want false, nil", reaped, err)
	}
	m, _ := s.GetMigration(ctx, tid, db)
	if m == nil || m.Generation != 2 {
		t.Fatalf("migration = %+v, want the new request's generation 2 kept", m)
	}
	// n1's generation-1 ack is the reaped generation's and may go with it;
	// it never counts for the new request.
	if done, err := s.IsComplete(ctx, tid, db); err != nil || done {
		t.Fatalf("IsComplete = %v, %v; want the new request incomplete", done, err)
	}
}

func TestStore_CompareAndSwapMigration_RefusesAStaleRead(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	_, stale, _ := s.GetMigrationVersion(ctx, tid, db)
	seedMigration(t, s, 2, []string{"n1"})

	next := &ClusterMigration{TenantID: tid, Database: db, Target: seal.StrategyPassphrase, Generation: 9}
	if swapped, err := s.CompareAndSwapMigration(ctx, stale, next); swapped || err != nil {
		t.Fatalf("CompareAndSwapMigration(stale) = %v, %v; want false, nil", swapped, err)
	}
	if swapped, err := s.CompareAndSwapMigration(ctx, MigrationVersion{}, next); swapped || err != nil {
		t.Fatalf("CompareAndSwapMigration(absent over a record) = %v, %v; want false, nil", swapped, err)
	}
	m, _ := s.GetMigration(ctx, tid, db)
	if m.Generation != 2 {
		t.Fatalf("Generation = %d, want 2 unchanged", m.Generation)
	}
}

func TestStore_CompareAndDeleteMigration_RefusesAStaleRead(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	ack(t, s, "n1", 1, seal.PhaseComplete)
	_, stale, _ := s.GetMigrationVersion(ctx, tid, db)
	seedMigration(t, s, 2, []string{"n1"})

	if deleted, err := s.CompareAndDeleteMigration(ctx, stale); deleted || err != nil {
		t.Fatalf("CompareAndDeleteMigration(stale) = %v, %v; want false, nil", deleted, err)
	}
	if m, _ := s.GetMigration(ctx, tid, db); m == nil || m.Generation != 2 {
		t.Fatalf("migration = %+v, want generation 2 kept", m)
	}
	_, current, _ := s.GetMigrationVersion(ctx, tid, db)
	if deleted, err := s.CompareAndDeleteMigration(ctx, current); !deleted || err != nil {
		t.Fatalf("CompareAndDeleteMigration(current) = %v, %v; want true, nil", deleted, err)
	}
	if m, _ := s.GetMigration(ctx, tid, db); m != nil {
		t.Fatalf("migration = %+v after the delete, want none", m)
	}
}

func TestNotShardLeaderError_Error(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want []string
	}{
		{&NotShardLeaderError{ShardID: 7}, []string{"clustermig:", "shard 7", "leader"}},
		{&NotShardLeaderError{ShardID: 18446744073709551615}, []string{"18446744073709551615"}},
	} {
		for _, w := range tc.want {
			if !strings.Contains(tc.err.Error(), w) {
				t.Fatalf("%q does not mention %q", tc.err.Error(), w)
			}
		}
	}
}

func TestNotShardMemberError_Error(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want []string
	}{
		{&NotShardMemberError{ShardID: 7, NodeID: "10.0.0.2:7000"}, []string{"clustermig:", "10.0.0.2:7000", "shard 7"}},
		{&NotShardMemberError{ShardID: 1, NodeID: "n9"}, []string{"n9", "shard 1"}},
	} {
		for _, w := range tc.want {
			if !strings.Contains(tc.err.Error(), w) {
				t.Fatalf("%q does not mention %q", tc.err.Error(), w)
			}
		}
	}
}

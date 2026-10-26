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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// stubLeaderIDs answers LeaderID with fixed values.
type stubLeaderIDs struct {
	id  uint64
	ok  bool
	err error
}

func (s *stubLeaderIDs) LeaderID(uint64) (uint64, bool, error) { return s.id, s.ok, s.err }

func TestSystemShardLeader(t *testing.T) {
	cases := []struct {
		name string
		src  *stubLeaderIDs
		want bool
	}{
		{"this replica leads", &stubLeaderIDs{id: 3, ok: true}, true},
		{"another replica leads", &stubLeaderIDs{id: 4, ok: true}, false},
		{"no leader known", &stubLeaderIDs{id: 3}, false},
		{"lookup failed", &stubLeaderIDs{id: 3, ok: true, err: errors.New("closed")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := systemShardLeader(tc.src, 3)(); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// oneEntryIndex is a system store holding one shared-placement index entry.
type oneEntryIndex struct{ entry []byte }

func (o *oneEntryIndex) Get(context.Context, string) ([]byte, error) { return o.entry, nil }
func (o *oneEntryIndex) Delete(context.Context, string) error        { return nil }
func (o *oneEntryIndex) Range(context.Context, []byte, []byte, ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	return []*service.KeyValue{{Key: []byte("__reclaim:index/job"), Value: o.entry}}, 1, 1, nil
}

// leaderRecordingResolver records the systemLeader answer each sweep hands
// it and owns nothing.
type leaderRecordingResolver struct {
	mu   sync.Mutex
	seen []bool
}

func (r *leaderRecordingResolver) Owns(_ service.ReclaimIndexEntry, systemLeader bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, systemLeader)
	return false
}

func (r *leaderRecordingResolver) ReclaimStore(context.Context, service.ReclaimIndexEntry) (service.ReclaimStore, error) {
	return nil, errors.New("unreachable: owns nothing")
}

func (r *leaderRecordingResolver) sweeps() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.seen...)
}

func sweeperWithOneEntry(t *testing.T) (*service.ReclaimSweeper, *leaderRecordingResolver) {
	t.Helper()
	raw, err := json.Marshal(service.ReclaimIndexEntry{JobID: "job", Placement: service.StorePlacementShared})
	if err != nil {
		t.Fatal(err)
	}
	sweeper := service.NewReclaimSweeper(&oneEntryIndex{entry: raw})
	resolver := &leaderRecordingResolver{}
	sweeper.SetStoreResolver(resolver)
	return sweeper, resolver
}

// TestStartReclaimSweeper_StandaloneDaemonSweepsAsTheLeader is the codex
// round-2 finding: a standalone daemon (no Raft host, so no leadership
// predicate) must still run the sweeper, and must own shared-store jobs.
func TestStartReclaimSweeper_StandaloneDaemonSweepsAsTheLeader(t *testing.T) {
	sweeper, resolver := sweeperWithOneEntry(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startReclaimSweeper(ctx, sweeper, time.Millisecond, nil, nil)

	deadline := time.Now().Add(5 * time.Second)
	for len(resolver.sweeps()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the standalone daemon's sweeper never swept")
		}
		time.Sleep(time.Millisecond)
	}
	for _, leader := range resolver.sweeps() {
		if !leader {
			t.Fatal("a standalone daemon must sweep as the owner of shared-store jobs")
		}
	}
}

// TestStartReclaimSweeper_ClusteredFollowerStillSweeps proves the loop runs
// on a node that does not lead the system shard -- it may own node-local or
// dedicated-group jobs -- while reporting that it is not the leader.
func TestStartReclaimSweeper_ClusteredFollowerStillSweeps(t *testing.T) {
	sweeper, resolver := sweeperWithOneEntry(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startReclaimSweeper(ctx, sweeper, time.Millisecond, systemShardLeader(&stubLeaderIDs{id: 9, ok: true}, 3), nil)

	deadline := time.Now().Add(5 * time.Second)
	for len(resolver.sweeps()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("a follower's sweeper never swept")
		}
		time.Sleep(time.Millisecond)
	}
	if resolver.sweeps()[0] {
		t.Fatal("a follower must not claim system-shard leadership")
	}
}

func TestStartReclaimSweeper_NilSweeperIsANoOp(t *testing.T) {
	startReclaimSweeper(context.Background(), nil, time.Millisecond, nil, nil)
}

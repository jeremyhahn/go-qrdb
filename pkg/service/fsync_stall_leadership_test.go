// Copyright 2026 Jeremy Hahn
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

package service

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb/waldb"

	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch/syncwatchtest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/raftharness"
)

// fsyncStallShardID is the data shard the fsync-stall tests start beside the
// harness's system shard.
const fsyncStallShardID = 9060

// walFsyncFamily is the WAL fsync latency histogram storage-latency-
// observability exports.
const walFsyncFamily = "quicraft_logdb_fsync_latency_seconds"

// electionTimeout is the hosts' election timeout: the harness leaves
// ElectionRTT and RTTMillisecond at go-quicraft's defaults.
const electionTimeout = time.Duration(quicraftconfig.DefaultElectionRTT*quicraftconfig.DefaultRTTMillisecond) * time.Millisecond

// fsyncStallHold is how long the test parks the followers' WAL fsyncs:
// three election timeouts, long enough that a leader which counted the
// stall against itself would have lost its leadership.
const fsyncStallHold = 3 * electionTimeout

// fsyncWatch is one replica's WAL metrics collector. It records every
// observation in the RaftMetrics it wraps and then reports each fsync
// lasting at least fsyncStallHold, so the test waits on that replica's own
// observation rather than inferring it from the histogram every host
// shares.
type fsyncWatch struct {
	*database.RaftMetrics
	stalled chan time.Duration
}

var _ waldb.LogDBMetrics = (*fsyncWatch)(nil)

func newFsyncWatch() *fsyncWatch {
	return &fsyncWatch{RaftMetrics: database.NewRaftMetrics(), stalled: make(chan time.Duration, 1)}
}

// ObserveFsyncLatency records d, then reports it if it is a stalled fsync.
// The report is sent after the histogram observation, so a receiver reads
// a histogram that already holds it.
func (w *fsyncWatch) ObserveFsyncLatency(d time.Duration) {
	w.RaftMetrics.ObserveFsyncLatency(d)
	if d >= fsyncStallHold {
		select {
		case w.stalled <- d:
		default:
		}
	}
}

// fsyncStallCluster is a three-host cluster whose WALs run over StallFS, with
// a data shard started on every host and a KV and admin service per host.
type fsyncStallCluster struct {
	*raftharness.Cluster
	fs    map[uint64]*raftharness.StallFS
	fsync map[uint64]*fsyncWatch
	kv    []KVServicer
	admin []AdminServicer
}

func startFsyncStallCluster(t *testing.T) *fsyncStallCluster {
	t.Helper()
	sc := &fsyncStallCluster{fs: map[uint64]*raftharness.StallFS{}, fsync: map[uint64]*fsyncWatch{}}
	var mu sync.Mutex
	sc.Cluster = raftharness.NewCluster(t, 3, raftharness.WithHostOptions(
		func(t testing.TB, replicaID uint64, walDir string) []quicraft.HostOption {
			// The shape NewRaftWAL builds for a sealed host: a WAL the
			// caller opens, handed the collector the fsync histogram reads.
			fs := raftharness.NewStallFS()
			metrics := newFsyncWatch()
			db, err := waldb.Open(walDir, waldb.WithFS(fs), waldb.WithMetrics(metrics))
			if err != nil {
				t.Fatalf("waldb.Open(replica %d): %v", replicaID, err)
			}
			t.Cleanup(func() { _ = db.Close() })
			mu.Lock()
			sc.fs[replicaID] = fs
			sc.fsync[replicaID] = metrics
			mu.Unlock()
			return []quicraft.HostOption{quicraft.WithLogDB(db), quicraft.WithMetrics(metrics.RaftMetrics)}
		}))
	members := make(map[uint64]string, len(sc.Hosts))
	for i := range sc.Hosts {
		members[uint64(i+1)] = sc.Addrs[i]
	}
	for i, host := range sc.Hosts {
		if err := host.StartShard(members, false, sc.CreateFn(), quicraftconfig.Config{
			ShardID: fsyncStallShardID, ReplicaID: uint64(i + 1), MemberIdentities: sc.Identities,
		}); err != nil {
			t.Fatalf("StartShard(host %d): %v", i, err)
		}
		adapter := raftbackend.New(host)
		sc.kv = append(sc.kv, NewKVService(adapter, fsyncStallShardID))
		sc.admin = append(sc.admin, NewAdminService(adapter))
	}
	sc.WaitReadable(t, fsyncStallShardID, sc.Hosts...)
	return sc
}

// leader returns the data shard's leader and term as every host sees them,
// polling until all hosts agree on one.
func (sc *fsyncStallCluster) leader(t *testing.T) (id, term uint64) {
	t.Helper()
	deadline := time.Now().Add(raftharness.Bound)
	for {
		id, term = sc.agreedLeader()
		if id != 0 {
			return id, term
		}
		if time.Now().After(deadline) {
			t.Fatalf("hosts never agreed on a leader of shard %d", fsyncStallShardID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// agreedLeader returns the leader and term every host reports, or zero when a
// host knows no leader or two hosts disagree.
func (sc *fsyncStallCluster) agreedLeader() (id, term uint64) {
	for i, host := range sc.Hosts {
		hid, hterm, known, err := host.LeaderIDAndTerm(fsyncStallShardID)
		if err != nil || !known || (i > 0 && (hid != id || hterm != term)) {
			return 0, 0
		}
		id, term = hid, hterm
	}
	return id, term
}

// TestFsyncStall_LeaderKeepsLeadershipThroughFollowerWALStall is SPEC row
// fsync-stall-leadership: both followers of a three-replica shard park in a WAL
// fsync for three election timeouts, a quorum stall, and the leader keeps its
// leadership and term throughout, serving linearizable reads and admin
// requests, while the WAL fsync histogram records the stall. Without
// go-quicraft's heartbeat-under-fsync the followers' heartbeat replies wait
// behind the fsync, CheckQuorum deposes the leader one election timeout in,
// and the term moves.
func TestFsyncStall_LeaderKeepsLeadershipThroughFollowerWALStall(t *testing.T) {
	sc := startFsyncStallCluster(t)
	ctx, cancel := context.WithTimeout(context.Background(), raftharness.Bound)
	defer cancel()

	leaderID, term := sc.leader(t)
	kv, admin := sc.kv[leaderID-1], sc.admin[leaderID-1]
	if err := kv.Put(ctx, "before", []byte("v1")); err != nil {
		t.Fatalf("Put(before): %v", err)
	}
	count0, sum0 := syncwatchtest.HistogramSample(t, database.SharedRaftMetrics().Registry, walFsyncFamily, "", "")

	// Arm both followers, then write: the append each follower makes for the
	// write is the WAL write whose fsync parks. The bound is a failure bound
	// only; the test releases the stall itself.
	var stalls []*raftharness.Stall
	for id, fs := range sc.fs {
		if id != leaderID {
			stalls = append(stalls, fs.Arm(raftharness.Bound))
		}
	}
	defer func() {
		for _, s := range stalls {
			s.Release()
		}
	}()
	putDone := make(chan error, 1)
	go func() { putDone <- kv.Put(ctx, "during", []byte("v2")) }()
	for _, s := range stalls {
		select {
		case <-s.Entered():
		case <-ctx.Done():
			t.Fatal("no follower WAL fsync parked on the armed stall")
		}
	}

	// Hold the stall past three election timeouts, checking at every
	// heartbeat interval that the leader stands and serves.
	heartbeat := time.NewTicker(time.Duration(quicraftconfig.DefaultRTTMillisecond) * time.Millisecond)
	defer heartbeat.Stop()
	for stalledAt := time.Now(); time.Since(stalledAt) < fsyncStallHold; <-heartbeat.C {
		if id, tm := sc.agreedLeader(); id != leaderID || tm != term {
			t.Fatalf("%v into the fsync stall: leader %d term %d, want leader %d term %d",
				time.Since(stalledAt), id, tm, leaderID, term)
		}
		if v, err := kv.Get(ctx, "before"); err != nil || !bytes.Equal(v, []byte("v1")) {
			t.Fatalf("linearizable Get during the fsync stall = %q, %v", v, err)
		}
		peers, err := admin.Peers(ctx)
		if err != nil {
			t.Fatalf("admin Peers during the fsync stall: %v", err)
		}
		if got := peersLeader(peers, fsyncStallShardID); got != leaderID {
			t.Fatalf("admin Peers during the fsync stall reports leader %d, want %d", got, leaderID)
		}
		select {
		case err := <-putDone:
			t.Fatalf("Put committed with both followers' fsyncs parked (err %v): the stall is not real", err)
		default:
		}
	}

	for _, s := range stalls {
		s.Release()
	}
	if err := <-putDone; err != nil {
		t.Fatalf("Put(during) after the stall: %v", err)
	}
	if id, tm := sc.leader(t); id != leaderID || tm != term {
		t.Fatalf("after the fsync stall: leader %d term %d, want leader %d term %d", id, tm, leaderID, term)
	}
	// Put commits on one follower's acknowledgement, so its return says
	// nothing about the other follower: wait for each follower's own
	// collector to report its stalled fsync before reading the histogram.
	for id := range sc.fs {
		if id == leaderID {
			continue
		}
		select {
		case d := <-sc.fsync[id].stalled:
			t.Logf("replica %d: stalled WAL fsync observed at %v", id, d)
		case <-ctx.Done():
			t.Fatalf("replica %d: no WAL fsync of at least %v observed after the stall", id, fsyncStallHold)
		}
	}
	// Both stalled fsyncs reached the collector, so the shared histogram
	// holds both: at least two more observations, and at least the two
	// stall durations more in its sum.
	count1, sum1 := syncwatchtest.HistogramSample(t, database.SharedRaftMetrics().Registry, walFsyncFamily, "", "")
	want := uint64(len(stalls))
	if count1 < count0+want || sum1-sum0 < float64(want)*fsyncStallHold.Seconds() {
		t.Fatalf("WAL fsync histogram went count %d -> %d, sum %.3fs -> %.3fs; want %d stalled fsyncs, each at least %v",
			count0, count1, sum0, sum1, want, fsyncStallHold)
	}
}

// TestFsyncStall_LeaderFsyncsDoNotPark pins the cluster the stall test builds:
// with no stall armed every host's WAL fsyncs through its StallFS and a write
// commits, so a stall the test arms is the only thing that parks one.
func TestFsyncStall_LeaderFsyncsDoNotPark(t *testing.T) {
	sc := startFsyncStallCluster(t)
	ctx, cancel := context.WithTimeout(context.Background(), raftharness.Bound)
	defer cancel()
	leaderID, _ := sc.leader(t)
	if err := sc.kv[leaderID-1].Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(sc.fs) != len(sc.Hosts) {
		t.Fatalf("%d StallFS for %d hosts", len(sc.fs), len(sc.Hosts))
	}
	for id, fs := range sc.fs {
		if fs.Syncs() == 0 {
			t.Fatalf("replica %d: WAL never fsynced through its StallFS", id)
		}
	}
}

func peersLeader(peers *PeersInfo, shardID uint64) uint64 {
	for _, sp := range peers.Shards {
		if sp.ShardID == shardID {
			return sp.LeaderID
		}
	}
	return 0
}

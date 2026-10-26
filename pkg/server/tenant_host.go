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

package server

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// tenantHostShardID is the single local Raft shard hosted by every per-tenant
// IsolationVolume Host. The tenant Host is a single-node, transport-less Raft
// cluster; all of the tenant's databases share this one shard and are isolated
// from one another by the composite (tenant, db) namespace prefix applied by
// the resolver, exactly as databases are isolated on the shared system shard.
const tenantHostShardID = shard.ShardKV

// tenantHost owns a single-node, transport-less per-tenant Raft Host backing an
// IsolationVolume tenant. The Host's WAL and state-machine data live entirely
// under the tenant's DataRoot ({DataRoot}/databases), and every Pebble file it
// writes is encrypted at rest under the tenant's own DEK via the tenant
// barrier. It exposes a full data-plane service set (KV/MVCC/lease/watch/
// timeseries/...) bound to the Host's single shard.
type tenantHost struct {
	tenantID string
	dataRoot string
	host     *quicraft.Host
	adapter  service.Host
	hub      *watch.Hub
	lessor   *lease.Lessor
	services *DatabaseServices
	cancel   context.CancelFunc
	logger   *slog.Logger
}

// buildTenantRaftConfigs builds the Raft host and shard configuration for a
// per-tenant volume Host rooted at dataRoot.
//
// Per-tenant Raft host state lives under {DataRoot}/databases so that the
// "barrier" sibling directory (the IAM Pebble) stays disjoint. The addresses
// are placeholders that quicraft's config validation requires but never uses:
// the Host is built WithoutTransport and performs no network I/O.
//
// Both transport-less Raft tunings apply here without qualification. The Host
// has no network, so go-quicraft's WAN RTT estimate sizes an election budget
// for a link it does not have; and its single shard is started with a
// one-entry member map on a host no peer can reach, so its voting membership
// can never grow and its quorum is permanently itself. See
// cluster.TransportlessRTTMillisecond and cluster.SingleNodeElectionRTT
// (go-qrdb#250).
func buildTenantRaftConfigs(dataRoot string) (quicraftconfig.HostConfig, quicraftconfig.Config) {
	hostRoot := filepath.Join(dataRoot, "databases")
	hostCfg := quicraftconfig.HostConfig{
		WALDir:        filepath.Join(hostRoot, database.TenantHostWALDirName),
		NodeHostDir:   filepath.Join(hostRoot, database.TenantHostNodeHostDirName),
		RaftAddress:   "127.0.0.1:63901", // unused: WithoutTransport disables network I/O
		ListenAddress: "127.0.0.1:63901",
		DeploymentID:  1,
	}
	cluster.ApplyTransportlessHostTuning(&hostCfg)

	shardCfg := quicraftconfig.Config{ShardID: tenantHostShardID, ReplicaID: 1}
	cluster.ApplyTransportlessShardTuning(&shardCfg)

	return hostCfg, shardCfg
}

// newTenantHost builds and starts a per-tenant Raft Host rooted at the tenant's
// DataRoot, encrypting all on-disk state under the tenant barrier's DEK, and
// returns the host wrapper with its bound data-plane services.
//
// The tenant barrier (tb) MUST be unsealed before this is called: StartShard
// opens the encrypted Pebble immediately and a sealed barrier would fail every
// file open with seal.ErrSealed. The caller (TenantHostRegistry) drives the
// barrier through Initialize (create) or Unseal (boot re-attach) first.
func newTenantHost(ctx context.Context, tenantID, dataRoot string, tb *seal.TenantBarrier, importSessionCfg qrdbconfig.ImportSessionConfig, logger *slog.Logger) (*tenantHost, error) {
	if logger == nil {
		logger = slog.Default()
	}

	hostCfg, shardCfg := buildTenantRaftConfigs(dataRoot)

	// Encrypt the Raft WAL/LogDB under the tenant barrier's epoch DEK so the
	// dedicated volume is fully encrypted at rest: the state-machine Pebble is
	// wrapped by the encfs Encryptor below, and the replication log is wrapped
	// here. The underlying EpochBarrier satisfies quicraft.Barrier and must be
	// unsealed (the caller guarantees this).
	host, err := quicraft.NewHost(hostCfg,
		quicraft.WithoutTransport(),
		quicraft.WithBarrier(tb.Barrier().EpochBarrier()),
		quicraft.WithMetrics(database.NewRaftMetrics(
			syncwatch.WithLogger(logger),
			syncwatch.WithAttrs(slog.String("tenant", tenantID), slog.String("wal_dir", hostCfg.WALDir)),
		)),
	)
	if err != nil {
		return nil, &TenantHostError{TenantID: tenantID, Op: "new-host", Err: err}
	}

	// Fresh per-tenant subsystems so leases and watches are isolated from the
	// system shard and from other tenants. The barrier-backed Encryptor wraps
	// every Pebble file the state machine creates with AES-256-GCM keyed by the
	// tenant DEK.
	lessor := lease.NewLessor()
	hub := watch.NewHub()
	createFn := newDataPlaneCreateFunc(tb, lessor, hub)

	members := map[uint64]string{1: hostCfg.RaftAddress}
	if err := host.StartShard(members, false, createFn, shardCfg); err != nil {
		_ = host.Close()
		return nil, &TenantHostError{TenantID: tenantID, Op: "start-shard", Err: err}
	}

	// Wait for the single-node shard to elect itself leader before binding
	// services; until then proposals fail with "not leader". StartShard only
	// registers the replica — leadership is established asynchronously. The
	// wait honours ctx -- the request building the host -- so a caller that
	// gives up is not held for the readiness budget.
	if err := WaitForShardServing(ctx, host, tenantHostShardID); err != nil {
		_ = host.StopShard(tenantHostShardID)
		_ = host.Close()
		return nil, &TenantHostError{TenantID: tenantID, Op: "await-leader", Err: err}
	}

	adapter := raftbackend.New(host)
	hostCtx, cancel := context.WithCancel(context.Background())

	th := &tenantHost{
		tenantID: tenantID,
		dataRoot: dataRoot,
		host:     host,
		adapter:  adapter,
		hub:      hub,
		lessor:   lessor,
		cancel:   cancel,
		logger:   logger,
		services: buildDataPlaneServices(adapter, tenantHostShardID, hub, importSessionCfg),
	}

	// Leader-side lease-expiry sweeper. The tenant host is a single-node
	// cluster, so it is always the leader; the sweeper revokes elapsed leases
	// through consensus, deleting attached keys and notifying watchers.
	if th.services.Lease != nil {
		go th.lessor.RunExpiry(hostCtx, lease.DefaultExpiryInterval,
			func() bool { return true },
			th.services.Lease.Revoke,
			logger,
		)
	}

	logger.Info("per-tenant volume host started",
		slog.String("tenant", tenantID),
		slog.String("data_root", dataRoot),
		slog.Uint64("shard_id", tenantHostShardID))

	return th, nil
}

// tenantShardLeaderTimeout bounds how long newTenantHost waits for the
// single-node tenant shard to elect a leader.
//
// This budget is NOT a tuning knob to widen when the suite goes red. It is
// already ~7x the election's configured cost (go-quicraft computes the election
// timeout as ElectionRTT * RTTMillisecond, and this shard takes the defaults:
// 10 * 200ms = 2s), and a budget padded to survive a thrashing host is a budget
// that can no longer fail for the right reason -- a genuine election regression
// that quintupled the cost would still fit inside it. If this wait times out,
// read the diagnostic on ShardLeaderWaitError before touching this number: it
// says whether the host starved the poller or the shard genuinely never
// elected, and only the second is a reason to change anything here.
const tenantShardLeaderTimeout = 15 * time.Second

// shardLeaderPollInterval is how long waitForShardServingWithin sleeps between polls.
// It is also the yardstick the starvation discriminator measures against: on an
// unloaded host a sleep of this length overruns by microseconds, so a gap of
// many multiples means the goroutine lost the CPU rather than the shard losing
// an election.
const shardLeaderPollInterval = 20 * time.Millisecond

// shardLeaderStarvationFactor is how many poll intervals must elapse between
// two consecutive polls before the wait reports host starvation rather than a
// failed election.
//
// Ten was chosen because it cannot be reached by ordinary timer slop: a 20ms
// sleep would have to overrun to 200ms, i.e. the goroutine did not run for ten
// consecutive scheduling opportunities. It is deliberately a statement about
// the SCHEDULER, not about raft -- raising it would blur the two causes back
// together, which is the whole thing this discriminator exists to separate.
const shardLeaderStarvationFactor = 10

// LeaderSource is what WaitForShardLeader polls: the current leader of a
// shard, as a Raft node reports it. *quicraft.Host satisfies it.
type LeaderSource interface {
	LeaderID(shardID uint64) (uint64, bool, error)
}

// shardLeaderSource is the two things waitForShardServingWithin needs from a
// Host: the current leader of a shard, and whether that leader can serve a
// read. *quicraft.Host satisfies it.
//
// It exists so the wait's own failure paths are testable without booting a raft
// group -- a wait whose three diagnostic branches were only reachable by
// starving a real host could never be tested for the case it exists to report.
// pkg/service.RaftHost is the same narrowing for the same reason.
type shardLeaderSource interface {
	LeaderSource
	// ReadReady makes one linearizable read probe on shardID: nil once the
	// leader can serve a read, quicraft.ErrReadIndexNotReady while it has
	// not yet committed an entry in its term.
	ReadReady(ctx context.Context, shardID uint64) error
}

// shardReadProbeTimeout bounds one ReadIndex probe. It is far below the
// readiness budget, so a probe that hangs costs one poll, not the wait.
const shardReadProbeTimeout = 500 * time.Millisecond

// quicraftShardSource adapts *quicraft.Host to shardLeaderSource.
type quicraftShardSource struct{ host *quicraft.Host }

var _ shardLeaderSource = quicraftShardSource{}

// LeaderID reports the shard's leader.
func (s quicraftShardSource) LeaderID(shardID uint64) (uint64, bool, error) {
	return s.host.LeaderID(shardID)
}

// ReadReady issues one ReadIndex on shardID and waits for its outcome.
func (s quicraftShardSource) ReadReady(ctx context.Context, shardID uint64) error {
	ctx, cancel := context.WithTimeout(ctx, shardReadProbeTimeout)
	defer cancel()
	rs, err := s.host.ReadIndex(ctx, shardID)
	if err != nil {
		return err
	}
	defer rs.Release()
	_, err = rs.Result()
	return err
}

// WaitForShardServing blocks until shardID on host has a leader that can serve
// a linearizable read -- the leader is known AND has committed an entry in its
// term -- within the readiness budget every host-open path shares. A leader
// known is not enough: until its term's first commit, quicraft answers every
// ReadIndex with ErrReadIndexNotReady, so a caller handed the shard any earlier
// fails its first read. It returns *ShardLeaderWaitError when no leader is
// elected, *ShardReadWaitError when the leader never becomes read-ready, or
// ctx's error.
func WaitForShardServing(ctx context.Context, host *quicraft.Host, shardID uint64) error {
	return waitForShardServingWithin(ctx, quicraftShardSource{host: host}, shardID,
		tenantShardLeaderTimeout, shardLeaderPollInterval, realShardLeaderClock{})
}

// WaitForShardLeader blocks until shardID has a known leader, within budget,
// and returns *ShardLeaderWaitError -- naming the shard, the budget, the real
// time spent and whether the host starved the poller -- when none is elected,
// or ctx's error when ctx ends first.
//
// It is the leader phase of WaitForShardServing on its own, for a caller that
// needs a leader but not a linearizable read -- one about to propose, whose
// proposal is ordered after the new leader's first entry in its term anyway.
// The budget is the caller's because the cost of an election is the caller's
// configuration: a caller derives it from the election timeout its shard was
// started with, rather than from a fixed count of polls that means a
// different amount of wall time on every host.
func WaitForShardLeader(ctx context.Context, src LeaderSource, shardID uint64, budget time.Duration) error {
	clock := realShardLeaderClock{}
	return awaitShardLeader(ctx, src, shardID, clock.Now(), budget, shardLeaderPollInterval, clock)
}

// shardLeaderClock is the time source waitForShardServingWithin reads. It is
// exactly the two operations the poll loop performs on time and no more:
// reading the instant, and pausing between polls.
//
// It exists because the loop's own diagnostics are FUNCTIONS OF THE POLL
// SEQUENCE -- how many polls happened, which of them errored, and the longest
// gap between two of them -- while the loop decides when to stop by consulting
// a wall clock. On an unloaded host those two agree closely enough that a test
// can pick a budget and get the poll count it wanted. On a loaded one they do
// not: go-qrdb#312 is a CI run in which a 30ms budget at a 1ms interval bought
// TWO polls instead of the thirty-two an idle host delivers, so a test that
// scripted a failure onto poll 3 never reached poll 3. Scaling the budget up
// moves that flake to a busier host rather than removing it; making the loop's
// clock injectable removes the wall clock from the test's premises entirely.
//
// Production is unchanged: waitForShardServingWithin passes realShardLeaderClock, so
// every number the error reports -- elapsed, longest gap -- is still measured
// against real wall time, which is the whole point of the starvation verdict.
// A fake clock that reported starvation in production would destroy the one
// discriminator this wait exists to provide.
type shardLeaderClock interface {
	// Now returns the current instant. The loop calls it to start the budget,
	// to measure the gap between consecutive polls, and to test the deadline.
	Now() time.Time
	// Sleep pauses for d before the next poll.
	Sleep(d time.Duration)
}

// realShardLeaderClock is the production clock: real wall time and a real
// sleep. It is the only implementation outside the package's own tests.
type realShardLeaderClock struct{}

// Now returns the current wall-clock instant.
func (realShardLeaderClock) Now() time.Time { return time.Now() }

// Sleep blocks the calling goroutine for d.
func (realShardLeaderClock) Sleep(d time.Duration) { time.Sleep(d) }

// waitForShardServingWithin waits, within one budget, first for shardID to
// have a leader and then for that leader to serve a linearizable read. A
// leader elected a moment ago refuses ReadIndex until it has committed an
// entry in its term; a host-open path that returned on "leader known" handed
// its caller a shard whose first read failed with ErrReadIndexNotReady. Every
// host-open path waits through it: the per-tenant volume Host and the
// dedicated per-database raft shards here, and the system shard in the
// composers (WaitForShardServing).
//
// On timeout it returns what it OBSERVED while waiting, not merely that it gave
// up: *ShardLeaderWaitError when no leader was elected (it wraps
// ErrTenantShardNoLeader for errors.Is), *ShardReadWaitError when a leader was
// but never served a read. The bare sentinel this replaced sent every reader
// to the job logs to answer the only question that matters: was the shard
// broken, or was the host too busy to let the poller ask? Five CI incidents
// were diagnosed by hand for want of that one line (go-qrdb#139).
//
// Three causes are distinguished, and the error says which one was measured:
//
//   - The poll ERRORED on one or more attempts. Then the timeout is a symptom
//     and that error is the finding. The error COUNT is carried too, because
//     "could not be asked on 3 of 47 polls" and "on 47 of 47" are different
//     problems with different investigations.
//   - The poller was STARVED: some gap between consecutive polls exceeded
//     shardLeaderStarvationFactor intervals, so the wall-clock budget was never
//     really spent asking. The shard may have been fine.
//   - Neither: the poller ran attentively for the whole budget and the shard
//     never elected, or its leader never committed in its term. THAT is a real
//     defect, and it is the case this wait exists to catch. Do not widen the
//     budget to make it go away.
//
// The budget, poll interval and clock are parameters so the timeout and
// starvation paths are exercised in milliseconds instead of fifteen seconds.
// The clock is a parameter rather than a package-level variable ON PURPOSE: a
// mutable package global shared by every test in pkg/server is a data race the
// moment two of them run concurrently -- see shardLeaderClock.
func waitForShardServingWithin(ctx context.Context, host shardLeaderSource, shardID uint64, timeout, interval time.Duration, clock shardLeaderClock) error {
	start := clock.Now()
	deadline := start.Add(timeout)

	// Each phase's outcome is judged before ctx: a poll that found what it
	// waited for is a finished wait even when ctx was cancelled at the same
	// moment. Only an unfinished wait reports ctx's error.
	if err := awaitShardLeader(ctx, host, shardID, start, timeout, interval, clock); err != nil {
		return err
	}

	read, ok := pollShard(ctx, clock, "shard to serve reads", shardID, start, deadline, interval, func() (bool, error) {
		err := host.ReadReady(ctx, shardID)
		if err == nil {
			return true, nil
		}
		// The expected "not yet": the leader has not committed in its term.
		if errors.Is(err, quicraft.ErrReadIndexNotReady) {
			return false, nil
		}
		return false, err
	})
	if !ok {
		if err := ctx.Err(); err != nil {
			return err
		}
		return &ShardReadWaitError{
			ShardID:  shardID,
			Timeout:  timeout,
			Interval: interval,
			Elapsed:  read.elapsed,
			Polls:    read.polls,
			MaxGap:   read.maxGap,
			ErrCount: read.errCount,
			FirstErr: read.firstErr,
			LastErr:  read.lastErr,
		}
	}
	return nil
}

// awaitShardLeader is the leader phase of every wait in this file: it polls
// src until shardID has a leader or the budget that began at start runs out.
// waitForShardServingWithin runs it before its read phase, against the same
// start and budget; WaitForShardLeader runs it alone.
//
// It returns nil once a leader is known -- even when ctx was cancelled at the
// same moment, since a finished wait is finished -- ctx's error when the wait
// was cancelled before it finished, and otherwise *ShardLeaderWaitError
// carrying what the poll observed.
func awaitShardLeader(ctx context.Context, src LeaderSource, shardID uint64, start time.Time, timeout, interval time.Duration, clock shardLeaderClock) error {
	leader, ok := pollShard(ctx, clock, "shard leader", shardID, start, start.Add(timeout), interval, func() (bool, error) {
		_, ok, err := src.LeaderID(shardID)
		return err == nil && ok, err
	})
	if ok {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return &ShardLeaderWaitError{
		ShardID:  shardID,
		Timeout:  timeout,
		Interval: interval,
		Elapsed:  leader.elapsed,
		Polls:    leader.polls,
		MaxGap:   leader.maxGap,
		ErrCount: leader.errCount,
		FirstErr: leader.firstErr,
		LastErr:  leader.lastErr,
	}
}

// pollStats is what pollShard observed: the measurement the readiness errors
// report.
type pollStats struct {
	elapsed  time.Duration
	polls    int
	maxGap   time.Duration
	errCount int
	firstErr error
	lastErr  error
}

// pollShard calls poll every interval until it reports done, ctx ends, or the
// clock passes deadline, and reports whether it finished and what it saw.
// Elapsed is measured from start, the beginning of the whole readiness wait.
// While it waits it logs, every StartupWaitReportInterval, what it is waiting
// for -- what, on shardID -- and what the polls have seen so far.
func pollShard(ctx context.Context, clock shardLeaderClock, what string, shardID uint64, start, deadline time.Time, interval time.Duration, poll func() (bool, error)) (pollStats, bool) {
	var st pollStats
	last := clock.Now()
	lastReport := start
	for {
		now := clock.Now()
		if gap := now.Sub(last); gap > st.maxGap {
			st.maxGap = gap
		}
		last = now

		st.polls++
		done, err := poll()
		if done {
			return st, true
		}
		// Kept rather than discarded: it is the difference between "the shard
		// did not get there" and "the shard could not be asked".
		//
		// GUARDED ON err != nil, and that guard is load-bearing. An
		// unconditional `lastErr = err` is ERASED by the very next poll that
		// answers cleanly -- so on the INTERMITTENT sequence (error, not-yet,
		// not-yet, ... timeout) the failure is lost and the wait reports the
		// wrong cause, which is the class of mistake it exists to prevent.
		if err != nil {
			st.errCount++
			if st.firstErr == nil {
				st.firstErr = err
			}
			st.lastErr = err
		}

		// Deliberately a FRESH read rather than the `now` taken above the
		// poll: a poll is a call into raft and can itself consume the budget,
		// so a loop that judged the deadline on a pre-poll timestamp would run
		// one extra poll past it and under-report Elapsed.
		end := clock.Now()
		st.elapsed = end.Sub(start)
		if end.After(deadline) || ctx.Err() != nil {
			return st, false
		}
		if end.Sub(lastReport) >= StartupWaitReportInterval {
			lastReport = end
			slog.Warn("startup still waiting",
				slog.String("for", what),
				slog.Uint64("shard_id", shardID),
				slog.Int("polls", st.polls),
				slog.Duration("elapsed", st.elapsed),
				slog.Duration("budget", deadline.Sub(start)),
				slog.Any("last_error", st.lastErr))
		}
		clock.Sleep(interval)
	}
}

// newDataPlaneCreateFunc builds the MVCC state-machine CreateFunc for a
// dedicated per-tenant shard. Every Pebble file the state machine creates is
// wrapped with AES-256-GCM keyed by the tenant DEK via the barrier-backed
// at-rest Encryptor, and the shard gets a fresh Lessor and watch Hub so leases
// and watches are isolated from the system shard and from other tenants. It is
// shared by the IsolationVolume per-tenant Host (tenant_host.go) and the
// IsolationRaft dedicated shards on the shared host (raft_backend_factory.go).
func newDataPlaneCreateFunc(tb *seal.TenantBarrier, lessor *lease.Lessor, hub *watch.Hub) sm.CreateFunc {
	enc := atrest.New(tb.Barrier())
	return store.NewMVCCCreateFuncWith(
		[]store.Option{store.WithEncryptor(enc)},
		store.WithLessor(lessor),
		// A registry of its own per state machine, so the many tenant and
		// database groups on a node never share heads.
		store.WithLogs(),
		store.WithWatchHub(hub),
		store.WithTimeSeriesEnabled(),
	)
}

// buildDataPlaneServices binds the full data-plane service set to a single
// shard on the given host and watch hub. It mirrors the core binding block of
// lifecycle.BuildServices for the system shard and is shared by the per-tenant
// volume Host and the dedicated per-database raft shards. Vector storage is
// intentionally omitted: it requires a separate per-tenant Pebble vector store
// rather than the Raft state machine, which is out of scope here.
//
// importSessionCfg bounds a chunked import session on this shard
// (config.ImportSession, "import_session:" in YAML); the zero value
// resolves to the package defaults WithImportSessionLimits documents.
//
// mvccOpts configure the MVCC service: a dedicated per-database raft shard
// passes service.WithDedicatedKeyspace, because the shard is that one
// database's whole keyspace; the volume Host, which every database of the
// tenant shares, passes nothing, so it refuses the empty compaction prefix.
func buildDataPlaneServices(host service.Host, shardID uint64, hub *watch.Hub, importSessionCfg qrdbconfig.ImportSessionConfig, mvccOpts ...service.MVCCKVServiceOption) *DatabaseServices {
	kv := service.NewKVService(host, shardID)
	mvcc := service.NewMVCCKVService(host, shardID, mvccOpts...)
	idx := service.NewIndexService(host, shardID)
	txn := service.NewTransactionService(host, shardID)
	return &DatabaseServices{
		Name:    "",
		ShardID: shardID,
		KV:      kv,
		MVCC:    mvcc,
		Batch: service.NewBatchService(host, shardID, service.WithImportSessionLimits(
			importSessionCfg.ResolvedMaxSessionBytes(),
			importSessionCfg.ResolvedMaxChunksPerSession(),
			importSessionCfg.ResolvedMaxSessionsPerTenant(),
			importSessionCfg.ResolvedSessionTTLSeconds(),
		)),
		Transaction: txn,
		Lease:       service.NewLeaseService(host, shardID),
		TimeSeries:  service.NewTimeSeriesService(host, shardID),
		Index:       idx,
		Watch:       service.NewWatchService(hub),
		// SequenceServicer backs Collection's IDStrategyIncrement document
		// IDs with a Raft-replicated compare-and-swap counter instead of the
		// process-local counter that would otherwise restart at 0 on every
		// process restart and diverge across replicas -- see
		// service.CollectionService.WithSequenceService.
		//
		// This Collection is built over the RAW kv/idx, which is correct for
		// the dedicated per-database raft shard (the shard is the tenant's
		// keyspace) and is NOT what a prefix/volume tenant is served: there
		// restServiceRegistry.applyComposite rebuilds it over the namespaced
		// engines, because a struct copy would carry these raw ones through.
		Collection: service.NewCollectionServiceWithIndex(kv, idx).
			WithSequenceService(service.NewSequenceService(mvcc, txn)),
	}
}

// Close stops the tenant Host's lease sweeper, stops its shard, and closes the
// Host, releasing the Pebble lock on {DataRoot}/databases so a subsequent boot
// (or test re-open) can re-attach to the same on-disk location.
func (t *tenantHost) Close() error {
	if t.cancel != nil {
		t.cancel()
	}
	var firstErr error
	if err := t.host.StopShard(tenantHostShardID); err != nil {
		firstErr = &TenantHostError{TenantID: t.tenantID, Op: "stop-shard", Err: err}
	}
	if err := t.host.Close(); err != nil && firstErr == nil {
		firstErr = &TenantHostError{TenantID: t.tenantID, Op: "close-host", Err: err}
	}
	if firstErr == nil && t.logger != nil {
		t.logger.Info("per-tenant volume host closed", slog.String("tenant", t.tenantID))
	}
	return firstErr
}

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
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// DefaultCompactionInterval is how often the compaction scheduler runs the
// scheduled-compaction policies.
const DefaultCompactionInterval = time.Minute

// compactionPassTimeout bounds the work one pass spends on one store.
const compactionPassTimeout = 30 * time.Second

// compactionStore is a store's raw shard service as the scheduler drives
// it: its replicated compaction table, its current revision and its
// compactions. *service.MVCCKVService satisfies it.
type compactionStore interface {
	service.CompactionStateStore
	service.PrefixCompactor
	CurrentRevision(ctx context.Context) (int64, error)
}

var _ compactionStore = (*service.MVCCKVService)(nil)

// scheduledStore is one store whose compaction table this node runs.
type scheduledStore struct {
	// name identifies the store in logs.
	name string
	// store is the store's raw shard service.
	store compactionStore
	// leader, when set, gates the store: the table is run only while this
	// node leads its shard, so exactly one node runs each policy. nil for a
	// store that is this node's alone.
	leader service.ShardLeader
}

// compactionScheduler runs the scheduled-compaction policies. A policy and
// its samples are a replicated record in the store that holds the data they
// govern (pkg/compaction); the scheduler holds no state of its own. Each pass
// visits the stores this node may act on -- the shared store and every raft
// group only where this node leads the shard, a node-local store where it is
// started -- reads each store's compaction table, and for each enabled
// policy proposes a sample (a period policy, under the sample cap) and the
// compaction its policy targets. After a leader change the new leader
// continues from the replicated samples, so a retention clock never starts
// over; in standalone mode the single node leads.
//
// Every proposal for a database's policy carries that database's drop fence
// for the record's incarnation (service.CompactionScheduleGuard), so a record
// that outlived its database, or one a re-created database of the name never
// wrote, compacts nothing. Nothing is compacted implicitly: a store with no
// record is never compacted. See docs/key-value/engineering.md "Scheduled
// compaction".
type compactionScheduler struct {
	stores   func(ctx context.Context) []scheduledStore
	interval time.Duration
	now      func() time.Time
	logger   *slog.Logger

	// ticks, when non-nil, replaces the interval ticker as run's pass
	// trigger, so a test drives each pass with one send.
	ticks <-chan time.Time

	running atomic.Bool
}

// run compacts on the configured interval until ctx is cancelled.
func (c *compactionScheduler) run(ctx context.Context) {
	ticks := c.ticks
	if ticks == nil {
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			c.passOnce(ctx)
		}
	}
}

// passOnce runs every policy this node may run once and returns how many
// scopes it compacted. It is a no-op when a pass is already running.
func (c *compactionScheduler) passOnce(ctx context.Context) int {
	if !c.running.CompareAndSwap(false, true) {
		return 0
	}
	defer c.running.Store(false)

	compacted := 0
	for _, st := range c.stores(ctx) {
		if st.leader != nil && !st.leader.LeadsShard() {
			continue
		}
		compacted += c.runStore(ctx, st)
	}
	return compacted
}

// runStore runs every enabled policy in one store's compaction table.
func (c *compactionScheduler) runStore(ctx context.Context, st scheduledStore) int {
	sctx, cancel := context.WithTimeout(ctx, compactionPassTimeout)
	defer cancel()

	states, err := st.store.CompactionStates(sctx)
	if err != nil {
		c.logger.Warn("compaction scheduler: compaction table unreadable",
			slog.String("store", st.name), slog.Any("error", err))
		return 0
	}
	compacted := 0
	for scope, state := range states {
		if state == nil || !state.Policy.Enabled() {
			continue
		}
		if c.runPolicy(sctx, st, scope, state) {
			compacted++
		}
	}
	return compacted
}

// runPolicy runs one scope's policy and reports whether it compacted.
func (c *compactionScheduler) runPolicy(ctx context.Context, st scheduledStore, scope string, state *compaction.State) bool {
	attrs := []any{slog.String("store", st.name), slog.String("tenant_id", state.Tenant), slog.String("database", state.Database)}

	guard, guarded, err := service.CompactionScheduleGuard(state)
	if err != nil {
		c.logger.Warn("compaction scheduler: drop-fence guard", append(attrs, slog.Any("error", err))...)
		return false
	}
	if guarded {
		ctx = service.WithWriteGuard(ctx, guard)
	}

	current, err := st.store.CurrentRevision(ctx)
	if err != nil {
		c.logger.Warn("compaction scheduler: current revision unavailable", append(attrs, slog.Any("error", err))...)
		return false
	}
	now := c.now()

	// A period policy samples the store's revision, under the cap, into the
	// replicated record; the state machine keeps only what the cap admits,
	// so a repeat is harmless.
	if c.dueForSample(state, now) {
		if err := st.store.RecordCompactionSample(ctx, scope, now, current); err != nil {
			c.logWriteRefusal("sample", err, attrs)
			return false
		}
	}

	target := state.Target(now, current)
	if target < 1 {
		return false
	}
	err = c.compact(ctx, st.store, scope, target)
	var already *service.RevisionCompactedError
	switch {
	case err == nil:
		c.logger.Info("compaction scheduler: compacted",
			append(attrs, slog.String("mode", string(state.Policy.Mode)), slog.Int64("revision", target))...)
		return true
	case errors.As(err, &already):
		// Compacted to this revision already -- a previous pass, or a former
		// leader's proposal that landed. Floors only move forward.
		return false
	default:
		c.logWriteRefusal("compaction", err, append(attrs, slog.Int64("revision", target)))
		return false
	}
}

// dueForSample reports whether a period policy's newest sample is a sample
// interval old -- or there is none -- so a proposal is worth making.
func (c *compactionScheduler) dueForSample(state *compaction.State, now time.Time) bool {
	period := state.Policy.RetainDuration()
	if period <= 0 {
		return false
	}
	n := len(state.Samples)
	return n == 0 || now.Sub(time.Unix(0, state.Samples[n-1].At)) >= period/compaction.SampleResolution
}

// compact compacts scope to target: the node keyspace, or one database's
// prefix.
func (c *compactionScheduler) compact(ctx context.Context, store compactionStore, scope string, target int64) error {
	if scope == compaction.NodeScope {
		node, ok := store.(service.NodeKeyspaceCompactor)
		if !ok {
			return &service.CompactScopeRequiredError{}
		}
		return node.CompactNodeKeyspace(ctx, target)
	}
	return store.CompactPrefix(ctx, scope, target)
}

// logWriteRefusal logs a refused sample or compaction. A database that was
// dropped, or dropped and created again, refuses through its drop fence: the
// record outlived its database, which the drop's reclamation clears.
func (c *compactionScheduler) logWriteRefusal(what string, err error, attrs []any) {
	var dropping *database.DroppingError
	var refused *service.WriteGuardRefusedError
	if errors.As(err, &dropping) || errors.As(err, &refused) {
		c.logger.Debug("compaction scheduler: "+what+" refused by the database's drop fence; skipped",
			append(attrs, slog.Any("error", err))...)
		return
	}
	c.logger.Warn("compaction scheduler: "+what+" failed", append(attrs, slog.Any("error", err))...)
}

// compactionStores returns the stores this server's node runs the policies
// of -- the same set, and the same gating, as the collection sweeper
// (collectionSweepTargets):
//
//   - each shared bundle's store, gated by leadership of its shard;
//   - each started volume tenant's Host, which is node-local;
//   - each started raft group, gated by leadership of the group.
//
// A store that keeps no MVCC history holds no compaction table and is left
// out.
func (s *Server) compactionStores(ctx context.Context) []scheduledStore {
	var out []scheduledStore
	add := func(name string, mvcc service.MVCCKVServicer, gated bool) {
		store, ok := mvcc.(compactionStore)
		if !ok {
			return
		}
		if r, ok := mvcc.(service.RevisionHistoryReporter); ok && !r.KeepsRevisionHistory() {
			return
		}
		st := scheduledStore{name: name, store: store}
		if gated {
			leader, ok := mvcc.(service.ShardLeader)
			if !ok {
				// A gated store that cannot report leadership is run by no
				// node rather than by every node.
				return
			}
			st.leader = leader
		}
		out = append(out, st)
	}

	for _, svcs := range s.sharedStoreBundles() {
		add("shared", svcs.MVCC, true)
	}
	if s.opts.tenantHosts != nil {
		for tenantID, svcs := range s.opts.tenantHosts.startedServices() {
			if svcs != nil {
				add("tenant host "+tenantID, svcs.MVCC, false)
			}
		}
	}
	if s.opts.raftBackend != nil {
		for _, shard := range s.opts.raftBackend.ListStarted() {
			svcs, err := s.opts.raftBackend.Services(ctx, shard.TenantID, shard.Database)
			if err != nil || svcs == nil {
				continue
			}
			add("raft "+tenant.ComposeNamespace(shard.TenantID, shard.Database), svcs.MVCC, true)
		}
	}
	return out
}

// sharedStoreBundles returns every service bundle the registry's shared path
// is built over -- the default bundle and each one registered by name --
// de-duplicated by identity.
func (s *Server) sharedStoreBundles() []*DatabaseServices {
	if s.opts.dbRegistry == nil {
		return nil
	}
	var out []*DatabaseServices
	seen := make(map[*DatabaseServices]bool)
	add := func(svcs *DatabaseServices) {
		if svcs == nil || seen[svcs] {
			return
		}
		seen[svcs] = true
		out = append(out, svcs)
	}
	add(s.opts.dbRegistry.Default())
	for _, name := range s.opts.dbRegistry.List() {
		add(s.opts.dbRegistry.Get(name))
	}
	return out
}

// WithCompactionInterval overrides how often the compaction scheduler runs
// the scheduled-compaction policies. A non-positive value keeps
// DefaultCompactionInterval.
func WithCompactionInterval(interval time.Duration) ServerOption {
	return func(o *serverOptions) {
		if interval > 0 {
			o.compactionInterval = interval
		}
	}
}

// WithCompactionClock sets the time source the compaction scheduler stamps a
// period policy's revision samples with and ages them by against the policy's
// retain period. The samples are replicated, so every node of a cluster must
// be given the same clock. Nil keeps the real clock.
func WithCompactionClock(c clock.Clock) ServerOption {
	return func(o *serverOptions) { o.compactionClock = c }
}

// startCompactionScheduler starts the scheduler. It runs on every node; each
// pass acts only on the stores this node leads or holds alone.
func (s *Server) startCompactionScheduler() {
	interval := s.opts.compactionInterval
	if interval <= 0 {
		interval = DefaultCompactionInterval
	}
	now := time.Now
	if c := s.opts.compactionClock; c != nil {
		now = c.Now
	}
	scheduler := &compactionScheduler{
		stores:   s.compactionStores,
		interval: interval,
		now:      now,
		logger:   s.logger,
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.compactorCancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		scheduler.run(ctx)
	}()
}

// stopCompactionScheduler stops the scheduler started by
// startCompactionScheduler. Stop waits for it through the server's wait
// group.
func (s *Server) stopCompactionScheduler() {
	if s.compactorCancel != nil {
		s.compactorCancel()
		s.compactorCancel = nil
	}
}

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

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// DefaultCollectionSweepInterval is how often the collection sweeper looks for
// collections an interrupted operation left CREATING or DROPPING.
const DefaultCollectionSweepInterval = time.Minute

// DefaultCollectionStaleAfter is how long a collection may sit CREATING or
// DROPPING before the sweeper treats the operation that put it there as
// abandoned. It is far longer than any create -- which builds indexes over an
// empty collection -- and long enough that a large drop still running is
// merely repeated, never contradicted: every sweep step is idempotent.
const DefaultCollectionStaleAfter = 5 * time.Minute

// DefaultCollectionSweepTargetTimeout bounds the work one pass spends on one
// database. A teardown of a very large collection that runs past it resumes
// on the next pass -- every step is idempotent -- and the databases after it
// in the pass are swept in the meantime rather than waiting behind it.
const DefaultCollectionSweepTargetTimeout = 30 * time.Second

// collectionSweepWarnAfter is how many consecutive passes a database holding
// journaled collection work may fail to resolve before the sweeper logs it at
// Warn rather than Debug. One failed pass is routine -- a barrier still
// sealed after a restart, a leadership change mid-pass -- and a stuck
// collection that stays unreachable for several is something an operator has
// to see.
const collectionSweepWarnAfter = 3

// collectionSweepTarget is one (tenant, database) whose collections the
// sweeper examines.
//
// A target found through the collection work journal carries the journal
// entries that named it and the raw KV they were read from: it is resolved
// without the node-local catalog (restServiceRegistry.journaledCollectionService),
// and after the sweep each entry whose collection is active or gone is
// settled -- compare-and-deleted -- from that KV.
type collectionSweepTarget struct {
	Tenant   string
	Database string
	work     []collectionWorkItem
	workKV   service.KVServicer
}

// journaled reports whether the target was found through the work journal.
func (t collectionSweepTarget) journaled() bool {
	return t.workKV != nil
}

// key identifies the target for the unresolvable-database counter.
func (t collectionSweepTarget) key() string {
	return t.Tenant + "/" + t.Database
}

// collectionSweeper finishes collections left in a transitional lifecycle
// state, so a crashed or failed create or drop never needs an operator to
// find it and re-issue it by hand (go-qrdb#415, go-qrdb#425).
//
// Each pass resolves every target's collection service -- a journaled target
// through the registry's journaled resolver, every other one through the same
// service.Registry the transports use -- so it sees exactly the namespaced
// keyspace a client would, and a sealed tenant is skipped rather than
// bypassed -- and calls service.CollectionSweeper.SweepStuckCollections on it.
//
// Correctness does not rest on only one node sweeping a database: every step
// of a sweep is idempotent or guarded, so two nodes sweeping one collection
// across a leadership change repeat work and never disagree, and a teardown
// counts only the documents its own guarded deletes removed, so two of them
// never release one document's quota twice. What the leadership gating in
// collectionSweepTargets saves is every other replica's scans and proposals.
type collectionSweeper struct {
	registry   service.Registry
	targets    func(ctx context.Context) ([]collectionSweepTarget, error)
	interval   time.Duration
	staleAfter time.Duration
	// targetTimeout bounds one database's sweep; zero uses
	// DefaultCollectionSweepTargetTimeout.
	targetTimeout time.Duration
	logger        *slog.Logger
	// ticks, when non-nil, replaces the interval ticker as run's pass
	// trigger. Tests inject it so each pass is one send rather than a race
	// against a wall-clock ticker that can fire again before cancellation
	// is observed.
	ticks   <-chan time.Time
	running atomic.Bool
	// unresolved counts consecutive passes a journaled target failed to
	// resolve. Passes never overlap (running), so it needs no lock of its own.
	unresolved map[string]int
}

// run sweeps on the configured interval until ctx is cancelled. Passes never
// overlap.
func (s *collectionSweeper) run(ctx context.Context) {
	ticks := s.ticks
	if ticks == nil {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			s.sweepOnce(ctx)
		}
	}
}

// sweepOnce performs one pass and returns how many collections it finished.
// It is a no-op returning zero when a pass is already running.
func (s *collectionSweeper) sweepOnce(ctx context.Context) int {
	if !s.running.CompareAndSwap(false, true) {
		return 0
	}
	defer s.running.Store(false)

	targets, err := s.targets(ctx)
	if err != nil {
		s.logger.Warn("collection sweeper: target enumeration failed", slog.Any("error", err))
		return 0
	}
	total := 0
	for _, t := range targets {
		total += s.sweepTarget(ctx, t)
	}
	return total
}

// sweepTarget sweeps one target and returns how many collections it finished.
func (s *collectionSweeper) sweepTarget(ctx context.Context, t collectionSweepTarget) int {
	svc, err := s.resolve(t)
	if err != nil {
		s.noteUnresolved(t, err)
		return 0
	}
	delete(s.unresolved, t.key())
	sweeper, ok := svc.(service.CollectionSweeper)
	if !ok {
		return 0
	}
	timeout := s.targetTimeout
	if timeout <= 0 {
		timeout = DefaultCollectionSweepTargetTimeout
	}
	tctx, cancel := context.WithTimeout(tenant.WithTenantID(ctx, t.Tenant), timeout)
	defer cancel()
	result, err := sweeper.SweepStuckCollections(tctx, t.Database, s.staleAfter)
	if result.Swept > 0 || result.FreedBytes > 0 {
		s.logger.Info("collection sweeper: finished interrupted collection operations",
			slog.String("tenant_id", t.Tenant), slog.String("database", t.Database),
			slog.Int("collections", result.Swept), slog.Int64("freed_bytes", result.FreedBytes))
	}
	if err != nil {
		s.logger.Warn("collection sweeper: sweep failed",
			slog.String("tenant_id", t.Tenant), slog.String("database", t.Database), slog.Any("error", err))
	}
	s.settleWork(tctx, t, svc)
	return result.Swept
}

// resolve returns a target's collection service: a journaled target through
// the registry's journaled resolver, which does not consult the node-local
// catalog, and every other target through the registry as a request would.
func (s *collectionSweeper) resolve(t collectionSweepTarget) (service.CollectionServicer, error) {
	if !t.journaled() {
		return s.registry.CollectionService(t.Tenant, t.Database)
	}
	resolver, ok := s.registry.(journaledCollectionResolver)
	if !ok {
		return nil, &CollectionWorkNotServedError{TenantID: t.Tenant, Database: t.Database}
	}
	return resolver.journaledCollectionService(t.Tenant, t.Database)
}

// noteUnresolved logs a target that could not be resolved. A tenant whose
// barrier is sealed, or a database without a collection surface, is not
// sweepable now and a later pass tries again, so one failure is Debug. A
// journaled target holds collections known to have entered a transitional
// state; one that stays unresolvable for collectionSweepWarnAfter passes is
// logged at Warn, because its stuck collections are going nowhere.
func (s *collectionSweeper) noteUnresolved(t collectionSweepTarget, err error) {
	level := slog.LevelDebug
	if t.journaled() {
		if s.unresolved == nil {
			s.unresolved = make(map[string]int)
		}
		s.unresolved[t.key()]++
		if s.unresolved[t.key()] >= collectionSweepWarnAfter {
			level = slog.LevelWarn
		}
	}
	s.logger.Log(context.Background(), level, "collection sweeper: skipping unresolvable database",
		slog.String("tenant_id", t.Tenant), slog.String("database", t.Database),
		slog.Int("pending_collections", len(t.work)), slog.Int("consecutive_failures", s.unresolved[t.key()]),
		slog.Any("error", err))
}

// settleWork removes the journal entries of a swept target whose collection
// is no longer in a transitional state -- active, or gone. Each removal is a
// compare-and-delete of the exact entry scanned, so an entry a later
// transition re-recorded survives. An entry whose collection is still
// transitional, or whose state cannot be read, stays for the next pass.
func (s *collectionSweeper) settleWork(ctx context.Context, t collectionSweepTarget, svc service.CollectionServicer) {
	for _, item := range t.work {
		info, err := svc.GetCollectionInfo(ctx, item.entry.Database, item.entry.Name)
		var notFound *service.CollectionNotFoundError
		settled := errors.As(err, &notFound) || (err == nil && info.State == service.CollectionStateActive)
		if !settled {
			continue
		}
		if _, err := t.workKV.CompareAndDelete(ctx, item.key, item.raw); err != nil {
			s.logger.Debug("collection sweeper: could not settle a work entry",
				slog.String("tenant_id", t.Tenant), slog.String("database", t.Database), slog.Any("error", err))
		}
	}
}

// collectionSweepTargets enumerates the databases whose collections this node
// sweeps: every database that can hold collection work, that this node
// already SERVES, and -- where the store is replicated -- that it LEADS.
//
// "Already serves" is deliberate: resolving a database starts what serving it
// needs -- a raft-isolated tenant's dedicated shard, a volume-isolated
// tenant's own Host -- and the sweeper must not be the reason a node starts
// either. "Leads" makes it one node per replicated store.
//
//   - The shared replicated store (every prefix- and barrier-isolated
//     tenant's databases): swept from its leader, as the
//     WithCollectionSweepLeader probe reports (a composition without one is a
//     single node). Its databases are found from the collection work journal
//     IN that store, never from this node's catalog: a prefix tenant's named
//     database is recorded only in the catalog of the node that created it,
//     while its data -- and its journal -- is replicated.
//   - A volume tenant's own Host is node-local: swept on every node where it is
//     started, from the journal in that Host's store.
//   - A raft tenant's dedicated shard: swept where this node's replica leads it
//     (RaftBackendFactory.LeadsShard).
//   - A database with its own node-local dedicated store: swept on every node
//     whose catalog holds it, with no leadership gating -- each node's store is
//     its own.
//
// A journal entry whose tenant no longer exists, and that is older than the
// staleness threshold, is removed: the tenant's data went with it.
func (s *Server) collectionSweepTargets(ctx context.Context) ([]collectionSweepTarget, error) {
	isolation := map[string]tenant.IsolationType{tenantid.System: tenant.IsolationPrefix}
	var known map[string]bool
	if s.opts.tenantSvc != nil {
		infos, err := s.opts.tenantSvc.ListTenants(ctx)
		if err != nil {
			return nil, err
		}
		known = map[string]bool{tenantid.System: true}
		for i := range infos {
			isolation[infos[i].ID] = infos[i].EffectiveIsolationType()
			known[infos[i].ID] = true
		}
	}

	var out []collectionSweepTarget
	if s.opts.collectionSweepLeader == nil || s.opts.collectionSweepLeader() {
		for _, kv := range s.sharedStoreKVs() {
			targets, err := s.journaledTargets(ctx, kv, known)
			if err != nil {
				return nil, err
			}
			out = append(out, targets...)
		}
	}
	if s.opts.tenantHosts != nil {
		for _, svcs := range s.opts.tenantHosts.startedServices() {
			if svcs == nil || svcs.KV == nil {
				continue
			}
			targets, err := s.journaledTargets(ctx, svcs.KV, known)
			if err != nil {
				return nil, err
			}
			out = append(out, targets...)
		}
	}
	if s.opts.raftBackend != nil {
		for _, shard := range s.opts.raftBackend.ListStarted() {
			if isolation[shard.TenantID] == tenant.IsolationRaft && s.opts.raftBackend.LeadsShard(shard.TenantID, shard.Database) {
				out = append(out, collectionSweepTarget{Tenant: shard.TenantID, Database: shard.Database})
			}
		}
	}
	if s.opts.dbManager != nil {
		for _, info := range s.opts.dbManager.List() {
			dbase, err := s.opts.dbManager.Get(info.Name)
			if err != nil {
				continue
			}
			if cfg := dbase.Config(); !servesFromNodeLocalStore(&cfg) {
				continue
			}
			owner := info.TenantID
			if owner == "" {
				owner = tenantid.System
			}
			out = append(out, collectionSweepTarget{Tenant: owner, Database: info.Name})
		}
	}
	return out, nil
}

// sharedStoreKVs returns the raw KV of every bundle the registry's shared
// path is built over (sharedStoreBundles). A KV is not used as a map key,
// since an implementation need not be comparable: the bundles are
// de-duplicated by identity instead.
func (s *Server) sharedStoreKVs() []service.KVServicer {
	var out []service.KVServicer
	for _, svcs := range s.sharedStoreBundles() {
		if svcs.KV != nil {
			out = append(out, svcs.KV)
		}
	}
	return out
}

// journaledTargets groups the collection work journal in kv into one target
// per (tenant, database). known, when non-nil, is the replicated tenant list:
// an entry of a tenant not in it, older than the staleness threshold, is
// removed rather than swept.
func (s *Server) journaledTargets(ctx context.Context, kv service.KVServicer, known map[string]bool) ([]collectionSweepTarget, error) {
	items, err := scanCollectionWork(ctx, kv)
	if err != nil {
		return nil, err
	}
	staleAfter := s.opts.collectionStaleAfter
	if staleAfter <= 0 {
		staleAfter = DefaultCollectionStaleAfter
	}
	index := make(map[string]int)
	var out []collectionSweepTarget
	for _, item := range items {
		if known != nil && !known[item.entry.Tenant] {
			if time.Since(item.entry.RecordedAt) >= staleAfter {
				_, _ = kv.CompareAndDelete(ctx, item.key, item.raw)
			}
			continue
		}
		key := item.entry.Tenant + "/" + item.entry.Database
		i, ok := index[key]
		if !ok {
			i = len(out)
			index[key] = i
			out = append(out, collectionSweepTarget{Tenant: item.entry.Tenant, Database: item.entry.Database, workKV: kv})
		}
		out[i].work = append(out[i].work, item)
	}
	return out, nil
}

// WithCollectionSweep overrides how often the collection sweeper runs and how
// long a collection may stay CREATING or DROPPING before it is finished.
// Non-positive values keep the defaults (DefaultCollectionSweepInterval,
// DefaultCollectionStaleAfter).
func WithCollectionSweep(interval, staleAfter time.Duration) ServerOption {
	return func(o *serverOptions) {
		if interval > 0 {
			o.collectionSweepInterval = interval
		}
		if staleAfter > 0 {
			o.collectionStaleAfter = staleAfter
		}
	}
}

// WithCollectionSweepLeader sets the probe the collection sweeper asks, each
// pass, whether this node leads the system shard -- the one serving every
// prefix-isolated tenant's databases -- so those databases are swept by one
// node rather than by every replica. Without it the server is taken to be a
// single node that leads everything it serves.
func WithCollectionSweepLeader(leads func() bool) ServerOption {
	return func(o *serverOptions) {
		o.collectionSweepLeader = leads
	}
}

// startCollectionSweeper starts the sweeper when the server resolves
// collections through a registry. A registry-less composition serves one
// process-wide collection service with no tenant or database enumeration to
// drive, and its interrupted operations are finished by re-issuing them.
func (s *Server) startCollectionSweeper() {
	registry := s.buildServiceRegistry()
	if registry == nil {
		return
	}
	interval := s.opts.collectionSweepInterval
	if interval <= 0 {
		interval = DefaultCollectionSweepInterval
	}
	staleAfter := s.opts.collectionStaleAfter
	if staleAfter <= 0 {
		staleAfter = DefaultCollectionStaleAfter
	}
	sweeper := &collectionSweeper{
		registry:   registry,
		targets:    s.collectionSweepTargets,
		interval:   interval,
		staleAfter: staleAfter,
		logger:     s.logger,
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.sweeperCancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		sweeper.run(ctx)
	}()
}

// stopCollectionSweeper stops the sweeper started by startCollectionSweeper.
// Stop waits for it through the server's wait group.
func (s *Server) stopCollectionSweeper() {
	if s.sweeperCancel != nil {
		s.sweeperCancel()
		s.sweeperCancel = nil
	}
}

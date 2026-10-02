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
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// DefaultCompactionInterval is how often the compaction scheduler runs the
// databases' compaction policies.
const DefaultCompactionInterval = time.Minute

// compactionPassTimeout bounds the work one pass spends on one database.
const compactionPassTimeout = 30 * time.Second

// periodSampleResolution is how many revision samples a period policy keeps
// per period: a new sample is taken only once the newest is period/
// periodSampleResolution old, so a target holds about this many however
// often the scheduler runs, and its compaction target is at most one sample
// interval older than the period -- precision lost on the side of keeping
// history.
const periodSampleResolution = 64

// compactionSamplesFile is the file, in the node's data directory, that holds
// the period policies' samples across restarts.
const compactionSamplesFile = "compaction-samples.json"

// compactionTarget is one keyspace with a compaction policy: a database --
// its owning tenant, its name -- or, with Node set, the node keyspace.
type compactionTarget struct {
	Tenant   string
	Database string
	Policy   *compaction.Policy

	// Node marks the node keyspace: every key outside all database
	// namespaces, compacted by the platform's own policy.
	Node bool
}

// key identifies the target's revision samples.
func (t compactionTarget) key() string {
	if t.Node {
		// A NUL can appear in no tenant id or database name.
		return "\x00node"
	}
	return t.Tenant + "/" + t.Database
}

// compactionView is what a pass needs of a target: the store's current
// revision, and the compaction of exactly that target's keyspace.
type compactionView interface {
	CurrentRevision(ctx context.Context) (int64, error)
	compact(ctx context.Context, revision int64) error
}

// databaseView compacts one database through its namespaced view.
type databaseView struct{ service.MVCCKVServicer }

func (v databaseView) compact(ctx context.Context, revision int64) error {
	return v.Compact(ctx, revision)
}

// nodeKeyspaceStore is the raw shard service as the node-keyspace target
// needs it.
type nodeKeyspaceStore interface {
	service.NodeKeyspaceCompactor
	CurrentRevision(ctx context.Context) (int64, error)
}

// nodeView compacts the node keyspace through the raw shard service.
type nodeView struct{ nodeKeyspaceStore }

func (v nodeView) compact(ctx context.Context, revision int64) error {
	return v.CompactNodeKeyspace(ctx, revision)
}

// revisionSample is a database's current revision as observed at a time.
type revisionSample struct {
	at       time.Time
	revision int64
}

// compactionScheduler runs each database's scheduled-compaction policy. It
// compacts ONLY databases whose owning tenant configured a policy -- nothing
// is compacted implicitly -- and compacts each through the database's own
// namespaced view, resolved for the owning tenant exactly as an explicit
// Compact from that tenant is, so it can never reach another database's or
// another tenant's history. See docs/key-value/engineering.md "Scheduled
// compaction".
type compactionScheduler struct {
	registry service.Registry
	targets  func() []compactionTarget
	interval time.Duration
	now      func() time.Time
	logger   *slog.Logger

	// node is the raw shard service the node-keyspace target compacts
	// through; nil when the server has none.
	node nodeKeyspaceStore

	// ticks, when non-nil, replaces the interval ticker as run's pass
	// trigger, so a test drives each pass with one send.
	ticks <-chan time.Time

	running atomic.Bool

	// samples holds each period-policy database's observed revisions,
	// oldest first. Passes never overlap (running), so it needs no lock.
	samples map[string][]revisionSample

	// samplesPath, when set, is where samples persist across restarts, so a
	// node that restarts more often than a policy's period still compacts.
	// Empty keeps them in memory only.
	samplesPath string

	// samplesDirty marks samples changed since they were last saved.
	samplesDirty bool
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

// passOnce runs every configured policy once and returns how many databases
// it compacted. It is a no-op when a pass is already running.
func (c *compactionScheduler) passOnce(ctx context.Context) int {
	if !c.running.CompareAndSwap(false, true) {
		return 0
	}
	defer c.running.Store(false)

	now := c.now()
	live := make(map[string]bool)
	compacted := 0
	for _, t := range c.targets() {
		if !t.Policy.Enabled() {
			continue
		}
		live[t.key()] = true
		if c.compactTarget(ctx, t, now) {
			compacted++
		}
	}
	// Forget the samples of databases that no longer carry a period policy.
	for k := range c.samples {
		if !live[k] {
			delete(c.samples, k)
			c.samplesDirty = true
		}
	}
	if c.samplesDirty {
		if err := c.saveSamples(); err != nil {
			c.logger.Warn("compaction scheduler: period samples not saved; a restart waits a full period",
				slog.Any("error", err))
		} else {
			c.samplesDirty = false
		}
	}
	return compacted
}

// compactTarget runs one database's policy and reports whether it compacted.
func (c *compactionScheduler) compactTarget(ctx context.Context, t compactionTarget, now time.Time) bool {
	tctx, cancel := context.WithTimeout(tenant.WithTenantID(ctx, t.Tenant), compactionPassTimeout)
	defer cancel()

	view, err := c.view(t)
	if err != nil {
		// Sealed, being dropped, or not served here: skipped for this pass,
		// and never compacted on its behalf any other way.
		c.logger.Debug("compaction scheduler: keyspace not resolvable; skipped",
			slog.String("tenant_id", t.Tenant), slog.String("database", t.Database), slog.Any("error", err))
		return false
	}
	current, err := view.CurrentRevision(tctx)
	if err != nil {
		c.logger.Warn("compaction scheduler: current revision unavailable",
			slog.String("tenant_id", t.Tenant), slog.String("database", t.Database), slog.Any("error", err))
		return false
	}

	target := c.target(t, current, now)
	if target < 1 {
		return false
	}
	err = view.compact(tctx, target)
	var already *service.RevisionCompactedError
	switch {
	case err == nil:
		c.logger.Info("compaction scheduler: compacted database",
			slog.String("tenant_id", t.Tenant), slog.String("database", t.Database),
			slog.String("mode", string(t.Policy.Mode)), slog.Int64("revision", target))
		return true
	case errors.As(err, &already):
		// Another pass or node got there first; nothing to do.
		return false
	default:
		c.logger.Warn("compaction scheduler: compaction failed",
			slog.String("tenant_id", t.Tenant), slog.String("database", t.Database),
			slog.Int64("revision", target), slog.Any("error", err))
		return false
	}
}

// view returns the keyspace t compacts: the node keyspace through the raw
// shard service, or a database through the registry, resolved for its owner.
func (c *compactionScheduler) view(t compactionTarget) (compactionView, error) {
	if t.Node {
		if c.node == nil {
			return nil, &service.CompactScopeRequiredError{}
		}
		return nodeView{c.node}, nil
	}
	mvcc, err := c.registry.MVCCService(t.Tenant, t.Database)
	if err != nil {
		return nil, err
	}
	return databaseView{mvcc}, nil
}

// compactionTargeters computes a policy's target revision. A map rather than
// a switch: the modes are values and the dispatch is a lookup.
var compactionTargeters = map[compaction.Mode]func(c *compactionScheduler, t compactionTarget, current int64, now time.Time) int64{
	compaction.Revision: func(_ *compactionScheduler, t compactionTarget, current int64, _ time.Time) int64 {
		return current - t.Policy.RetainRevisions
	},
	compaction.Period: (*compactionScheduler).periodTarget,
}

// target returns the revision t's policy compacts to now, or a value below 1
// when there is nothing to compact yet.
func (c *compactionScheduler) target(t compactionTarget, current int64, now time.Time) int64 {
	targeter, ok := compactionTargeters[t.Policy.Mode]
	if !ok {
		return 0
	}
	return targeter(c, t, current, now)
}

// periodTarget records the database's current revision -- once the newest
// sample is a sample interval (period/periodSampleResolution) old -- and
// returns the newest sampled revision at least the policy's period old:
// everything newer than the period is kept. With no sample that old -- a
// fresh policy, or a node whose samples could not be loaded -- it returns 0
// and nothing is compacted: the policy errs toward keeping history.
func (c *compactionScheduler) periodTarget(t compactionTarget, current int64, now time.Time) int64 {
	key := t.key()
	period := t.Policy.RetainDuration()
	samples := c.samples[key]
	if n := len(samples); n == 0 || now.Sub(samples[n-1].at) >= period/periodSampleResolution {
		samples = append(samples, revisionSample{at: now, revision: current})
		c.samplesDirty = true
	}
	cutoff := now.Add(-period)

	// The newest sample at or before the cutoff is the target; every older
	// one is no longer needed.
	newest := -1
	for i, s := range samples {
		if s.at.After(cutoff) {
			break
		}
		newest = i
	}
	var target int64
	if newest >= 0 {
		target = samples[newest].revision
		if newest > 0 {
			samples = samples[newest:]
			c.samplesDirty = true
		}
	}
	c.samples[key] = samples
	return target
}

// persistedSample is a revision sample as the samples file holds it.
type persistedSample struct {
	At       time.Time `json:"at"`
	Revision int64     `json:"revision"`
}

// loadSamples replaces the samples with the ones persisted at samplesPath. A
// missing file is no samples. A file that cannot be read or parsed is a
// *CompactionSamplesError and leaves no samples: the scheduler then waits a
// full period before compacting, as a fresh policy does, rather than guess.
func (c *compactionScheduler) loadSamples() error {
	c.samples = make(map[string][]revisionSample)
	if c.samplesPath == "" {
		return nil
	}
	data, err := os.ReadFile(c.samplesPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return &CompactionSamplesError{Path: c.samplesPath, Err: err}
	}
	var persisted map[string][]persistedSample
	if err := json.Unmarshal(data, &persisted); err != nil {
		return &CompactionSamplesError{Path: c.samplesPath, Err: err}
	}
	for key, list := range persisted {
		samples := make([]revisionSample, 0, len(list))
		for _, p := range list {
			samples = append(samples, revisionSample{at: p.At, revision: p.Revision})
		}
		c.samples[key] = samples
	}
	return nil
}

// saveSamples writes the samples to samplesPath atomically; a no-op without
// a path.
func (c *compactionScheduler) saveSamples() error {
	if c.samplesPath == "" {
		return nil
	}
	persisted := make(map[string][]persistedSample, len(c.samples))
	for key, list := range c.samples {
		out := make([]persistedSample, 0, len(list))
		for _, smp := range list {
			out = append(out, persistedSample{At: smp.at, Revision: smp.revision})
		}
		persisted[key] = out
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		return &CompactionSamplesError{Path: c.samplesPath, Err: err}
	}
	if err := clustertls.WriteFileAtomic(c.samplesPath, data, 0o600); err != nil {
		return &CompactionSamplesError{Path: c.samplesPath, Err: err}
	}
	return nil
}

// CompactionSamplesError reports that the compaction scheduler could not read
// or write its persisted period samples.
type CompactionSamplesError struct {
	// Path is the samples file.
	Path string
	// Err is the cause.
	Err error
}

// Error returns a human-readable description.
func (e *CompactionSamplesError) Error() string {
	return "server: compaction samples " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the cause.
func (e *CompactionSamplesError) Unwrap() error { return e.Err }

// compactionTargets returns the keyspaces this node compacts: the node
// keyspace when the platform's configuration sets a policy for it, and the
// databases in this node's catalog that carry an enabled compaction policy,
// with their owning tenant.
func (s *Server) compactionTargets() []compactionTarget {
	var out []compactionTarget
	if policy := s.nodeCompactionPolicy(); policy.Enabled() {
		out = append(out, compactionTarget{Tenant: tenant.SystemTenantID, Node: true, Policy: policy})
	}
	if s.opts.dbManager == nil {
		return out
	}
	for _, info := range s.opts.dbManager.List() {
		policy := info.Compaction
		if !policy.Enabled() {
			continue
		}
		owner := info.TenantID
		if owner == "" {
			owner = tenant.SystemTenantID
		}
		out = append(out, compactionTarget{Tenant: owner, Database: info.Name, Policy: policy})
	}
	return out
}

// nodeCompactionPolicy returns the platform's node-keyspace policy from the
// node's configuration file; nil when none is set.
func (s *Server) nodeCompactionPolicy() *compaction.Policy {
	if s.config == nil {
		return nil
	}
	return s.config.MVCC.NodeCompaction
}

// WithCompactionInterval overrides how often the compaction scheduler runs
// the databases' compaction policies. A non-positive value keeps
// DefaultCompactionInterval.
func WithCompactionInterval(interval time.Duration) ServerOption {
	return func(o *serverOptions) {
		if interval > 0 {
			o.compactionInterval = interval
		}
	}
}

// startCompactionScheduler starts the scheduler when the server resolves
// databases through a registry, and has either a database catalog to read
// policies from or a node-keyspace policy and the raw shard service to run
// it through.
func (s *Server) startCompactionScheduler() {
	registry := s.buildServiceRegistry()
	if registry == nil {
		return
	}
	node, _ := s.opts.mvccSvc.(nodeKeyspaceStore)
	if s.opts.dbManager == nil && (node == nil || !s.nodeCompactionPolicy().Enabled()) {
		return
	}
	interval := s.opts.compactionInterval
	if interval <= 0 {
		interval = DefaultCompactionInterval
	}
	scheduler := &compactionScheduler{
		registry: registry,
		node:     node,
		targets:  s.compactionTargets,
		interval: interval,
		now:      time.Now,
		logger:   s.logger,
		samples:  make(map[string][]revisionSample),
	}
	if s.config != nil && s.config.Raft.DataDir != "" {
		scheduler.samplesPath = filepath.Join(s.config.Raft.DataDir, compactionSamplesFile)
	}
	if err := scheduler.loadSamples(); err != nil {
		s.logger.Warn("compaction scheduler: period samples not loaded; period policies wait a full period",
			slog.Any("error", err))
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

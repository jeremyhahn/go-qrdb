// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
)

// Reporter publishes this node's own NodeReport into the catalog on a
// lease, the SAME mechanism cluster.Advertiser uses to publish a
// cluster.Member: Grant once, refresh on a ticker with KeepAlive, and
// attach the record to that lease with PutWithLease so the engine deletes
// it automatically if this node stops refreshing it -- "a dead node
// reports nothing rather than stale health" is the engine's own lease
// expiry, not a reader-side staleness guess.
//
// Unlike a cluster.Member, a NodeReport's content (per-pool usage and
// health) changes on every refresh, not only at registration, so each
// refresh re-attaches the lease AND rewrites the value: calling Grant (or
// KeepAlive on an already-granted lease) then PutWithLease with the
// caller's fresh content, every cycle.
type Reporter struct {
	mvcc     MVCCServicer
	leases   lease.LeaseServicer
	nodeID   string
	ttl      time.Duration
	interval time.Duration
	logger   *slog.Logger
	seal     SealProbe

	mu      sync.Mutex
	leaseID int64
	granted bool
	started bool
	closed  bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// ReporterOption configures a Reporter at construction time.
type ReporterOption func(*Reporter)

// WithReporterLogger sets the Reporter's logger. A nil logger is ignored.
func WithReporterLogger(l *slog.Logger) ReporterOption {
	return func(r *Reporter) {
		if l != nil {
			r.logger = l
		}
	}
}

// WithReporterLeaseTTL overrides DefaultLeaseTTL. A non-positive value is
// ignored.
func WithReporterLeaseTTL(d time.Duration) ReporterOption {
	return func(r *Reporter) {
		if d > 0 {
			r.ttl = d
		}
	}
}

// WithReporterInterval overrides DefaultReportInterval. A non-positive value
// is ignored.
func WithReporterInterval(d time.Duration) ReporterOption {
	return func(r *Reporter) {
		if d > 0 {
			r.interval = d
		}
	}
}

// SealProbe reports whether the node barrier is sealed. While it is, the
// node's replicated log cannot take an entry, and an entry proposed into it
// halts the shard, so a write that is not a client's request must not be
// proposed at all.
type SealProbe interface {
	IsSealed() bool
}

// WithReporterSealProbe makes the Reporter consult p before every write it
// would propose: while the node barrier is sealed a refresh is skipped and
// Report is refused with *NodeSealedError. The lease expiry sweep holds while
// sealed, so the report survives until the first refresh after the unseal
// renews it. A nil p writes unconditionally.
func WithReporterSealProbe(p SealProbe) ReporterOption {
	return func(r *Reporter) {
		r.seal = p
	}
}

// NewReporter builds a Reporter that publishes nodeID's NodeReport over
// mvcc, leased through leases. Both are required.
func NewReporter(mvcc MVCCServicer, leases lease.LeaseServicer, nodeID string, opts ...ReporterOption) (*Reporter, error) {
	if mvcc == nil {
		return nil, &InvalidCatalogDependencyError{Reason: "nil MVCCServicer"}
	}
	if leases == nil {
		return nil, &InvalidCatalogDependencyError{Reason: "nil LeaseServicer"}
	}
	if nodeID == "" {
		return nil, &InvalidCatalogDependencyError{Reason: "empty node id"}
	}
	r := &Reporter{mvcc: mvcc, leases: leases, nodeID: nodeID, ttl: DefaultLeaseTTL, interval: DefaultReportInterval, logger: slog.Default()}
	for _, opt := range opts {
		opt(r)
	}
	return r, nil
}

// Start publishes an initial report (built by the given func) and then
// refreshes it every DefaultReportInterval until ctx is cancelled or Close
// is called. The refresh goroutine logs (rather than returns) a failed
// refresh and retries on the next tick, exactly as cluster.Advertiser's
// keepAlive loop tolerates a transient failure instead of giving up.
//
// The initial report is no exception: a failed one is logged and left to the
// refresh loop, not returned. It fails whenever the system shard has no
// leader -- an election during boot is enough -- and a Reporter that gave up
// then would leave this node without a report for as long as it runs, so
// placement would never put a replica of a pool's tenant on it.
//
// A sealed node barrier is the exception: Start is refused with
// *NodeSealedError and nothing is started, because the caller started the
// Reporter too early rather than meeting a transient failure.
//
// Start is not idempotent: a second call on a started (or closed) Reporter
// is an *InvalidCatalogDependencyError.
func (r *Reporter) Start(ctx context.Context, build func() NodeReport) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return &InvalidCatalogDependencyError{Reason: "reporter is closed"}
	}
	if r.started {
		r.mu.Unlock()
		return &InvalidCatalogDependencyError{Reason: "reporter already started"}
	}
	r.mu.Unlock()

	if r.sealed() {
		return &NodeSealedError{Op: "node report"}
	}
	if err := r.Report(ctx, build()); err != nil {
		r.logger.Warn("volume: initial node report failed; retrying on the refresh interval",
			slog.String("node", r.nodeID), slog.String("error", err.Error()))
	}

	runCtx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.started = true
	r.cancel = cancel
	r.mu.Unlock()

	r.wg.Add(1)
	go r.refreshLoop(runCtx, build)
	return nil
}

// refreshLoop re-publishes the report on the Reporter's interval until
// runCtx is cancelled.
func (r *Reporter) refreshLoop(runCtx context.Context, build func() NodeReport) {
	defer r.wg.Done()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			return
		case <-ticker.C:
			// A sealed node barrier halts the shard on the next proposal,
			// so the refresh is skipped; the lease expiry sweep holds while
			// sealed too, so the report outlives the seal.
			if r.sealed() {
				continue
			}
			if err := r.Report(runCtx, build()); err != nil {
				r.logger.Warn("volume: node report refresh failed", slog.String("node", r.nodeID), slog.String("error", err.Error()))
			}
		}
	}
}

// sealed reports whether the node barrier is sealed, per the configured probe.
func (r *Reporter) sealed() bool {
	return r.seal != nil && r.seal.IsSealed()
}

// Report publishes report once: it grants a lease on the first call (or
// after the previous lease was lost) and refreshes it with KeepAlive on
// every later call, then writes report under NodeReportKey(report.NodeID)
// attached to that lease. ReportedAt is stamped with the current time
// regardless of what the caller set, so a published report never already
// reads as stale.
func (r *Reporter) Report(ctx context.Context, report NodeReport) error {
	if report.NodeID == "" {
		report.NodeID = r.nodeID
	}
	if report.NodeID != r.nodeID {
		return &InvalidCatalogDependencyError{Reason: "report names a different node id than this Reporter"}
	}
	if r.sealed() {
		return &NodeSealedError{Op: "node report"}
	}
	report.ReportedAt = time.Now()
	data, err := json.Marshal(report)
	if err != nil {
		return &EncodeError{Err: err}
	}

	leaseID, err := r.leaseIDFor(ctx)
	if err != nil {
		return err
	}
	if _, err := r.mvcc.PutWithLease(ctx, NodeReportKey(report.NodeID), data, leaseID); err != nil {
		return err
	}
	return nil
}

// leaseIDFor returns a live lease id for this Reporter's report: the
// existing one, refreshed with KeepAlive, or a freshly granted one when
// there is none yet or the existing one was lost. Mirrors
// sdkMembershipStore.PutMember's self-healing: a KeepAlive failure drops
// the local lease state so the NEXT call re-grants rather than retrying a
// dead lease forever.
func (r *Reporter) leaseIDFor(ctx context.Context) (int64, error) {
	r.mu.Lock()
	leaseID, granted := r.leaseID, r.granted
	r.mu.Unlock()

	if granted {
		if _, err := r.leases.KeepAlive(ctx, leaseID); err == nil {
			return leaseID, nil
		}
		r.mu.Lock()
		if r.leaseID == leaseID {
			r.granted = false
			r.leaseID = 0
		}
		r.mu.Unlock()
	}

	ttlSeconds := int64(r.ttl.Seconds())
	if ttlSeconds < 1 {
		ttlSeconds = 1
	}
	granted2, err := r.leases.Grant(ctx, ttlSeconds)
	if err != nil {
		return 0, err
	}
	r.mu.Lock()
	r.leaseID = granted2.ID
	r.granted = true
	r.mu.Unlock()
	return granted2.ID, nil
}

// Close stops the refresh loop and revokes the lease, which deletes the
// published report immediately rather than leaving it to expire on its
// own. Idempotent.
func (r *Reporter) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	cancel := r.cancel
	r.cancel = nil
	r.started = false // retained for clarity; Close is already terminal
	leaseID, granted := r.leaseID, r.granted
	r.granted = false
	r.leaseID = 0
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	r.wg.Wait()

	if !granted {
		return nil
	}
	return r.leases.Revoke(context.Background(), leaseID)
}

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

package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/barrierinit"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// BarrierInit is this node's half of per-node barrier init
// (docs/barrier-seal/architecture.md): the init records and anchors in the
// system store, and the tenant service that initializes this node's own key
// for a barrier from its owner's verified, keyless statement. No key leaves
// the node it was generated on.
type BarrierInit struct {
	// KV is the system store the init records and anchors live in.
	KV barrierinit.KV
	// Anchors holds each barrier's pinned first admin: pinned when its owner
	// is onboarded, and what every statement for it is verified against.
	Anchors *barrierinit.Pins

	selfInit barrierSelfIniter
}

// barrierSelfIniter initializes this node's own tenant barrier key from a
// verified statement's strategy and key reference.
type barrierSelfIniter interface {
	TenantBarrierSelfInit(ctx context.Context, tenantID string, strategyID seal.StrategyID, keyRef string) error
}

var _ barrierSelfIniter = (*tenant.TenantService)(nil)

// newBarrierInit returns the records over kv, the system store.
func newBarrierInit(kv service.KVServicer) *BarrierInit {
	initKV := server.NewBarrierInitKV(kv)
	return &BarrierInit{KV: initKV, Anchors: barrierinit.NewPins(initKV)}
}

// claimer is what tenant barrier inits claim their owner's statement through:
// a Book that records nothing until required returns nil
// (featuregate.PerNodeBarrierKeys; nil on a node that gates nothing),
// verifying against the pinned anchors.
func (b *BarrierInit) claimer(required func() error) *barrierinit.Claimer {
	return barrierinit.NewClaimer(barrierinit.NewBook(b.KV, required), b.Anchors.Anchors)
}

// book is the tenant service's view of claimer: it claims statements and
// reads a barrier's verified statement back when the barrier is unsealed.
func (b *BarrierInit) book(required func() error) tenantInitBook {
	return tenantInitBook{b.claimer(required)}
}

// tenantInitBook adapts a barrierinit.Claimer to tenant.BarrierInitBook,
// which names the statement's terms in the tenant package's own type
// (pkg/tenant cannot import pkg/barrierinit, which depends on it).
type tenantInitBook struct {
	*barrierinit.Claimer
}

var _ tenant.BarrierInitBook = tenantInitBook{}

// TenantInit returns the terms of tenantID's verified init statement, and
// found false when the barrier has none.
func (t tenantInitBook) TenantInit(ctx context.Context, tenantID string) (tenant.BarrierInitTerms, bool, error) {
	s, err := t.Claimer.TenantInit(ctx, tenantID)
	var none *barrierinit.NoInitStatementError
	switch {
	case errors.As(err, &none):
		return tenant.BarrierInitTerms{}, false, nil
	case err != nil:
		return tenant.BarrierInitTerms{}, false, err
	}
	return tenant.BarrierInitTerms{StrategyID: s.StrategyID, KeyRef: s.KeyRef, Epoch: s.Epoch}, true, nil
}

// perNodeBarrierKeysRequired is the per-node-barrier-keys activation check
// for host: nil on a host that gates nothing (a standalone node, whose groups
// hold only this binary), otherwise one that refuses with
// *featuregate.NotActiveError until the cluster has activated the feature. A
// single-member cluster activates at its first check.
func perNodeBarrierKeysRequired(host service.Host) func() error {
	gated, ok := host.(service.FeatureGatedHost)
	if !ok {
		return nil
	}
	return func() error {
		// The feature is judged cluster-wide, so no group is named.
		if gated.FeatureEnabled(0, featuregate.PerNodeBarrierKeys) {
			return nil
		}
		return &featuregate.NotActiveError{Feature: featuregate.PerNodeBarrierKeys}
	}
}

// UnsupportedBarrierKindError is returned for an init record of a barrier
// level this node does not yet initialize its own key for from a statement.
// The watcher retries it on its next poll.
type UnsupportedBarrierKindError struct {
	Barrier clusterseal.Barrier
}

// Error implements error.
func (e *UnsupportedBarrierKindError) Error() string {
	return "lifecycle: no per-node init for barrier " + strconv.Quote(string(e.Barrier))
}

// tenantPlacements lists a tenant's placements: its home group and its
// dedicated databases. *placement.Store implements it.
type tenantPlacements interface {
	ListForTenant(ctx context.Context, tenantID string) ([]*placement.Placement, error)
}

// barrierInitWatch is what a node's barrierinit.Watcher is composed from: the
// statements it handles and the barriers it serves, by level.
type barrierInitWatch struct {
	init *BarrierInit
	// placements is where a clustered node's serves looks its tenants up; a
	// standalone node serves every tenant barrier (servesEveryTenant).
	placements tenantPlacements
	// self is this node's identity in placements, its Raft address.
	self string
}

// handle initializes this node's own key for s's barrier, dispatched on the
// barrier's level.
func (w *barrierInitWatch) handle(ctx context.Context, s barrierinit.InitStatement) error {
	handlers := map[clusterseal.Kind]func(context.Context, barrierinit.InitStatement) error{
		clusterseal.KindTenant: func(ctx context.Context, s barrierinit.InitStatement) error {
			return w.init.selfInit.TenantBarrierSelfInit(ctx, s.Barrier.Tenant(), s.StrategyID, s.KeyRef)
		},
	}
	h, ok := handlers[s.Barrier.Kind()]
	if !ok {
		return &UnsupportedBarrierKindError{Barrier: s.Barrier}
	}
	return h(ctx, s)
}

// serves reports whether this node serves b: whether a group of b's tenant --
// its home group, or one of its dedicated databases -- has a replica placed
// here. Only tenant barriers are served from a statement today.
func (w *barrierInitWatch) serves(ctx context.Context, b clusterseal.Barrier) (bool, error) {
	if b.Kind() != clusterseal.KindTenant {
		return false, nil
	}
	ps, err := w.placements.ListForTenant(ctx, b.Tenant())
	if err != nil {
		return false, err
	}
	for _, p := range ps {
		if _, ok := p.ReplicaForNode(w.self); ok {
			return true, nil
		}
	}
	return false, nil
}

// startBarrierInitWatcher runs a barrierinit.Watcher on this node until it
// stops: each tenant barrier one of whose groups is placed here is
// initialized with this node's own key once its owner's statement is claimed
// and verifies. It is a no-op on a node with no barrier init records (no
// tenant service) or no placement table.
func startBarrierInitWatcher(n *Node, self string, logger *slog.Logger) error {
	if n.BarrierInit == nil || n.PlacementStore == nil {
		return nil
	}
	watch := &barrierInitWatch{init: n.BarrierInit, placements: n.PlacementStore, self: self}
	return runBarrierInitWatcher(n, watch.serves, watch.handle, logger)
}

// startLocalBarrierInitWatcher runs a barrierinit.Watcher on a standalone
// node until it stops. A standalone node is the only node, so it serves every
// tenant barrier: each one is initialized with this node's own key once its
// owner's statement is claimed and verifies, which on this node is the init
// that claimed it, and so already done. It is a no-op on a node with no
// barrier init records (no tenant service).
func startLocalBarrierInitWatcher(n *Node, logger *slog.Logger) error {
	if n.BarrierInit == nil {
		return nil
	}
	watch := &barrierInitWatch{init: n.BarrierInit}
	return runBarrierInitWatcher(n, servesEveryTenant, watch.handle, logger)
}

// servesEveryTenant is a standalone node's barrierinit.WatcherConfig.Serves:
// every tenant barrier, and no other level yet.
func servesEveryTenant(_ context.Context, b clusterseal.Barrier) (bool, error) {
	return b.Kind() == clusterseal.KindTenant, nil
}

// runBarrierInitWatcher runs a barrierinit.Watcher over n's init records,
// serving the barriers serves reports, until n is closed.
func runBarrierInitWatcher(n *Node, serves func(context.Context, clusterseal.Barrier) (bool, error), handle func(context.Context, barrierinit.InitStatement) error, logger *slog.Logger) error {
	w, err := barrierinit.NewWatcher(barrierinit.WatcherConfig{
		KV:      n.BarrierInit.KV,
		Anchors: n.BarrierInit.Anchors.Anchors,
		Serves:  serves,
		Handle:  handle,
		Logger:  logger,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx) //nolint:errcheck // Run returns nil; it logs each incomplete poll itself
	}()
	n.addCloser(func() error {
		cancel()
		<-done
		return nil
	})
	return nil
}

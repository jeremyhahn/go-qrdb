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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/barrierinit"
	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// selfInitCall is one TenantBarrierSelfInit call.
type selfInitCall struct {
	tenantID string
	strategy seal.StrategyID
	keyRef   string
}

// armCall is one TenantBarrierArmShamir call.
type armCall struct {
	tenantID      string
	roster, check []byte
}

// selfIniterDouble records each self-init and arming it is handed and answers
// with err.
type selfIniterDouble struct {
	calls []selfInitCall
	arms  []armCall
	err   error
}

func (d *selfIniterDouble) TenantBarrierSelfInit(_ context.Context, tenantID string, strategyID seal.StrategyID, keyRef string) error {
	d.calls = append(d.calls, selfInitCall{tenantID, strategyID, keyRef})
	return d.err
}

func (d *selfIniterDouble) TenantBarrierArmShamir(_ context.Context, tenantID string, roster, check []byte) error {
	d.arms = append(d.arms, armCall{tenantID, roster, check})
	return d.err
}

var _ barrierSelfIniter = (*selfIniterDouble)(nil)

// placementsDouble lists each tenant's placements, or fails with err.
type placementsDouble struct {
	byTenant map[string][]*placement.Placement
	err      error
}

func (d *placementsDouble) ListForTenant(_ context.Context, tenantID string) ([]*placement.Placement, error) {
	if d.err != nil {
		return nil, d.err
	}
	return d.byTenant[tenantID], nil
}

var _ tenantPlacements = (*placementsDouble)(nil)

func placedOn(nodes ...string) *placement.Placement {
	p := &placement.Placement{}
	for i, n := range nodes {
		p.Replicas = append(p.Replicas, placement.Replica{ReplicaID: uint64(i + 1), NodeID: n, RaftAddr: n})
	}
	return p
}

func TestBarrierInitWatch_HandleInitializesThisNodesTenantKey(t *testing.T) {
	self := &selfIniterDouble{}
	w := &barrierInitWatch{init: &BarrierInit{selfInit: self}}
	s := barrierinit.InitStatement{Barrier: clusterseal.TenantBarrier("acme"), Epoch: 1, StrategyID: "test-kms", KeyRef: "kms://owner-key"}
	if err := w.handle(context.Background(), s); err != nil {
		t.Fatalf("handle: %v", err)
	}
	want := selfInitCall{tenantID: "acme", strategy: "test-kms", keyRef: "kms://owner-key"}
	if len(self.calls) != 1 || self.calls[0] != want {
		t.Fatalf("self-inits = %+v, want [%+v]", self.calls, want)
	}

	// A failed init is the watcher's to retry on its next poll.
	self.err = errors.New("provider unavailable")
	if err := w.handle(context.Background(), s); !errors.Is(err, self.err) {
		t.Fatalf("handle = %v, want the self-init failure", err)
	}
}

func TestBarrierInitWatch_HandleArmsAShamirTenantFromTheStatement(t *testing.T) {
	self := &selfIniterDouble{}
	w := &barrierInitWatch{init: &BarrierInit{selfInit: self}}
	s := barrierinit.InitStatement{Barrier: clusterseal.TenantBarrier("acme"), Epoch: 1, StrategyID: seal.StrategyShamir,
		Threshold: 2, Roster: []byte("roster"), UnsealKeyCheck: []byte("check")}
	if err := w.handle(context.Background(), s); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(self.calls) != 0 || len(self.arms) != 1 || self.arms[0].tenantID != "acme" ||
		string(self.arms[0].roster) != "roster" || string(self.arms[0].check) != "check" {
		t.Fatalf("self-inits = %+v, arms = %+v", self.calls, self.arms)
	}
	self.err = errors.New("pool unavailable")
	if err := w.handle(context.Background(), s); !errors.Is(err, self.err) {
		t.Fatalf("handle = %v, want the arming failure", err)
	}
}

func TestBarrierInitWatch_HandleRefusesALevelItDoesNotInitialize(t *testing.T) {
	self := &selfIniterDouble{}
	w := &barrierInitWatch{init: &BarrierInit{selfInit: self}}
	for _, b := range []clusterseal.Barrier{clusterseal.NodeBarrier(), clusterseal.DatabaseBarrier("acme", "orders")} {
		err := w.handle(context.Background(), barrierinit.InitStatement{Barrier: b, Epoch: 1, StrategyID: seal.StrategyNone})
		var unsupported *UnsupportedBarrierKindError
		if !errors.As(err, &unsupported) || unsupported.Barrier != b || unsupported.Error() == "" {
			t.Fatalf("handle(%s) = %v, want *UnsupportedBarrierKindError", b, err)
		}
	}
	if len(self.calls) != 0 {
		t.Fatalf("a non-tenant statement reached the tenant service: %+v", self.calls)
	}
}

func TestBarrierInitWatch_ServesATenantPlacedOnThisNode(t *testing.T) {
	w := &barrierInitWatch{
		self: "10.0.0.2:7000",
		placements: &placementsDouble{byTenant: map[string][]*placement.Placement{
			// The home group is elsewhere; a dedicated database has a replica here.
			"acme":   {placedOn("10.0.0.1:7000"), placedOn("10.0.0.1:7000", "10.0.0.2:7000")},
			"globex": {placedOn("10.0.0.1:7000", "10.0.0.3:7000")},
		}},
	}
	ctx := context.Background()
	for b, want := range map[clusterseal.Barrier]bool{
		clusterseal.TenantBarrier("acme"):             true,
		clusterseal.TenantBarrier("globex"):           false,
		clusterseal.TenantBarrier("initech"):          false,
		clusterseal.NodeBarrier():                     false,
		clusterseal.DatabaseBarrier("acme", "orders"): false,
	} {
		got, err := w.serves(ctx, b)
		if err != nil || got != want {
			t.Fatalf("serves(%s) = %v, %v; want %v", b, got, err, want)
		}
	}
}

func TestBarrierInitWatch_ServesReportsAnUnreadablePlacementTable(t *testing.T) {
	unavailable := errors.New("placement table unavailable")
	w := &barrierInitWatch{self: "10.0.0.2:7000", placements: &placementsDouble{err: unavailable}}
	if _, err := w.serves(context.Background(), clusterseal.TenantBarrier("acme")); !errors.Is(err, unavailable) {
		t.Fatalf("serves = %v, want the placement failure", err)
	}
}

// signallingSelfIniter hands each self-init to calls.
type signallingSelfIniter struct {
	calls chan selfInitCall
}

func (d *signallingSelfIniter) TenantBarrierSelfInit(_ context.Context, tenantID string, strategyID seal.StrategyID, keyRef string) error {
	d.calls <- selfInitCall{tenantID, strategyID, keyRef}
	return nil
}

func (d *signallingSelfIniter) TenantBarrierArmShamir(_ context.Context, tenantID string, _, _ []byte) error {
	d.calls <- selfInitCall{tenantID, seal.StrategyShamir, ""}
	return nil
}

// The node's composition end to end: the owner's statement, claimed through
// the claimer its tenant service is given and verified against the pinned
// first admin, reaches this node's own self-init through the watcher, which
// stops with the node.
func TestStartBarrierInitWatcher_InitializesAPlacedTenantFromItsClaimedStatement(t *testing.T) {
	ctx := context.Background()
	bi, self := newClaimedBarrierInit(t, "acme")

	placements, _ := newPlacementStoreOverHub(t)
	home := placedOn("10.0.0.1:7000", "10.0.0.2:7000")
	home.TenantID, home.Database = "acme", "home"
	if err := placements.Put(ctx, home); err != nil {
		t.Fatalf("Put placement: %v", err)
	}
	n := &Node{BarrierInit: bi, PlacementStore: placements}
	if err := startBarrierInitWatcher(n, "10.0.0.2:7000", slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("startBarrierInitWatcher: %v", err)
	}
	got := <-self.calls
	if want := (selfInitCall{tenantID: "acme", strategy: seal.StrategyNone}); got != want {
		t.Fatalf("self-init = %+v, want %+v", got, want)
	}
	if err := n.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// newSignedBarrierInit returns barrier init records over a fresh system store
// in which tenantID's first admin is pinned, the self-initializer they deliver
// to, and that admin's signed epoch-1 statement for the none strategy,
// unclaimed.
func newSignedBarrierInit(t *testing.T, tenantID string) (*BarrierInit, *signallingSelfIniter, []byte) {
	t.Helper()
	ctx := context.Background()
	stg := memory.NewStorage()
	t.Cleanup(func() { _ = stg.Close() })
	bi := newBarrierInit(service.NewKVService(backend.NewLocalBackend(engine.NewKVEngine(stg)), 0), clocktest.NewAt(platformNow))
	self := &signallingSelfIniter{calls: make(chan selfInitCall, 1)}
	bi.selfInit = self

	owner, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	thumbprint, err := user.PublicKeySPKIThumbprint(owner.Public())
	if err != nil {
		t.Fatal(err)
	}
	if err := bi.Anchors.PinFirstAdmin(ctx, clusterseal.TenantBarrier(tenantID), thumbprint); err != nil {
		t.Fatalf("PinFirstAdmin: %v", err)
	}
	signed, err := barrierinit.Sign(owner, barrierinit.InitStatement{
		Barrier: clusterseal.TenantBarrier(tenantID), Epoch: 1, IssuedAt: platformNow.Unix(), StrategyID: seal.StrategyNone,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	raw, err := signed.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return bi, self, raw
}

// newClaimedBarrierInit is newSignedBarrierInit with the statement claimed
// through a claimer that gates nothing, as a standalone node's does.
func newClaimedBarrierInit(t *testing.T, tenantID string) (*BarrierInit, *signallingSelfIniter) {
	t.Helper()
	bi, self, raw := newSignedBarrierInit(t, tenantID)
	if err := bi.claimer(nil).ClaimTenantInit(context.Background(), tenantID, seal.StrategyNone, "", raw); err != nil {
		t.Fatalf("ClaimTenantInit: %v", err)
	}
	return bi, self
}

func TestStartBarrierInitWatcher_NoOpWithoutItsParts(t *testing.T) {
	for name, n := range map[string]*Node{
		"no barrier init":    {PlacementStore: &placement.Store{}},
		"no placement table": {BarrierInit: &BarrierInit{}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := startBarrierInitWatcher(n, "10.0.0.2:7000", slog.New(slog.DiscardHandler)); err != nil {
				t.Fatalf("startBarrierInitWatcher = %v, want nil", err)
			}
			if len(n.closers) != 0 {
				t.Fatal("a watcher was started without its parts")
			}
		})
	}
}

func TestStartLocalBarrierInitWatcher_InitializesATenantFromItsClaimedStatement(t *testing.T) {
	bi, self := newClaimedBarrierInit(t, "acme")
	n := &Node{BarrierInit: bi}
	if err := startLocalBarrierInitWatcher(n, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("startLocalBarrierInitWatcher: %v", err)
	}
	got := <-self.calls
	if want := (selfInitCall{tenantID: "acme", strategy: seal.StrategyNone}); got != want {
		t.Fatalf("self-init = %+v, want %+v", got, want)
	}
	if err := n.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestStartLocalBarrierInitWatcher_NoOpWithoutBarrierInit(t *testing.T) {
	n := &Node{}
	if err := startLocalBarrierInitWatcher(n, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("startLocalBarrierInitWatcher = %v, want nil", err)
	}
	if len(n.closers) != 0 {
		t.Fatal("a watcher was started without barrier init records")
	}
}

func TestServesEveryTenant_ServesTenantBarriersOnly(t *testing.T) {
	tests := map[string]struct {
		barrier clusterseal.Barrier
		want    bool
	}{
		"tenant":   {barrier: clusterseal.TenantBarrier("acme"), want: true},
		"node":     {barrier: clusterseal.NodeBarrier()},
		"database": {barrier: clusterseal.DatabaseBarrier("acme", "orders")},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := servesEveryTenant(context.Background(), tt.barrier)
			if err != nil || got != tt.want {
				t.Fatalf("servesEveryTenant(%s) = %v, %v; want %v, nil", tt.barrier, got, err, tt.want)
			}
		})
	}
}

func TestStandaloneHost(t *testing.T) {
	tests := map[string]struct {
		host service.Host
		want bool
	}{
		"standalone": {host: newStandaloneHost(), want: true},
		"raft":       {host: raftAddrHost{AdminHost: newStandaloneHost(), addr: "10.0.0.1:7000"}},
		"no host":    {},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := standaloneHost(tt.host); got != tt.want {
				t.Fatalf("standaloneHost = %v, want %v", got, tt.want)
			}
		})
	}
}

// perNodeBarrierKeysActive is a gated host on which per-node-barrier-keys is
// active.
var perNodeBarrierKeysActive = gatedHost{enabled: map[featuregate.Feature]bool{featuregate.PerNodeBarrierKeys: true}}

func TestPerNodeBarrierKeysRequired_NilOnAHostThatGatesNothing(t *testing.T) {
	for name, host := range map[string]service.Host{"standalone": newStandaloneHost(), "no host": nil} {
		t.Run(name, func(t *testing.T) {
			if required := perNodeBarrierKeysRequired(host); required != nil {
				t.Fatalf("perNodeBarrierKeysRequired = %v, want nil: the host gates nothing", required())
			}
		})
	}
}

func TestPerNodeBarrierKeysRequired_FollowsTheGate(t *testing.T) {
	// Another feature being active does not stand in for this one.
	before := gatedHost{enabled: map[featuregate.Feature]bool{featuregate.GuardedWrites: true}}
	var notActive *featuregate.NotActiveError
	err := perNodeBarrierKeysRequired(before)()
	if !errors.As(err, &notActive) || notActive.Feature != featuregate.PerNodeBarrierKeys || notActive.ShardID != 0 {
		t.Fatalf("required before activation = %v, want cluster-wide NotActiveError(%s)", err, featuregate.PerNodeBarrierKeys)
	}
	if err := perNodeBarrierKeysRequired(perNodeBarrierKeysActive)(); err != nil {
		t.Fatalf("required after activation = %v, want nil", err)
	}
}

// TestBarrierInitClaimer_RecordsNothingBeforeActivation pins that the claimer
// the tenant service is composed with waits on the same gate: before
// per-node-barrier-keys is active a statement is refused and not recorded,
// so no node initialises a key from it.
func TestBarrierInitClaimer_RecordsNothingBeforeActivation(t *testing.T) {
	const tenantID = "acme"
	bi, _, raw := newSignedBarrierInit(t, tenantID)
	ctx := context.Background()

	var notActive *featuregate.NotActiveError
	err := bi.claimer(perNodeBarrierKeysRequired(gatedHost{})).ClaimTenantInit(ctx, tenantID, seal.StrategyNone, "", raw)
	if !errors.As(err, &notActive) || notActive.Feature != featuregate.PerNodeBarrierKeys {
		t.Fatalf("ClaimTenantInit before activation = %v, want NotActiveError(%s)", err, featuregate.PerNodeBarrierKeys)
	}
	if err := bi.claimer(perNodeBarrierKeysRequired(perNodeBarrierKeysActive)).ClaimTenantInit(ctx, tenantID, seal.StrategyNone, "", raw); err != nil {
		t.Fatalf("ClaimTenantInit after activation = %v, want nil: the refused claim recorded nothing", err)
	}
}

// The tenant service reads a barrier's verified statement back in its own
// terms: the strategy, key reference and epoch the owner signed.
func TestTenantInitBook_ReadsTheClaimedStatement(t *testing.T) {
	bi, _ := newClaimedBarrierInit(t, "acme")
	terms, found, err := bi.book(nil).TenantInit(context.Background(), "acme")
	if err != nil || !found {
		t.Fatalf("TenantInit = (%+v, %v, %v), want the claimed statement", terms, found, err)
	}
	want := tenant.BarrierInitTerms{StrategyID: seal.StrategyNone, Epoch: 1}
	if terms != want {
		t.Fatalf("terms = %+v, want %+v", terms, want)
	}
}

// A barrier with no statement is not found rather than an error: the tenant
// service decides whether its isolation could have been initialized without
// one.
func TestTenantInitBook_NoStatementIsNotFound(t *testing.T) {
	bi, _ := newClaimedBarrierInit(t, "acme")
	terms, found, err := bi.book(nil).TenantInit(context.Background(), "globex")
	if err != nil || found || terms != (tenant.BarrierInitTerms{}) {
		t.Fatalf("TenantInit(no statement) = (%+v, %v, %v), want not found", terms, found, err)
	}
}

// A statement whose anchors cannot be read is an error, never not-found.
func TestTenantInitBook_AnUnreadableAnchorIsAnError(t *testing.T) {
	bi, _ := newClaimedBarrierInit(t, "acme")
	boom := errors.New("anchors unreadable")
	book := tenantInitBook{barrierinit.NewClaimer(barrierinit.NewBook(bi.KV, nil),
		func(context.Context, clusterseal.Barrier) (barrierinit.Anchors, error) {
			return barrierinit.Anchors{}, boom
		})}
	if _, found, err := book.TenantInit(context.Background(), "acme"); !errors.Is(err, boom) || found {
		t.Fatalf("TenantInit = (found %v, %v), want the anchor error", found, err)
	}
}

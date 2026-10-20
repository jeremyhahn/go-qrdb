// Copyright (c) 2026 Jeremy Hahn
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

package lifecycle

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/barrierinit"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// deliveryWiringDouble is a tenant service that records the init delivery it
// is wired with.
type deliveryWiringDouble struct {
	tenant.TenantServicer
	delivery tenant.BarrierInitDelivery
}

func (d *deliveryWiringDouble) SetBarrierInitDelivery(delivery tenant.BarrierInitDelivery) {
	d.delivery = delivery
}

var _ tenant.BarrierInitDeliveryWiring = (*deliveryWiringDouble)(nil)

// newUnpolledDelivery returns a watcherDelivery over bi whose watcher serves
// every tenant barrier and never polls on its own, so each delivery a test
// sees is one it asked for.
func newUnpolledDelivery(t *testing.T, bi *BarrierInit) watcherDelivery {
	t.Helper()
	w, err := barrierinit.NewWatcher(barrierinit.WatcherConfig{
		KV:      bi.KV,
		Anchors: bi.anchors,
		Serves:  func(context.Context, clusterseal.Barrier) (bool, error) { return true, nil },
		Handle:  (&barrierInitWatch{init: bi}).handle,
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	return watcherDelivery{watcher: w}
}

// TestWatcherDelivery_DeliversTheClaimedStatementNow is the regression for
// go-qrdb#812 at the composition: a node asked about a tenant's barrier
// before its watcher polled initializes the barrier from the claimed
// statement on demand.
func TestWatcherDelivery_DeliversTheClaimedStatementNow(t *testing.T) {
	bi, self := newClaimedBarrierInit(t, "acme")
	d := newUnpolledDelivery(t, bi)

	if err := d.DeliverTenantInit(context.Background(), "acme"); err != nil {
		t.Fatalf("DeliverTenantInit: %v", err)
	}
	select {
	case got := <-self.calls:
		if want := (selfInitCall{tenantID: "acme", strategy: seal.StrategyNone}); got != want {
			t.Fatalf("self-init = %+v, want %+v", got, want)
		}
	default:
		t.Fatal("DeliverTenantInit returned before the barrier was initialized")
	}
}

func TestWatcherDelivery_ATenantWithNoStatementDeliversNothing(t *testing.T) {
	bi, self := newClaimedBarrierInit(t, "acme")
	d := newUnpolledDelivery(t, bi)

	if err := d.DeliverTenantInit(context.Background(), "globex"); err != nil {
		t.Fatalf("DeliverTenantInit: %v", err)
	}
	select {
	case got := <-self.calls:
		t.Fatalf("self-init %+v for a tenant with no statement", got)
	default:
	}
}

// The watcher a node starts is the delivery its tenant service answers holder
// quorum requests through; a tenant service that takes none still gets the
// watcher.
func TestRunBarrierInitWatcher_WiresTheTenantServiceDelivery(t *testing.T) {
	ctx := context.Background()
	serves := func(context.Context, clusterseal.Barrier) (bool, error) { return false, nil }
	handle := func(context.Context, barrierinit.InitStatement) error { return nil }

	for name, tc := range map[string]struct {
		svc   tenant.TenantServicer
		wired func(tenant.TenantServicer) bool
	}{
		"wiring": {
			svc:   &deliveryWiringDouble{},
			wired: func(s tenant.TenantServicer) bool { return s.(*deliveryWiringDouble).delivery != nil },
		},
		"no wiring": {svc: unwiredTenantService{}, wired: func(tenant.TenantServicer) bool { return true }},
		"no tenant": {wired: func(tenant.TenantServicer) bool { return true }},
	} {
		t.Run(name, func(t *testing.T) {
			bi, _ := newClaimedBarrierInit(t, "acme")
			n := &Node{BarrierInit: bi, Tenant: tc.svc}
			if err := runBarrierInitWatcher(n, serves, handle, slog.New(slog.DiscardHandler)); err != nil {
				t.Fatalf("runBarrierInitWatcher: %v", err)
			}
			if !tc.wired(tc.svc) {
				t.Fatal("the tenant service was not wired with the watcher's delivery")
			}
			if err := n.Stop(ctx); err != nil {
				t.Fatalf("Stop: %v", err)
			}
		})
	}
}

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

package tenant

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// recordingDelivery is a BarrierInitDelivery that records each tenant it is
// asked to deliver and answers err.
type recordingDelivery struct {
	mu      sync.Mutex
	tenants []string
	err     error
}

var _ BarrierInitDelivery = (*recordingDelivery)(nil)

func (d *recordingDelivery) DeliverTenantInit(_ context.Context, tenantID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tenants = append(d.tenants, tenantID)
	return d.err
}

func (d *recordingDelivery) calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.tenants...)
}

func TestDeliverInit_UnwiredDeliversNothing(t *testing.T) {
	var c barrierInitClaims
	if err := c.deliverInit(context.Background(), "acme"); err != nil {
		t.Fatalf("deliverInit with no delivery wired = %v, want nil", err)
	}
	c.initDelivery.Store(&initDeliveryBox{})
	if err := c.deliverInit(context.Background(), "acme"); err != nil {
		t.Fatalf("deliverInit with a nil delivery wired = %v, want nil", err)
	}
}

func TestDeliverInit_WrapsTheDeliveryError(t *testing.T) {
	unreadable := errors.New("init record unreadable")
	d := &recordingDelivery{err: unreadable}
	svc := newHolderTenantService(t)
	svc.SetBarrierInitDelivery(d)

	err := svc.deliverInit(context.Background(), "acme")
	var be *BarrierError
	if !errors.As(err, &be) || be.TenantID != "acme" || be.Op != "init delivery" {
		t.Fatalf("deliverInit = %v, want a BarrierError for acme's init delivery", err)
	}
	if !errors.Is(err, unreadable) {
		t.Fatalf("deliverInit = %v, want it to wrap the delivery's error", err)
	}
	if got := d.calls(); len(got) != 1 || got[0] != "acme" {
		t.Fatalf("delivered %v, want [acme]", got)
	}
}

func TestSetBarrierInitDelivery_ReplacesTheWiredDelivery(t *testing.T) {
	first, second := &recordingDelivery{}, &recordingDelivery{}
	svc := newHolderTenantService(t)
	svc.SetBarrierInitDelivery(first)
	svc.SetBarrierInitDelivery(second)
	if err := svc.deliverInit(context.Background(), "acme"); err != nil {
		t.Fatalf("deliverInit: %v", err)
	}
	if len(first.calls()) != 0 || len(second.calls()) != 1 {
		t.Fatalf("calls: first %v, second %v; want only the second", first.calls(), second.calls())
	}
}

// TestTenantBarrierQuorum_DeliversTheInitStatementFirst is the regression for
// go-qrdb#812: a holder quorum request reaching a node other than the
// barrier's creator was answered from a registry the node's init watcher had
// not yet filled. Begin and submit now deliver the claimed statement first.
func TestTenantBarrierQuorum_DeliversTheInitStatementFirst(t *testing.T) {
	const tenantID = "acme"
	svc := newHolderTenantService(t)
	ctx := tenantAdminCtx(tenantID)
	if _, err := svc.CreateTenant(context.Background(), tenantID, "Acme", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	holders := []*tenantHolder{newTenantHolder(t, "alice"), newTenantHolder(t, "bob")}
	shamirTenantWithClaimedHolders(t, svc, ctx, tenantID, holders)
	d := &recordingDelivery{}
	svc.SetBarrierInitDelivery(d)

	if _, err := submitTenant(t, svc, ctx, tenantID, seal.OpSeal, holders[0]); err != nil {
		t.Fatalf("seal vote: %v", err)
	}
	// submitTenant begins the quorum and then submits: one delivery each.
	if got := d.calls(); len(got) != 2 || got[0] != tenantID || got[1] != tenantID {
		t.Fatalf("delivered %v, want [%s %s]", got, tenantID, tenantID)
	}
}

// A delivery that fails refuses the request rather than answering it from a
// registry that may be behind the creator's.
func TestTenantBarrierQuorum_RefusedWhenDeliveryFails(t *testing.T) {
	const tenantID = "acme"
	svc := newHolderTenantService(t)
	ctx := tenantAdminCtx(tenantID)
	if _, err := svc.CreateTenant(context.Background(), tenantID, "Acme", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	unreadable := errors.New("init record unreadable")
	svc.SetBarrierInitDelivery(&recordingDelivery{err: unreadable})

	if _, err := svc.TenantBarrierQuorumBegin(ctx, tenantID, seal.OpUnseal, "alice", ""); !errors.Is(err, unreadable) {
		t.Fatalf("QuorumBegin = %v, want the delivery's error", err)
	}
	sub := &seal.HolderSubmission{Op: seal.OpUnseal, Holder: "alice", Share: []byte("share")}
	if _, err := svc.TenantBarrierQuorumSubmit(ctx, tenantID, sub); !errors.Is(err, unreadable) {
		t.Fatalf("QuorumSubmit = %v, want the delivery's error", err)
	}
}

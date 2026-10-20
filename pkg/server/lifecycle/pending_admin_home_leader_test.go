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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// leadershipServices is the tenant-home services of a node whose Raft host
// hands it home-group leadership on demand: LeadsDatabase answers from the
// injected leadership, and handOver flips it and delivers the leader-ready
// event to whatever AttachTenantHomes registered, as the host's dispatcher
// does.
type leadershipServices struct {
	fakeHomeServices
	*injectedLeadership
	handlers []func(tenantID string)
}

func (s *leadershipServices) OnHomeLeaderReady(fn func(tenantID string)) {
	s.handlers = append(s.handlers, fn)
}

func (s *leadershipServices) handOver(tenantID string) {
	s.leads.Store(true)
	for _, fn := range s.handlers {
		fn(tenantID)
	}
}

var (
	_ homeLeaderEvents = (*leadershipServices)(nil)
	_ databaseLeader   = (*leadershipServices)(nil)
)

// homeLeaderPair is one tenant's pending admin as two nodes of its home group
// see it: the replicated pending, user and role stores are shared, node A
// served the init while another node led the home group, and node B -- whose
// barrier for the tenant is initialized and unsealed -- is about to be handed
// the leadership.
type homeLeaderPair struct {
	pending  *gatedPendingAdmins
	users    user.UserServicer
	a, b     *Node
	services *leadershipServices
}

func newHomeLeaderPair(t *testing.T) *homeLeaderPair {
	t.Helper()
	ctx := context.Background()
	users, roles := newRealUserAndRole(t)
	p := &homeLeaderPair{
		pending:  &gatedPendingAdmins{Store: tenantadmin.NewKVStore(kvstore.NewMemoryKVStore()), reads: make(chan struct{}, 8)},
		users:    users,
		services: &leadershipServices{injectedLeadership: &injectedLeadership{}},
	}
	p.a, _ = homeGroupNode(p.pending, users, roles, time.Minute)
	p.b, _ = homeGroupNode(p.pending, users, roles, time.Minute)
	p.b.StoreResolver = &homeRouterResolver{}
	p.b.Database = &provisioningDatabase{}

	// B's barrier comes up before the admin is onboarded, so its own init
	// finds nothing pending.
	svc := tenantServiceWithHook(t, p.b, "acme")
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 0, 0, seal.StrategyNone, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy on B: %v", err)
	}
	p.b.Tenant = svc
	if !AttachTenantHomes(p.b, p.services) {
		t.Fatal("AttachTenantHomes on B refused")
	}

	putPending(t, p.pending, "acme", "admin", spkiThumbprint(t))
	if err := p.a.completePendingTenantAdmin(discardLog())(ctx, "acme", tenant.AdminCompletionOnce); !errors.Is(err, tenant.ErrTenantAdminPending) {
		t.Fatalf("the init's completion on A = %v; want tenant.ErrTenantAdminPending", err)
	}
	p.drainReads()
	return p
}

func (p *homeLeaderPair) drainReads() {
	for len(p.pending.reads) > 0 {
		<-p.pending.reads
	}
}

// handOver hands B the leadership of tenantID's home group and waits for the
// completion the event started, which runs as B's own work.
func (p *homeLeaderPair) handOver(tenantID string) {
	p.services.handOver(tenantID)
	p.b.work.wg.Wait()
}

// TestAttachTenantHomes_HomeLeaderCompletesAPendingAdmin is the trigger an
// init on a non-leader was missing: the record it leaves pending is completed
// by the node that next becomes the home group's leader, the moment it does,
// with no init, unseal or status read on that node.
func TestAttachTenantHomes_HomeLeaderCompletesAPendingAdmin(t *testing.T) {
	ctx := context.Background()
	p := newHomeLeaderPair(t)
	if _, err := p.users.GetUserByUsername(ctx, "acme", "admin"); err == nil {
		t.Fatal("test precondition: the admin exists before any node led the home group")
	}

	p.handOver("acme")

	if got := len(p.pending.reads); got != 1 {
		t.Fatalf("gaining the leadership ran the completion %d times; want once", got)
	}
	if _, err := p.users.GetUserByUsername(ctx, "acme", "admin"); err != nil {
		t.Fatalf("the admin after B gained the leadership: %v; want the account created", err)
	}
	if got, err := p.pending.Store.Get(ctx, "acme"); err != nil || got != nil {
		t.Fatalf("pending after the completion = %+v, %v; want the record deleted", got, err)
	}
}

// TestAttachTenantHomes_HomeLeaderCompletionErrorKeepsThePendingAdmin proves
// a completion the new leader cannot finish leaves the record for the next
// trigger, and the leadership change itself does not fail.
func TestAttachTenantHomes_HomeLeaderCompletionErrorKeepsThePendingAdmin(t *testing.T) {
	ctx := context.Background()
	p := newHomeLeaderPair(t)
	// An account of the admin's name that is not the tenant's Admin: the
	// completion refuses to adopt it.
	if _, err := p.users.CreateUser(ctx, &user.CreateUserRequest{WithoutEmailLoginKey: true, Username: "admin", TenantID: "acme"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	p.handOver("acme")

	if got := len(p.pending.reads); got != 1 {
		t.Fatalf("gaining the leadership ran the completion %d times; want once", got)
	}
	if got, err := p.pending.Store.Get(ctx, "acme"); err != nil || got == nil {
		t.Fatalf("pending after the failed completion = %v, %v; want the record kept", got, err)
	}
}

// TestAttachTenantHomes_OtherTenantsLeadershipLeavesThePendingAdmin proves
// the trigger is per tenant: leading another tenant's home group completes
// nothing for this one.
func TestAttachTenantHomes_OtherTenantsLeadershipLeavesThePendingAdmin(t *testing.T) {
	ctx := context.Background()
	p := newHomeLeaderPair(t)

	p.handOver("globex")

	if _, err := p.users.GetUserByUsername(ctx, "acme", "admin"); err == nil {
		t.Fatal("leading globex's home group created acme's admin")
	}
	if got, err := p.pending.Store.Get(ctx, "acme"); err != nil || got == nil {
		t.Fatalf("pending = %v, %v; want acme's record kept", got, err)
	}
}

// stoppablePendingAdmins is a pending-admin store whose Get blocks until the
// completion reading it is cancelled, as a read through a Raft host that is
// closing does. It signals entered when a read starts and reports the
// cancellation it saw on cancelled.
type stoppablePendingAdmins struct {
	tenantadmin.Store
	entered   chan struct{}
	cancelled chan error
}

func newStoppablePendingAdmins(store tenantadmin.Store) *stoppablePendingAdmins {
	return &stoppablePendingAdmins{Store: store, entered: make(chan struct{}, 8), cancelled: make(chan error, 8)}
}

func (s *stoppablePendingAdmins) Get(ctx context.Context, _ string) (*tenantadmin.Pending, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	select {
	case s.cancelled <- ctx.Err():
	default:
	}
	return nil, ctx.Err()
}

// TestAttachTenantHomes_HomeLeaderTriggerStopsWithTheNode is the shutdown
// hang: a node that gained a home group's leadership as it stopped ran the
// completion on the Raft host's event dispatcher, detached from every
// context, so it retried for its whole budget and the host never closed. The
// leader-ready event returns at once, and Stop cancels the completion and
// waits for it.
func TestAttachTenantHomes_HomeLeaderTriggerStopsWithTheNode(t *testing.T) {
	p := newHomeLeaderPair(t)
	blocked := newStoppablePendingAdmins(p.pending)
	p.b.PendingAdmins = blocked

	p.services.handOver("acme")
	<-blocked.entered

	if err := p.b.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case err := <-blocked.cancelled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the completion ended with %v; want it cancelled by Stop", err)
		}
	default:
		t.Fatal("Stop returned while the completion it started was still running")
	}
}

// TestNode_PendingAdminCompletionWithinStopsWithTheNode proves the completion
// an unseal or status read starts, which outlives that request, does not
// outlive the node: Stop cancels it.
func TestNode_PendingAdminCompletionWithinStopsWithTheNode(t *testing.T) {
	p := newHomeLeaderPair(t)
	blocked := newStoppablePendingAdmins(p.pending)
	p.b.PendingAdmins = blocked

	returned := make(chan error, 1)
	go func() {
		returned <- p.b.completePendingTenantAdmin(discardLog())(context.Background(), "acme", tenant.AdminCompletionWithin)
	}()
	<-blocked.entered

	if err := p.b.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	<-returned
	if err := <-blocked.cancelled; !errors.Is(err, context.Canceled) {
		t.Fatalf("the completion ended with %v; want it cancelled by Stop", err)
	}
}

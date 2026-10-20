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
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// injectedLeadership is a databaseLeader whose answer the test sets.
type injectedLeadership struct {
	leads   atomic.Bool
	askedDB atomic.Value
}

func (l *injectedLeadership) LeadsDatabase(_, db string) bool {
	l.askedDB.Store(db)
	return l.leads.Load()
}

var _ databaseLeader = (*injectedLeadership)(nil)

// quickBounds retry at once, so a test waits on the work, not the backoff.
func quickBounds(budget time.Duration) bootstrap.AdminCompletionBounds {
	return bootstrap.AdminCompletionBounds{Budget: budget, AttemptTimeout: 5 * time.Second, Delay: time.Millisecond, MaxDelay: time.Millisecond}
}

// homeGroupNode is one node of a tenant's home group over the group's shared
// replicated identity state.
func homeGroupNode(pending tenantadmin.Store, users user.UserServicer, roles role.RoleServicer, budget time.Duration) (*Node, *injectedLeadership) {
	n := &Node{PendingAdmins: pending, User: users, Role: roles, adminCompletionBounds: quickBounds(budget)}
	leadership := &injectedLeadership{}
	n.homeLeader.Store(&databaseLeadership{leadership})
	return n, leadership
}

// TestCompletePendingTenantAdmin_OnlyTheHomeLeaderCompletes: a follower's
// completion writes nothing and gives up within its budget, the leader's
// completes, and the follower's next completion finds nothing to do.
func TestCompletePendingTenantAdmin_OnlyTheHomeLeaderCompletes(t *testing.T) {
	ctx := context.Background()
	users, roles := newRealUserAndRole(t)
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	putPending(t, pending, "acme", "admin", spkiThumbprint(t))
	follower, _ := homeGroupNode(pending, users, roles, 20*time.Millisecond)
	leader, leadership := homeGroupNode(pending, users, roles, 5*time.Second)
	leadership.leads.Store(true)

	var deferred *bootstrap.AdminCompletionDeferredError
	if err := follower.completePendingTenantAdmin(discardLog())(ctx, "acme", tenant.AdminCompletionWithin); !errors.As(err, &deferred) {
		t.Fatalf("the follower's completion = %v; want *bootstrap.AdminCompletionDeferredError at its budget", err)
	}
	if _, err := users.GetUserByUsername(ctx, "acme", "admin"); err == nil {
		t.Fatal("a follower created the admin account")
	}
	if got, err := pending.Get(ctx, "acme"); err != nil || got == nil {
		t.Fatalf("pending after the follower = %v, %v; want the record kept", got, err)
	}

	if err := leader.completePendingTenantAdmin(discardLog())(ctx, "acme", tenant.AdminCompletionWithin); err != nil {
		t.Fatalf("the leader's completion = %v; want nil", err)
	}
	admin, err := users.GetUserByUsername(ctx, "acme", "admin")
	if err != nil {
		t.Fatalf("the leader did not create the admin account: %v", err)
	}
	if db, _ := leadership.askedDB.Load().(string); db != database.TenantHomeName {
		t.Fatalf("leadership asked of database %q; want the tenant home %q", db, database.TenantHomeName)
	}

	if err := follower.completePendingTenantAdmin(discardLog())(ctx, "acme", tenant.AdminCompletionWithin); err != nil {
		t.Fatalf("the follower's completion after the leader's = %v; want nil", err)
	}
	again, err := users.GetUserByUsername(ctx, "acme", "admin")
	if err != nil || again.ID != admin.ID || len(again.CertBindings) != 1 {
		t.Fatalf("admin after the follower's retry = %+v, %v; want the leader's account, one binding", again, err)
	}
}

// TestCompletePendingTenantAdmin_AFollowerThatBecomesLeaderFinishes: a
// follower's completion keeps checking within its budget and completes the
// still-pending record once the node takes the home group's leadership.
func TestCompletePendingTenantAdmin_AFollowerThatBecomesLeaderFinishes(t *testing.T) {
	ctx := context.Background()
	users, roles := newRealUserAndRole(t)
	gate := &gatedPendingAdmins{Store: tenantadmin.NewKVStore(kvstore.NewMemoryKVStore()), reads: make(chan struct{}, 1)}
	putPending(t, gate, "acme", "admin", spkiThumbprint(t))
	n, leadership := homeGroupNode(gate, users, roles, time.Minute)

	// The hook runs inside its request; the test is that request's caller.
	errc := make(chan error, 1)
	go func() { errc <- n.completePendingTenantAdmin(discardLog())(ctx, "acme", tenant.AdminCompletionWithin) }()
	<-gate.reads // the first attempt has read the record as a follower
	leadership.leads.Store(true)
	if err := <-errc; err != nil {
		t.Fatalf("the completion = %v; want nil once the node leads", err)
	}

	if _, err := users.GetUserByUsername(ctx, "acme", "admin"); err != nil {
		t.Fatalf("the new leader did not finish the completion: %v", err)
	}
	if got, err := gate.Store.Get(ctx, "acme"); err != nil || got != nil {
		t.Fatalf("pending after the new leader = %v, %v; want it gone", got, err)
	}
}

// gatedPendingAdmins signals each read.
type gatedPendingAdmins struct {
	tenantadmin.Store
	reads chan struct{}
}

func (g *gatedPendingAdmins) Get(ctx context.Context, tenantID string) (*tenantadmin.Pending, error) {
	select {
	case g.reads <- struct{}{}:
	default:
	}
	return g.Store.Get(ctx, tenantID)
}

// TestCompletePendingTenantAdmin_OnceLeavesTheRecordWithoutALeader is a
// Shamir init on a node whose tenant home group has no leader yet: one
// attempt, then tenant.ErrTenantAdminPending with the record kept and no
// account written. A minute's budget would hold the test for a minute if the
// mode retried; the single read proves it did not.
func TestCompletePendingTenantAdmin_OnceLeavesTheRecordWithoutALeader(t *testing.T) {
	ctx := context.Background()
	users, roles := newRealUserAndRole(t)
	gate := &gatedPendingAdmins{Store: tenantadmin.NewKVStore(kvstore.NewMemoryKVStore()), reads: make(chan struct{}, 8)}
	putPending(t, gate, "acme", "admin", spkiThumbprint(t))
	n, _ := homeGroupNode(gate, users, roles, time.Minute)

	if err := n.completePendingTenantAdmin(discardLog())(ctx, "acme", tenant.AdminCompletionOnce); !errors.Is(err, tenant.ErrTenantAdminPending) {
		t.Fatalf("the completion = %v; want tenant.ErrTenantAdminPending", err)
	}
	if got := len(gate.reads); got != 1 {
		t.Fatalf("the completion read the record %d times; want one attempt", got)
	}
	if _, err := users.GetUserByUsername(ctx, "acme", "admin"); err == nil {
		t.Fatal("a node without the home group's leadership created the admin account")
	}
	if got, err := gate.Store.Get(ctx, "acme"); err != nil || got == nil {
		t.Fatalf("pending after the attempt = %v, %v; want the record kept", got, err)
	}
}

// TestCompletePendingTenantAdmin_OnceCompletesOnTheLeader is a Shamir init on
// the home group's leader: the one attempt creates the admin.
func TestCompletePendingTenantAdmin_OnceCompletesOnTheLeader(t *testing.T) {
	ctx := context.Background()
	users, roles := newRealUserAndRole(t)
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	putPending(t, pending, "acme", "admin", spkiThumbprint(t))
	n, leadership := homeGroupNode(pending, users, roles, time.Minute)
	leadership.leads.Store(true)

	if err := n.completePendingTenantAdmin(discardLog())(ctx, "acme", tenant.AdminCompletionOnce); err != nil {
		t.Fatalf("the completion = %v; want nil", err)
	}
	if _, err := users.GetUserByUsername(ctx, "acme", "admin"); err != nil {
		t.Fatalf("the leader did not create the admin account: %v", err)
	}
	if got, err := pending.Get(ctx, "acme"); err != nil || got != nil {
		t.Fatalf("pending after the leader = %v, %v; want it gone", got, err)
	}
}

// TestCompletePendingTenantAdmin_OnceFailsOnOtherErrors keeps a failure that
// is not the home group's a failure: an unreadable record is not pending.
func TestCompletePendingTenantAdmin_OnceFailsOnOtherErrors(t *testing.T) {
	users, roles := newRealUserAndRole(t)
	n, leadership := homeGroupNode(failingPendingAdmins{}, users, roles, time.Minute)
	leadership.leads.Store(true)

	err := n.completePendingTenantAdmin(discardLog())(context.Background(), "acme", tenant.AdminCompletionOnce)
	var readErr *bootstrap.PendingTenantAdminError
	if !errors.As(err, &readErr) || errors.Is(err, tenant.ErrTenantAdminPending) {
		t.Fatalf("the completion = %v; want *bootstrap.PendingTenantAdminError, not pending", err)
	}
}

// TestCompletePendingTenantAdmin_ALaterTriggerFinishesAPendingOnce is the
// rest of a Shamir init: the init's attempt leaves the record, the holders
// unseal the other nodes, the home group elects this node, and the next
// unseal or status read -- AdminCompletionWithin -- completes the admin.
func TestCompletePendingTenantAdmin_ALaterTriggerFinishesAPendingOnce(t *testing.T) {
	ctx := context.Background()
	users, roles := newRealUserAndRole(t)
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	putPending(t, pending, "acme", "admin", spkiThumbprint(t))
	n, leadership := homeGroupNode(pending, users, roles, time.Minute)
	hook := n.completePendingTenantAdmin(discardLog())

	if err := hook(ctx, "acme", tenant.AdminCompletionOnce); !errors.Is(err, tenant.ErrTenantAdminPending) {
		t.Fatalf("the init's completion = %v; want tenant.ErrTenantAdminPending", err)
	}
	leadership.leads.Store(true)
	if err := hook(ctx, "acme", tenant.AdminCompletionWithin); err != nil {
		t.Fatalf("the later trigger's completion = %v; want nil", err)
	}
	if _, err := users.GetUserByUsername(ctx, "acme", "admin"); err != nil {
		t.Fatalf("the later trigger did not create the admin account: %v", err)
	}
	if got, err := pending.Get(ctx, "acme"); err != nil || got != nil {
		t.Fatalf("pending after the later trigger = %v, %v; want it gone", got, err)
	}
}

// failingPendingAdmins fails every read.
type failingPendingAdmins struct{ tenantadmin.Store }

var errPendingUnreadable = errors.New("pending admins: unreadable")

func (failingPendingAdmins) Get(context.Context, string) (*tenantadmin.Pending, error) {
	return nil, errPendingUnreadable
}

// TestNodeHomeLeader covers both node shapes: without replicated homes a node
// is every tenant's only writer; with them it leads only where the tenant's
// home group says so.
func TestNodeHomeLeader(t *testing.T) {
	n := &Node{}
	if !(nodeHomeLeader{n: n}).LeadsHome("acme") {
		t.Fatal("a node without replicated homes does not lead its tenants")
	}
	leadership := &injectedLeadership{}
	n.homeLeader.Store(&databaseLeadership{leadership})
	if (nodeHomeLeader{n: n}).LeadsHome("acme") {
		t.Fatal("a follower of the home group leads it")
	}
	leadership.leads.Store(true)
	if !(nodeHomeLeader{n: n}).LeadsHome("acme") {
		t.Fatal("the home group's leader does not lead it")
	}
}

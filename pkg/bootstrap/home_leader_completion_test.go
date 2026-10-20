package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// homeLeaderFunc is a HomeLeader whose answer the test sets.
type homeLeaderFunc func(tenantID string) bool

func (f homeLeaderFunc) LeadsHome(tenantID string) bool { return f(tenantID) }

var _ HomeLeader = homeLeaderFunc(nil)

// replicatedIdentity is one tenant's replicated identity state as every node
// of its home group sees it: the pending-admin record and the real user and
// role stores, each over an in-memory store with the guarded writes the
// replicated one offers.
type replicatedIdentity struct {
	pending *tenantadmin.KVStore
	users   user.UserServicer
	roles   role.RoleServicer
}

func newReplicatedIdentity(t *testing.T) *replicatedIdentity {
	t.Helper()
	idgen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatal(err)
	}
	users, err := user.NewStore(kvstore.NewMemoryKVStore(), idgen)
	if err != nil {
		t.Fatal(err)
	}
	roles, err := role.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatal(err)
	}
	r := &replicatedIdentity{pending: tenantadmin.NewKVStore(kvstore.NewMemoryKVStore()), users: users, roles: roles}
	if err := r.pending.Create(context.Background(), "acme", &tenantadmin.Pending{
		Username: "admin", Thumbprint: adminThumbprint(),
		NotAfter: time.Now().Add(time.Hour).UTC(), Serial: "1",
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

// node returns the dependencies one node completes with, over the shared
// replicated state, leading the home group while leads says so.
func (r *replicatedIdentity) node(pending tenantadmin.Store, leads func() bool) Dependencies {
	return Dependencies{
		PendingAdmins: pending, Users: r.users, Roles: r.roles, Logger: discardLogger(),
		HomeLeader: homeLeaderFunc(func(string) bool { return leads() }),
	}
}

func (r *replicatedIdentity) admin(t *testing.T) *user.User {
	t.Helper()
	u, err := r.users.(firstbootUserLookup).GetUserByUsername(context.Background(), "acme", "admin")
	if err != nil {
		return nil
	}
	return u
}

func (r *replicatedIdentity) stillPending(t *testing.T) bool {
	t.Helper()
	rec, err := r.pending.Get(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	return rec != nil
}

type firstbootUserLookup interface {
	GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error)
}

func always(v bool) func() bool { return func() bool { return v } }

// adminThumbprint is a canonical SPKI thumbprint: base64url of a SHA-256.
func adminThumbprint() string {
	sum := sha256.Sum256([]byte("admin-spki"))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// TestCompletePendingTenantAdmin_RunsOnceOnTheHomeLeader drives the same
// tenant's completion from a follower and from the leader: the follower
// defers without writing, the leader completes, and a retry is a no-op that
// still succeeds.
func TestCompletePendingTenantAdmin_RunsOnceOnTheHomeLeader(t *testing.T) {
	ctx := context.Background()
	r := newReplicatedIdentity(t)

	done, err := CompletePendingTenantAdmin(ctx, "acme", r.node(r.pending, always(false)))
	var deferred *AdminCompletionDeferredError
	if done || !errors.As(err, &deferred) || deferred.TenantID != "acme" {
		t.Fatalf("follower = %v, %v; want false, *AdminCompletionDeferredError", done, err)
	}
	if r.admin(t) != nil || !r.stillPending(t) {
		t.Fatal("the follower wrote: an admin account exists or the record is gone")
	}
	if _, err := r.roles.GetRole(ctx, role.SystemRoleID("acme", role.RoleNameAdmin)); err == nil {
		t.Fatal("the follower seeded the tenant's roles")
	}

	if done, err := CompletePendingTenantAdmin(ctx, "acme", r.node(r.pending, always(true))); err != nil || !done {
		t.Fatalf("leader = %v, %v; want true, nil", done, err)
	}
	admin := r.admin(t)
	if admin == nil || !slices.Contains(admin.RoleIDs, role.SystemRoleID("acme", role.RoleNameAdmin)) || len(admin.CertBindings) != 1 {
		t.Fatalf("admin after the leader's completion = %+v; want the Admin role and one binding", admin)
	}
	if r.stillPending(t) {
		t.Fatal("the record outlived the leader's completion")
	}

	// A retry on the leader, and the follower's next attempt, find nothing
	// pending: success, and nothing written.
	for name, leads := range map[string]bool{"leader retry": true, "follower": false} {
		if done, err := CompletePendingTenantAdmin(ctx, "acme", r.node(r.pending, always(leads))); err != nil || done {
			t.Fatalf("%s after completion = %v, %v; want false, nil", name, done, err)
		}
	}
	if again := r.admin(t); again.ID != admin.ID || len(again.CertBindings) != 1 {
		t.Fatalf("admin after the retries = %+v; want the same account, one binding", again)
	}
}

// TestCompletePendingTenantAdmin_ANewLeaderFinishesADeposedOnesWork: the
// leader created and bound the admin, then lost the leadership before
// deleting the record. The new leader adopts that account and deletes the
// record; the deposed leader's retry, now a follower, finds nothing to do.
func TestCompletePendingTenantAdmin_ANewLeaderFinishesADeposedOnesWork(t *testing.T) {
	ctx := context.Background()
	r := newReplicatedIdentity(t)
	cut := deleteFailingStore{inner: r.pending, err: errors.New("leadership lost")}

	if _, err := CompletePendingTenantAdmin(ctx, "acme", r.node(cut, always(true))); err == nil {
		t.Fatal("the deposed leader's cut-off attempt reported success")
	}
	first := r.admin(t)
	if first == nil || !r.stillPending(t) {
		t.Fatal("setup: want the account made and the record still pending")
	}

	if done, err := CompletePendingTenantAdmin(ctx, "acme", r.node(r.pending, always(true))); err != nil || !done {
		t.Fatalf("new leader = %v, %v; want true, nil", done, err)
	}
	if got := r.admin(t); got.ID != first.ID || len(got.CertBindings) != 1 {
		t.Fatalf("admin after the new leader = %+v; want the deposed leader's account, one binding", got)
	}
	if r.stillPending(t) {
		t.Fatal("the new leader left the record")
	}
	if done, err := CompletePendingTenantAdmin(ctx, "acme", r.node(r.pending, always(false))); err != nil || done {
		t.Fatalf("deposed leader's retry = %v, %v; want false, nil", done, err)
	}
}

// racingPendingAdmins runs a rival completion between this one's read and
// its writes: two nodes that both believe they lead, as across an election.
type racingPendingAdmins struct {
	tenantadmin.Store
	rival func()
}

func (r *racingPendingAdmins) Get(ctx context.Context, tenantID string) (*tenantadmin.Pending, error) {
	p, err := r.Store.Get(ctx, tenantID)
	if r.rival != nil {
		rival := r.rival
		r.rival = nil
		rival()
	}
	return p, err
}

// TestCompletePendingTenantAdmin_LosingTheRaceIsSuccess: a completion whose
// record a rival completed after it was read adopts the rival's account, its
// guarded delete removes nothing, and it reports success.
func TestCompletePendingTenantAdmin_LosingTheRaceIsSuccess(t *testing.T) {
	ctx := context.Background()
	r := newReplicatedIdentity(t)
	var rivalDone bool
	racing := &racingPendingAdmins{Store: r.pending, rival: func() {
		done, err := CompletePendingTenantAdmin(ctx, "acme", r.node(r.pending, always(true)))
		rivalDone = done && err == nil
	}}

	done, err := CompletePendingTenantAdmin(ctx, "acme", r.node(racing, always(true)))
	if err != nil || done {
		t.Fatalf("the slower completion = %v, %v; want false, nil", done, err)
	}
	if !rivalDone {
		t.Fatal("the rival did not complete")
	}
	if admin := r.admin(t); admin == nil || len(admin.CertBindings) != 1 || r.stillPending(t) {
		t.Fatalf("after the race: admin %+v, pending %v; want one bound account, no record", admin, r.stillPending(t))
	}
}

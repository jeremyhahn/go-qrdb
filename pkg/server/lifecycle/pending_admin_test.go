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
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// spkiThumbprint mints a throwaway self-signed certificate and returns its
// canonical SPKI thumbprint -- the shape AddCertBinding requires, which a
// hand-typed string is not.
func spkiThumbprint(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "admin"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return user.ComputeSPKIThumbprint(c)
}

// discardLog returns a logger that writes nowhere, for tests that only
// care about the hook's side effects.
func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newRealUserAndRole builds n.User/n.Role exactly the way BuildServices
// builds them for a non-dedicated-isolation tenant: user.NewStore and
// role.NewStore directly over a shared kvstore.KVStore, so
// completePendingTenantAdmin's success path is exercised against the real
// provisioning seams, not a hand-rolled double.
func newRealUserAndRole(t *testing.T) (user.UserServicer, role.RoleServicer) {
	t.Helper()
	store := kvstore.NewMemoryKVStore()
	idGen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	users, err := user.NewStore(store, idGen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	roles, err := role.NewStore(store)
	if err != nil {
		t.Fatalf("role.NewStore: %v", err)
	}
	return users, roles
}

// sealedRoleServicer embeds a REAL role.RoleServicer for every method except
// CreateRole, which it fails with a fixed error -- the one condition this
// file's deferred-on-sealed-tenant test needs, without losing fidelity on
// every other method a less careful double would have to stub out blind.
type sealedRoleServicer struct {
	role.RoleServicer
	err error
}

func (s *sealedRoleServicer) CreateRole(context.Context, *role.CreateRoleRequest) (*role.Role, error) {
	return nil, s.err
}

// putPending registers tenantID's pending admin record directly, bypassing
// the onboarding flow that normally writes it.
func putPending(t *testing.T, store tenantadmin.Store, tenantID, username, thumbprint string) {
	t.Helper()
	if err := store.Create(context.Background(), tenantID, &tenantadmin.Pending{
		Username:   username,
		Thumbprint: thumbprint,
	}); err != nil {
		t.Fatalf("Create pending admin: %v", err)
	}
}

// ---------------------------------------------------------------------------
// nil dependencies: completePendingTenantAdmin's hook must be a no-op.
// ---------------------------------------------------------------------------

// TestCompletePendingTenantAdmin_NilPendingAdmins_NoOp proves a Node with no
// PendingAdmins store at all runs the hook with no effect and no panic.
func TestCompletePendingTenantAdmin_NilPendingAdmins_NoOp(t *testing.T) {
	users, roles := newRealUserAndRole(t)
	n := &Node{User: users, Role: roles} // PendingAdmins deliberately nil

	hook := n.completePendingTenantAdmin(discardLog())
	if err := hook(context.Background(), "acme", tenant.AdminCompletionWithin); err != nil {
		t.Fatalf("hook = %v; want nil", err)
	}
}

// TestCompletePendingTenantAdmin_NilUser_NoOp proves a Node with no User
// service leaves a pending record untouched -- the hook does not even
// attempt completion, so the record is still there to retry once a user
// service is wired.
func TestCompletePendingTenantAdmin_NilUser_NoOp(t *testing.T) {
	ctx := context.Background()
	_, roles := newRealUserAndRole(t)
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	putPending(t, pending, "acme", "admin", "thumbprint-abc")

	n := &Node{PendingAdmins: pending, Role: roles} // User deliberately nil

	hook := n.completePendingTenantAdmin(discardLog())
	if err := hook(ctx, "acme", tenant.AdminCompletionWithin); err != nil {
		t.Fatalf("hook = %v; want nil", err)
	}

	got, err := pending.Get(ctx, "acme")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected the pending record to remain untouched with no User service wired")
	}
}

// TestCompletePendingTenantAdmin_NilRole_NoOp is the same proof for a Node
// with no Role service.
func TestCompletePendingTenantAdmin_NilRole_NoOp(t *testing.T) {
	ctx := context.Background()
	users, _ := newRealUserAndRole(t)
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	putPending(t, pending, "acme", "admin", "thumbprint-abc")

	n := &Node{PendingAdmins: pending, User: users} // Role deliberately nil

	hook := n.completePendingTenantAdmin(discardLog())
	if err := hook(ctx, "acme", tenant.AdminCompletionWithin); err != nil {
		t.Fatalf("hook = %v; want nil", err)
	}

	got, err := pending.Get(ctx, "acme")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected the pending record to remain untouched with no Role service wired")
	}
}

// ---------------------------------------------------------------------------
// tenant.ErrTenantSealed: the hook defers rather than consuming the record.
// ---------------------------------------------------------------------------

// TestCompletePendingTenantAdmin_ErrTenantSealed_Deferred proves that when
// completion fails because the tenant's identity records are still
// unreadable (tenant.ErrTenantSealed), the hook logs and returns without
// consuming the pending record -- it is retried on the next unseal or
// status read.
func TestCompletePendingTenantAdmin_ErrTenantSealed_Deferred(t *testing.T) {
	ctx := context.Background()
	users, roles := newRealUserAndRole(t)
	sealedRoles := &sealedRoleServicer{RoleServicer: roles, err: tenant.ErrTenantSealed}
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	putPending(t, pending, "acme", "admin", "thumbprint-abc")

	n := &Node{PendingAdmins: pending, User: users, Role: sealedRoles}

	hook := n.completePendingTenantAdmin(discardLog())
	if err := hook(ctx, "acme", tenant.AdminCompletionWithin); err != nil {
		t.Fatalf("hook = %v; want nil", err)
	}

	got, err := pending.Get(ctx, "acme")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected the pending record to remain for a later retry after a sealed-tenant deferral")
	}

	if _, err := users.GetUserByUsername(ctx, "acme", "admin"); err == nil {
		t.Fatal("expected no admin account to have been created while the tenant is reported sealed")
	}
}

// ---------------------------------------------------------------------------
// Success: the admin account is created, bound, and the record consumed.
// ---------------------------------------------------------------------------

// TestCompletePendingTenantAdmin_Success_CreatesAccount proves the hook
// creates the onboarded admin's account, binds its certificate thumbprint,
// and deletes the pending record -- using the REAL user and role services
// built the same way BuildServices builds them.
func TestCompletePendingTenantAdmin_Success_CreatesAccount(t *testing.T) {
	ctx := context.Background()
	users, roles := newRealUserAndRole(t)
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	thumbprint := spkiThumbprint(t)
	putPending(t, pending, "acme", "admin", thumbprint)

	n := &Node{PendingAdmins: pending, User: users, Role: roles}

	hook := n.completePendingTenantAdmin(discardLog())
	if err := hook(ctx, "acme", tenant.AdminCompletionWithin); err != nil {
		t.Fatalf("hook = %v; want nil", err)
	}

	u, err := users.GetUserByUsername(ctx, "acme", "admin")
	if err != nil {
		t.Fatalf("expected the admin account to have been created, GetUserByUsername: %v", err)
	}
	foundBinding := false
	for _, b := range u.CertBindings {
		if b.Thumbprint == thumbprint {
			foundBinding = true
		}
	}
	if !foundBinding {
		t.Errorf("expected the onboarded certificate's thumbprint to be bound, got bindings: %+v", u.CertBindings)
	}

	got, err := pending.Get(ctx, "acme")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Error("expected the pending record to be deleted once completion succeeds")
	}
}

// callerKey marks the context a caller raised the hook with.
type callerKey struct{}

// roleCall is the context one CreateRole ran under, as it stood at the call.
type roleCall struct {
	caller      any
	err         error
	hasDeadline bool
}

// contextRoleServicer embeds a REAL role.RoleServicer and records the context
// each CreateRole runs under before delegating.
type contextRoleServicer struct {
	role.RoleServicer
	seen []roleCall
}

func (s *contextRoleServicer) CreateRole(ctx context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
	_, hasDeadline := ctx.Deadline()
	s.seen = append(s.seen, roleCall{caller: ctx.Value(callerKey{}), err: ctx.Err(), hasDeadline: hasDeadline})
	return s.RoleServicer.CreateRole(ctx, req)
}

// TestCompletePendingTenantAdmin_RunsAsTheCallerOnItsOwnClock proves the
// completion keeps the caller's identity but not the caller's deadline: an
// init that spent its whole request budget waiting for the tenant's groups to
// elect still gets its first admin.
func TestCompletePendingTenantAdmin_RunsAsTheCallerOnItsOwnClock(t *testing.T) {
	users, roles := newRealUserAndRole(t)
	recording := &contextRoleServicer{RoleServicer: roles}
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	putPending(t, pending, "acme", "admin", spkiThumbprint(t))
	n := &Node{PendingAdmins: pending, User: users, Role: recording}

	caller, cancel := context.WithCancel(context.WithValue(context.Background(), callerKey{}, "rt1-admin"))
	cancel() // the request's deadline is already spent
	if err := n.completePendingTenantAdmin(discardLog())(caller, "acme", tenant.AdminCompletionWithin); err != nil {
		t.Fatalf("hook = %v; want nil", err)
	}

	if len(recording.seen) == 0 {
		t.Fatal("the completion created no role")
	}
	for _, call := range recording.seen {
		if call.caller != "rt1-admin" {
			t.Fatal("the completion did not run as the caller")
		}
		if call.err != nil {
			t.Fatalf("the completion ran on the caller's spent clock: %v", call.err)
		}
		if !call.hasDeadline {
			t.Fatal("the completion ran without a deadline of its own")
		}
	}
	if _, err := users.GetUserByUsername(context.Background(), "acme", "admin"); err != nil {
		t.Fatalf("the admin account was not created: %v", err)
	}
}

// TestCompletePendingTenantAdmin_NoRecord_NoOp proves a tenant with no
// pending admin at all is a harmless no-op -- the common case, since this
// hook runs on every barrier-ready event for every tenant.
func TestCompletePendingTenantAdmin_NoRecord_NoOp(t *testing.T) {
	ctx := context.Background()
	users, roles := newRealUserAndRole(t)
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())

	n := &Node{PendingAdmins: pending, User: users, Role: roles}

	hook := n.completePendingTenantAdmin(discardLog())
	if err := hook(ctx, "no-such-tenant", tenant.AdminCompletionWithin); err != nil {
		t.Fatalf("hook = %v; want nil", err)
	}

	if _, err := users.GetUserByUsername(ctx, "no-such-tenant", "admin"); err == nil {
		t.Fatal("expected no account to be created for a tenant with no pending record")
	}
}

// tenantServiceWithHook builds a TenantService whose barrier-ready hook is
// n's, over an initialized in-memory system barrier, and creates tenantID in
// it.
func tenantServiceWithHook(t *testing.T, n *Node, tenantID string) *tenant.TenantService {
	t.Helper()
	ctx := context.Background()
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte("pending-admin-system-passphrase"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	system, err := seal.NewStorageBarrier(discardLog(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "system/root-key"}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := system.Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatalf("Initialize (system): %v", err)
	}
	reg, _, _ := sealtest.PooledRegistry(t, system)
	svc, err := tenant.NewTenantService(seal.NewMemoryBackend(), reg, discardLog(),
		tenant.WithBarrierReadyHook(n.completePendingTenantAdmin(discardLog())))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	if _, err := svc.CreateTenant(ctx, tenantID, tenantID, ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	return svc
}

// TestCompletePendingTenantAdmin_AccountExistsWhenInitReturns proves the
// pending admin's account is created inside the barrier init request: the
// moment the init returns, the account exists and the record is gone, so the
// caller's next request -- the admin's first login -- finds it.
func TestCompletePendingTenantAdmin_AccountExistsWhenInitReturns(t *testing.T) {
	ctx := context.Background()
	users, roles := newRealUserAndRole(t)
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	n := &Node{PendingAdmins: pending, User: users, Role: roles}
	svc := tenantServiceWithHook(t, n, "acme")
	putPending(t, pending, "acme", "admin", spkiThumbprint(t))

	if _, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", []byte("acme-passphrase"), 0, 0, seal.StrategyPassphrase, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}

	admin, err := users.GetUserByUsername(ctx, "acme", "admin")
	if err != nil {
		t.Fatalf("the admin account did not exist when the init returned: %v", err)
	}
	if len(admin.CertBindings) != 1 {
		t.Fatalf("admin bindings = %+v; want the onboarded certificate's", admin.CertBindings)
	}
	if got, err := pending.Get(ctx, "acme"); err != nil || got != nil {
		t.Fatalf("pending after the init = %+v, %v; want it gone", got, err)
	}
}

// TestCompletePendingTenantAdmin_InitFailsWithTheCompletionError proves a
// completion that cannot create the account fails the init request with the
// completion's typed error rather than answering success over a missing
// admin.
func TestCompletePendingTenantAdmin_InitFailsWithTheCompletionError(t *testing.T) {
	ctx := context.Background()
	users, roles := newRealUserAndRole(t)
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
	n := &Node{PendingAdmins: pending, User: users, Role: roles}
	svc := tenantServiceWithHook(t, n, "acme")
	putPending(t, pending, "acme", "admin", spkiThumbprint(t))
	// An account of the admin's name that is not the tenant's Admin: the
	// completion must not adopt it, and no retry changes that.
	if _, err := users.CreateUser(ctx, &user.CreateUserRequest{WithoutEmailLoginKey: true, Username: "admin", TenantID: "acme"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	res, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", []byte("acme-passphrase"), 0, 0, seal.StrategyPassphrase, nil)
	var conflict *bootstrap.PendingAdminAccountConflictError
	if !errors.As(err, &conflict) || res != nil {
		t.Fatalf("TenantBarrierInitWithStrategy = %+v, %v; want nil and *bootstrap.PendingAdminAccountConflictError", res, err)
	}
	if got, err := pending.Get(ctx, "acme"); err != nil || got == nil {
		t.Fatalf("pending after the failed init = %+v, %v; want it kept for the next unseal or status read", got, err)
	}
}

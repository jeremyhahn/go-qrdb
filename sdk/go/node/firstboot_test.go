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

package node

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// go-qrdb#517 review: the SDK node must hold first-boot to its own
// mtls_binding_policy and refuse to start with an existing admin locked out.
// These run the REAL first-boot over the REAL kvstore-backed user and role
// stores -- the checks read them, so a double would prove nothing.

func realStores(t *testing.T) (user.UserServicer, role.RoleServicer) {
	t.Helper()
	gen, err := dao.NewSnowflakeGenerator(1)
	require.NoError(t, err)
	store := kvstore.NewMemoryKVStore()
	users, err := user.NewStore(store, gen)
	require.NoError(t, err)
	roles, err := role.NewStore(store)
	require.NoError(t, err)
	return users, roles
}

// memTenants creates the system tenant and reports it on re-runs.
type memTenants struct{ created atomic.Bool }

func (m *memTenants) CreateTenant(_ context.Context, id, name string, iso tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	if !m.created.CompareAndSwap(false, true) {
		return nil, tenant.ErrTenantAlreadyExists
	}
	return &tenant.TenantInfo{ID: id, Name: name, IsolationType: iso}, nil
}

// pemCA issues a real self-signed admin certificate.
type pemCA struct{ t *testing.T }

func (c pemCA) GenerateAdminCert(context.Context, string, string) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(c.t, err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "admin"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(c.t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), []byte("KEY"), nil
}

// storeBinder enrols through the canonical verb.
type storeBinder struct{ users user.UserServicer }

func (b storeBinder) BindAdminCert(ctx context.Context, _ string, userID uint64, thumbprint string) error {
	_, err := b.users.AddCertBinding(ctx, userID, thumbprint, 0)
	return err
}

func fbDeps(users user.UserServicer, roles role.RoleServicer) firstboot.Dependencies {
	return firstboot.Dependencies{
		Tenants: &memTenants{}, Users: users, Roles: roles,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// TestFirstBootUnderPolicy_RequiredWithNoPathBootsAdminUnbound (go-qrdb#560
// review B2, reversing this test's old "refuses" premise): an embedder with
// no credential path and no Binder wired at all is the everyday "nothing to
// bind with on this run" case, not a wiring bug. firstBootUnderPolicy
// proceeds, creates the admin unbound, and leaves the
// awaiting-admin-credential posture to wireClientAndServer's own check
// rather than refusing to boot the node outright.
func TestFirstBootUnderPolicy_RequiredWithNoPathBootsAdminUnbound(t *testing.T) {
	users, roles := realStores(t)
	deps := fbDeps(users, roles) // embedder left the policy at its zero value
	result, err := firstBootUnderPolicy(context.Background(), deps, auth.MTLSBindingRequired, users)
	require.NoError(t, err)
	assert.True(t, result.AdminCreated)
	assert.Empty(t, result.AdminCertThumbprint)
	admin, lerr := users.GetUserByUsername(context.Background(), tenantid.System, "admin")
	require.NoError(t, lerr, "first boot must still create the admin")
	assert.Empty(t, admin.CertBindings, "no credential path was wired")
}

func TestFirstBootUnderPolicy_RequiredWithABinderBootsAndVerifies(t *testing.T) {
	users, roles := realStores(t)
	deps := fbDeps(users, roles)
	deps.CA, deps.Binder = pemCA{t: t}, storeBinder{users: users}
	result, err := firstBootUnderPolicy(context.Background(), deps, auth.MTLSBindingRequired, users)
	require.NoError(t, err)
	assert.True(t, result.AdminCreated)
	assert.NotEmpty(t, result.AdminCertThumbprint)
}

// TestFirstBootUnderPolicy_VerdictReadFromTheNodesStore: the node judges the
// admin by the store its middleware authenticates against. A node store that
// cannot see a binding the embedder's adapter claims is a locked-out admin.
func TestFirstBootUnderPolicy_VerdictReadFromTheNodesStore(t *testing.T) {
	users, roles := realStores(t)
	nodeUsers, _ := realStores(t) // a different store: the admin is not in it
	deps := fbDeps(users, roles)
	deps.CA, deps.Binder = pemCA{t: t}, storeBinder{users: users}
	_, err := firstBootUnderPolicy(context.Background(), deps, auth.MTLSBindingRequired, nodeUsers)
	var refusal *firstboot.AdminCertBindingRequiredError
	require.True(t, errors.As(err, &refusal), "err = %v", err)
	assert.Equal(t, firstboot.AdminBindingMissing, refusal.Reason)
}

func TestFirstBootUnderPolicy_EnrolledUnchanged(t *testing.T) {
	users, roles := realStores(t)
	result, err := firstBootUnderPolicy(context.Background(), fbDeps(users, roles), auth.MTLSBindingEnrolled, users)
	require.NoError(t, err)
	assert.True(t, result.AdminCreated)
}

// flakyLookup fails transiently a few times, then answers from the store.
type flakyLookup struct {
	users    user.UserServicer
	failures atomic.Int32
}

func (f *flakyLookup) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	if f.failures.Add(-1) >= 0 {
		return nil, errors.New("raft: no leader")
	}
	return f.users.GetUserByUsername(ctx, tenantID, username)
}

func TestVerifyExistingAdminBinding(t *testing.T) {
	ctx := context.Background()
	t.Run("no admin yet is deferred to first boot", func(t *testing.T) {
		users, _ := realStores(t)
		assert.NoError(t, verifyExistingAdminBinding(ctx, users, time.Second))
	})
	t.Run("existing unbound admin refuses the start", func(t *testing.T) {
		users, roles := realStores(t)
		_, err := firstBootUnderPolicy(ctx, fbDeps(users, roles), auth.MTLSBindingEnrolled, users)
		require.NoError(t, err)
		err = verifyExistingAdminBinding(ctx, users, time.Second)
		var refusal *firstboot.AdminCertBindingRequiredError
		require.True(t, errors.As(err, &refusal), "err = %v", err)
		assert.Equal(t, firstboot.AdminBindingNone, refusal.Reason)
	})
	t.Run("existing bound admin starts", func(t *testing.T) {
		users, roles := realStores(t)
		deps := fbDeps(users, roles)
		deps.CA, deps.Binder = pemCA{t: t}, storeBinder{users: users}
		_, err := firstBootUnderPolicy(ctx, deps, auth.MTLSBindingEnrolled, users)
		require.NoError(t, err)
		assert.NoError(t, verifyExistingAdminBinding(ctx, users, time.Second))
	})
	t.Run("transient lookup failure is retried", func(t *testing.T) {
		users, _ := realStores(t)
		f := &flakyLookup{users: users}
		f.failures.Store(2)
		assert.NoError(t, verifyExistingAdminBinding(ctx, f, 5*time.Second))
	})
	t.Run("persistent lookup failure refuses the start", func(t *testing.T) {
		users, _ := realStores(t)
		f := &flakyLookup{users: users}
		f.failures.Store(1 << 20)
		err := verifyExistingAdminBinding(ctx, f, 100*time.Millisecond)
		var lookup *firstboot.AdminLookupError
		assert.True(t, errors.As(err, &lookup), "err = %v", err)
	})
	t.Run("nil store refuses the start", func(t *testing.T) {
		err := verifyExistingAdminBinding(ctx, nil, time.Second)
		var refusal *firstboot.AdminCertBindingRequiredError
		require.True(t, errors.As(err, &refusal), "err = %v", err)
		assert.Equal(t, firstboot.AdminBindingUnverifiable, refusal.Reason)
	})
}

// TestNodeFirstBoot_UsesTheWiredPolicy pins that Node.FirstBoot runs under
// the node's OWN configured policy rather than deps.MTLSBindingPolicy's zero
// value: a bare &Node{} has no svcNode, so n.UserService() -- the store
// firstBootUnderPolicy verifies the OUTCOME against -- is nil, and the
// verdict comes back AdminBindingUnverifiable. That is still proof the
// "required" check ran at all (under "enrolled" this verification is never
// reached), without standing up a full node just to exercise this seam; a
// node with a real UserService is exactly
// TestFirstBootUnderPolicy_RequiredWithNoPathBootsAdminUnbound, already
// covered above.
func TestNodeFirstBoot_UsesTheWiredPolicy(t *testing.T) {
	n := &Node{}
	n.bindingPolicy.Store(int32(auth.MTLSBindingRequired))
	users, roles := realStores(t)
	_, err := n.FirstBoot(context.Background(), fbDeps(users, roles))
	var refusal *firstboot.AdminCertBindingRequiredError
	require.True(t, errors.As(err, &refusal), "err = %v", err)
	assert.Equal(t, firstboot.AdminBindingUnverifiable, refusal.Reason)
}

// go-qrdb#777: Node.FirstBoot holds a presented admin certificate to the
// trust domain the node's mTLS authenticator pins, so it never binds a
// certificate whose SPIFFE ID no login on this node would accept.
func TestNodeFirstBoot_PinsTheWiredTrustDomain(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	// The SPIFFE ID names the system tenant as its trust domain; the cluster
	// trust domain is not in it, so the wired domain is observable only where
	// the tenant rule compares the two.
	uris, err := auth.TenantPrincipalURIs("example.com", tenantid.System, spiffe.KindUser, "admin")
	require.NoError(t, err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7),
		Subject: pkix.Name{CommonName: "admin", OrganizationalUnit: []string{tenantid.System}}, URIs: uris,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(cert)

	boot := func(trustDomain string) error {
		n := &Node{}
		n.bindingPolicy.Store(int32(auth.MTLSBindingEnrolled))
		n.spiffeTrustDomain.Store(&trustDomain)
		users, roles := realStores(t)
		deps := fbDeps(users, roles)
		deps.AdminCertPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		deps.AdminCertPool = pool
		_, bootErr := n.FirstBoot(context.Background(), deps)
		return bootErr
	}

	var tenantErr *auth.CertificateTenantError
	err = boot(tenantid.System)
	require.True(t, errors.As(err, &tenantErr), "a cluster trust domain that is the tenant's name must be refused: err = %v", err)
	require.ErrorIs(t, err, auth.ErrTenantIsClusterTrustDomain)
	err = boot("example.com")
	assert.False(t, errors.As(err, &tenantErr), "the pinned domain must pass the tenant rule: err = %v", err)
}

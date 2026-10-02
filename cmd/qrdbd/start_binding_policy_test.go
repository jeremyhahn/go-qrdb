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

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// These tests pin go-qrdb#517's boot behaviour on the daemon: an
// unrecognised auth.mtls_binding_policy aborts, and under "required" a
// bootstrap admin that could not log in by certificate aborts the boot with a
// diagnostic naming the cause and the way back -- the owner's ruling, "refuse
// to boot". They run the daemon's own first boot (bootstrapUserRoleStores)
// against the real user store, so the admin they check is the one the daemon
// actually created.

// bootstrapForBindingPolicy runs the daemon's first boot and returns the
// management stores it produced.
func bootstrapForBindingPolicy(t *testing.T) *mgmtBootstrapResult {
	t.Helper()
	store := make(map[string][]byte)
	mock := newMockKVStore(store)
	mgmt, err := bootstrapUserRoleStores(
		context.Background(), mock, mock, newMockTransactionServicer(store), &mockRaftNode{},
		1, 1, &mockTenantServicer{}, false, nil, "", nil,
	)
	require.NoError(t, err)
	return mgmt
}

func runAuthBootstrapWithPolicy(t *testing.T, mgmt *mgmtBootstrapResult, policy string) (*authBootstrapResult, error) {
	t.Helper()
	return bootstrapAuthEnforcement(
		context.Background(), mgmt, t.TempDir(), &mockRaftNode{},
		audit.NewNoopLogger(), false, "", qrdbconfig.AuthABACConfig{},
		nil, nil, "", "", policy,
	)
}

// adminThumbprint mints a certificate for a fresh key and returns its SPKI
// thumbprint, the value an operator enrols.
func adminThumbprint(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "admin", OrganizationalUnit: []string{tenantid.System}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return user.ComputeSPKIThumbprint(cert)
}

func TestBootstrapAuthEnforcement_InvalidMTLSBindingPolicyFailsBoot(t *testing.T) {
	_, err := runAuthBootstrapWithPolicy(t, bootstrapForBindingPolicy(t), "requierd")
	var abe *AuthBootstrapError
	require.True(t, errors.As(err, &abe), "err = %v", err)
	assert.Equal(t, "auth.mtls_binding_policy", abe.Step)
	var inv *auth.InvalidMTLSBindingPolicyError
	assert.True(t, errors.As(err, &inv))
}

// TestBootstrapAuthEnforcement_RequiredWithUnboundAdminRefusesBoot is the
// stock-daemon case the ruling addresses: first boot wires no CertBinder, so
// the admin it created holds no binding, and "required" would lock it out.
func TestBootstrapAuthEnforcement_RequiredWithUnboundAdminRefusesBoot(t *testing.T) {
	result, err := runAuthBootstrapWithPolicy(t, bootstrapForBindingPolicy(t), "required")
	require.Nil(t, result)
	var abe *AuthBootstrapError
	require.True(t, errors.As(err, &abe), "err = %v", err)
	assert.Equal(t, "auth.mtls_binding_policy", abe.Step)
	var refusal *firstboot.AdminCertBindingRequiredError
	require.True(t, errors.As(err, &refusal), "err = %v", err)
	assert.Equal(t, firstboot.AdminBindingNone, refusal.Reason)
	// Diagnosable and undoable: the message names the way back.
	assert.Contains(t, err.Error(), `"enrolled"`)
	assert.Contains(t, err.Error(), "users:add_cert_binding")
}

// TestBootstrapAuthEnforcement_EnrolledWithUnboundAdminBoots is the control:
// the same unbound admin boots under the default posture, so the refusal
// above is the policy's and nothing else's.
func TestBootstrapAuthEnforcement_EnrolledWithUnboundAdminBoots(t *testing.T) {
	for _, policy := range []string{"", "enrolled"} {
		result, err := runAuthBootstrapWithPolicy(t, bootstrapForBindingPolicy(t), policy)
		require.NoError(t, err, "policy %q", policy)
		assert.Equal(t, auth.MTLSBindingEnrolled, result.authMW.MTLSBindingPolicy())
		_ = result.auditLogger.Close()
	}
}

// servingAdmin serves the admin record the daemon REALLY created, with the
// given bindings, from a lookup-only double -- standing in for a store at a
// particular replication point.
func servingAdmin(t *testing.T, mgmt *mgmtBootstrapResult, bindings ...user.CertBinding) *mockUserServicer {
	t.Helper()
	admin, err := mgmt.bootUserSvc.GetUserByUsername(context.Background(), tenantid.System, "admin")
	require.NoError(t, err)
	for _, b := range bindings {
		admin.AddCertBinding(b)
	}
	return &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, tenantID, username string) (*user.User, error) {
			if tenantID != tenantid.System || username != "admin" {
				return nil, &user.UserNotFoundError{Lookup: username}
			}
			clone := *admin
			return &clone, nil
		},
	}
}

// TestBootstrapAuthEnforcement_RequiredWithBoundAdminBoots is the migrated
// state: the admin enrolled a certificate, so "required" boots and the
// middleware enforces it.
func TestBootstrapAuthEnforcement_RequiredWithBoundAdminBoots(t *testing.T) {
	mgmt := bootstrapForBindingPolicy(t)
	mgmt.userSvc = servingAdmin(t, mgmt, user.CertBinding{Thumbprint: adminThumbprint(t), Kind: user.CertBindingSPKIThumbprint})

	result, err := runAuthBootstrapWithPolicy(t, mgmt, "required")
	require.NoError(t, err)
	assert.Equal(t, auth.MTLSBindingRequired, result.authMW.MTLSBindingPolicy())
	_ = result.auditLogger.Close()
}

// TestBootstrapAuthEnforcement_RequiredJudgesTheLinearizableRecord: on a
// follower the StaleRead boot store may not have the binding the leader
// enrolled yet. The check reads the linearizable store, so a lagging stale
// record does not refuse a boot that should succeed -- and a linearizable
// record with no binding still refuses, whatever the stale store says.
func TestBootstrapAuthEnforcement_RequiredJudgesTheLinearizableRecord(t *testing.T) {
	binding := user.CertBinding{Thumbprint: adminThumbprint(t), Kind: user.CertBindingSPKIThumbprint}

	mgmt := bootstrapForBindingPolicy(t)
	// Both doubles are built from the real record before either replaces a
	// store, so neither reads the other.
	lagging, committed := servingAdmin(t, mgmt), servingAdmin(t, mgmt, binding)
	mgmt.bootUserSvc, mgmt.userSvc = lagging, committed
	result, err := runAuthBootstrapWithPolicy(t, mgmt, "required")
	require.NoError(t, err, "a lagging stale record refused the boot")
	_ = result.auditLogger.Close()

	mgmt = bootstrapForBindingPolicy(t)
	stale, committedUnbound := servingAdmin(t, mgmt, binding), servingAdmin(t, mgmt)
	mgmt.bootUserSvc, mgmt.userSvc = stale, committedUnbound
	_, err = runAuthBootstrapWithPolicy(t, mgmt, "required")
	var refusal *firstboot.AdminCertBindingRequiredError
	require.True(t, errors.As(err, &refusal), "err = %v", err)
	assert.Equal(t, firstboot.AdminBindingNone, refusal.Reason)
}

// TestBootstrapAuthEnforcement_RequiredWaitsOutAnUnreadableStore: a
// linearizable read that cannot be served yet is retried within the bounded
// wait and then judged; past the bound the boot is refused with the lookup
// failure.
func TestBootstrapAuthEnforcement_RequiredWaitsOutAnUnreadableStore(t *testing.T) {
	saved := adminBindingWait
	t.Cleanup(func() { adminBindingWait = saved })
	adminBindingWait = 5 * time.Second

	mgmt := bootstrapForBindingPolicy(t)
	bound := servingAdmin(t, mgmt, user.CertBinding{Thumbprint: adminThumbprint(t), Kind: user.CertBindingSPKIThumbprint})
	failures := 2
	mgmt.userSvc = &mockUserServicer{
		getUserByUsernameFn: func(ctx context.Context, tenantID, username string) (*user.User, error) {
			if failures > 0 {
				failures--
				return nil, errors.New("read index not ready")
			}
			return bound.GetUserByUsername(ctx, tenantID, username)
		},
	}
	result, err := runAuthBootstrapWithPolicy(t, mgmt, "required")
	require.NoError(t, err, "a transiently unreadable store refused the boot")
	_ = result.auditLogger.Close()

	adminBindingWait = 100 * time.Millisecond
	mgmt = bootstrapForBindingPolicy(t)
	mgmt.userSvc = &mockUserServicer{
		getUserByUsernameFn: func(context.Context, string, string) (*user.User, error) {
			return nil, errors.New("read index not ready")
		},
	}
	_, err = runAuthBootstrapWithPolicy(t, mgmt, "required")
	var lookup *firstboot.AdminLookupError
	assert.True(t, errors.As(err, &lookup), "err = %v", err)
}

func TestApplyConfigToFlags_MTLSBindingPolicy(t *testing.T) {
	f := &startFlags{}
	cfg := qrdbconfig.DefaultConfig()
	cfg.Auth.MTLSBindingPolicy = "required"
	applyConfigToFlags(&cobra.Command{}, f, cfg)
	assert.Equal(t, "required", f.mtlsBindingPolicy)
}

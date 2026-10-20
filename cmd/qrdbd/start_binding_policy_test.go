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

// bootstrapForBindingPolicy runs the daemon's first boot UNDER THE "enrolled"
// OPT-OUT and returns the management stores it produced: these tests are
// about bootstrapAuthEnforcement's OWN, SEPARATE later binding check (the
// daemon's second, linearizable-store verification of whatever first boot
// already committed), not about first boot's own pre-flight -- so first boot
// here is deliberately run under "enrolled" to produce its baseline
// pre-go-qrdb#548 outcome, an admin that exists with no certificate binding,
// regardless of what policy a given sub-test later re-checks against. First
// boot's OWN new pre-flight under "required" (go-qrdb#548) is covered
// separately by TestBootstrapUserRoleStores_RequiredWithNoCertPathRefusesFirstBoot.
func bootstrapForBindingPolicy(t *testing.T) *mgmtBootstrapResult {
	t.Helper()
	store := make(map[string][]byte)
	mock := newMockKVStore(store)
	mgmt, err := bootstrapUserRoleStores(
		context.Background(), mock, mock, newMockTransactionServicer(store), &mockRaftNode{}, testLeaderWait,
		1, 1, &mockTenantServicer{}, false, nil, "", nil,
		auth.MTLSBindingEnrolled, nil, nil, nil,
	)
	require.NoError(t, err)
	return mgmt
}

// TestBootstrapUserRoleStores_RequiredWithNoCertPathBootsIntoAwaiting is the
// go-qrdb#560 review B2 fix, at the layer bootstrapForBindingPolicy's helper
// deliberately avoids: under the DEFAULT "required" posture, with no
// ceremony having run on this node (no pending credential) and no admin
// existing yet, first boot no longer refuses when runFirstBoot's own
// bounded wait for a pending admin credential times out -- it logs and
// proceeds, creating the admin unbound, so bootstrapAuthEnforcement's own
// later check (TestBootstrapAuthEnforcement_RequiredWithUnboundAdminBootsAwaiting)
// is the one that puts the node into "awaiting admin credential". The
// earlier ruling this reverses -- "first boot now refuses" -- is exactly
// the go-qrdb#560 NOT READY finding: a node in this state must boot and
// never exit.
func TestBootstrapUserRoleStores_RequiredWithNoCertPathBootsIntoAwaiting(t *testing.T) {
	// No ceremony ran on this node (nil ceremonyCoreSvc) and nothing else
	// writes the pending-admin-cert record in this test, so the bounded wait
	// for one to arrive from elsewhere in the cluster genuinely runs out --
	// shorten it rather than block this unit test for the production bound.
	saved := adminBindingWait
	adminBindingWait = 50 * time.Millisecond
	t.Cleanup(func() { adminBindingWait = saved })

	store := make(map[string][]byte)
	mock := newMockKVStore(store)
	mgmt, err := bootstrapUserRoleStores(
		context.Background(), mock, mock, newMockTransactionServicer(store), &mockRaftNode{}, testLeaderWait,
		1, 1, &mockTenantServicer{}, false, nil, "", nil,
		auth.MTLSBindingRequired, nil, nil, nil,
	)
	require.NoError(t, err, "a timed-out pending-credential wait must boot into awaiting, not refuse")
	admin, lerr := mgmt.bootUserSvc.GetUserByUsername(context.Background(), tenantid.System, "admin")
	require.NoError(t, lerr, "first boot must still create the admin")
	assert.Empty(t, admin.CertBindings, "no credential path was available on this run")
}

func runAuthBootstrapWithPolicy(t *testing.T, mgmt *mgmtBootstrapResult, policy string) (*authBootstrapResult, error) {
	t.Helper()
	return bootstrapAuthEnforcement(
		context.Background(), mgmt, t.TempDir(), &mockRaftNode{},
		audit.NewNoopLogger(), false, "", qrdbconfig.AuthABACConfig{},
		nil, nil, nil, nil, nil, "", "", policy,
		firstboot.NewAwaitingAdminCredentialProbe(), nil,
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

// TestBootstrapAuthEnforcement_RequiredWithUnboundAdminBootsAwaiting is the
// stock-daemon case go-qrdb#548 review item 3 addresses: first boot wires no
// CertBinder, so the admin it created holds no binding. Under the EARLIER
// ruling "required" locked the node out by refusing to boot at all; the
// review reversed that -- the node now boots into "awaiting admin
// credential" instead, with awaitingAdminCredentialProbe reporting it, so a
// retried `qrdb init` against the leader has a running daemon to reach.
func TestBootstrapAuthEnforcement_RequiredWithUnboundAdminBootsAwaiting(t *testing.T) {
	result, err := runAuthBootstrapWithPolicy(t, bootstrapForBindingPolicy(t), "required")
	require.NoError(t, err)
	require.NotNil(t, result.awaitingAdminCredentialProbe)
	assert.True(t, result.awaitingAdminCredentialProbe.AwaitingAdminCredential(),
		"an unbound admin under \"required\" must report awaiting")
	_ = result.auditLogger.Close()
}

// TestBootstrapAuthEnforcement_EnrolledWithUnboundAdminBoots is the control:
// the same unbound admin boots under the explicit "enrolled" opt-out, with
// the awaiting-admin-credential probe left reporting not-awaiting -- that
// boot state exists ONLY for "required", so the "enrolled" posture never
// sees it (the probe's pointer identity stays non-nil, go-qrdb#560 review
// B1, since the ceremony service is composed against it before this
// policy is even known, but Initialize never flips it for any other
// policy).
func TestBootstrapAuthEnforcement_EnrolledWithUnboundAdminBoots(t *testing.T) {
	result, err := runAuthBootstrapWithPolicy(t, bootstrapForBindingPolicy(t), "enrolled")
	require.NoError(t, err)
	assert.Equal(t, auth.MTLSBindingEnrolled, result.authMW.MTLSBindingPolicy())
	require.NotNil(t, result.awaitingAdminCredentialProbe)
	assert.False(t, result.awaitingAdminCredentialProbe.AwaitingAdminCredential())
	_ = result.auditLogger.Close()
}

// TestBootstrapAuthEnforcement_EmptyPolicyIsRequired pins the go-qrdb#548
// default: an empty auth.mtls_binding_policy resolves to "required", not
// "enrolled" -- the same unbound admin that boots under "enrolled" above
// reports awaiting when the policy string is simply omitted.
func TestBootstrapAuthEnforcement_EmptyPolicyIsRequired(t *testing.T) {
	result, err := runAuthBootstrapWithPolicy(t, bootstrapForBindingPolicy(t), "")
	require.NoError(t, err)
	require.NotNil(t, result.awaitingAdminCredentialProbe)
	assert.True(t, result.awaitingAdminCredentialProbe.AwaitingAdminCredential())
	_ = result.auditLogger.Close()
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
	require.NotNil(t, result.awaitingAdminCredentialProbe)
	assert.False(t, result.awaitingAdminCredentialProbe.AwaitingAdminCredential(),
		"a bound admin under \"required\" must never report awaiting")
	_ = result.auditLogger.Close()
}

// TestBootstrapAuthEnforcement_ExpiredAdminBindingBootsAwaiting is the
// go-qrdb#560 review B1/B2 restart case: an admin whose ONLY certificate
// binding has EXPIRED (as opposed to never having bound one at all) must
// restart cleanly into "awaiting admin credential", not refuse to boot --
// "an expired admin is recovered by restarting into awaiting mode, then an
// SO-PIN-gated reissue" (go-qrdb#560 review B1's own ruling). Mirrors
// TestBootstrapAuthEnforcement_RequiredWithUnboundAdminBootsAwaiting, whose
// admin never bound anything at all; this one bound once and the
// certificate simply expired since.
func TestBootstrapAuthEnforcement_ExpiredAdminBindingBootsAwaiting(t *testing.T) {
	mgmt := bootstrapForBindingPolicy(t)
	mgmt.userSvc = servingAdmin(t, mgmt, user.CertBinding{
		Thumbprint: adminThumbprint(t), Kind: user.CertBindingSPKIThumbprint,
		NotAfter: time.Now().Add(-time.Hour),
	})

	result, err := runAuthBootstrapWithPolicy(t, mgmt, "required")
	require.NoError(t, err, "an expired-but-once-bound admin must restart cleanly, not refuse to boot")
	require.NotNil(t, result.awaitingAdminCredentialProbe)
	assert.True(t, result.awaitingAdminCredentialProbe.AwaitingAdminCredential(),
		"an admin with no LIVE binding (expired counts) must report awaiting on restart")
	_ = result.auditLogger.Close()
}

// TestBootstrapAuthEnforcement_RequiredJudgesTheLinearizableRecord: on a
// follower the StaleRead boot store may not have the binding the leader
// enrolled yet. The check reads the linearizable store, so a lagging stale
// record does not stop a boot that should serve normally -- and a
// linearizable record with no binding boots awaiting, whatever the stale
// store says.
func TestBootstrapAuthEnforcement_RequiredJudgesTheLinearizableRecord(t *testing.T) {
	binding := user.CertBinding{Thumbprint: adminThumbprint(t), Kind: user.CertBindingSPKIThumbprint}

	mgmt := bootstrapForBindingPolicy(t)
	// Both doubles are built from the real record before either replaces a
	// store, so neither reads the other.
	lagging, committed := servingAdmin(t, mgmt), servingAdmin(t, mgmt, binding)
	mgmt.bootUserSvc, mgmt.userSvc = lagging, committed
	result, err := runAuthBootstrapWithPolicy(t, mgmt, "required")
	require.NoError(t, err, "a lagging stale record refused the boot")
	require.NotNil(t, result.awaitingAdminCredentialProbe)
	assert.False(t, result.awaitingAdminCredentialProbe.AwaitingAdminCredential(),
		"the linearizable record is bound, so this must not report awaiting")
	_ = result.auditLogger.Close()

	mgmt = bootstrapForBindingPolicy(t)
	stale, committedUnbound := servingAdmin(t, mgmt, binding), servingAdmin(t, mgmt)
	mgmt.bootUserSvc, mgmt.userSvc = stale, committedUnbound
	result, err = runAuthBootstrapWithPolicy(t, mgmt, "required")
	require.NoError(t, err)
	require.NotNil(t, result.awaitingAdminCredentialProbe)
	assert.True(t, result.awaitingAdminCredentialProbe.AwaitingAdminCredential(),
		"the linearizable record has no binding, so this must report awaiting regardless of the stale store")
	_ = result.auditLogger.Close()
}

// TestBootstrapAuthEnforcement_RequiredWaitsOutAnUnreadableStore: a
// linearizable read that cannot be served yet is retried within the bounded
// wait and then judged; past the bound the boot proceeds rather than
// refusing outright (go-qrdb#548 review item 3). A store that never
// answers at all is a FAILED lookup, not a verdict -- go-qrdb#560 review
// B1 rules that is NOT "awaiting": the probe is left reporting
// not-awaiting (serving normally) rather than fail-closed to the
// restricted boot state, since an infrastructure fault distinct from the
// admin's own binding state must not additionally lock the node down to
// the two ceremony verbs that state serves.
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
	require.NotNil(t, result.awaitingAdminCredentialProbe)
	assert.False(t, result.awaitingAdminCredentialProbe.AwaitingAdminCredential(),
		"the store answered bound once the transient failures cleared, so this must not report awaiting")
	_ = result.auditLogger.Close()

	adminBindingWait = 100 * time.Millisecond
	mgmt = bootstrapForBindingPolicy(t)
	mgmt.userSvc = &mockUserServicer{
		getUserByUsernameFn: func(context.Context, string, string) (*user.User, error) {
			return nil, errors.New("read index not ready")
		},
	}
	result, err = runAuthBootstrapWithPolicy(t, mgmt, "required")
	require.NoError(t, err, "a store that never answers must boot normally, not refuse")
	require.NotNil(t, result.awaitingAdminCredentialProbe)
	assert.False(t, result.awaitingAdminCredentialProbe.AwaitingAdminCredential(),
		"a store that never answers is a failed lookup, not a binding verdict: it must not report awaiting")
	_ = result.auditLogger.Close()
}

func TestApplyConfigToFlags_MTLSBindingPolicy(t *testing.T) {
	f := &startFlags{}
	cfg := qrdbconfig.DefaultConfig()
	cfg.Auth.MTLSBindingPolicy = "required"
	applyConfigToFlags(&cobra.Command{}, f, cfg)
	assert.Equal(t, "required", f.mtlsBindingPolicy)
}

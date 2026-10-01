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

package middleware

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// The tenant-barrier bootstrap windows (barrier_bootstrap.go) grant what RBAC
// structurally cannot. They must not grant what the attested-caller gate
// refused (go-qrdb#278 review B1): neither by reading the gate's refusal as
// "RBAC said no" and opening the window, nor by granting on the sealed-window
// path, where authorize() refuses the sealed identity before the gate runs.

// bootstrapGate is an authenticated mTLS request for bob@acme whose gate
// covers the barrier-bootstrap permissions, with RBAC answering rbacErr.
func bootstrapGate(t *testing.T, rbacErr error) *gateFixture {
	t.Helper()
	svc := newRealUserStore(t)
	createUser(t, svc, "acme", "bob", []string{"operator"})
	cert := mintCert(t, "bob", "acme", nil, "")
	now := time.Now().UTC()
	v := &cacheVerifier{evidence: map[string]cachedEvidence{}}
	gate, err := NewPeerAttestationGate(v, PeerAttestationPolicy{
		Permissions: []permission.Permission{
			permission.TenantBarrierInit, permission.TenantBarrierUnseal, permission.TenantBarrierStatus,
		},
		MaxEvidenceAge: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewPeerAttestationGate: %v", err)
	}
	gate.now = func() time.Time { return now }
	f := &gateFixture{cert: cert, verifier: v, audit: &mockAuditLogger{}, authz: &mockAuthorizer{err: rbacErr}, now: now}
	f.m = New(f.authz,
		WithAuthenticator(auth.NewMTLSAuthenticator()),
		WithUserService(svc),
		WithAuditLogger(f.audit),
		WithPeerAttestationGate(gate),
	)
	ctx, identity, err := f.m.authenticate(mtlsContext(cert))
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	f.ctx, f.identity = ctx, identity
	return f
}

// bootstrapRefusals are the three refusals the review named, each set up on a
// fresh fixture.
var bootstrapRefusals = []struct {
	name     string
	setup    func(t *testing.T, f *gateFixture)
	reason   PeerAttestationReason
	security bool
}{
	{"verifier error", func(_ *testing.T, f *gateFixture) {
		f.verifier.err = errors.New("xkms: evidence cache unavailable")
	}, PeerAttestationVerifierError, false},
	{"stale evidence", func(_ *testing.T, f *gateFixture) {
		f.verifier.evidence[identityKey("acme", "bob")] = cachedEvidence{
			peerThumbprint: user.ComputeSPKIThumbprint(f.cert.cert), collectedAt: f.now.Add(-time.Hour), passed: true,
		}
	}, PeerAttestationEvidenceStale, false},
	{"thumbprint mismatch", func(t *testing.T, f *gateFixture) {
		other := mintCert(t, "bob", "acme", nil, "")
		f.verifier.evidence[identityKey("acme", "bob")] = cachedEvidence{
			peerThumbprint: user.ComputeSPKIThumbprint(other.cert), collectedAt: f.now.Add(-time.Minute), passed: true,
		}
	}, PeerAttestationPeerMismatch, true},
}

func uninitialized() *fakeBarrierStatusReader { return &fakeBarrierStatusReader{initialized: false} }

// countingStatusReader reports an uninitialized barrier and counts how often
// the window consulted it.
type countingStatusReader struct {
	fakeBarrierStatusReader
	calls int
}

func (c *countingStatusReader) TenantBarrierStatus(ctx context.Context, tenantID string) (*tenant.TenantBarrierStatus, error) {
	c.calls++
	return c.fakeBarrierStatusReader.TenantBarrierStatus(ctx, tenantID)
}

func decisionRecords(entries []*audit.Entry) int {
	n := 0
	for _, e := range entries {
		if e.Action == "auth.authorize" {
			n++
		}
	}
	return n
}

// TestAuthorizeTenantBarrierInit_GateRefusalDoesNotOpenTheWindow: RBAC
// GRANTED, the gate refused; the refusal must come back unchanged rather than
// being taken for an RBAC denial that trust-on-first-init then considers. The
// window must not even be consulted -- no barrier-status read, and exactly one
// decision record -- because the grant path re-running the gate would mask a
// window that wrongly opened.
func TestAuthorizeTenantBarrierInit_GateRefusalDoesNotOpenTheWindow(t *testing.T) {
	for _, tc := range bootstrapRefusals {
		t.Run(tc.name, func(t *testing.T) {
			f := bootstrapGate(t, nil)
			tc.setup(t, f)
			status := &countingStatusReader{}
			err := f.m.AuthorizeTenantBarrierInit(f.ctx, "acme", status)
			requirePeerRefusal(t, err, tc.reason, tc.security)
			if status.calls != 0 {
				t.Fatalf("the bootstrap window was consulted %d time(s) for a gate refusal", status.calls)
			}
			if n := decisionRecords(f.audit.entries); n != 1 {
				t.Fatalf("decision records = %d, want 1: the window must not record a second decision", n)
			}
		})
	}
}

// TestAuthorizeTenantBarrierInit_WindowGrantIsGated: RBAC DENIED, so the
// trust-on-first-init window grants -- and that grant must pass the gate.
func TestAuthorizeTenantBarrierInit_WindowGrantIsGated(t *testing.T) {
	for _, tc := range bootstrapRefusals {
		t.Run(tc.name, func(t *testing.T) {
			f := bootstrapGate(t, errors.New("rbac: permission denied"))
			tc.setup(t, f)
			err := f.m.AuthorizeTenantBarrierInit(f.ctx, "acme", uninitialized())
			requirePeerRefusal(t, err, tc.reason, tc.security)
			last := f.audit.entries[len(f.audit.entries)-1]
			if last.Action != "auth.authorize" || last.Result != "denied" {
				t.Fatalf("final decision record = %+v, want the window's decision recorded as denied", last)
			}
		})
	}
	// Control: an attested peer still gets the window.
	f := bootstrapGate(t, errors.New("rbac: permission denied"))
	f.attest()
	if err := f.m.AuthorizeTenantBarrierInit(f.ctx, "acme", uninitialized()); err != nil {
		t.Fatalf("attested trust-on-first-init refused: %v", err)
	}
}

// TestAuthorizeTenantBarrierSealedOp_WindowGrantIsGated: a sealed identity's
// request is answered "sealed" by authorize() BEFORE the gate runs, so the
// window's grant is the only point the gate can apply -- and must.
func TestAuthorizeTenantBarrierSealedOp_WindowGrantIsGated(t *testing.T) {
	for _, perm := range []permission.Permission{permission.TenantBarrierUnseal, permission.TenantBarrierStatus} {
		for _, tc := range bootstrapRefusals {
			t.Run(perm.String()+"/"+tc.name, func(t *testing.T) {
				f := bootstrapGate(t, nil)
				tc.setup(t, f)
				sealed := &auth.Identity{Subject: "bob", TenantID: "acme", Metadata: map[string]string{tenantIAMSealedMetadataKey: "true"}}
				ctx := auth.WithIdentity(f.ctx, sealed)
				err := f.m.AuthorizeTenantBarrierSealedOp(ctx, "acme", perm)
				requirePeerRefusal(t, err, tc.reason, tc.security)
			})
		}
	}
	f := bootstrapGate(t, nil)
	f.attest()
	sealed := &auth.Identity{Subject: "bob", TenantID: "acme", Metadata: map[string]string{tenantIAMSealedMetadataKey: "true"}}
	if err := f.m.AuthorizeTenantBarrierSealedOp(auth.WithIdentity(f.ctx, sealed), "acme", permission.TenantBarrierUnseal); err != nil {
		t.Fatalf("attested sealed-window unseal refused: %v", err)
	}
}

// TestAuthorizeTenantBarrierSealedOp_GateRefusalReturnedUnchanged: with the
// barrier unsealed RBAC can grant, the gate refuses, and the refusal is not a
// reason to consult the window.
func TestAuthorizeTenantBarrierSealedOp_GateRefusalReturnedUnchanged(t *testing.T) {
	f := bootstrapGate(t, nil)
	f.verifier.err = errors.New("xkms: evidence cache unavailable")
	err := f.m.AuthorizeTenantBarrierSealedOp(f.ctx, "acme", permission.TenantBarrierUnseal)
	requirePeerRefusal(t, err, PeerAttestationVerifierError, false)
}

func TestIsGateRefusal(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"attestation refusal":         {&PeerAttestationRefusedError{Reason: PeerAttestationEvidenceStale}, true},
		"rate limited":                {&RateLimitedError{Permission: permission.TenantBarrierInit}, true},
		"wrapped attestation refusal": {errors.Join(errors.New("ctx"), &PeerAttestationRefusedError{}), true},
		"rbac denial":                 {auth.ErrUnauthorized, false},
		"sealed tenant":               {&StoreUnavailableError{Err: context.Canceled}, false},
		"nil":                         {nil, false},
	}
	for name, tc := range cases {
		if got := isGateRefusal(tc.err); got != tc.want {
			t.Errorf("%s: isGateRefusal = %v, want %v", name, got, tc.want)
		}
	}
}

// TestBootstrapWindows_RateLimitHolds: a rate-limited request is refused by
// the throttle before RBAC is consulted, and neither window may read that
// refusal as the RBAC denial it substitutes for (go-qrdb#278 review).
func TestBootstrapWindows_RateLimitHolds(t *testing.T) {
	newLimited := func(t *testing.T, perm permission.Permission) *AuthMiddleware {
		limiter := ratelimit.NewTokenBucketLimiter()
		t.Cleanup(func() { _ = limiter.Close() })
		policies := ratelimit.NewRegistry()
		policies.Register(perm, ratelimit.Policy{RatePerSecond: 0.001, Burst: 1})
		return New(&mockAuthorizer{err: errors.New("rbac: permission denied")},
			WithRateLimiter(limiter), WithRateLimitPolicies(policies))
	}
	t.Run("trust-on-first-init", func(t *testing.T) {
		m := newLimited(t, permission.TenantBarrierInit)
		ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "admin", TenantID: "t1"})
		if err := m.AuthorizeTenantBarrierInit(ctx, "t1", uninitialized()); err != nil {
			t.Fatalf("first call within burst = %v, want the window's grant", err)
		}
		var limited *RateLimitedError
		if err := m.AuthorizeTenantBarrierInit(ctx, "t1", uninitialized()); !errors.As(err, &limited) {
			t.Fatalf("second call over burst = %v, want *RateLimitedError", err)
		}
	})
	t.Run("sealed window", func(t *testing.T) {
		m := newLimited(t, permission.TenantBarrierUnseal)
		ctx := sealedCtx("t1")
		if err := m.AuthorizeTenantBarrierSealedOp(ctx, "t1", permission.TenantBarrierUnseal); err != nil {
			t.Fatalf("first call within burst = %v, want the window's grant", err)
		}
		var limited *RateLimitedError
		if err := m.AuthorizeTenantBarrierSealedOp(ctx, "t1", permission.TenantBarrierUnseal); !errors.As(err, &limited) {
			t.Fatalf("second call over burst = %v, want *RateLimitedError", err)
		}
	})
}

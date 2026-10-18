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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// These tests pin go-qrdb#278's gate. The verifier is an injected interface
// whose real implementation is go-xkms's, so the double below implements the
// CONTRACT that interface states, not a shortcut: evidence is cached per
// AUTHENTICATED identity, recorded with the thumbprint of the peer it was
// collected from and the time it was collected, and a verdict reports both.
// That is what lets "evidence for a different peer" and "stale evidence" be
// exercised as the gate will meet them. Every fail-closed case the issue
// lists is here, because a test of the happy path alone passes against a gate
// that admits on error.

// cachedEvidence is what a background collector left for one identity.
type cachedEvidence struct {
	peerThumbprint string
	collectedAt    time.Time
	passed         bool
	failure        auth.PeerAttestationFailure
	reason         string
}

// cacheVerifier is a PeerAttestationVerifier over a pre-populated evidence
// cache keyed by the authenticated identity (tenant/subject), per the
// interface contract. It never "collects".
type cacheVerifier struct {
	mu       sync.Mutex
	evidence map[string]cachedEvidence
	err      error
	nilReply bool
	calls    []auth.AttestedPeer
}

func identityKey(tenantID, subject string) string { return tenantID + "/" + subject }

func (v *cacheVerifier) VerifyPeer(_ context.Context, p auth.AttestedPeer) (*auth.PeerAttestation, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls = append(v.calls, p)
	if v.err != nil {
		return nil, v.err
	}
	if v.nilReply {
		return nil, nil
	}
	ev, ok := v.evidence[identityKey(p.Identity.TenantID, p.Identity.Subject)]
	if !ok {
		// No evidence at all: an absence verdict with nothing collected.
		return &auth.PeerAttestation{
			PeerThumbprint: p.Thumbprint,
			Failure:        auth.PeerAttestationFailureAbsence,
			Reason:         "no cached evidence",
		}, nil
	}
	return &auth.PeerAttestation{
		PeerThumbprint: ev.peerThumbprint,
		CollectedAt:    ev.collectedAt,
		Passed:         ev.passed,
		Failure:        ev.failure,
		Reason:         ev.reason,
	}, nil
}

func (v *cacheVerifier) callCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.calls)
}

const gatedPerm = permission.KVGet

// gateFixture is an authenticated mTLS request context for bob@acme plus a
// middleware whose gate covers gatedPerm.
type gateFixture struct {
	ctx      context.Context
	identity *auth.Identity
	cert     certKey
	verifier *cacheVerifier
	audit    *mockAuditLogger
	authz    *mockAuthorizer
	m        *AuthMiddleware
	now      time.Time
	// subject is bob's login Subject: his immutable user id.
	subject string
}

func newGateFixture(t *testing.T) *gateFixture {
	t.Helper()
	svc := newRealUserStore(t)
	u := createUser(t, svc, "acme", "bob", []string{"operator"})
	cert := mintCert(t, "bob", "acme", nil, "")
	enroll(t, svc, u.ID, cert)

	now := time.Now().UTC()
	v := &cacheVerifier{evidence: map[string]cachedEvidence{}}
	gate, err := NewPeerAttestationGate(v, PeerAttestationPolicy{
		Permissions:    []permission.Permission{gatedPerm, permission.IdentityGated, permission.AdminHealth},
		MaxEvidenceAge: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewPeerAttestationGate: %v", err)
	}
	gate.now = func() time.Time { return now }

	f := &gateFixture{cert: cert, verifier: v, audit: &mockAuditLogger{}, authz: &mockAuthorizer{}, now: now, subject: auth.UserSubject(u.ID)}
	f.m = newTestMiddleware(f.authz,
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

// attest records fresh, passing evidence for bob collected from his own peer.
func (f *gateFixture) attest() {
	f.verifier.evidence[identityKey("acme", f.subject)] = cachedEvidence{
		peerThumbprint: user.ComputeSPKIThumbprint(f.cert.cert),
		collectedAt:    f.now.Add(-time.Minute),
		passed:         true,
	}
}

func requirePeerRefusal(t *testing.T, err error, subject string, reason PeerAttestationReason, securityRelevant bool) *PeerAttestationRefusedError {
	t.Helper()
	var refusal *PeerAttestationRefusedError
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %T(%v), want *PeerAttestationRefusedError(%s)", err, err, reason)
	}
	if refusal.Reason != reason || refusal.SecurityRelevant != securityRelevant {
		t.Fatalf("refusal = {reason %q, security %v}, want {%q, %v}", refusal.Reason, refusal.SecurityRelevant, reason, securityRelevant)
	}
	if refusal.Subject != subject || refusal.TenantID != "acme" || refusal.Permission == "" {
		t.Fatalf("refusal names %s/%s for %q, want acme/%s", refusal.TenantID, refusal.Subject, refusal.Permission, subject)
	}
	return refusal
}

func TestPeerAttestationGate_AttestedPeerAdmitted(t *testing.T) {
	f := newGateFixture(t)
	f.attest()
	if err := f.m.authorize(f.ctx, f.identity, gatedPerm); err != nil {
		t.Fatalf("attested peer refused: %v", err)
	}
	if f.verifier.callCount() != 1 {
		t.Fatalf("verifier calls = %d, want 1", f.verifier.callCount())
	}
	got := f.verifier.calls[0]
	if got.Identity.Subject != f.subject || got.Identity.UserID == 0 || got.Identity.TenantID != "acme" {
		t.Fatalf("verifier was handed %s/%s, want the authenticated identity", got.Identity.TenantID, got.Identity.Subject)
	}
	if got.Thumbprint != user.ComputeSPKIThumbprint(f.cert.cert) || !got.Certificate.Equal(f.cert.cert) {
		t.Fatal("verifier was not handed the certificate on the connection")
	}
}

func TestPeerAttestationGate_UngatedPermissionNotVerified(t *testing.T) {
	f := newGateFixture(t)
	if err := f.m.authorize(f.ctx, f.identity, permission.KVPut); err != nil {
		t.Fatalf("ungated permission refused: %v", err)
	}
	if f.verifier.callCount() != 0 {
		t.Fatal("the verifier ran for a permission the policy does not gate")
	}
}

// TestPeerAttestationGate_FailsClosed is the issue's verification list, plus
// every other way a verdict can fail to establish an attested peer.
func TestPeerAttestationGate_FailsClosed(t *testing.T) {
	peerThumb := func(f *gateFixture) string { return user.ComputeSPKIThumbprint(f.cert.cert) }
	cases := []struct {
		name     string
		setup    func(f *gateFixture)
		ctx      func(f *gateFixture) context.Context
		reason   PeerAttestationReason
		security bool
	}{
		{name: "no evidence at all", setup: func(*gateFixture) {}, reason: PeerAttestationNoEvidence},
		{name: "evidence older than the max age", setup: func(f *gateFixture) {
			f.verifier.evidence[identityKey("acme", f.subject)] = cachedEvidence{peerThumbprint: peerThumb(f), collectedAt: f.now.Add(-6 * time.Minute), passed: true}
		}, reason: PeerAttestationEvidenceStale},
		{name: "evidence for a different peer than the connection's", setup: func(f *gateFixture) {
			other := mintCert(t, "bob", "acme", nil, "")
			f.verifier.evidence[identityKey("acme", f.subject)] = cachedEvidence{peerThumbprint: user.ComputeSPKIThumbprint(other.cert), collectedAt: f.now.Add(-time.Minute), passed: true}
		}, reason: PeerAttestationPeerMismatch, security: true},
		{name: "the verifier returns an error", setup: func(f *gateFixture) {
			f.attest()
			f.verifier.err = errors.New("xkms: policy has no anchors")
		}, reason: PeerAttestationVerifierError},
		{name: "the verifier returns neither verdict nor error", setup: func(f *gateFixture) {
			f.attest()
			f.verifier.nilReply = true
		}, reason: PeerAttestationNoVerdict},
		{name: "evidence dated in the future", setup: func(f *gateFixture) {
			f.verifier.evidence[identityKey("acme", f.subject)] = cachedEvidence{peerThumbprint: peerThumb(f), collectedAt: f.now.Add(time.Hour), passed: true}
		}, reason: PeerAttestationEvidenceFromFuture, security: true},
		{name: "state rejected for absence", setup: func(f *gateFixture) {
			f.verifier.evidence[identityKey("acme", f.subject)] = cachedEvidence{peerThumbprint: peerThumb(f), collectedAt: f.now.Add(-time.Minute), failure: auth.PeerAttestationFailureAbsence, reason: "PCR 10 not quoted"}
		}, reason: PeerAttestationStateRejected},
		{name: "state rejected cryptographically", setup: func(f *gateFixture) {
			f.verifier.evidence[identityKey("acme", f.subject)] = cachedEvidence{peerThumbprint: peerThumb(f), collectedAt: f.now.Add(-time.Minute), failure: auth.PeerAttestationFailureCryptographic, reason: "quote signature invalid"}
		}, reason: PeerAttestationStateRejected, security: true},
		{name: "a passing verdict that names a failure", setup: func(f *gateFixture) {
			f.verifier.evidence[identityKey("acme", f.subject)] = cachedEvidence{peerThumbprint: peerThumb(f), collectedAt: f.now.Add(-time.Minute), passed: true, failure: auth.PeerAttestationFailureCryptographic}
		}, reason: PeerAttestationInconsistentVerdict},
		{name: "no peer certificate on the connection", setup: func(f *gateFixture) { f.attest() },
			ctx: func(f *gateFixture) context.Context {
				return auth.WithTLSState(f.ctx, &tls.ConnectionState{})
			}, reason: PeerAttestationNoPeerCertificate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGateFixture(t)
			tc.setup(f)
			ctx := f.ctx
			if tc.ctx != nil {
				ctx = tc.ctx(f)
			}
			err := f.m.authorize(ctx, f.identity, gatedPerm)
			refusal := requirePeerRefusal(t, err, f.subject, tc.reason, tc.security)

			wantClass := AuthFailureUnauthorized
			if tc.reason == PeerAttestationVerifierError {
				wantClass = AuthFailureUnavailable
				if !errors.Is(err, f.verifier.err) {
					t.Fatal("the verifier's error is not reachable through the refusal")
				}
			}
			if got := ClassifyAuthError(err); got != wantClass {
				t.Fatalf("ClassifyAuthError = %v, want %v", got, wantClass)
			}
			if !strings.Contains(refusal.Error(), string(tc.reason)) {
				t.Fatalf("message %q does not name the reason", refusal.Error())
			}
		})
	}
}

// TestPeerAttestationGate_VerifierErrorClassifiedByTheGate: the verifier is
// an injected implementation whose error type this package does not control.
// One that happens to be an *auth.AuthError must still classify as the gate's
// refusal (unavailable), not as an authentication failure -- which is why the
// rule sits ahead of the AuthError rule.
func TestPeerAttestationGate_VerifierErrorClassifiedByTheGate(t *testing.T) {
	f := newGateFixture(t)
	f.verifier.err = auth.ErrUnauthenticated
	err := f.m.authorize(f.ctx, f.identity, gatedPerm)
	requirePeerRefusal(t, err, f.subject, PeerAttestationVerifierError, false)
	if got := ClassifyAuthError(err); got != AuthFailureUnavailable {
		t.Fatalf("ClassifyAuthError = %v, want AuthFailureUnavailable", got)
	}
}

// TestPeerAttestationGate_ForwardedIdentityRefusedFirst: an on-behalf
// identity is refused with its own, non-security reason before the verifier
// is consulted, even when the subject has perfectly good evidence -- the
// certificate on this connection is the forwarding node's.
func TestPeerAttestationGate_ForwardedIdentityRefusedFirst(t *testing.T) {
	f := newGateFixture(t)
	f.attest()
	forwarded := &auth.Identity{Subject: f.subject, TenantID: "acme", Metadata: map[string]string{
		authSourceMetadataKey: authSourceOnBehalf, "forwarder_nodeid": "node-2",
	}}
	err := f.m.authorize(f.ctx, forwarded, gatedPerm)
	refusal := requirePeerRefusal(t, err, f.subject, PeerAttestationForwarded, false)
	if !strings.Contains(refusal.Detail, "node-2") {
		t.Fatalf("detail %q does not name the forwarding node", refusal.Detail)
	}
	if f.verifier.callCount() != 0 {
		t.Fatal("the verifier was consulted for a forwarded identity")
	}
	if got := ClassifyAuthError(err); got != AuthFailureUnauthorized {
		t.Fatalf("ClassifyAuthError = %v, want AuthFailureUnauthorized", got)
	}
	// An ungated permission is unaffected by forwarding.
	if err := f.m.authorize(f.ctx, forwarded, permission.KVPut); err != nil {
		t.Fatalf("forwarded ungated request refused: %v", err)
	}
}

// TestPeerAttestationGate_RunsOnlyAfterRBACGrants: a request RBAC denied is
// denied for that reason and costs no verification.
func TestPeerAttestationGate_RunsOnlyAfterRBACGrants(t *testing.T) {
	f := newGateFixture(t)
	f.attest()
	f.authz.err = auth.ErrUnauthorized
	err := f.m.authorize(f.ctx, f.identity, gatedPerm)
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("err = %v, want the RBAC denial", err)
	}
	if f.verifier.callCount() != 0 {
		t.Fatal("the verifier ran for a request RBAC had already denied")
	}
}

// TestPeerAttestationGate_GatesTheRBACBypassingBranches: IdentityGated and the
// sealed-tenant health allowance skip RBAC; a gated permission on either is
// still gated.
func TestPeerAttestationGate_GatesTheRBACBypassingBranches(t *testing.T) {
	t.Run("IdentityGated", func(t *testing.T) {
		f := newGateFixture(t)
		requirePeerRefusal(t, f.m.authorize(f.ctx, f.identity, permission.IdentityGated), f.subject, PeerAttestationNoEvidence, false)
		f.attest()
		if err := f.m.authorize(f.ctx, f.identity, permission.IdentityGated); err != nil {
			t.Fatalf("attested IdentityGated refused: %v", err)
		}
	})
	t.Run("sealed tenant health", func(t *testing.T) {
		f := newGateFixture(t)
		// Sealed tenant IAM resolves no account, so the Subject stays the CN.
		f.subject = "bob"
		sealed := &auth.Identity{Subject: f.subject, TenantID: "acme", Metadata: map[string]string{tenantIAMSealedMetadataKey: "true"}}
		requirePeerRefusal(t, f.m.authorize(f.ctx, sealed, permission.AdminHealth), f.subject, PeerAttestationNoEvidence, false)
		f.attest()
		if err := f.m.authorize(f.ctx, sealed, permission.AdminHealth); err != nil {
			t.Fatalf("attested sealed-tenant health refused: %v", err)
		}
	})
}

// TestPeerAttestationGate_RefusalIsAuditedAsItsOwnEvent: a measured-state
// refusal must be distinguishable from an ordinary denial in the trail.
func TestPeerAttestationGate_RefusalIsAuditedAsItsOwnEvent(t *testing.T) {
	f := newGateFixture(t)
	f.verifier.evidence[identityKey("acme", f.subject)] = cachedEvidence{
		peerThumbprint: user.ComputeSPKIThumbprint(f.cert.cert), collectedAt: f.now.Add(-time.Minute),
		failure: auth.PeerAttestationFailureCryptographic, reason: "PCR 10 mismatch",
	}
	_ = f.m.authorize(f.ctx, f.identity, gatedPerm)

	var attest, decision *audit.Entry
	for _, e := range f.audit.entries {
		switch e.Action {
		case peerAttestationAuditAction:
			attest = e
		case "auth.authorize":
			decision = e
		}
	}
	if attest == nil || decision == nil {
		t.Fatalf("audit entries = %+v, want an auth.peer_attestation record AND the decision", f.audit.entries)
	}
	if attest.Result != audit.ResultDenied || decision.Result != audit.ResultDenied {
		t.Fatalf("results = %q / %q, want both denied", attest.Result, decision.Result)
	}
	if attest.Metadata["reason"] != string(PeerAttestationStateRejected) ||
		attest.Metadata["security_relevant"] != "true" ||
		attest.Metadata["detail"] != "PCR 10 mismatch" ||
		attest.Subject != f.subject || attest.TenantID != "acme" || attest.Resource != gatedPerm.String() {
		t.Fatalf("attestation record = %+v", attest)
	}

	// An admitted request writes no attestation record, only the decision.
	f2 := newGateFixture(t)
	f2.attest()
	_ = f2.m.authorize(f2.ctx, f2.identity, gatedPerm)
	for _, e := range f2.audit.entries {
		if e.Action == peerAttestationAuditAction {
			t.Fatal("an admitted request wrote a refusal record")
		}
	}
}

// TestPeerAttestationGate_UnrecordableRefusal follows the audit posture: under
// fail-closed an unrecordable refusal is an audit.GateError (still a refusal),
// under fail-open the refusal stands.
func TestPeerAttestationGate_UnrecordableRefusal(t *testing.T) {
	for _, policy := range []audit.FailurePolicy{audit.FailClosed, audit.FailOpen} {
		t.Run(policy.String(), func(t *testing.T) {
			f := newGateFixture(t)
			f.audit.err = errors.New("disk full")
			WithAuditFailurePolicy(policy)(f.m)
			err := f.m.authorize(f.ctx, f.identity, gatedPerm)
			if err == nil {
				t.Fatal("an unrecordable refusal admitted the request")
			}
			var gateErr *audit.GateError
			if policy == audit.FailClosed && !errors.As(err, &gateErr) {
				t.Fatalf("fail-closed err = %v, want *audit.GateError", err)
			}
			if policy == audit.FailOpen {
				requirePeerRefusal(t, err, f.subject, PeerAttestationNoEvidence, false)
			}
		})
	}
}

func TestNewPeerAttestationGate_RefusesIncompletePolicy(t *testing.T) {
	v := &cacheVerifier{}
	ok := PeerAttestationPolicy{Permissions: []permission.Permission{gatedPerm}, MaxEvidenceAge: time.Minute}
	cases := map[string]struct {
		v auth.PeerAttestationVerifier
		p PeerAttestationPolicy
	}{
		"nil verifier":     {nil, ok},
		"no permissions":   {v, PeerAttestationPolicy{MaxEvidenceAge: time.Minute}},
		"zero max age":     {v, PeerAttestationPolicy{Permissions: ok.Permissions}},
		"negative max age": {v, PeerAttestationPolicy{Permissions: ok.Permissions, MaxEvidenceAge: -time.Second}},
		"empty permission": {v, PeerAttestationPolicy{Permissions: []permission.Permission{""}, MaxEvidenceAge: time.Minute}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gate, err := NewPeerAttestationGate(tc.v, tc.p)
			var cfg *PeerAttestationConfigError
			if gate != nil || !errors.As(err, &cfg) || !strings.Contains(err.Error(), cfg.Reason) {
				t.Fatalf("NewPeerAttestationGate = (%v, %v), want *PeerAttestationConfigError", gate, err)
			}
		})
	}
	if gate, err := NewPeerAttestationGate(v, ok); err != nil || !gate.Gates(gatedPerm) || gate.Gates(permission.KVPut) {
		t.Fatalf("valid policy = (%v, %v)", gate, err)
	}
	var nilGate *PeerAttestationGate
	if nilGate.Gates(gatedPerm) {
		t.Fatal("a nil gate gates something")
	}
}

// ---------------------------------------------------------------------------
// Every transport that has a peer reaches the gate through ONE option.
// ---------------------------------------------------------------------------

// transportGate builds a middleware gating perm with a verifier that refuses
// everything (no evidence), so each entry point below either reaches the gate
// (and is refused) or does not (and would be admitted).
func transportGate(t *testing.T, perm permission.Permission) (*AuthMiddleware, certKey, *cacheVerifier) {
	t.Helper()
	svc := newRealUserStore(t)
	u := createUser(t, svc, "acme", "bob", []string{"operator"})
	ck := mintCert(t, "bob", "acme", nil, "")
	enroll(t, svc, u.ID, ck)
	v := &cacheVerifier{evidence: map[string]cachedEvidence{}}
	gate, err := NewPeerAttestationGate(v, PeerAttestationPolicy{Permissions: []permission.Permission{perm}, MaxEvidenceAge: time.Minute})
	if err != nil {
		t.Fatalf("NewPeerAttestationGate: %v", err)
	}
	m := newTestMiddleware(&mockAuthorizer{},
		WithAuthenticator(auth.NewMTLSAuthenticator()),
		WithUserService(svc),
		WithPeerAttestationGate(gate),
	)
	return m, ck, v
}

func grpcPeerContext(ck certKey) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1},
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{ck.cert},
			VerifiedChains:   [][]*x509.Certificate{{ck.cert}},
		}},
	})
}

// errRESTRefusedByGate stands for "the REST handler was not reached and the
// middleware answered 403": REST reports through the response, not an error.
var errRESTRefusedByGate = errors.New("REST: refused with 403 before the handler")

type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *fakeServerStream) Context() context.Context { return s.ctx }

func TestPeerAttestationGate_ReachesEveryTransportWithAPeer(t *testing.T) {
	const unaryMethod = "/qrdb.v1.QRDBService/Get"
	const streamMethod = "/qrdb.v1.QRDBService/Watch"
	unaryPerm, ok := LookupGRPCFromDescriptor(unaryMethod)
	if !ok {
		t.Fatalf("no permission descriptor for %s", unaryMethod)
	}
	streamPerm, ok := LookupGRPCFromDescriptor(streamMethod)
	if !ok {
		t.Fatalf("no permission descriptor for %s", streamMethod)
	}

	entryPoints := map[string]struct {
		perm permission.Permission
		call func(m *AuthMiddleware, ck certKey) error
	}{
		"REST": {gatedPerm, func(m *AuthMiddleware, ck certKey) error {
			routes := NewRESTRouteRegistry()
			routes.Register(http.MethodGet, "/v1/gated", gatedPerm)
			reached := false
			h := m.RESTMiddleware(routes, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true }))
			req := httptest.NewRequest(http.MethodGet, "/v1/gated", nil)
			req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{ck.cert}, VerifiedChains: [][]*x509.Certificate{{ck.cert}}}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			switch {
			case reached:
				return nil
			case rec.Code != http.StatusForbidden:
				return errors.New("REST answered " + http.StatusText(rec.Code) + ", want Forbidden")
			}
			return errRESTRefusedByGate
		}},
		"gRPC and unix (unary)": {unaryPerm, func(m *AuthMiddleware, ck certKey) error {
			_, err := m.UnaryInterceptor()(grpcPeerContext(ck), nil, &grpc.UnaryServerInfo{FullMethod: unaryMethod},
				func(context.Context, any) (any, error) { return nil, nil })
			return err
		}},
		"gRPC and unix (stream)": {streamPerm, func(m *AuthMiddleware, ck certKey) error {
			return m.StreamInterceptor()(nil, &fakeServerStream{ctx: grpcPeerContext(ck)}, &grpc.StreamServerInfo{FullMethod: streamMethod},
				func(any, grpc.ServerStream) error { return nil })
		}},
		"QUIC": {gatedPerm, func(m *AuthMiddleware, ck certKey) error {
			ctx, err := m.AuthenticateQUIC(mtlsContext(ck))
			if err != nil {
				return err
			}
			methods := NewQUICMethodRegistry()
			methods.Register("kv.Get", gatedPerm)
			return m.AuthorizeQUIC(ctx, methods, "kv.Get")
		}},
		"MCP": {gatedPerm, func(m *AuthMiddleware, ck certKey) error {
			ctx, _, err := m.Authenticate(mtlsContext(ck))
			if err != nil {
				return err
			}
			tools := NewMCPToolRegistry()
			tools.Register("qrdb_get", gatedPerm)
			return m.AuthorizeMCP(ctx, tools, "qrdb_get")
		}},
		"tenant-targeted handlers": {gatedPerm, func(m *AuthMiddleware, ck certKey) error {
			ctx, _, err := m.Authenticate(mtlsContext(ck))
			if err != nil {
				return err
			}
			return m.AuthorizeTenant(ctx, "acme", gatedPerm)
		}},
	}
	for name, ep := range entryPoints {
		t.Run(name, func(t *testing.T) {
			m, ck, v := transportGate(t, ep.perm)
			err := ep.call(m, ck)
			if err == nil {
				t.Fatal("an unattested peer was admitted")
			}
			if !errors.Is(err, errRESTRefusedByGate) && !strings.Contains(err.Error(), string(PeerAttestationNoEvidence)) {
				t.Fatalf("err = %v, want the gate's no-evidence refusal", err)
			}
			if v.callCount() != 1 {
				t.Fatalf("verifier calls = %d, want 1: the entry point did not reach the gate", v.callCount())
			}
		})
	}
}

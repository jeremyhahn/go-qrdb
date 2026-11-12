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

package clusterkeys_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterkeys"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
)

// ---------------------------------------------------------------------------
// Revocation test doubles
// ---------------------------------------------------------------------------

// fakeCertAuthority is a cert.CertProvider that answers only the revocation
// question. The tests drive the REAL cert.RevocationCache over it rather than
// stubbing auth.RevocationChecker directly, so what they exercise includes the
// serial rendering (cert.FormatSerial) and the sealed-barrier error mapping —
// the two places where an enforcement lookup can silently match nothing while
// every assertion about the policy still passes.
type fakeCertAuthority struct {
	// revoked holds serials in cert.FormatSerial form.
	revoked map[string]bool

	// err, when non-nil, is returned instead of an answer. A
	// *cert.BarrierSealedError here is what the cache turns into "no
	// revocation state to read".
	err error

	// lookups counts IsRevoked calls so a test can assert the check ran (or
	// did not) rather than inferring it from the outcome.
	lookups atomic.Int32
}

func (f *fakeCertAuthority) IsRevoked(_ context.Context, serial string) (bool, error) {
	f.lookups.Add(1)
	if f.err != nil {
		return false, f.err
	}
	return f.revoked[serial], nil
}

func (f *fakeCertAuthority) IssueClientCert(context.Context, *cert.IssueRequest) (*cert.IssuedCert, error) {
	return nil, errors.New("fakeCertAuthority: not implemented")
}

func (f *fakeCertAuthority) ExportPKCS12(context.Context, []byte, []byte, string) ([]byte, error) {
	return nil, errors.New("fakeCertAuthority: not implemented")
}

func (f *fakeCertAuthority) RevokeCert(context.Context, string) (*cert.RevokeResult, error) {
	return nil, errors.New("fakeCertAuthority: not implemented")
}

func (f *fakeCertAuthority) ListCerts(context.Context, string) ([]*cert.CertRecord, error) {
	return nil, errors.New("fakeCertAuthority: not implemented")
}

func (f *fakeCertAuthority) CACertPEM() []byte { return nil }

// Compile-time check: the fake must stay a full cert.CertProvider, because
// cert.NewRevocationCache takes the interface and a missing method would
// otherwise surface as an unrelated construction failure.
var _ cert.CertProvider = (*fakeCertAuthority)(nil)

// revokedCheckerFor returns a revocation checker that reports the given
// certificates as revoked, built the way production builds one: the real
// bounded-TTL cache over a provider. ttl is zero so every call consults the
// provider and the lookup counter means what it says.
func revokedCheckerFor(t *testing.T, certs ...*x509.Certificate) (auth.RevocationChecker, *fakeCertAuthority) {
	t.Helper()
	ca := &fakeCertAuthority{revoked: make(map[string]bool, len(certs))}
	for _, c := range certs {
		ca.revoked[cert.FormatSerial(c.SerialNumber)] = true
	}
	return cert.NewRevocationCache(ca, 0), ca
}

// failingChecker returns a checker whose lookups fail with err.
func failingChecker(t *testing.T, err error) (auth.RevocationChecker, *fakeCertAuthority) {
	t.Helper()
	ca := &fakeCertAuthority{err: err}
	return cert.NewRevocationCache(ca, 0), ca
}

// peerCertInjector wraps h and presents peerCert as the verified TLS peer, so
// a plaintext httptest server exercises the handler's mTLS branch. The
// receiver reads only req.TLS.PeerCertificates[0].
type peerCertInjector struct {
	peer *x509.Certificate
	next http.Handler
}

func (p *peerCertInjector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{p.peer}}
	p.next.ServeHTTP(w, r)
}

// deliveryRequest builds a syntactically valid delivery POST addressed to the
// follower. It never reaches HPKE open in these tests: the peer-certificate
// gate runs first, and where it admits, a later stage answers instead.
func deliveryRequest(t *testing.T, peer *x509.Certificate) *http.Request {
	t.Helper()
	body, err := json.Marshal(&keydelivery.Envelope{
		Meta: keydelivery.Meta{
			TenantID:     "tenant-1",
			Database:     "db-1",
			ShardID:      1,
			ReplicaID:    2,
			TargetNodeID: "follower-node-1",
			Mode:         testUnknownMode,
		},
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", bytes.NewReader(body))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{peer}}
	return req
}

// newReceiverWithRevocation builds a Receiver for follower-node-1 whose
// placement lookup FAILS. A test that reaches the placement gate therefore
// gets 403 and can distinguish "the revocation gate admitted" from "the
// revocation gate refused" by status code alone.
func newReceiverWithRevocation(t *testing.T, ca *testCABundle, follower *testNodeBundle, revocation auth.RevocationChecker) *clusterkeys.Receiver {
	t.Helper()
	recv, err := clusterkeys.NewReceiver(
		newTestKDReceiver(t, follower.key, "follower-node-1"),
		&staticPlacementGetter{err: errors.New("placement gate reached")},
		"cluster.local", "follower-node-1",
		ca.pool, revocation, follower.pem,
		newTestLogger(),
	)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	return recv
}

// ---------------------------------------------------------------------------
// Receiver: the security property
// ---------------------------------------------------------------------------

// TestHandler_RevokedPeerCert_Refused is the property go-qrdb did not have:
// an operator who revokes a compromised node's certificate stops it taking
// part in cluster key delivery.
//
// The peer here is a WELL-FORMED cluster peer — CA-signed, node SPIFFE SAN
// under the pinned trust domain — so both pre-existing checks pass. Only the
// revocation marker distinguishes it, which is the whole point: chain validity
// and node-SAN match are satisfied by a revoked certificate.
func TestHandler_RevokedPeerCert_Refused(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	leader := newTestNodeCert(t, ca, "cluster.local", "leader-node-1")

	checker, authority := revokedCheckerFor(t, leader.cert)
	recv := newReceiverWithRevocation(t, ca, follower, checker)

	rec := httptest.NewRecorder()
	recv.Handler()(rec, deliveryRequest(t, leader.cert))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked peer: status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if authority.lookups.Load() != 1 {
		t.Errorf("revocation lookups = %d, want 1", authority.lookups.Load())
	}
	if !strings.Contains(rec.Body.String(), "revoked") {
		t.Errorf("body = %q, want it to name revocation", rec.Body.String())
	}
}

// TestHandler_RevokedPeerCert_AdmittedWithoutChecker is the control for the
// test above, and it is the defect this change closes, kept executable.
//
// The SAME revoked certificate on the SAME request reaches the placement gate
// when no checker is wired. If the guard is ever removed or bypassed, this
// test keeps passing and the one above fails — which is what makes the pair
// evidence rather than an assertion about code that happens to be present.
func TestHandler_RevokedPeerCert_AdmittedWithoutChecker(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	leader := newTestNodeCert(t, ca, "cluster.local", "leader-node-1")

	// Revoked in the authority's eyes; the receiver is simply not asking.
	recv := newReceiverWithRevocation(t, ca, follower, nil)

	rec := httptest.NewRecorder()
	recv.Handler()(rec, deliveryRequest(t, leader.cert))

	if rec.Code != http.StatusForbidden {
		t.Errorf("no checker wired: status = %d, want %d (reached the placement gate)",
			rec.Code, http.StatusForbidden)
	}
}

// TestHandler_RevocationLookupFailure_FailsClosed verifies the second of the
// three outcomes: a lookup that could have answered and did not refuses the
// delivery with a retryable 503, rather than admitting a peer whose standing
// is unknown.
func TestHandler_RevocationLookupFailure_FailsClosed(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	leader := newTestNodeCert(t, ca, "cluster.local", "leader-node-1")

	checker, authority := failingChecker(t, errors.New("read index timeout"))
	recv := newReceiverWithRevocation(t, ca, follower, checker)

	rec := httptest.NewRecorder()
	recv.Handler()(rec, deliveryRequest(t, leader.cert))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("failed lookup: status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if authority.lookups.Load() != 1 {
		t.Errorf("revocation lookups = %d, want 1", authority.lookups.Load())
	}
	if strings.Contains(rec.Body.String(), "read index timeout") {
		t.Errorf("body = %q, want the internal lookup failure kept out of the response", rec.Body.String())
	}
}

// TestHandler_RevocationStateUnavailable_Admits verifies the third outcome and
// the one this endpoint most depends on. Cluster key delivery is precisely
// what runs on a node whose barriers are not open. The revocation markers live
// under the SYSTEM barrier (pkg/cert reads "__sys:certs_revoked/"), not the
// tenant barrier being delivered, so this branch is reached only in the window
// before this node's first system unseal — and refusing there would make a
// node that cannot yet read revocation state unable to receive the key it
// needs, with no operator remedy.
//
// The request is admitted past the certificate gate and answered by a later
// stage (the placement lookup fails: 403), which is what proves admission
// rather than silent success.
func TestHandler_RevocationStateUnavailable_Admits(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	leader := newTestNodeCert(t, ca, "cluster.local", "leader-node-1")

	checker, authority := failingChecker(t, &cert.BarrierSealedError{})
	recv := newReceiverWithRevocation(t, ca, follower, checker)

	rec := httptest.NewRecorder()
	recv.Handler()(rec, deliveryRequest(t, leader.cert))

	if rec.Code != http.StatusForbidden {
		t.Errorf("sealed barrier: status = %d, want %d (admitted, answered by the placement gate)",
			rec.Code, http.StatusForbidden)
	}
	if authority.lookups.Load() != 1 {
		t.Errorf("revocation lookups = %d, want 1", authority.lookups.Load())
	}
}

// TestHandler_UnrevokedPeerCert_Admitted verifies the gate does not refuse a
// peer that is merely unknown to the revocation table — the ordinary case, and
// the one a fail-closed bug would break for every delivery in the cluster.
func TestHandler_UnrevokedPeerCert_Admitted(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	leader := newTestNodeCert(t, ca, "cluster.local", "leader-node-1")

	// Some OTHER node's certificate is revoked, not this peer's.
	other := newTestNodeCert(t, ca, "cluster.local", "node-3")
	checker, authority := revokedCheckerFor(t, other.cert)
	recv := newReceiverWithRevocation(t, ca, follower, checker)

	rec := httptest.NewRecorder()
	recv.Handler()(rec, deliveryRequest(t, leader.cert))

	if rec.Code != http.StatusForbidden {
		t.Errorf("unrevoked peer: status = %d, want %d (admitted, answered by the placement gate)",
			rec.Code, http.StatusForbidden)
	}
	if authority.lookups.Load() != 1 {
		t.Errorf("revocation lookups = %d, want 1", authority.lookups.Load())
	}
}

// TestHandler_NoPeerCert_SkipsRevocationCheck verifies the no-mTLS downgrade
// path is unchanged: with no peer certificate there is no certificate to ask
// about, so the checker is never consulted and the request proceeds to the
// stages that do apply (HPKE recipient-binding, placement gate).
func TestHandler_NoPeerCert_SkipsRevocationCheck(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")

	checker, authority := failingChecker(t, errors.New("must not be consulted"))
	recv := newReceiverWithRevocation(t, ca, follower, checker)

	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", strings.NewReader("{bad json"))
	rec := httptest.NewRecorder()
	recv.Handler()(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("no peer cert: status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if authority.lookups.Load() != 0 {
		t.Errorf("revocation lookups = %d, want 0 (no certificate to ask about)", authority.lookups.Load())
	}
}

// TestHandler_RevocationCheckedAfterSAN verifies the gate ORDER: a peer that
// fails the node-SAN check is refused there, without a revocation lookup. The
// order matters for what an unauthenticated caller can make this node do — a
// revocation lookup is a replicated read, and it must sit behind the checks
// that establish the caller is a cluster peer at all.
func TestHandler_RevocationCheckedAfterSAN(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	wrongDomain := newTestNodeCertWrongDomain(t, ca, "leader-node-1")

	checker, authority := revokedCheckerFor(t, wrongDomain.cert)
	recv := newReceiverWithRevocation(t, ca, follower, checker)

	rec := httptest.NewRecorder()
	recv.Handler()(rec, deliveryRequest(t, wrongDomain.cert))

	if rec.Code != http.StatusForbidden {
		t.Errorf("wrong trust domain: status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if authority.lookups.Load() != 0 {
		t.Errorf("revocation lookups = %d, want 0 (SAN check refuses first)", authority.lookups.Load())
	}
}

// ---------------------------------------------------------------------------
// Receiver: end-to-end, against a delivery that would otherwise succeed
// ---------------------------------------------------------------------------

// revocationRoundTrip seals an envelope from a leader to a follower over an
// httptest server that presents the leader's certificate as the verified TLS
// peer, and returns the receiver's HTTP status and body.
//
// It exists so the refused case and the admitted case differ in EXACTLY one
// input — the receiver's revocation option. No tenant root key is ever
// shipped, so an admitted envelope clears every transport gate and is then
// refused at the mode dispatch (400 unknown delivery mode); a revoked
// delivering node is refused before it.
func revocationRoundTrip(t *testing.T, tenantID string, revocation auth.RevocationChecker) (*seal.BarrierRegistry, int, string) {
	t.Helper()

	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	leader := newTestNodeCert(t, ca, "cluster.local", "leader-node-1")
	followerDataRoot := t.TempDir()
	followerReg, followerAlloc := newTestRegistry(t)
	initTestSystemBarrier(t, followerReg)
	sealtest.ReserveVolumeTenant(t, followerAlloc, tenantID)

	p := newTestPlacement(tenantID, "db-main", 1, 2, "follower-node-1", followerDataRoot)
	recv, err := clusterkeys.NewReceiver(
		newTestKDReceiver(t, follower.key, "follower-node-1"),
		&staticPlacementGetter{p: p},
		"cluster.local", "follower-node-1",
		ca.pool, revocation, follower.pem,
		newTestLogger(),
	)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/cluster/keydelivery", recv.Handler())
	// leader.cert is the peer the receiver sees; a test revoking it must build
	// its checker from this same certificate, which is why the helper hands the
	// option in rather than the certificate out.
	srv := httptest.NewServer(&peerCertInjector{peer: leader.cert, next: mux})
	t.Cleanup(srv.Close)

	status, body := postSealedEnvelope(t, &follower.key.PublicKey, srv, keydelivery.Meta{
		TenantID:     tenantID,
		Database:     "db-main",
		ShardID:      1,
		ReplicaID:    2,
		TargetNodeID: "follower-node-1",
		Mode:         testUnknownMode,
	})
	return followerReg, status, body
}

// TestRoundTrip_RevokedLeader_Refused is the mutation-visible pair, run end
// to end: an otherwise-valid envelope, refused because the delivering node's
// certificate is revoked -- before the mode dispatch -- with nothing
// registered on the follower.
func TestRoundTrip_RevokedLeader_Refused(t *testing.T) {
	// The certificate the injector presents is minted inside the helper, so
	// this revokes by SUBJECT rather than by serial: every leader-node-1
	// certificate the helper's CA issues counts as revoked. Serial-keyed
	// lookup through the production cache is covered by
	// TestFormatSerialAgreement.
	checker := &subjectRevocationChecker{subject: "leader-node-1"}

	followerReg, status, body := revocationRoundTrip(t, "tenant-revoked", checker)
	if status == http.StatusOK || strings.Contains(body, "unknown delivery mode") {
		t.Fatalf("status %d body %q; want a revocation refusal before the mode dispatch", status, body)
	}
	if _, gerr := followerReg.GetTenant("tenant-revoked"); gerr == nil {
		t.Error("follower registered a tenant barrier from a revoked node's delivery")
	}
}

// TestRoundTrip_NoChecker_ReachesModeDispatch is the same envelope with the
// option omitted: every transport gate admits it, and the mode dispatch
// refuses it because no tenant root key is ever shipped.
func TestRoundTrip_NoChecker_ReachesModeDispatch(t *testing.T) {
	followerReg, status, body := revocationRoundTrip(t, "tenant-unchecked", nil)
	assertUnknownDeliveryMode(t, status, body)
	if _, gerr := followerReg.GetTenant("tenant-unchecked"); gerr == nil {
		t.Error("follower registered a tenant barrier from a refused delivery")
	}
}

// subjectRevocationChecker revokes every certificate whose Subject CN matches,
// regardless of serial. It exists only for the round-trip helper, which mints
// the peer certificate internally; every other test revokes by serial through
// the real cert.RevocationCache.
type subjectRevocationChecker struct {
	subject string
	lookups atomic.Int32
}

func (s *subjectRevocationChecker) IsCertRevoked(_ context.Context, peer *x509.Certificate) (bool, error) {
	s.lookups.Add(1)
	return peer != nil && peer.Subject.CommonName == s.subject, nil
}

var _ auth.RevocationChecker = (*subjectRevocationChecker)(nil)

// ---------------------------------------------------------------------------
// The constructor parameter
// ---------------------------------------------------------------------------

// TestNilRevocationChecker_IsEnforcementOff states the documented posture for
// a node with no certificate authority wired: a nil checker admits. The parameter is required so that posture is
// stated at the construction site; nil is a legal thing to state.
func TestNilRevocationChecker_IsEnforcementOff(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	leader := newTestNodeCert(t, ca, "cluster.local", "leader-node-1")

	recv := newReceiverWithRevocation(t, ca, follower, nil)
	rec := httptest.NewRecorder()
	recv.Handler()(rec, deliveryRequest(t, leader.cert))
	if rec.Code != http.StatusForbidden {
		t.Errorf("receiver with nil checker: status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// TestTypedNilRevocationCache_Admits guards the typed-nil trap that
// pkg/auth/wiring documents: cert.NewRevocationCache returns a typed nil
// pointer for a nil provider, and a typed nil in an interface is not a nil
// interface. Enforcement must still be off rather than panicking through a nil
// receiver on the first delivery of the cluster's life.
func TestTypedNilRevocationCache_Admits(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	leader := newTestNodeCert(t, ca, "cluster.local", "leader-node-1")

	typedNil := cert.NewRevocationCache(nil, 0)
	recv := newReceiverWithRevocation(t, ca, follower, typedNil)

	rec := httptest.NewRecorder()
	recv.Handler()(rec, deliveryRequest(t, leader.cert))
	if rec.Code != http.StatusForbidden {
		t.Errorf("typed-nil cache: status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// TestFormatSerialAgreement pins the assumption the receiver's enforcement
// rests on: the serial the cache looks up is the serial the certificate
// authority keys its marker with. Two independent renderings of a serial is
// how an enforcement check silently matches nothing while every test about the
// policy still passes.
func TestFormatSerialAgreement(t *testing.T) {
	ca := newTestCA(t)
	peer := newTestNodeCert(t, ca, "cluster.local", "leader-node-1")
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")

	authority := &fakeCertAuthority{revoked: map[string]bool{
		cert.FormatSerial(peer.cert.SerialNumber): true,
	}}
	recv := newReceiverWithRevocation(t, ca, follower,
		cert.NewRevocationCache(authority, 0))

	rec := httptest.NewRecorder()
	recv.Handler()(rec, deliveryRequest(t, peer.cert))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d: the lookup key did not match the marker key",
			rec.Code, http.StatusUnauthorized)
	}

	// The rendering is lowercase base-16, unpadded — asserted directly so a
	// change to it fails here rather than only as a missed refusal somewhere.
	if got := cert.FormatSerial(big.NewInt(255)); got != "ff" {
		t.Errorf("FormatSerial(255) = %q, want %q", got, "ff")
	}
}

// TenantForSerial is not exercised by the revocation cache -- it consumes
// IsRevoked only -- so this double owns no tenant mapping and reports the
// deny-by-serial case. It exists to satisfy cert.CertProvider.
func (f *fakeCertAuthority) TenantForSerial(_ context.Context, _ string) (string, bool, error) {
	return "", false, nil
}

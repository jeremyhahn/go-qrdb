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
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
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
			DatabaseID:   "db-1",
			ShardID:      1,
			ReplicaID:    2,
			TargetNodeID: "follower-node-1",
			Mode:         keydelivery.ModeSelfManaged,
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
	reg, _ := newTestRegistry(t)
	recv, err := clusterkeys.NewReceiver(
		newTestKDReceiver(t, follower.key, "follower-node-1"),
		reg,
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

// revocationRoundTrip runs a complete self-managed distribution from a
// leader to a follower over an httptest server that presents the leader's
// certificate as the verified TLS peer, and reports whether the follower ended
// up holding the tenant key.
//
// It exists so the refused case and the admitted case differ in EXACTLY one
// input — the receiver's revocation option — and are compared on the outcome
// that matters (does the follower hold the key?) rather than on a status code.
func revocationRoundTrip(t *testing.T, tenantID string, revocation auth.RevocationChecker) (*seal.BarrierRegistry, error) {
	t.Helper()
	ctx := context.Background()

	leaderReg, leaderAlloc := newTestRegistry(t)
	initTestSystemBarrier(t, leaderReg)
	leaderDataRoot := t.TempDir()
	leaderBarrier := initLeaderTenant(t, leaderReg, leaderAlloc, tenantID)

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
		followerReg,
		&staticPlacementGetter{p: p},
		"cluster.local", "follower-node-1",
		ca.pool, revocation, follower.pem,
		newTestLogger(),
	)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/cluster/nodekey", recv.NodeKeyHandler())
	mux.HandleFunc("/v1/cluster/keydelivery", recv.Handler())
	srv := httptest.NewServer(&peerCertInjector{peer: leader.cert, next: mux})
	t.Cleanup(srv.Close)

	resolver := &staticURLResolver{raftAddr: "follower-node-1:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	distPlacement := &placement.Placement{
		TenantID:        tenantID,
		DatabaseID:      "db-main",
		Database:        "db-main",
		ShardID:         1,
		RF:              2,
		LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "leader-node-1", RaftAddr: "leader-node-1:9000", DataRoot: leaderDataRoot},
			{ReplicaID: 2, NodeID: "follower-node-1", RaftAddr: "follower-node-1:9000", DataRoot: followerDataRoot},
		},
	}
	// leader.cert is the peer the receiver sees; a test revoking it must build
	// its checker from this same certificate, which is why the helper hands the
	// option in rather than the certificate out.
	return followerReg, sender.DistributeTenantKey(ctx, distPlacement, leaderBarrier, "leader-node-1")
}

// TestRoundTrip_RevokedLeader_KeyNotImported is the mutation-visible pair, run
// end to end: an otherwise-valid self-managed delivery, refused because the
// delivering node's certificate is revoked, with the follower's tenant barrier
// left absent rather than merely a non-200 on the wire.
func TestRoundTrip_RevokedLeader_KeyNotImported(t *testing.T) {
	// The certificate the injector presents is minted inside the helper, so
	// this revokes by SUBJECT rather than by serial: every leader-node-1
	// certificate the helper's CA issues counts as revoked. Serial-keyed
	// lookup through the production cache is covered by
	// TestFormatSerialAgreement.
	checker := &subjectRevocationChecker{subject: "leader-node-1"}

	followerReg, err := revocationRoundTrip(t, "tenant-revoked",
		checker)
	if err == nil {
		t.Fatal("DistributeTenantKey succeeded against a revoked delivering node")
	}
	if _, gerr := followerReg.GetTenant("tenant-revoked"); gerr == nil {
		t.Error("follower holds the tenant key after a delivery from a revoked node")
	}
}

// TestRoundTrip_NoChecker_KeyImported is the same distribution with the option
// omitted. It documents the pre-change behaviour precisely: the revoked node's
// delivery is accepted and the follower's tenant barrier is registered, holding
// the imported blob sealed until its owner unseals it.
func TestRoundTrip_NoChecker_KeyImported(t *testing.T) {
	followerReg, err := revocationRoundTrip(t, "tenant-unchecked", nil)
	if err != nil {
		t.Fatalf("DistributeTenantKey: %v", err)
	}
	tb, gerr := followerReg.GetTenant("tenant-unchecked")
	if gerr != nil {
		t.Fatalf("GetTenant: %v", gerr)
	}
	if !tb.IsSealed() {
		t.Error("follower tenant barrier must remain sealed after a self-managed import")
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
// Sender: the leader must not seal a root key to a revoked recipient
// ---------------------------------------------------------------------------

// newRevocationSender builds a Sender pointed at a nodekey endpoint serving
// certPEM, and returns it with the counter of delivery POSTs the endpoint saw.
// A test asserting the leader refused wants both facts: the error, and that no
// envelope was ever sent.
func newRevocationSender(t *testing.T, ca *testCABundle, certPEM []byte, revocation auth.RevocationChecker) (*clusterkeys.Sender, *atomic.Int32) {
	t.Helper()
	var deliveries atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/cluster/nodekey", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		_, _ = w.Write(certPEM)
	})
	mux.HandleFunc("/v1/cluster/keydelivery", func(w http.ResponseWriter, _ *http.Request) {
		deliveries.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"imported":true}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resolver := &staticURLResolver{raftAddr: "follower-node-1:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", revocation, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	return sender, &deliveries
}

// deliverTo runs one Deliver against the follower-node-1 replica.
func deliverTo(t *testing.T, sender *clusterkeys.Sender) error {
	t.Helper()
	target := placement.Replica{ReplicaID: 2, NodeID: "follower-node-1", RaftAddr: "follower-node-1:9000"}
	meta := keydelivery.Meta{
		TenantID:     "tenant-1",
		DatabaseID:   "db-main",
		ShardID:      1,
		ReplicaID:    2,
		TargetNodeID: "follower-node-1",
		Mode:         keydelivery.ModeSelfManaged,
	}
	return sender.Deliver(context.Background(), target, meta, []byte(`{"root_key":"AA=="}`))
}

// TestSender_RevokedRecipient_RefusesToSeal is the disclosure half of the same
// gap. HPKE recipient-binding guarantees only the holder of the recipient key
// can open the envelope; against a revoked node that guarantee works FOR the
// attacker, delivering the tenant root key to precisely the node the operator
// withdrew. The leader must decline before sealing.
func TestSender_RevokedRecipient_RefusesToSeal(t *testing.T) {
	ca := newTestCA(t)
	recipient := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")

	checker, authority := revokedCheckerFor(t, recipient.cert)
	sender, deliveries := newRevocationSender(t, ca, recipient.pem, checker)

	err := deliverTo(t, sender)
	if !errors.Is(err, clusterkeys.ErrRecipientRevoked) {
		t.Errorf("Deliver error = %v, want ErrRecipientRevoked", err)
	}
	if !errors.Is(err, auth.ErrCertificateRevoked) {
		t.Errorf("Deliver error = %v, want the auth-layer cause preserved", err)
	}
	if deliveries.Load() != 0 {
		t.Errorf("delivery POSTs = %d, want 0 (nothing may be sealed to a revoked recipient)", deliveries.Load())
	}
	if authority.lookups.Load() != 1 {
		t.Errorf("revocation lookups = %d, want 1", authority.lookups.Load())
	}
}

// TestSender_RevokedRecipient_SealsWithoutChecker is the control: the same
// revoked recipient receives the key when no checker is wired. This is the
// behaviour before this change.
func TestSender_RevokedRecipient_SealsWithoutChecker(t *testing.T) {
	ca := newTestCA(t)
	recipient := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")

	sender, deliveries := newRevocationSender(t, ca, recipient.pem, nil)

	if err := deliverTo(t, sender); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if deliveries.Load() != 1 {
		t.Errorf("delivery POSTs = %d, want 1", deliveries.Load())
	}
}

// TestSender_RecipientRevocationUnknown_FailsClosed verifies the leader
// refuses when it cannot determine the recipient's standing, and that the
// refusal is distinguishable from a revocation so a caller can retry it.
func TestSender_RecipientRevocationUnknown_FailsClosed(t *testing.T) {
	ca := newTestCA(t)
	recipient := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")

	checker, _ := failingChecker(t, errors.New("read index timeout"))
	sender, deliveries := newRevocationSender(t, ca, recipient.pem, checker)

	err := deliverTo(t, sender)
	if !errors.Is(err, clusterkeys.ErrRecipientRevocationUnknown) {
		t.Errorf("Deliver error = %v, want ErrRecipientRevocationUnknown", err)
	}
	if errors.Is(err, clusterkeys.ErrRecipientRevoked) {
		t.Error("an undetermined status must not be reported as a revocation")
	}
	if deliveries.Load() != 0 {
		t.Errorf("delivery POSTs = %d, want 0", deliveries.Load())
	}
}

// TestSender_RecipientRevocationStateUnavailable_Delivers verifies the sealed-
// barrier posture on the sending side. A leader whose own system barrier has
// no revocation state to read still distributes keys; refusing would stall
// cluster formation in exactly the window it has to work.
func TestSender_RecipientRevocationStateUnavailable_Delivers(t *testing.T) {
	ca := newTestCA(t)
	recipient := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")

	checker, authority := failingChecker(t, &cert.BarrierSealedError{})
	sender, deliveries := newRevocationSender(t, ca, recipient.pem, checker)

	if err := deliverTo(t, sender); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if deliveries.Load() != 1 {
		t.Errorf("delivery POSTs = %d, want 1", deliveries.Load())
	}
	if authority.lookups.Load() != 1 {
		t.Errorf("revocation lookups = %d, want 1", authority.lookups.Load())
	}
}

// TestSender_UnrevokedRecipient_Delivers is the ordinary case with the checker
// wired: a recipient nobody revoked is delivered to.
func TestSender_UnrevokedRecipient_Delivers(t *testing.T) {
	ca := newTestCA(t)
	recipient := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	other := newTestNodeCert(t, ca, "cluster.local", "node-3")

	checker, authority := revokedCheckerFor(t, other.cert)
	sender, deliveries := newRevocationSender(t, ca, recipient.pem, checker)

	if err := deliverTo(t, sender); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if deliveries.Load() != 1 {
		t.Errorf("delivery POSTs = %d, want 1", deliveries.Load())
	}
	if authority.lookups.Load() != 1 {
		t.Errorf("revocation lookups = %d, want 1", authority.lookups.Load())
	}
}

// TestSender_RevocationCheckedAfterNodeIdentity verifies the sender's gate
// order: a recipient whose SAN names the wrong node is refused on identity,
// without a revocation lookup.
func TestSender_RevocationCheckedAfterNodeIdentity(t *testing.T) {
	ca := newTestCA(t)
	wrongNode := newTestNodeCert(t, ca, "cluster.local", "node-9")

	checker, authority := revokedCheckerFor(t, wrongNode.cert)
	sender, _ := newRevocationSender(t, ca, wrongNode.pem, checker)

	err := deliverTo(t, sender)
	if !errors.Is(err, clusterkeys.ErrRecipientNodeMismatch) {
		t.Errorf("Deliver error = %v, want ErrRecipientNodeMismatch", err)
	}
	if authority.lookups.Load() != 0 {
		t.Errorf("revocation lookups = %d, want 0 (identity refuses first)", authority.lookups.Load())
	}
}

// ---------------------------------------------------------------------------
// The constructor parameter
// ---------------------------------------------------------------------------

// TestNilRevocationChecker_IsEnforcementOff states the documented posture for
// a node with no certificate authority wired: a nil checker admits, and does
// so on both halves of the plane. The parameter is required so that posture is
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

	sender, deliveries := newRevocationSender(t, ca, follower.pem, nil)
	if err := deliverTo(t, sender); err != nil {
		t.Fatalf("sender with nil checker: Deliver: %v", err)
	}
	if deliveries.Load() != 1 {
		t.Errorf("delivery POSTs = %d, want 1", deliveries.Load())
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

// TestRevocationErrorSentinels_AreDistinct keeps the two sender refusals from
// collapsing into one. They carry different operator meanings — withdraw the
// node, versus retry the delivery — and errors.Is must not confuse them.
func TestRevocationErrorSentinels_AreDistinct(t *testing.T) {
	if errors.Is(clusterkeys.ErrRecipientRevoked, clusterkeys.ErrRecipientRevocationUnknown) {
		t.Error("ErrRecipientRevoked matches ErrRecipientRevocationUnknown")
	}
	if errors.Is(clusterkeys.ErrRecipientRevocationUnknown, clusterkeys.ErrRecipientRevoked) {
		t.Error("ErrRecipientRevocationUnknown matches ErrRecipientRevoked")
	}
	for _, sentinel := range []error{clusterkeys.ErrRecipientRevoked, clusterkeys.ErrRecipientRevocationUnknown} {
		if errors.Is(sentinel, clusterkeys.ErrRecipientUntrusted) {
			t.Errorf("%v matches ErrRecipientUntrusted", sentinel)
		}
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

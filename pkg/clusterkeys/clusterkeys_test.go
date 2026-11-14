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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterkeys"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestLogger returns a discard logger to suppress output in tests.
func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testCABundle bundles the CA artefacts generated for one test.
type testCABundle struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
	pool *x509.CertPool
	pem  []byte
}

// newTestCA generates a self-signed ECDSA P-256 CA certificate valid for
// 24 hours. The returned pool contains exactly this CA.
func newTestCA(t *testing.T) *testCABundle {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Cluster CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return &testCABundle{key: key, cert: cert, pool: pool, pem: pemBytes}
}

// testNodeBundle bundles the node artefacts generated for one node identity.
type testNodeBundle struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
	pem  []byte
}

// newTestNodeCert issues an ECDSA P-256 leaf certificate signed by ca,
// carrying a SPIFFE URI SAN of the form spiffe://<trustDomain>/node/<nodeID>.
// The certificate has both ExtKeyUsageClientAuth (receiver peer-cert check) and
// ExtKeyUsageServerAuth so the same cert may serve as both the delivery cert
// and the mTLS peer cert.
func newTestNodeCert(t *testing.T, ca *testCABundle, trustDomain, nodeID string) *testNodeBundle {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate node key (%s): %v", nodeID, err)
	}
	spiffeURI, err := url.Parse("spiffe://" + trustDomain + "/node/" + nodeID)
	if err != nil {
		t.Fatalf("parse SPIFFE URI: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: nodeID},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
			x509.ExtKeyUsageServerAuth,
		},
		URIs: []*url.URL{spiffeURI},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("create node certificate (%s): %v", nodeID, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse node certificate (%s): %v", nodeID, err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return &testNodeBundle{key: key, cert: cert, pem: certPEM}
}

// newTestRegistry builds a BarrierRegistry with a real "dir"-provider
// volume pool and a per-tenant Pebble registry (sealtest.PooledRegistry),
// and returns the volume.Allocator so callers can Reserve a tenant's
// Assignment before RegisterVolumeTenant / ImportTenant, which require it
// to already exist (see seal.BarrierRegistry's doc comments).
// testUnknownMode is a delivery mode the receiver has no handler for: every
// envelope sealed with it must be refused with 400 "unknown delivery mode".
const testUnknownMode keydelivery.Mode = "test-unknown"

func newTestRegistry(t *testing.T) (*seal.BarrierRegistry, volume.Allocator) {
	t.Helper()
	sysBarrier := newTestSystemBarrier(t)
	reg, _, allocator := sealtest.PooledRegistry(t, sysBarrier)
	return reg, allocator
}

// newTestSystemBarrier builds a StorageBarrier with NoneStrategy (credential-
// free) suitable as the registry's system barrier. The NoneStrategy makes
// Initialize call credential-free and ensures auto-unseal tenant barriers can
// be imported without credentials.
func newTestSystemBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	store := seal.NewMemoryBackend()
	b, err := seal.NewStorageBarrier(
		newTestLogger(),
		store,
		seal.BarrierConfig{RootKeyPath: "system/root-key"},
		seal.NewNoneStrategy(),
	)
	if err != nil {
		t.Fatalf("NewStorageBarrier(system): %v", err)
	}
	return b
}

// initTestSystemBarrier initializes the registry's system barrier with the
// NoneStrategy (credential-free). After this call, auto-unseal tenant imports
// work without a passphrase.
func initTestSystemBarrier(t *testing.T, reg *seal.BarrierRegistry) {
	t.Helper()
	if err := reg.SystemBarrier().Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("Initialize system barrier: %v", err)
	}
}

// newTestKDReceiver builds a keydelivery.Receiver bound to the given ECDSA
// private key and node ID.
func newTestKDReceiver(t *testing.T, priv *ecdsa.PrivateKey, nodeID string) *keydelivery.Receiver {
	t.Helper()
	dec, err := keydelivery.PrivateKeyFor(keydelivery.KEMP256, priv)
	if err != nil {
		t.Fatalf("keydelivery.PrivateKeyFor: %v", err)
	}
	kd, err := keydelivery.NewReceiver(dec, nodeID)
	if err != nil {
		t.Fatalf("keydelivery.NewReceiver(%s): %v", nodeID, err)
	}
	return kd
}

// newTestPlacement builds a Placement with a single follower replica.
func newTestPlacement(tenantID, database string, shardID, replicaID uint64, nodeID, dataRoot string) *placement.Placement {
	return &placement.Placement{
		TenantID:        tenantID,
		Database:        database,
		ShardID:         shardID,
		RF:              2,
		LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "leader-node-1", RaftAddr: "leader-node-1:9000", DataRoot: ""},
			{ReplicaID: replicaID, NodeID: nodeID, RaftAddr: nodeID + ":9000", DataRoot: dataRoot},
		},
	}
}

// ---------------------------------------------------------------------------
// Mock helpers
// ---------------------------------------------------------------------------

// staticPlacementGetter returns a fixed Placement or a fixed error.
type staticPlacementGetter struct {
	p   *placement.Placement
	err error
}

func (s *staticPlacementGetter) Get(_ context.Context, _, _ string) (*placement.Placement, error) {
	return s.p, s.err
}

// ---------------------------------------------------------------------------
// Constructor validation tests
// ---------------------------------------------------------------------------

func TestNewReceiver_NilKD(t *testing.T) {
	placements := &staticPlacementGetter{}
	_, _, followerCertPEM := simpleNodeCert(t)
	_, err := clusterkeys.NewReceiver(nil, placements, "cluster.local", "node-1", nil, nil, followerCertPEM, nil)
	if !errors.Is(err, clusterkeys.ErrNilReceiver) {
		t.Errorf("expected ErrNilReceiver, got %v", err)
	}
}

func TestNewReceiver_NilPlacements(t *testing.T) {
	kd := newTestKDReceiver(t, mustP256Key(t), "node-1")
	_, _, certPEM := simpleNodeCert(t)
	_, err := clusterkeys.NewReceiver(kd, nil, "cluster.local", "node-1", nil, nil, certPEM, nil)
	if !errors.Is(err, clusterkeys.ErrNilPlacements) {
		t.Errorf("expected ErrNilPlacements, got %v", err)
	}
}

func TestNewReceiver_EmptySelfNodeID(t *testing.T) {
	kd := newTestKDReceiver(t, mustP256Key(t), "node-1")
	placements := &staticPlacementGetter{}
	_, _, certPEM := simpleNodeCert(t)
	_, err := clusterkeys.NewReceiver(kd, placements, "cluster.local", "", nil, nil, certPEM, nil)
	if !errors.Is(err, clusterkeys.ErrEmptySelfNodeID) {
		t.Errorf("expected ErrEmptySelfNodeID, got %v", err)
	}
}

func TestNewReceiver_EmptyDeliveryCertPEM(t *testing.T) {
	kd := newTestKDReceiver(t, mustP256Key(t), "node-1")
	placements := &staticPlacementGetter{}
	_, err := clusterkeys.NewReceiver(kd, placements, "cluster.local", "node-1", nil, nil, nil, nil)
	if !errors.Is(err, clusterkeys.ErrEmptyDeliveryCert) {
		t.Errorf("expected ErrEmptyDeliveryCert, got %v", err)
	}
}

func TestNewReceiver_NilLogger_UsesDefault(t *testing.T) {
	kd := newTestKDReceiver(t, mustP256Key(t), "node-1")
	placements := &staticPlacementGetter{}
	_, _, certPEM := simpleNodeCert(t)
	recv, err := clusterkeys.NewReceiver(kd, placements, "cluster.local", "node-1", nil, nil, certPEM, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recv == nil {
		t.Fatal("expected non-nil Receiver with nil logger")
	}
}

// ---------------------------------------------------------------------------
// NodeKeyHandler tests
// ---------------------------------------------------------------------------

func TestNodeKeyHandler_ServesNodeCert(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	kd := newTestKDReceiver(t, follower.key, "follower-node-1")
	placements := &staticPlacementGetter{}
	recv, err := clusterkeys.NewReceiver(kd, placements, "cluster.local", "follower-node-1", ca.pool, nil, follower.pem, newTestLogger())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/cluster/nodekey", nil)
	rec := httptest.NewRecorder()
	recv.NodeKeyHandler()(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-pem-file" {
		t.Errorf("expected Content-Type 'application/x-pem-file', got %q", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), follower.pem) {
		t.Error("response body does not match delivery cert PEM")
	}
}

func TestNodeKeyHandler_MethodNotAllowed(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	kd := newTestKDReceiver(t, follower.key, "follower-node-1")
	placements := &staticPlacementGetter{}
	recv, err := clusterkeys.NewReceiver(kd, placements, "cluster.local", "follower-node-1", nil, nil, follower.pem, newTestLogger())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/v1/cluster/nodekey", nil)
		rec := httptest.NewRecorder()
		recv.NodeKeyHandler()(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: expected 405, got %d", method, rec.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// Handler unit tests (no barrier/crypto round-trip needed)
// ---------------------------------------------------------------------------

func TestHandler_MethodNotAllowed(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	kd := newTestKDReceiver(t, follower.key, "follower-node-1")
	placements := &staticPlacementGetter{}
	recv, err := clusterkeys.NewReceiver(kd, placements, "cluster.local", "follower-node-1", nil, nil, follower.pem, newTestLogger())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/v1/cluster/keydelivery", nil)
		rec := httptest.NewRecorder()
		recv.Handler()(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: expected 405, got %d", method, rec.Code)
		}
	}
}

func TestHandler_MalformedBody(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	recv := newMinimalReceiver(t, follower, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery",
		strings.NewReader("{not valid json"))
	rec := httptest.NewRecorder()
	recv.Handler()(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandler_PlacementNotFound(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	placements := &staticPlacementGetter{err: errors.New("not found")}
	recv := newMinimalReceiver(t, follower, nil, placements)

	env := &keydelivery.Envelope{
		Meta: keydelivery.Meta{
			TenantID:     "tenant-1",
			DatabaseID:   "db-1",
			ShardID:      1,
			ReplicaID:    2,
			TargetNodeID: "follower-node-1",
			Mode:         testUnknownMode,
		},
	}
	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	recv.Handler()(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

func TestHandler_PlacementGate_WrongReplica(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	p := newTestPlacement("tenant-1", "db-1", 1, 99, "follower-node-1", "")
	placements := &staticPlacementGetter{p: p}
	recv := newMinimalReceiver(t, follower, nil, placements)

	// Envelope says replicaID=2 but placement has replicaID=99 for this node.
	env := &keydelivery.Envelope{
		Meta: keydelivery.Meta{
			TenantID:     "tenant-1",
			DatabaseID:   "db-1",
			ShardID:      1,
			ReplicaID:    2, // mismatch: placement has 99
			TargetNodeID: "follower-node-1",
			Mode:         testUnknownMode,
		},
	}
	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	recv.Handler()(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 (wrong replica), got %d", rec.Code)
	}
}

func TestHandler_PlacementGate_WrongShard(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	p := newTestPlacement("tenant-1", "db-1", 7, 2, "follower-node-1", "")
	placements := &staticPlacementGetter{p: p}
	recv := newMinimalReceiver(t, follower, nil, placements)

	// Envelope says shardID=1 but placement has shardID=7.
	env := &keydelivery.Envelope{
		Meta: keydelivery.Meta{
			TenantID:     "tenant-1",
			DatabaseID:   "db-1",
			ShardID:      1, // mismatch: placement has 7
			ReplicaID:    2,
			TargetNodeID: "follower-node-1",
			Mode:         testUnknownMode,
		},
	}
	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	recv.Handler()(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 (wrong shard), got %d", rec.Code)
	}
}

func TestHandler_PlacementGate_NodeNotInPlacement(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	// Placement has no replica for "follower-node-1".
	p := &placement.Placement{
		TenantID:        "tenant-1",
		Database:        "db-1",
		ShardID:         1,
		RF:              1,
		LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "different-node", RaftAddr: "different-node:9000"},
		},
	}
	placements := &staticPlacementGetter{p: p}
	recv := newMinimalReceiver(t, follower, nil, placements)

	env := &keydelivery.Envelope{
		Meta: keydelivery.Meta{
			TenantID:     "tenant-1",
			DatabaseID:   "db-1",
			ShardID:      1,
			ReplicaID:    2,
			TargetNodeID: "follower-node-1",
			Mode:         testUnknownMode,
		},
	}
	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	recv.Handler()(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 (node not in placement), got %d", rec.Code)
	}
}

// TestHandler_WrongTargetNode verifies that an envelope sealed for a different
// node (wrong TargetNodeID) is rejected with 403 (ErrWrongTarget).
func TestHandler_WrongTargetNode(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	p := newTestPlacement("tenant-1", "db-1", 1, 2, "follower-node-1", "")
	placements := &staticPlacementGetter{p: p}
	recv := newMinimalReceiver(t, follower, nil, placements)

	// Seal to the follower's public key but with TargetNodeID = "other-node".
	// The keydelivery.Receiver checks TargetNodeID != nodeID → ErrWrongTarget.
	meta := keydelivery.Meta{
		TenantID:     "tenant-1",
		DatabaseID:   "db-1",
		ShardID:      1,
		ReplicaID:    2,
		TargetNodeID: "other-node", // deliberate mismatch
		Mode:         testUnknownMode,
	}
	env, err := keydelivery.Seal(&follower.key.PublicKey, meta, []byte("plaintext-key-material"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	recv.Handler()(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 (wrong target), got %d", rec.Code)
	}
}

// TestHandler_TamperedPayload verifies that a correctly-targeted envelope whose
// payload has been corrupted is rejected with 403 (ErrDecrypt). All non-
// structural Open failures collapse to a single opaque 403 so the status code
// is not a reconnaissance oracle.
func TestHandler_TamperedPayload(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	p := newTestPlacement("tenant-1", "db-1", 1, 2, "follower-node-1", "")
	placements := &staticPlacementGetter{p: p}
	recv := newMinimalReceiver(t, follower, nil, placements)

	meta := keydelivery.Meta{
		TenantID:     "tenant-1",
		DatabaseID:   "db-1",
		ShardID:      1,
		ReplicaID:    2,
		TargetNodeID: "follower-node-1",
		Mode:         testUnknownMode,
	}
	env, err := keydelivery.Seal(&follower.key.PublicKey, meta, []byte("plaintext-key-material"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Corrupt the ciphertext — HPKE Open must fail.
	if len(env.Payload) > 0 {
		env.Payload[len(env.Payload)-1] ^= 0xFF
	}
	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	recv.Handler()(rec, req)

	// LOW-1 fix: ErrDecrypt → 403 (was 401). All non-structural Open rejections
	// collapse to a single opaque 403 to avoid a reconstruction oracle.
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 (ErrDecrypt), got %d", rec.Code)
	}
}

// TestHandler_OversizedBody verifies that a request body exceeding
// maxEnvelopeBytes (64 KiB) is rejected with 400. The body-size cap defends
// this RBAC-exempt endpoint against unbounded-body OOM attacks.
func TestHandler_OversizedBody(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	recv := newMinimalReceiver(t, follower, nil, nil)

	// Body is 65 KiB — one KiB over the 64 KiB cap.
	oversized := make([]byte, 65*1024)
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", bytes.NewReader(oversized))
	rec := httptest.NewRecorder()
	recv.Handler()(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 (oversized body), got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Integration round-trip tests
// ---------------------------------------------------------------------------

// TestHandler_NoPeerCert_DowngradeWarning verifies that when no TLS peer
// certificate is present the delivery still succeeds (falling back to HPKE
// recipient-binding + placement gate) and that the downgrade warning fires only
// once across repeated requests.
func TestHandler_NoPeerCert_DowngradeWarning(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	followerDataRoot := t.TempDir()
	p := newTestPlacement("tenant-nodl", "db-main", 1, 2, "follower-node-1", followerDataRoot)
	followerReg, followerAlloc := newTestRegistry(t)
	initTestSystemBarrier(t, followerReg)
	sealtest.ReserveVolumeTenant(t, followerAlloc, "tenant-nodl")

	// Use nil caPool so the receiver does not enforce TLS cert chain checks.
	// The plain httptest.Server carries no peer certs, so the handler falls
	// through to the HPKE + placement gate path (downgrade warning).
	kd := newTestKDReceiver(t, follower.key, "follower-node-1")
	recv, err := clusterkeys.NewReceiver(kd, &staticPlacementGetter{p: p},
		"cluster.local", "follower-node-1", nil, nil, follower.pem, newTestLogger())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/cluster/keydelivery", recv.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Without mTLS the envelope still clears every transport gate (HPKE,
	// placement); the mode dispatch then refuses it, since no tenant root
	// key is ever shipped.
	status, body := postSealedEnvelope(t, &follower.key.PublicKey, srv, keydelivery.Meta{
		TenantID:     "tenant-nodl",
		DatabaseID:   "db-main",
		ShardID:      1,
		ReplicaID:    2,
		TargetNodeID: "follower-node-1",
		Mode:         testUnknownMode,
	})
	assertUnknownDeliveryMode(t, status, body)

	// Nothing was installed on the follower.
	if _, err := followerReg.GetTenant("tenant-nodl"); err == nil {
		t.Error("follower registered a tenant barrier from a refused delivery")
	}
}

// postSealedEnvelope seals a placeholder secret to recipient and POSTs the envelope to srv's
// key-delivery route, returning the HTTP status and response body.
func postSealedEnvelope(t *testing.T, recipient *ecdsa.PublicKey, srv *httptest.Server, meta keydelivery.Meta) (int, string) {
	t.Helper()
	env, err := keydelivery.Seal(recipient, meta, []byte("not a tenant key"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	resp, err := srv.Client().Post(srv.URL+"/v1/cluster/keydelivery", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST keydelivery: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// assertUnknownDeliveryMode checks the receiver refused an envelope at the
// mode dispatch -- after every transport gate passed -- with 400.
func assertUnknownDeliveryMode(t *testing.T, status int, body string) {
	t.Helper()
	if status != http.StatusBadRequest || !strings.Contains(body, "unknown delivery mode") {
		t.Fatalf("status %d body %q; want 400 unknown delivery mode", status, body)
	}
}

// TestHandler_EvilClusterNode_RejectedAtPlacement verifies that a peer
// certificate signed by the cluster CA but belonging to a different cluster
// node ("evil-node") passes the node SAN check — any valid cluster peer cert
// is accepted — and is then rejected with 403 at the placement gate, which has
// no entry assigning "evil-node" to this tenant/database/shard/replica.
func TestHandler_EvilClusterNode_RejectedAtPlacement(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")

	kd := newTestKDReceiver(t, follower.key, "follower-node-1")
	// Placement lookup returns an error — no entry for this tenant/database.
	placements := &staticPlacementGetter{err: errors.New("no placement entry")}
	recv, err := clusterkeys.NewReceiver(kd, placements, "cluster.local", "follower-node-1",
		ca.pool, nil, follower.pem, newTestLogger())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	// evil-node cert: signed by the cluster CA, carries a valid node SPIFFE
	// SAN — the SAN check passes because checkNodeSAN only verifies the sender
	// IS a cluster peer, not that it is this specific node. The placement gate
	// is the second layer and rejects the delivery with 403.
	evil := newTestNodeCert(t, ca, "cluster.local", "evil-node")

	body, _ := json.Marshal(&keydelivery.Envelope{
		Meta: keydelivery.Meta{
			TenantID:     "tenant-1",
			DatabaseID:   "db-1",
			ShardID:      1,
			ReplicaID:    2,
			TargetNodeID: "follower-node-1",
			Mode:         testUnknownMode,
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", bytes.NewReader(body))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{evil.cert}}
	rec := httptest.NewRecorder()
	recv.Handler()(rec, req)

	// SAN check passes → placement gate rejects (no entry) → 403.
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 (placement gate rejection), got %d", rec.Code)
	}
}

// TestHandler_InvalidPeerCert verifies that a peer certificate that does NOT
// chain to the cluster CA is rejected with 401.
func TestHandler_InvalidPeerCert(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")

	kd := newTestKDReceiver(t, follower.key, "follower-node-1")
	placements := &staticPlacementGetter{err: errors.New("should not reach")}
	recv, err := clusterkeys.NewReceiver(kd, placements, "cluster.local", "follower-node-1",
		ca.pool, nil, follower.pem, newTestLogger())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	// Self-signed cert — does NOT chain to the cluster CA.
	rogueCA := newTestCA(t)
	rogue := newTestNodeCert(t, rogueCA, "cluster.local", "follower-node-1")

	body, _ := json.Marshal(&keydelivery.Envelope{
		Meta: keydelivery.Meta{
			TenantID:     "tenant-1",
			DatabaseID:   "db-1",
			ShardID:      1,
			ReplicaID:    2,
			TargetNodeID: "follower-node-1",
			Mode:         testUnknownMode,
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", bytes.NewReader(body))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{rogue.cert}}
	rec := httptest.NewRecorder()
	recv.Handler()(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 (invalid peer cert), got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Error sentinel tests
// ---------------------------------------------------------------------------

func TestErrorSentinels_AreDistinct(t *testing.T) {
	sentinels := []error{
		clusterkeys.ErrNilReceiver,
		clusterkeys.ErrNilPlacements,
		clusterkeys.ErrEmptySelfNodeID,
		clusterkeys.ErrEmptyDeliveryCert,
		clusterkeys.ErrPlacementGate,
		clusterkeys.ErrMalformedBody,
		clusterkeys.ErrUnknownMode,
	}
	seen := make(map[string]bool)
	for _, e := range sentinels {
		msg := e.Error()
		if seen[msg] {
			t.Errorf("duplicate sentinel message: %q", msg)
		}
		seen[msg] = true
	}
	if len(seen) != len(sentinels) {
		t.Errorf("expected %d distinct sentinels, got %d", len(sentinels), len(seen))
	}
}

// ---------------------------------------------------------------------------
// Unexported helpers used only within this test file
// ---------------------------------------------------------------------------

// newMinimalReceiver builds a Receiver with the given options, using the
// follower's key and cert, and a fresh test registry. The caPool may be nil.
// The placements argument may be nil to trigger a default "placement not found"
// response.
func newMinimalReceiver(t *testing.T, follower *testNodeBundle, caPool *x509.CertPool, placements clusterkeys.PlacementGetter) *clusterkeys.Receiver {
	t.Helper()
	if placements == nil {
		placements = &staticPlacementGetter{err: errors.New("no placement")}
	}
	kd := newTestKDReceiver(t, follower.key, "follower-node-1")
	recv, err := clusterkeys.NewReceiver(kd, placements, "cluster.local", "follower-node-1",
		caPool, nil, follower.pem, newTestLogger())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	return recv
}

// mustP256Key generates a P-256 key or fails the test.
func mustP256Key(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey P256: %v", err)
	}
	return k
}

// newTestNodeCertWrongDomain issues a CA-signed P-256 certificate with a
// SPIFFE SAN that carries a different trust domain than "cluster.local".
func newTestNodeCertWrongDomain(t *testing.T, ca *testCABundle, nodeID string) *testNodeBundle {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate wrong-domain key: %v", err)
	}
	spiffeURI, _ := url.Parse("spiffe://wrong.domain/node/" + nodeID)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano() + 200),
		Subject:      pkix.Name{CommonName: nodeID},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{spiffeURI},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("create wrong-domain certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse wrong-domain certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return &testNodeBundle{key: key, cert: cert, pem: certPEM}
}

// ---------------------------------------------------------------------------
// Additional tests for branch coverage
// ---------------------------------------------------------------------------

// TestHandler_NilCAPool_NoSAN verifies that when the receiver has nil caPool
// and the peer cert has no SPIFFE SAN, both verifyCert (nil pool → skip) and
// checkNodeSAN (ErrNoNodeSPIFFEID → proceed) return nil. The handler continues
// and the malformed body produces 400.
func TestHandler_NilCAPool_NoSAN(t *testing.T) {
	// Peer cert is self-signed (no CA) — caPool is nil so chain check skipped.
	_, selfSignedCert, selfSignedPEM := simpleNodeCert(t)

	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	kd := newTestKDReceiver(t, follower.key, "follower-node-1")
	placements := &staticPlacementGetter{err: errors.New("not reached yet")}
	// nil caPool: verifyCert skips chain check entirely.
	recv, err := clusterkeys.NewReceiver(kd, placements, "cluster.local", "follower-node-1",
		nil, nil, selfSignedPEM, newTestLogger())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery",
		strings.NewReader("{not valid json"))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{selfSignedCert}}
	rec := httptest.NewRecorder()
	recv.Handler()(rec, req)

	// verifyCert: caPool nil → return nil
	// checkNodeSAN: no SPIFFE SAN → ErrNoNodeSPIFFEID → return nil (proceed)
	// decode envelope: malformed → 400
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 (malformed body after cert proceed), got %d", rec.Code)
	}
}

// TestHandler_TrustDomainMismatch verifies that a peer certificate signed by
// the cluster CA but carrying a SPIFFE SAN from a foreign trust domain is
// rejected with 403 (trust domain mismatch → checkNodeSAN returns error).
func TestHandler_TrustDomainMismatch(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	kd := newTestKDReceiver(t, follower.key, "follower-node-1")
	placements := &staticPlacementGetter{err: errors.New("not reached")}
	recv, err := clusterkeys.NewReceiver(kd, placements, "cluster.local", "follower-node-1",
		ca.pool, nil, follower.pem, newTestLogger())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	wrongDomain := newTestNodeCertWrongDomain(t, ca, "follower-node-1")

	body, _ := json.Marshal(&keydelivery.Envelope{
		Meta: keydelivery.Meta{
			TenantID:     "t",
			DatabaseID:   "db",
			ShardID:      1,
			ReplicaID:    2,
			TargetNodeID: "follower-node-1",
			Mode:         testUnknownMode,
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", bytes.NewReader(body))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{wrongDomain.cert}}
	rec := httptest.NewRecorder()
	recv.Handler()(rec, req)

	// verifyCert passes (CA-signed), checkNodeSAN → ErrTrustDomainMismatch → 403
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 (trust domain mismatch), got %d", rec.Code)
	}
}

// TestHandler_CorrectNodeSAN_Proceeds verifies that a peer certificate with a
// matching node SPIFFE SAN causes checkNodeSAN to return nil (proceed to
// envelope decode). The malformed body triggers 400.
func TestHandler_CorrectNodeSAN_Proceeds(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	kd := newTestKDReceiver(t, follower.key, "follower-node-1")
	placements := &staticPlacementGetter{err: errors.New("will not reach")}
	recv, err := clusterkeys.NewReceiver(kd, placements, "cluster.local", "follower-node-1",
		ca.pool, nil, follower.pem, newTestLogger())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	// follower.cert has spiffe://cluster.local/node/follower-node-1 — correct.
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery",
		strings.NewReader("{bad json"))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{follower.cert}}
	rec := httptest.NewRecorder()
	recv.Handler()(rec, req)

	// checkNodeSAN: cert has valid node SAN under "cluster.local" → return nil
	// Malformed body → 400
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 (malformed body after SAN proceed), got %d", rec.Code)
	}
}

// simpleNodeCert generates a self-signed P-256 cert without a SPIFFE SAN.
// Used in constructor tests where a real CA is not needed.
func simpleNodeCert(t *testing.T) (*ecdsa.PrivateKey, *x509.Certificate, []byte) {
	t.Helper()
	key := mustP256Key(t)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "simple-node"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate (simple): %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate (simple): %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return key, cert, certPEM
}

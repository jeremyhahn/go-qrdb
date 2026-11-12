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
	"sync/atomic"
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
// ExtKeyUsageServerAuth (sender fetchNodeKey chain check) so the same cert may
// serve as both the delivery cert and the mTLS peer cert.
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

// newTestNonP256CertPEM generates a P-384 ECDSA certificate (not P-256) signed
// by ca and returns its PEM encoding. The certificate carries both
// ExtKeyUsageServerAuth and ExtKeyUsageClientAuth, and a node SPIFFE SAN for
// the given trustDomain and nodeID, so fetchNodeKey passes the chain and SAN
// checks before failing at the P-256 key-type assertion.
func newTestNonP256CertPEM(t *testing.T, ca *testCABundle, trustDomain, nodeID string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate P-384 key: %v", err)
	}
	spiffeURI, err := url.Parse("spiffe://" + trustDomain + "/node/" + nodeID)
	if err != nil {
		t.Fatalf("parse SPIFFE URI for non-P256 cert: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(999),
		Subject:      pkix.Name{CommonName: "non-p256-node"},
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
		t.Fatalf("create non-P256 certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// newTestRegistry builds a BarrierRegistry with a real "dir"-provider
// volume pool and a per-tenant Pebble registry (sealtest.PooledRegistry),
// and returns the volume.Allocator so callers can Reserve a tenant's
// Assignment before RegisterVolumeTenant / ImportTenant, which require it
// to already exist (see seal.BarrierRegistry's doc comments).
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

// initLeaderTenant reserves tenantID on allocator's "default" pool and
// initializes a tenant barrier on a leader registry. Returns the sealed
// StorageBarrier after Initialize.
func initLeaderTenant(t *testing.T, reg *seal.BarrierRegistry, allocator volume.Allocator, tenantID string) *seal.StorageBarrier {
	t.Helper()
	ctx := context.Background()
	sealtest.ReserveVolumeTenant(t, allocator, tenantID)
	_, tb, err := reg.RegisterVolumeTenant(ctx, tenantID, true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant(%s): %v", tenantID, err)
	}
	if err := tb.Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatalf("Initialize tenant(%s): %v", tenantID, err)
	}
	return tb.Barrier()
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

// newTestReceiverFull builds a fully configured clusterkeys.Receiver, starts an
// httptest.Server with the delivery and nodekey routes, and returns both. The
// server is stopped when t cleans up.
func newTestReceiverFull(
	t *testing.T,
	ca *testCABundle,
	follower *testNodeBundle,
	followerNodeID string,
	reg *seal.BarrierRegistry,
	placements clusterkeys.PlacementGetter,
) (*clusterkeys.Receiver, *httptest.Server) {
	t.Helper()
	kd := newTestKDReceiver(t, follower.key, followerNodeID)
	recv, err := clusterkeys.NewReceiver(
		kd, reg, placements,
		"cluster.local", followerNodeID,
		ca.pool, nil, follower.pem,
		newTestLogger(),
	)
	if err != nil {
		t.Fatalf("clusterkeys.NewReceiver: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/cluster/nodekey", recv.NodeKeyHandler())
	mux.HandleFunc("/v1/cluster/keydelivery", recv.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return recv, srv
}

// newTestPlacement builds a Placement with a single follower replica.
func newTestPlacement(tenantID, database string, shardID, replicaID uint64, nodeID, dataRoot string) *placement.Placement {
	return &placement.Placement{
		TenantID:        tenantID,
		DatabaseID:      database,
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

// staticURLResolver resolves one raft address to one admin URL.
type staticURLResolver struct {
	raftAddr string
	adminURL string
}

func (s *staticURLResolver) ResolveAdminURL(raftAddr string) string {
	if raftAddr == s.raftAddr {
		return s.adminURL
	}
	return ""
}

// mapURLResolver resolves each raft address in it to its admin URL.
type mapURLResolver map[string]string

func (m mapURLResolver) ResolveAdminURL(raftAddr string) string {
	return m[raftAddr]
}

// newFollowerServer serves node's delivery certificate at
// /v1/cluster/nodekey and answers /v1/cluster/keydelivery with delivery.
func newFollowerServer(t *testing.T, node *testNodeBundle, delivery http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/cluster/nodekey", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		_, _ = w.Write(node.pem)
	})
	mux.HandleFunc("/v1/cluster/keydelivery", delivery)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// countingHandler wraps an http.Handler and counts requests.
type countingHandler struct {
	http.Handler
	count atomic.Int64
}

func (c *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.count.Add(1)
	c.Handler.ServeHTTP(w, r)
}

// ---------------------------------------------------------------------------
// Constructor validation tests
// ---------------------------------------------------------------------------

func TestNewReceiver_NilKD(t *testing.T) {
	reg, _ := newTestRegistry(t)
	placements := &staticPlacementGetter{}
	_, _, followerCertPEM := simpleNodeCert(t)
	_, err := clusterkeys.NewReceiver(nil, reg, placements, "cluster.local", "node-1", nil, nil, followerCertPEM, nil)
	if !errors.Is(err, clusterkeys.ErrNilReceiver) {
		t.Errorf("expected ErrNilReceiver, got %v", err)
	}
}

func TestNewReceiver_NilRegistry(t *testing.T) {
	kd := newTestKDReceiver(t, mustP256Key(t), "node-1")
	placements := &staticPlacementGetter{}
	_, _, certPEM := simpleNodeCert(t)
	_, err := clusterkeys.NewReceiver(kd, nil, placements, "cluster.local", "node-1", nil, nil, certPEM, nil)
	if !errors.Is(err, clusterkeys.ErrNilRegistry) {
		t.Errorf("expected ErrNilRegistry, got %v", err)
	}
}

func TestNewReceiver_NilPlacements(t *testing.T) {
	kd := newTestKDReceiver(t, mustP256Key(t), "node-1")
	reg, _ := newTestRegistry(t)
	_, _, certPEM := simpleNodeCert(t)
	_, err := clusterkeys.NewReceiver(kd, reg, nil, "cluster.local", "node-1", nil, nil, certPEM, nil)
	if !errors.Is(err, clusterkeys.ErrNilPlacements) {
		t.Errorf("expected ErrNilPlacements, got %v", err)
	}
}

func TestNewReceiver_EmptySelfNodeID(t *testing.T) {
	kd := newTestKDReceiver(t, mustP256Key(t), "node-1")
	reg, _ := newTestRegistry(t)
	placements := &staticPlacementGetter{}
	_, _, certPEM := simpleNodeCert(t)
	_, err := clusterkeys.NewReceiver(kd, reg, placements, "cluster.local", "", nil, nil, certPEM, nil)
	if !errors.Is(err, clusterkeys.ErrEmptySelfNodeID) {
		t.Errorf("expected ErrEmptySelfNodeID, got %v", err)
	}
}

func TestNewReceiver_EmptyDeliveryCertPEM(t *testing.T) {
	kd := newTestKDReceiver(t, mustP256Key(t), "node-1")
	reg, _ := newTestRegistry(t)
	placements := &staticPlacementGetter{}
	_, err := clusterkeys.NewReceiver(kd, reg, placements, "cluster.local", "node-1", nil, nil, nil, nil)
	if !errors.Is(err, clusterkeys.ErrEmptyDeliveryCert) {
		t.Errorf("expected ErrEmptyDeliveryCert, got %v", err)
	}
}

func TestNewReceiver_NilLogger_UsesDefault(t *testing.T) {
	kd := newTestKDReceiver(t, mustP256Key(t), "node-1")
	reg, _ := newTestRegistry(t)
	placements := &staticPlacementGetter{}
	_, _, certPEM := simpleNodeCert(t)
	recv, err := clusterkeys.NewReceiver(kd, reg, placements, "cluster.local", "node-1", nil, nil, certPEM, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recv == nil {
		t.Fatal("expected non-nil Receiver with nil logger")
	}
}

func TestNewSender_NilResolver(t *testing.T) {
	ca := newTestCA(t)
	_, err := clusterkeys.NewSender(nil, http.DefaultClient, ca.pool, "cluster.local", nil, newTestLogger())
	if !errors.Is(err, clusterkeys.ErrNilResolver) {
		t.Errorf("expected ErrNilResolver, got %v", err)
	}
}

func TestNewSender_NilClient(t *testing.T) {
	resolver := &staticURLResolver{}
	ca := newTestCA(t)
	_, err := clusterkeys.NewSender(resolver, nil, ca.pool, "cluster.local", nil, newTestLogger())
	if !errors.Is(err, clusterkeys.ErrNilClient) {
		t.Errorf("expected ErrNilClient, got %v", err)
	}
}

func TestNewSender_NilCAPool(t *testing.T) {
	resolver := &staticURLResolver{}
	_, err := clusterkeys.NewSender(resolver, http.DefaultClient, nil, "cluster.local", nil, newTestLogger())
	if !errors.Is(err, clusterkeys.ErrNilCAPool) {
		t.Errorf("expected ErrNilCAPool, got %v", err)
	}
}

func TestNewSender_EmptyTrustDomain(t *testing.T) {
	resolver := &staticURLResolver{}
	ca := newTestCA(t)
	_, err := clusterkeys.NewSender(resolver, http.DefaultClient, ca.pool, "", nil, newTestLogger())
	if !errors.Is(err, clusterkeys.ErrEmptyTrustDomain) {
		t.Errorf("expected ErrEmptyTrustDomain, got %v", err)
	}
}

func TestNewSender_NilLogger_UsesDefault(t *testing.T) {
	resolver := &staticURLResolver{}
	ca := newTestCA(t)
	sender, err := clusterkeys.NewSender(resolver, http.DefaultClient, ca.pool, "cluster.local", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sender == nil {
		t.Fatal("expected non-nil Sender with nil logger")
	}
}

// ---------------------------------------------------------------------------
// NodeKeyHandler tests
// ---------------------------------------------------------------------------

func TestNodeKeyHandler_ServesNodeCert(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	kd := newTestKDReceiver(t, follower.key, "follower-node-1")
	reg, _ := newTestRegistry(t)
	placements := &staticPlacementGetter{}
	recv, err := clusterkeys.NewReceiver(kd, reg, placements, "cluster.local", "follower-node-1", ca.pool, nil, follower.pem, newTestLogger())
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
	reg, _ := newTestRegistry(t)
	placements := &staticPlacementGetter{}
	recv, err := clusterkeys.NewReceiver(kd, reg, placements, "cluster.local", "follower-node-1", nil, nil, follower.pem, newTestLogger())
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
	reg, _ := newTestRegistry(t)
	placements := &staticPlacementGetter{}
	recv, err := clusterkeys.NewReceiver(kd, reg, placements, "cluster.local", "follower-node-1", nil, nil, follower.pem, newTestLogger())
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
			Mode:         keydelivery.ModeSelfManaged,
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
			Mode:         keydelivery.ModeSelfManaged,
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
			Mode:         keydelivery.ModeSelfManaged,
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
		DatabaseID:      "db-1",
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
			Mode:         keydelivery.ModeSelfManaged,
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
		Mode:         keydelivery.ModeSelfManaged,
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
		Mode:         keydelivery.ModeSelfManaged,
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

// TestHandler_SelfManaged_RoundTrip exercises the self-managed path:
//   - Leader initializes a tenant barrier and exports the sealed blob.
//   - Sender delivers the blob (HPKE-wrapped) to the follower.
//   - Receiver persists the blob; the follower barrier remains sealed.
func TestHandler_SelfManaged_RoundTrip(t *testing.T) {
	ctx := context.Background()

	// ---- Leader registry ----
	leaderReg, leaderAlloc := newTestRegistry(t)
	initTestSystemBarrier(t, leaderReg)
	leaderDataRoot := t.TempDir()
	leaderBarrier := initLeaderTenant(t, leaderReg, leaderAlloc, "tenant-selfmanaged")

	// ---- Follower setup ----
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	followerDataRoot := t.TempDir()
	p := newTestPlacement("tenant-selfmanaged", "db-main", 1, 2, "follower-node-1", followerDataRoot)
	followerReg, followerAlloc := newTestRegistry(t)
	initTestSystemBarrier(t, followerReg)
	sealtest.ReserveVolumeTenant(t, followerAlloc, "tenant-selfmanaged")

	placements := &staticPlacementGetter{p: p}
	_, srv := newTestReceiverFull(t, ca, follower, "follower-node-1", followerReg, placements)

	// ---- Sender ----
	resolver := &staticURLResolver{raftAddr: "follower-node-1:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	distPlacement := &placement.Placement{
		TenantID:        "tenant-selfmanaged",
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
	if err := sender.DistributeTenantKey(ctx, distPlacement, leaderBarrier, "leader-node-1"); err != nil {
		t.Fatalf("DistributeTenantKey (self-managed): %v", err)
	}

	// ---- Verify follower barrier is registered but SEALED ----
	followerTB, err := followerReg.GetTenant("tenant-selfmanaged")
	if err != nil {
		t.Fatalf("GetTenant (follower): %v", err)
	}
	if !followerTB.IsSealed() {
		t.Error("follower tenant barrier must remain sealed after self-managed import")
	}
}

// TestHandler_IdempotentDelivery verifies that delivering the same key twice
// returns 200 on both calls without error (the ErrAlreadyInitialized /
// ErrTenantAlreadyExists conditions are treated as idempotent success).
func TestHandler_IdempotentDelivery(t *testing.T) {
	ctx := context.Background()

	leaderReg, leaderAlloc := newTestRegistry(t)
	initTestSystemBarrier(t, leaderReg)
	leaderDataRoot := t.TempDir()
	leaderBarrier := initLeaderTenant(t, leaderReg, leaderAlloc, "tenant-idem")

	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	followerDataRoot := t.TempDir()
	p := newTestPlacement("tenant-idem", "db-main", 1, 2, "follower-node-1", followerDataRoot)
	followerReg, followerAlloc := newTestRegistry(t)
	initTestSystemBarrier(t, followerReg)
	sealtest.ReserveVolumeTenant(t, followerAlloc, "tenant-idem")

	_, srv := newTestReceiverFull(t, ca, follower, "follower-node-1", followerReg, &staticPlacementGetter{p: p})

	resolver := &staticURLResolver{raftAddr: "follower-node-1:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	distPlacement := &placement.Placement{
		TenantID:        "tenant-idem",
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

	// First delivery: should succeed.
	if err := sender.DistributeTenantKey(ctx, distPlacement, leaderBarrier, "leader-node-1"); err != nil {
		t.Fatalf("first DistributeTenantKey: %v", err)
	}
	// Second delivery: idempotent; must also succeed.
	if err := sender.DistributeTenantKey(ctx, distPlacement, leaderBarrier, "leader-node-1"); err != nil {
		t.Fatalf("second DistributeTenantKey (idempotent): %v", err)
	}
}

// TestHandler_NoPeerCert_DowngradeWarning verifies that when no TLS peer
// certificate is present the delivery still succeeds (falling back to HPKE
// recipient-binding + placement gate) and that the downgrade warning fires only
// once across repeated requests.
func TestHandler_NoPeerCert_DowngradeWarning(t *testing.T) {
	ctx := context.Background()

	leaderReg, leaderAlloc := newTestRegistry(t)
	initTestSystemBarrier(t, leaderReg)
	leaderDataRoot := t.TempDir()
	leaderBarrier := initLeaderTenant(t, leaderReg, leaderAlloc, "tenant-nodl")

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
	recv, err := clusterkeys.NewReceiver(kd, followerReg, &staticPlacementGetter{p: p},
		"cluster.local", "follower-node-1", nil, nil, follower.pem, newTestLogger())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/cluster/nodekey", recv.NodeKeyHandler())
	mux.HandleFunc("/v1/cluster/keydelivery", recv.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Sender authenticates the delivery cert against the cluster CA before
	// sealing, even though the transport channel is plain HTTP (no mTLS).
	resolver := &staticURLResolver{raftAddr: "follower-node-1:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	distPlacement := &placement.Placement{
		TenantID:        "tenant-nodl",
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

	// Delivery must succeed despite no mTLS.
	if err := sender.DistributeTenantKey(ctx, distPlacement, leaderBarrier, "leader-node-1"); err != nil {
		t.Fatalf("DistributeTenantKey (no-TLS): %v", err)
	}

	// Follower must hold the imported blob, sealed until its owner unseals it.
	followerTB, err := followerReg.GetTenant("tenant-nodl")
	if err != nil {
		t.Fatalf("GetTenant (follower): %v", err)
	}
	if !followerTB.IsSealed() {
		t.Error("follower tenant barrier must remain sealed after delivery")
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
	reg, _ := newTestRegistry(t)
	// Placement lookup returns an error — no entry for this tenant/database.
	placements := &staticPlacementGetter{err: errors.New("no placement entry")}
	recv, err := clusterkeys.NewReceiver(kd, reg, placements, "cluster.local", "follower-node-1",
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
			Mode:         keydelivery.ModeSelfManaged,
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
	reg, _ := newTestRegistry(t)
	placements := &staticPlacementGetter{err: errors.New("should not reach")}
	recv, err := clusterkeys.NewReceiver(kd, reg, placements, "cluster.local", "follower-node-1",
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
			Mode:         keydelivery.ModeSelfManaged,
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
// Sender tests
// ---------------------------------------------------------------------------

// TestSender_FetchNodeKey_NonP256 verifies that the sender returns
// ErrNonP256NodeKey when the nodekey endpoint serves a non-P256 certificate.
// The non-P256 cert carries ExtKeyUsageServerAuth and a matching node SPIFFE
// SAN so it passes the chain and SAN checks before failing the key-type check.
func TestSender_FetchNodeKey_NonP256(t *testing.T) {
	ca := newTestCA(t)
	// non256PEM passes CA chain check (signed by ca) and SAN check
	// (node="follower") before failing at the P-256 key-type assertion.
	non256PEM := newTestNonP256CertPEM(t, ca, "cluster.local", "follower")

	// Serve the P-384 cert from /v1/cluster/nodekey.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/cluster/nodekey" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/x-pem-file")
			_, _ = w.Write(non256PEM)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	resolver := &staticURLResolver{raftAddr: "follower:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	replica := placement.Replica{
		ReplicaID: 2,
		NodeID:    "follower",
		RaftAddr:  "follower:9000",
		DataRoot:  t.TempDir(),
	}
	meta := keydelivery.Meta{
		TenantID:     "tenant-1",
		DatabaseID:   "db-1",
		ShardID:      1,
		ReplicaID:    2,
		TargetNodeID: "follower",
		Mode:         keydelivery.ModeSelfManaged,
	}
	err = sender.Deliver(context.Background(), replica, meta, []byte("some-key-material"))
	if !errors.Is(err, clusterkeys.ErrNonP256NodeKey) {
		t.Errorf("expected ErrNonP256NodeKey, got %v", err)
	}
}

// TestSender_Deliver_ResolveURLFails verifies ErrResolveURL when the resolver
// returns an empty string for the replica's raft address.
func TestSender_Deliver_ResolveURLFails(t *testing.T) {
	ca := newTestCA(t)
	resolver := &staticURLResolver{raftAddr: "other-addr", adminURL: "http://irrelevant"}
	sender, err := clusterkeys.NewSender(resolver, http.DefaultClient, ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	replica := placement.Replica{
		ReplicaID: 2,
		NodeID:    "follower",
		RaftAddr:  "follower:9000", // does not match resolver's raftAddr
	}
	meta := keydelivery.Meta{
		TenantID:     "t",
		DatabaseID:   "db",
		ShardID:      1,
		ReplicaID:    2,
		TargetNodeID: "follower",
		Mode:         keydelivery.ModeSelfManaged,
	}
	err = sender.Deliver(context.Background(), replica, meta, []byte("key"))
	if !errors.Is(err, clusterkeys.ErrResolveURL) {
		t.Errorf("expected ErrResolveURL, got %v", err)
	}
}

// TestDistributeTenantKey_SkipsSelf verifies that the sender skips its own
// node (selfNodeID) and only delivers to the other replica.
func TestDistributeTenantKey_SkipsSelf(t *testing.T) {
	ctx := context.Background()

	// ---- Leader registry ----
	leaderReg, leaderAlloc := newTestRegistry(t)
	initTestSystemBarrier(t, leaderReg)
	leaderDataRoot := t.TempDir()
	leaderBarrier := initLeaderTenant(t, leaderReg, leaderAlloc, "tenant-skip")

	// ---- Follower setup ----
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	followerDataRoot := t.TempDir()
	p := newTestPlacement("tenant-skip", "db-main", 1, 2, "follower-node-1", followerDataRoot)
	followerReg, followerAlloc := newTestRegistry(t)
	initTestSystemBarrier(t, followerReg)
	sealtest.ReserveVolumeTenant(t, followerAlloc, "tenant-skip")

	// Use a counting handler to verify only one POST reaches the server.
	var postCount atomic.Int64
	kd := newTestKDReceiver(t, follower.key, "follower-node-1")
	recv, err := clusterkeys.NewReceiver(kd, followerReg, &staticPlacementGetter{p: p},
		"cluster.local", "follower-node-1", nil, nil, follower.pem, newTestLogger())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/cluster/nodekey", recv.NodeKeyHandler())
	mux.HandleFunc("/v1/cluster/keydelivery", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		postCount.Add(1)
		recv.Handler()(w, r)
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resolver := &staticURLResolver{raftAddr: "follower-node-1:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	// Placement has leader (replicaID=1, nodeID=leader-node-1) and
	// follower (replicaID=2, nodeID=follower-node-1).
	distPlacement := &placement.Placement{
		TenantID:        "tenant-skip",
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
	if err := sender.DistributeTenantKey(ctx, distPlacement, leaderBarrier, "leader-node-1"); err != nil {
		t.Fatalf("DistributeTenantKey: %v", err)
	}

	// Only one POST: the leader skipped itself.
	if n := postCount.Load(); n != 1 {
		t.Errorf("expected 1 POST (leader skipped self), got %d", n)
	}

	// Follower holds the imported blob, sealed until its owner unseals it.
	followerTB, err := followerReg.GetTenant("tenant-skip")
	if err != nil {
		t.Fatalf("GetTenant (follower): %v", err)
	}
	if !followerTB.IsSealed() {
		t.Error("follower tenant barrier must remain sealed after DistributeTenantKey")
	}
}

// TestDistributeTenantKey_DeliversToFollowersConcurrently pins concurrent
// delivery: every follower here answers only once all of them have been
// reached, so serial delivery would stall on the first one until its deadline.
func TestDistributeTenantKey_DeliversToFollowersConcurrently(t *testing.T) {
	leaderReg, leaderAlloc := newTestRegistry(t)
	initTestSystemBarrier(t, leaderReg)
	leaderBarrier := initLeaderTenant(t, leaderReg, leaderAlloc, "tenant-quorum")

	ca := newTestCA(t)
	followers := []string{"follower-1", "follower-2", "follower-3"}
	var arrived atomic.Int64
	allArrived := make(chan struct{})
	resolver := mapURLResolver{}
	p := &placement.Placement{
		TenantID: "tenant-quorum", DatabaseID: "__home", Database: "__home", ShardID: 7, RF: 4, LeaderReplicaID: 1,
		Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "leader", RaftAddr: "leader:9000"}},
	}
	for i, id := range followers {
		srv := newFollowerServer(t, newTestNodeCert(t, ca, "cluster.local", id), func(w http.ResponseWriter, r *http.Request) {
			if arrived.Add(1) == int64(len(followers)) {
				close(allArrived)
			}
			select {
			case <-allArrived:
				w.WriteHeader(http.StatusOK)
			case <-r.Context().Done():
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		})
		addr := id + ":9000"
		resolver[addr] = srv.URL
		p.Replicas = append(p.Replicas, placement.Replica{ReplicaID: uint64(i + 2), NodeID: id, RaftAddr: addr})
	}
	sender, err := clusterkeys.NewSender(resolver, &http.Client{}, ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sender.DistributeTenantKey(ctx, p, leaderBarrier, "leader"); err != nil {
		t.Fatalf("DistributeTenantKey: %v", err)
	}
	if n := arrived.Load(); n != int64(len(followers)) {
		t.Errorf("deliveries = %d, want %d", n, len(followers))
	}
}

// TestDistributeTenantKey_JoinsEveryFollowerError verifies that one follower's
// failure neither stops delivery to the others nor hides their failures: both
// distinct errors come back joined.
func TestDistributeTenantKey_JoinsEveryFollowerError(t *testing.T) {
	leaderReg, leaderAlloc := newTestRegistry(t)
	initTestSystemBarrier(t, leaderReg)
	leaderBarrier := initLeaderTenant(t, leaderReg, leaderAlloc, "tenant-errs")

	ca := newTestCA(t)
	var posts atomic.Int64
	refusing := newFollowerServer(t, newTestNodeCert(t, ca, "cluster.local", "refusing"), func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	accepting := newFollowerServer(t, newTestNodeCert(t, ca, "cluster.local", "accepting"), func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	resolver := mapURLResolver{"refusing:9000": refusing.URL, "accepting:9000": accepting.URL}
	p := &placement.Placement{
		TenantID: "tenant-errs", DatabaseID: "db-main", Database: "db-main", ShardID: 3, RF: 4, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "leader", RaftAddr: "leader:9000"},
			{ReplicaID: 2, NodeID: "refusing", RaftAddr: "refusing:9000"},
			{ReplicaID: 3, NodeID: "unresolvable", RaftAddr: "unresolvable:9000"},
			{ReplicaID: 4, NodeID: "accepting", RaftAddr: "accepting:9000"},
		},
	}
	sender, err := clusterkeys.NewSender(resolver, &http.Client{}, ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = sender.DistributeTenantKey(ctx, p, leaderBarrier, "leader")
	if !errors.Is(err, clusterkeys.ErrDelivery) {
		t.Errorf("want ErrDelivery from the refusing follower, got %v", err)
	}
	if !errors.Is(err, clusterkeys.ErrResolveURL) {
		t.Errorf("want ErrResolveURL from the unresolvable follower, got %v", err)
	}
	if joined, ok := err.(interface{ Unwrap() []error }); !ok || len(joined.Unwrap()) != 2 {
		t.Errorf("want exactly the two failures joined, got %v", err)
	}
	if n := posts.Load(); n != 2 {
		t.Errorf("POSTs = %d, want 2 (both reachable followers attempted)", n)
	}
}

// TestDistributeTenantKey_OwnKeyOnEveryNode_ShipsNothing proves a none or
// passphrase barrier's key is never shipped: every node serving it holds its
// own, so a follower is not even contacted.
func TestDistributeTenantKey_OwnKeyOnEveryNode_ShipsNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		strategy seal.SealingStrategy
		creds    func(t *testing.T) seal.Credentials
	}{
		"none": {seal.NewNoneStrategy(), func(*testing.T) seal.Credentials { return seal.Credentials{} }},
		"passphrase": {seal.NewDeferredSoftwareStrategy(), func(t *testing.T) seal.Credentials {
			creds, err := seal.NewCredentials(context.Background(), []byte("correct horse battery staple"))
			if err != nil {
				t.Fatalf("NewCredentials: %v", err)
			}
			return creds
		}},
	} {
		t.Run(name, func(t *testing.T) {
			b, err := seal.NewStorageBarrier(newTestLogger(), seal.NewMemoryBackend(),
				seal.BarrierConfig{RootKeyPath: "tenant/root-key"}, tc.strategy)
			if err != nil {
				t.Fatalf("NewStorageBarrier: %v", err)
			}
			if err := b.Initialize(context.Background(), tc.creds(t)); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			if got := b.ActiveStrategy(); got != seal.StrategyID(name) {
				t.Fatalf("ActiveStrategy = %q, want %q", got, name)
			}

			ca := newTestCA(t)
			var posts atomic.Int64
			follower := newFollowerServer(t, newTestNodeCert(t, ca, "cluster.local", "follower"), func(w http.ResponseWriter, _ *http.Request) {
				posts.Add(1)
				w.WriteHeader(http.StatusOK)
			})
			p := &placement.Placement{
				TenantID: "tenant-own", DatabaseID: "db-main", Database: "db-main", ShardID: 3, RF: 2, LeaderReplicaID: 1,
				Replicas: []placement.Replica{
					{ReplicaID: 1, NodeID: "leader", RaftAddr: "leader:9000"},
					{ReplicaID: 2, NodeID: "follower", RaftAddr: "follower:9000"},
				},
			}
			sender, err := clusterkeys.NewSender(mapURLResolver{"follower:9000": follower.URL}, &http.Client{}, ca.pool, "cluster.local", nil, newTestLogger())
			if err != nil {
				t.Fatalf("NewSender: %v", err)
			}
			if err := sender.DistributeTenantKey(context.Background(), p, b, "leader"); err != nil {
				t.Fatalf("DistributeTenantKey: %v", err)
			}
			if n := posts.Load(); n != 0 {
				t.Fatalf("POSTs = %d, want 0: a %s barrier's key is never shipped", n, name)
			}
		})
	}
}

// TestDistributeTenantKey_Shamir_ShipsNothing pins that a Shamir tenant key is
// never shipped, whether the leader is unsealed or restarted and still sealed
// (no active strategy): every replica keys itself at its holders' first quorum,
// and an imported blob, which lacks the node's wrapped-key and roster records,
// would leave it neither able to key itself nor to unwrap the import.
func TestDistributeTenantKey_Shamir_ShipsNothing(t *testing.T) {
	ctx := context.Background()
	newShamirBarrier := func(t *testing.T, base seal.StorageBackend) *seal.StorageBarrier {
		t.Helper()
		strategy, err := seal.NewShamirStrategy(2, 3)
		if err != nil {
			t.Fatal(err)
		}
		b, err := seal.NewStorageBarrier(newTestLogger(), base, seal.BarrierConfig{
			RootKeyPath: "tenant/root-key",
			Shamir:      &seal.ShamirConfig{Threshold: 2, TotalShares: 3},
		}, strategy)
		if err != nil {
			t.Fatalf("NewStorageBarrier: %v", err)
		}
		return b
	}
	base := seal.NewMemoryBackend()
	leader := newShamirBarrier(t, base)
	sealtest.NewCustody("alice", "bob", "carol").Install(leader)
	if _, err := leader.InitializeShamir(ctx, ""); err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	restarted := newShamirBarrier(t, base)
	if got := restarted.ActiveStrategy(); got != "" {
		t.Fatalf("restarted ActiveStrategy = %q, want none before an unseal", got)
	}

	for name, b := range map[string]*seal.StorageBarrier{"unsealed": leader, "restarted sealed": restarted} {
		t.Run(name, func(t *testing.T) {
			ca := newTestCA(t)
			var posts atomic.Int64
			follower := newFollowerServer(t, newTestNodeCert(t, ca, "cluster.local", "follower"), func(w http.ResponseWriter, _ *http.Request) {
				posts.Add(1)
				w.WriteHeader(http.StatusOK)
			})
			p := &placement.Placement{
				TenantID: "tenant-shamir", DatabaseID: "db-main", Database: "db-main", ShardID: 3, RF: 2, LeaderReplicaID: 1,
				Replicas: []placement.Replica{
					{ReplicaID: 1, NodeID: "leader", RaftAddr: "leader:9000"},
					{ReplicaID: 2, NodeID: "follower", RaftAddr: "follower:9000"},
				},
			}
			sender, err := clusterkeys.NewSender(mapURLResolver{"follower:9000": follower.URL}, &http.Client{}, ca.pool, "cluster.local", nil, newTestLogger())
			if err != nil {
				t.Fatalf("NewSender: %v", err)
			}
			if err := sender.DistributeTenantKey(ctx, p, b, "leader"); err != nil {
				t.Fatalf("DistributeTenantKey: %v", err)
			}
			if n := posts.Load(); n != 0 {
				t.Fatalf("POSTs = %d, want 0: a Shamir barrier's key is never shipped", n)
			}
		})
	}
}

// TestDistributeTenantKey_Uninitialized_ShipsNothing proves a barrier with no
// key yet -- a tenant's at create, before its admin initializes it -- ships
// nothing and reports no failure, rather than a delivery error per follower.
func TestDistributeTenantKey_Uninitialized_ShipsNothing(t *testing.T) {
	b, err := seal.NewStorageBarrier(newTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "tenant/root-key"}, seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	ca := newTestCA(t)
	var posts atomic.Int64
	follower := newFollowerServer(t, newTestNodeCert(t, ca, "cluster.local", "follower"), func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	p := &placement.Placement{
		TenantID: "tenant-new", DatabaseID: "__home", Database: "__home", ShardID: 4, RF: 2, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "leader", RaftAddr: "leader:9000"},
			{ReplicaID: 2, NodeID: "follower", RaftAddr: "follower:9000"},
		},
	}
	sender, err := clusterkeys.NewSender(mapURLResolver{"follower:9000": follower.URL}, &http.Client{}, ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	if err := sender.DistributeTenantKey(context.Background(), p, b, "leader"); err != nil {
		t.Fatalf("DistributeTenantKey: %v; want nothing to ship", err)
	}
	if n := posts.Load(); n != 0 {
		t.Fatalf("POSTs = %d, want 0 for a barrier with no key", n)
	}
}

// ---------------------------------------------------------------------------
// HIGH-1 attack-path tests: sender recipient-cert authentication
// ---------------------------------------------------------------------------

// TestSender_FetchNodeKey_MITMSelfSigned proves the HIGH-1 fix: a MITM
// attacker substituting its own self-signed (or foreign-CA-signed) delivery
// cert at the /v1/cluster/nodekey endpoint is rejected before the root key is
// sealed to it. Specifically, the Deliver call must return ErrRecipientUntrusted
// AND NO POST to /v1/cluster/keydelivery must occur (the seal cannot happen).
func TestSender_FetchNodeKey_MITMSelfSigned(t *testing.T) {
	ca := newTestCA(t)

	// Attacker's cert: signed by a rogue CA — not in the cluster CA pool.
	rogueCA := newTestCA(t)
	mitm := newTestNodeCert(t, rogueCA, "cluster.local", "follower-node-1")

	var postCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cluster/nodekey":
			// Return the MITM (rogue-CA-signed) cert instead of the real node cert.
			w.Header().Set("Content-Type", "application/x-pem-file")
			_, _ = w.Write(mitm.pem)
		case r.Method == http.MethodPost:
			// This branch must never be reached — the seal must fail first.
			postCount.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	resolver := &staticURLResolver{raftAddr: "follower:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	replica := placement.Replica{ReplicaID: 2, NodeID: "follower-node-1", RaftAddr: "follower:9000"}
	meta := keydelivery.Meta{
		TenantID: "tenant-1", DatabaseID: "db-1", ShardID: 1, ReplicaID: 2,
		TargetNodeID: "follower-node-1", Mode: keydelivery.ModeSelfManaged,
	}
	err = sender.Deliver(context.Background(), replica, meta, []byte("secret-key-material"))
	if !errors.Is(err, clusterkeys.ErrRecipientUntrusted) {
		t.Errorf("expected ErrRecipientUntrusted (MITM cert), got %v", err)
	}
	if n := postCount.Load(); n != 0 {
		t.Errorf("expected 0 POSTs (key must not be sealed to MITM), got %d", n)
	}
}

// TestSender_FetchNodeKey_WrongNodeSAN proves the HIGH-1 fix: a delivery cert
// that is validly CA-signed but carries a node SPIFFE SAN for a different node
// ("evil-node" vs target "follower-node-1") is rejected with
// ErrRecipientNodeMismatch before the root key is sealed.
func TestSender_FetchNodeKey_WrongNodeSAN(t *testing.T) {
	ca := newTestCA(t)
	// CA-signed cert but for "evil-node", not the intended target.
	wrongSAN := newTestNodeCert(t, ca, "cluster.local", "evil-node")

	var postCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cluster/nodekey":
			w.Header().Set("Content-Type", "application/x-pem-file")
			_, _ = w.Write(wrongSAN.pem)
		case r.Method == http.MethodPost:
			postCount.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	resolver := &staticURLResolver{raftAddr: "follower:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	replica := placement.Replica{ReplicaID: 2, NodeID: "follower-node-1", RaftAddr: "follower:9000"}
	meta := keydelivery.Meta{
		TenantID: "t", DatabaseID: "db", ShardID: 1, ReplicaID: 2,
		TargetNodeID: "follower-node-1", Mode: keydelivery.ModeSelfManaged,
	}
	err = sender.Deliver(context.Background(), replica, meta, []byte("key"))
	if !errors.Is(err, clusterkeys.ErrRecipientNodeMismatch) {
		t.Errorf("expected ErrRecipientNodeMismatch (wrong node SAN), got %v", err)
	}
	if n := postCount.Load(); n != 0 {
		t.Errorf("expected 0 POSTs (key must not be sealed to wrong node), got %d", n)
	}
}

// TestSender_FetchNodeKey_NoSAN proves the HIGH-1 fix: a CA-signed delivery
// cert with no URI SAN (and hence no node SPIFFE identity) is rejected with
// ErrRecipientUntrusted (wrapping spiffe.ErrNoNodeSPIFFEID). The sender fails
// closed when it cannot bind the recipient to a concrete node identity.
func TestSender_FetchNodeKey_NoSAN(t *testing.T) {
	ca := newTestCA(t)
	noSAN := newTestNodeCertNoSAN(t, ca)

	var postCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cluster/nodekey":
			w.Header().Set("Content-Type", "application/x-pem-file")
			_, _ = w.Write(noSAN.pem)
		case r.Method == http.MethodPost:
			postCount.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	resolver := &staticURLResolver{raftAddr: "follower:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	replica := placement.Replica{ReplicaID: 2, NodeID: "follower-node-1", RaftAddr: "follower:9000"}
	meta := keydelivery.Meta{
		TenantID: "t", DatabaseID: "db", ShardID: 1, ReplicaID: 2,
		TargetNodeID: "follower-node-1", Mode: keydelivery.ModeSelfManaged,
	}
	err = sender.Deliver(context.Background(), replica, meta, []byte("key"))
	if !errors.Is(err, clusterkeys.ErrRecipientUntrusted) {
		t.Errorf("expected ErrRecipientUntrusted (no SAN), got %v", err)
	}
	if n := postCount.Load(); n != 0 {
		t.Errorf("expected 0 POSTs (key must not be sealed to no-SAN cert), got %d", n)
	}
}

// ---------------------------------------------------------------------------
// Error sentinel tests
// ---------------------------------------------------------------------------

func TestErrorSentinels_AreDistinct(t *testing.T) {
	sentinels := []error{
		clusterkeys.ErrNilReceiver,
		clusterkeys.ErrNilRegistry,
		clusterkeys.ErrNilPlacements,
		clusterkeys.ErrEmptySelfNodeID,
		clusterkeys.ErrEmptyDeliveryCert,
		clusterkeys.ErrNilResolver,
		clusterkeys.ErrNilClient,
		clusterkeys.ErrNilCAPool,
		clusterkeys.ErrEmptyTrustDomain,
		clusterkeys.ErrPlacementGate,
		clusterkeys.ErrMalformedBody,
		clusterkeys.ErrUnknownMode,
		clusterkeys.ErrNonP256NodeKey,
		clusterkeys.ErrDeliveryCert,
		clusterkeys.ErrNodeKeyFetch,
		clusterkeys.ErrDelivery,
		clusterkeys.ErrResolveURL,
		clusterkeys.ErrRecipientUntrusted,
		clusterkeys.ErrRecipientNodeMismatch,
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
	reg, _ := newTestRegistry(t)
	recv, err := clusterkeys.NewReceiver(kd, reg, placements, "cluster.local", "follower-node-1",
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

// newTestNodeCertNoSAN issues a CA-signed P-256 certificate with no URI SANs.
// It carries ExtKeyUsageServerAuth so the sender's fetchNodeKey chain check
// passes before failing at the SAN extraction step (ErrNoNodeSPIFFEID).
func newTestNodeCertNoSAN(t *testing.T, ca *testCABundle) *testNodeBundle {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate no-SAN key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano() + 100),
		Subject:      pkix.Name{CommonName: "no-san-node"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
			x509.ExtKeyUsageServerAuth,
		},
		// No URIs field — no SPIFFE SAN.
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("create no-SAN certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse no-SAN certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return &testNodeBundle{key: key, cert: cert, pem: certPEM}
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
	reg, _ := newTestRegistry(t)
	placements := &staticPlacementGetter{err: errors.New("not reached yet")}
	// nil caPool: verifyCert skips chain check entirely.
	recv, err := clusterkeys.NewReceiver(kd, reg, placements, "cluster.local", "follower-node-1",
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
	reg, _ := newTestRegistry(t)
	placements := &staticPlacementGetter{err: errors.New("not reached")}
	recv, err := clusterkeys.NewReceiver(kd, reg, placements, "cluster.local", "follower-node-1",
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
			Mode:         keydelivery.ModeSelfManaged,
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
	reg, _ := newTestRegistry(t)
	placements := &staticPlacementGetter{err: errors.New("will not reach")}
	recv, err := clusterkeys.NewReceiver(kd, reg, placements, "cluster.local", "follower-node-1",
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

// TestHandler_SelfManaged_InvalidSealedBlob verifies that an HPKE envelope
// that decrypts to bytes that are not a sealed root-key blob is rejected with
// 500. This exercises importSelfManaged's ImportTenant error path and the
// Handler's "key import failed" response branch.
func TestHandler_SelfManaged_InvalidSealedBlob(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	followerDataRoot := t.TempDir()
	p := newTestPlacement("tenant-badjson", "db-main", 1, 2, "follower-node-1", followerDataRoot)

	followerReg, followerAlloc := newTestRegistry(t)
	initTestSystemBarrier(t, followerReg)
	sealtest.ReserveVolumeTenant(t, followerAlloc, "tenant-badjson")

	// Seal non-JSON bytes to the follower's key. Receiver will decrypt
	// them successfully but ImportSealedBlob will refuse them.
	meta := keydelivery.Meta{
		TenantID:     "tenant-badjson",
		DatabaseID:   "db-main",
		ShardID:      1,
		ReplicaID:    2,
		TargetNodeID: "follower-node-1",
		Mode:         keydelivery.ModeSelfManaged,
	}
	env, err := keydelivery.Seal(&follower.key.PublicKey, meta, []byte("not-valid-json"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	// Rebuild the receiver so it has the correct follower registry.
	kd := newTestKDReceiver(t, follower.key, "follower-node-1")
	fullRecv, err := clusterkeys.NewReceiver(kd, followerReg, &staticPlacementGetter{p: p},
		"cluster.local", "follower-node-1", nil, nil, follower.pem, newTestLogger())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	fullRecv.Handler()(rec, req)

	// ImportSealedBlob fails → importSelfManaged returns error → 500
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 (import failed after an invalid blob), got %d", rec.Code)
	}
}

// TestSender_FetchNodeKey_NonOKStatus verifies that a non-200 response from the
// nodekey endpoint returns ErrNodeKeyFetch.
func TestSender_FetchNodeKey_NonOKStatus(t *testing.T) {
	ca := newTestCA(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	resolver := &staticURLResolver{raftAddr: "follower:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	replica := placement.Replica{ReplicaID: 2, NodeID: "follower", RaftAddr: "follower:9000"}
	meta := keydelivery.Meta{
		TenantID:     "t",
		DatabaseID:   "db",
		ShardID:      1,
		ReplicaID:    2,
		TargetNodeID: "follower",
		Mode:         keydelivery.ModeSelfManaged,
	}
	err = sender.Deliver(context.Background(), replica, meta, []byte("key"))
	if !errors.Is(err, clusterkeys.ErrNodeKeyFetch) {
		t.Errorf("expected ErrNodeKeyFetch (non-200 status), got %v", err)
	}
}

// TestSender_FetchNodeKey_InvalidPEM verifies that a nodekey endpoint returning
// non-PEM bytes is rejected with ErrDeliveryCert.
func TestSender_FetchNodeKey_InvalidPEM(t *testing.T) {
	ca := newTestCA(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this is not PEM data"))
	}))
	defer srv.Close()

	resolver := &staticURLResolver{raftAddr: "follower:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	replica := placement.Replica{ReplicaID: 2, NodeID: "follower", RaftAddr: "follower:9000"}
	meta := keydelivery.Meta{
		TenantID:     "t",
		DatabaseID:   "db",
		ShardID:      1,
		ReplicaID:    2,
		TargetNodeID: "follower",
		Mode:         keydelivery.ModeSelfManaged,
	}
	err = sender.Deliver(context.Background(), replica, meta, []byte("key"))
	if !errors.Is(err, clusterkeys.ErrDeliveryCert) {
		t.Errorf("expected ErrDeliveryCert (invalid PEM), got %v", err)
	}
}

// TestSender_FetchNodeKey_BadCertDER verifies that a nodekey endpoint returning
// a PEM block with type CERTIFICATE but invalid DER is rejected with ErrDeliveryCert.
func TestSender_FetchNodeKey_BadCertDER(t *testing.T) {
	ca := newTestCA(t)
	badPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: []byte("not-valid-DER"),
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(badPEM)
	}))
	defer srv.Close()

	resolver := &staticURLResolver{raftAddr: "follower:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	replica := placement.Replica{ReplicaID: 2, NodeID: "follower", RaftAddr: "follower:9000"}
	meta := keydelivery.Meta{
		TenantID:     "t",
		DatabaseID:   "db",
		ShardID:      1,
		ReplicaID:    2,
		TargetNodeID: "follower",
		Mode:         keydelivery.ModeSelfManaged,
	}
	err = sender.Deliver(context.Background(), replica, meta, []byte("key"))
	if !errors.Is(err, clusterkeys.ErrDeliveryCert) {
		t.Errorf("expected ErrDeliveryCert (bad DER), got %v", err)
	}
}

// TestSender_Deliver_PostNonOK verifies that a non-200 response from the
// delivery POST endpoint returns ErrDelivery. The nodekey endpoint serves a
// valid CA-signed P-256 cert with the correct node SAN so fetchNodeKey
// succeeds, ensuring the POST is attempted and the 500 response is exercised.
func TestSender_Deliver_PostNonOK(t *testing.T) {
	ca := newTestCA(t)
	// Cert NodeID matches the replica's NodeID ("follower") so SAN check passes.
	follower := newTestNodeCert(t, ca, "cluster.local", "follower")

	// Server serves the P-256 cert for nodekey but returns 500 for delivery.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cluster/nodekey":
			w.Header().Set("Content-Type", "application/x-pem-file")
			_, _ = w.Write(follower.pem)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	resolver := &staticURLResolver{raftAddr: "follower:9000", adminURL: srv.URL}
	sender, err := clusterkeys.NewSender(resolver, srv.Client(), ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	replica := placement.Replica{ReplicaID: 2, NodeID: "follower", RaftAddr: "follower:9000"}
	meta := keydelivery.Meta{
		TenantID:     "t",
		DatabaseID:   "db",
		ShardID:      1,
		ReplicaID:    2,
		TargetNodeID: "follower",
		Mode:         keydelivery.ModeSelfManaged,
	}
	err = sender.Deliver(context.Background(), replica, meta, []byte("some-key-material"))
	if !errors.Is(err, clusterkeys.ErrDelivery) {
		t.Errorf("expected ErrDelivery (POST non-200), got %v", err)
	}
}

// TestSender_DeliverSelfManaged_UninitializedBarrier verifies that when the
// leader tenant barrier has no root key (ExportSealedBlob fails), the error
// is returned from deliverSelfManaged.
func TestSender_DeliverSelfManaged_UninitializedBarrier(t *testing.T) {
	// Uninitialized barrier: ExportSealedBlob returns ErrNotInitialized.
	uninitBarrier := newTestSystemBarrier(t)

	ca := newTestCA(t)
	resolver := &staticURLResolver{raftAddr: "irrelevant:9000", adminURL: "http://127.0.0.1:0"}
	sender, err := clusterkeys.NewSender(resolver, http.DefaultClient, ca.pool, "cluster.local", nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	distPlacement := &placement.Placement{
		TenantID:        "tenant-uninit",
		DatabaseID:      "db-main",
		Database:        "db-main",
		ShardID:         1,
		RF:              2,
		LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "leader-node-1", RaftAddr: "leader-node-1:9000"},
			{ReplicaID: 2, NodeID: "follower-node-1", RaftAddr: "irrelevant:9000", DataRoot: t.TempDir()},
		},
	}
	// No key yet: nothing is exported or sent, and no failure is reported.
	if err := sender.DistributeTenantKey(context.Background(), distPlacement, uninitBarrier, "leader-node-1"); err != nil {
		t.Fatalf("DistributeTenantKey on an uninitialized barrier: %v; want nothing to ship", err)
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

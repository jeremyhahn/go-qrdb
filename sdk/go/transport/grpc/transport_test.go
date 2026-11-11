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

package grpc

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	grpcserver "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/clientlimit"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock services
// ---------------------------------------------------------------------------

type mockKVService struct {
	store   map[string][]byte
	putErr  error
	getErr  error
	delErr  error
	listErr error
	scanErr error
}

func newMockKVService() *mockKVService {
	return &mockKVService{store: make(map[string][]byte)}
}

func (m *mockKVService) Put(_ context.Context, key string, value []byte) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.store[key] = value
	return nil
}

func (m *mockKVService) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	if m.putErr != nil {
		return false, m.putErr
	}
	if _, exists := m.store[key]; exists {
		return false, nil
	}
	m.store[key] = value
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *mockKVService) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	current, ok := m.store[key]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	delete(m.store, key)
	return true, nil
}

func (m *mockKVService) Get(_ context.Context, key string) ([]byte, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	v, ok := m.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return v, nil
}

func (m *mockKVService) Delete(_ context.Context, key string) error {
	if m.delErr != nil {
		return m.delErr
	}
	delete(m.store, key)
	return nil
}

func (m *mockKVService) List(_ context.Context, prefix string) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	var keys []string
	for k := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *mockKVService) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	if m.scanErr != nil {
		return nil, m.scanErr
	}
	result := make(map[string][]byte)
	for k, v := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return result, nil
}

func (m *mockKVService) PutEntity(ctx context.Context, req *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	key := string(req.EntityType) + "/" + req.ID
	if err := m.Put(ctx, key, req.Data); err != nil {
		return nil, err
	}
	return &service.PutEntityResponse{Version: 1, Revision: 1}, nil
}

func (m *mockKVService) GetEntity(ctx context.Context, req *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	key := string(req.EntityType) + "/" + req.ID
	data, err := m.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return &service.GetEntityResponse{Data: data, Version: 1, Revision: 1}, nil
}

func (m *mockKVService) DeleteEntity(ctx context.Context, req *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	key := string(req.EntityType) + "/" + req.ID
	if err := m.Delete(ctx, key); err != nil {
		return nil, err
	}
	return &service.DeleteEntityResponse{Deleted: true, Revision: 1}, nil
}

func (m *mockKVService) ListEntities(ctx context.Context, req *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	prefix := string(req.EntityType) + "/"
	keys, err := m.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	stripped := make([]string, 0, len(keys))
	for _, k := range keys {
		stripped = append(stripped, strings.TrimPrefix(k, prefix))
	}
	return &service.ListEntitiesResponse{Keys: stripped, TotalCount: int64(len(stripped))}, nil
}

func (m *mockKVService) GetStale(ctx context.Context, key string) ([]byte, error) {
	return m.Get(ctx, key)
}

func (m *mockKVService) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return m.List(ctx, prefix)
}

func (m *mockKVService) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return m.Scan(ctx, prefix)
}
func (m *mockKVService) PutWithMode(ctx context.Context, key string, value []byte, _ writemode.Mode) error {
	return m.Put(ctx, key, value)
}

func (m *mockKVService) DeleteWithMode(ctx context.Context, key string, _ writemode.Mode) error {
	return m.Delete(ctx, key)
}

func (m *mockKVService) GetWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) ([]byte, error) {
	return m.Get(ctx, key)
}

func (m *mockKVService) ListWithConsistency(ctx context.Context, prefix string, _ consistency.ReadOptions) ([]string, error) {
	return m.List(ctx, prefix)
}

func (m *mockKVService) ScanWithConsistency(ctx context.Context, prefix string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return m.Scan(ctx, prefix)
}

func (m *mockKVService) ExistsWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) (bool, error) {
	_, err := m.Get(ctx, key)
	if err != nil {
		return false, nil
	}
	return true, nil
}

type mockAdminService struct {
	// addIdentity records the member identity the last AddMember received.
	addIdentity     atomic.Value
	healthResult    *service.HealthStatus
	statusResult    *service.StatusInfo
	peersResult     *service.PeersInfo
	healthErr       error
	statusErr       error
	peersErr        error
	addMemberErr    error
	removeMemberErr error
	snapshotErr     error
	drainErr        error
	startShardErr   error
	stopShardErr    error
	shardStateErr   error
	topologyErr     error
}

func newMockAdminService() *mockAdminService {
	return &mockAdminService{
		healthResult: &service.HealthStatus{Status: "ok", Leader: true},
		statusResult: &service.StatusInfo{
			NodeID:      "node-1",
			RaftAddress: "127.0.0.1:63001",
			IsLeader:    true,
			LeaderID:    1,
			ShardCount:  1,
			IsDraining:  false,
		},
		peersResult: &service.PeersInfo{
			Shards: []service.ShardPeers{
				{
					ShardID:  1,
					LeaderID: 1,
					Peers: []service.PeerInfo{
						{ReplicaID: 1, Address: "127.0.0.1:63001", IsLeader: true},
						{ReplicaID: 2, Address: "127.0.0.2:63001", IsLeader: false},
					},
				},
			},
		},
	}
}

func (m *mockAdminService) Health(_ context.Context) (*service.HealthStatus, error) {
	if m.healthErr != nil {
		return nil, m.healthErr
	}
	return m.healthResult, nil
}

func (m *mockAdminService) Status(_ context.Context) (*service.StatusInfo, error) {
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	return m.statusResult, nil
}

func (m *mockAdminService) Peers(_ context.Context) (*service.PeersInfo, error) {
	if m.peersErr != nil {
		return nil, m.peersErr
	}
	return m.peersResult, nil
}

func (m *mockAdminService) AddMember(_ context.Context, _, _ uint64, _, identity string) error {
	m.addIdentity.Store(identity)
	return m.addMemberErr
}

func (m *mockAdminService) RemoveMember(_ context.Context, _, _ uint64) error {
	return m.removeMemberErr
}

func (m *mockAdminService) RequestSnapshot(_ context.Context, _ uint64) error {
	return m.snapshotErr
}

func (m *mockAdminService) Drain(_ context.Context) error {
	return m.drainErr
}

func (m *mockAdminService) Capabilities(_ context.Context) (*capabilities.Capabilities, error) {
	return &capabilities.Capabilities{
		KV:          true,
		MVCC:        true,
		Transaction: true,
		Batch:       true,
		Watch:       true,
		Lease:       true,
		Index:       true,
	}, nil
}

func (m *mockAdminService) StartShard(_ context.Context, _ *service.StartShardRequest) error {
	return m.startShardErr
}

func (m *mockAdminService) StopShard(_ context.Context, _ uint64) error {
	return m.stopShardErr
}

func (m *mockAdminService) GetShardState(_ context.Context, shardID uint64) (*service.ShardStateResponse, error) {
	if m.shardStateErr != nil {
		return nil, m.shardStateErr
	}
	return &service.ShardStateResponse{
		ShardID:           shardID,
		ConfigChangeIndex: 1,
		Term:              2,
		LeaderID:          1,
		HasLeader:         true,
	}, nil
}

func (m *mockAdminService) GetTopology(_ context.Context) (*service.TopologyResponse, error) {
	if m.topologyErr != nil {
		return nil, m.topologyErr
	}
	return &service.TopologyResponse{
		NodeID:      "test-node",
		RaftAddress: "127.0.0.1:63001",
		ShardCount:  1,
		Shards: []service.TopologyShardInfo{
			{
				ShardID:   1,
				ReplicaID: 1,
				IsLeader:  true,
				LeaderID:  1,
				Nodes:     map[uint64]string{1: "127.0.0.1:63001"},
			},
		},
	}, nil
}

func (m *mockAdminService) NodeCompaction(_ context.Context) (*compaction.Policy, error) {
	return nil, nil
}

func (m *mockAdminService) SetNodeCompaction(_ context.Context, _ *compaction.Policy) error {
	return nil
}

// Compile-time interface assertions for mock services.
var (
	_ service.KVServicer    = (*mockKVService)(nil)
	_ service.AdminServicer = (*mockAdminService)(nil)
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// startGRPCTestServer starts a real gRPC server on a random port with the
// given mock services and returns the listener address for SDK transports.
func startGRPCTestServer(t *testing.T, kvSvc service.KVServicer, adminSvc service.AdminServicer) (string, func()) {
	t.Helper()

	srv := grpcserver.NewServer(kvSvc, adminSvc, "127.0.0.1:0", grpcserver.WithTLS(sharedGRPCServerTLS))
	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start gRPC server: %v", err)
	}

	return srv.Addr(), func() { srv.Stop() }
}

// connectTransport creates a new gRPC SDK Transport, connects it, and
// returns it along with a cleanup function that closes the transport
// and stops the server.
func connectTransport(t *testing.T, kvSvc service.KVServicer, adminSvc service.AdminServicer) (*Transport, func()) {
	t.Helper()

	addr, stopSrv := startGRPCTestServer(t, kvSvc, adminSvc)

	tr, err := New(transport.WithAddress(addr), sharedGRPCClientTLSOption())
	if err != nil {
		stopSrv()
		t.Fatalf("New: %v", err)
	}

	if connErr := tr.Connect(context.Background()); connErr != nil {
		stopSrv()
		t.Fatalf("Connect: %v", connErr)
	}

	cleanup := func() {
		_ = tr.Close()
		stopSrv()
	}

	return tr, cleanup
}

// ---------------------------------------------------------------------------
// TLS test cert helpers
// ---------------------------------------------------------------------------

// testCertBundle holds paths to temp files containing a self-signed CA cert,
// a server cert/key pair signed by that CA, and a client cert/key pair.
type testCertBundle struct {
	caFile   string
	certFile string
	keyFile  string
	// In-memory TLS config built from the generated certs (server side).
	serverTLSConfig *tls.Config
	// In-memory TLS config built from the generated certs (client side).
	clientTLSConfig *tls.Config
	cleanup         func()
}

// generateTestCerts creates a self-signed CA and a leaf cert signed by it.
// Both the CA PEM and the leaf cert/key are written to temp files. The
// returned cleanup func removes the temp files.
func generateTestCerts(t *testing.T) *testCertBundle {
	t.Helper()

	// --- CA key + self-signed cert ---
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generateTestCerts: CA key: %v", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("generateTestCerts: CA cert: %v", err)
	}

	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("generateTestCerts: parse CA cert: %v", err)
	}

	// --- Leaf key + cert signed by CA ---
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generateTestCerts: leaf key: %v", err)
	}

	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}

	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("generateTestCerts: leaf cert: %v", err)
	}

	// --- Encode to PEM ---
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})

	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatalf("generateTestCerts: marshal leaf key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER})

	// --- Write to temp files ---
	caFile := writeTempFile(t, "ca-*.pem", caPEM)
	certFile := writeTempFile(t, "cert-*.pem", certPEM)
	keyFile := writeTempFile(t, "key-*.pem", keyPEM)

	// --- Build server TLS config (presents leaf cert, trusts CA) ---
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("generateTestCerts: X509KeyPair: %v", err)
	}

	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caPEM)

	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		ClientCAs:    caPool,
		ClientAuth:   tls.NoClientCert,
		MinVersion:   tls.VersionTLS13,
	}

	clientTLS := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS13,
		// The leaf cert has localhost as SAN so ServerName matches.
		ServerName: "localhost",
	}

	cleanup := func() {
		os.Remove(caFile)
		os.Remove(certFile)
		os.Remove(keyFile)
	}

	return &testCertBundle{
		caFile:          caFile,
		certFile:        certFile,
		keyFile:         keyFile,
		serverTLSConfig: serverTLS,
		clientTLSConfig: clientTLS,
		cleanup:         cleanup,
	}
}

// writeTempFile writes data to a new temp file with the given pattern and
// returns the file path.
func writeTempFile(t *testing.T, pattern string, data []byte) string {
	t.Helper()
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		t.Fatalf("writeTempFile(%q): %v", pattern, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		t.Fatalf("writeTempFile(%q) write: %v", pattern, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		t.Fatalf("writeTempFile(%q) close: %v", pattern, err)
	}
	return f.Name()
}

// ---------------------------------------------------------------------------
// Constructor tests
// ---------------------------------------------------------------------------

func TestNew_MissingAddress(t *testing.T) {
	_, err := New()
	if err == nil {
		t.Fatal("expected error for missing address")
	}
	var cfgErr *transport.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected *transport.ConfigError, got %T: %v", err, err)
	}
}

func TestNew_ValidAddress(t *testing.T) {
	tr, err := New(transport.WithAddress("127.0.0.1:9999"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if tr == nil {
		t.Fatal("expected non-nil transport")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle tests
// ---------------------------------------------------------------------------

func TestConnect_HealthyServer(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	if !tr.Healthy(context.Background()) {
		t.Fatal("expected Healthy to return true after Connect")
	}
}

func TestConnect_ClosedTransport(t *testing.T) {
	addr, stopSrv := startGRPCTestServer(t, newMockKVService(), newMockAdminService())
	defer stopSrv()

	tr, err := New(transport.WithAddress(addr))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Close before connect.
	_ = tr.Close()

	err = tr.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error on Connect after Close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestConnect_UnreachableServer(t *testing.T) {
	tr, err := New(transport.WithAddress("127.0.0.1:1"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = tr.Connect(context.Background())
	if err == nil {
		_ = tr.Close()
		t.Fatal("expected error connecting to unreachable server")
	}
	var connErr *transport.ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("expected *transport.ConnectionError, got %T: %v", err, err)
	}
}

// TestConnect_UnauthenticatedIsTheTypedRefusal: a server that refuses the
// caller's credential has answered. Connect returns that refusal, with its
// message, as the RequestError every other RPC returns -- not a connection
// failure that hides why.
// refusedCredentialError is a service error the server answers with
// codes.Unauthenticated, as the auth chain answers a refused certificate.
type refusedCredentialError struct{}

func (refusedCredentialError) Error() string {
	return "client certificate is invalid or unverified"
}

func (refusedCredentialError) ErrorClass() errclass.Class { return errclass.Unauthenticated }

func TestConnect_UnauthenticatedIsTheTypedRefusal(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.healthErr = refusedCredentialError{}
	addr, stopSrv := startGRPCTestServer(t, newMockKVService(), adminSvc)
	defer stopSrv()
	tr, err := New(transport.WithAddress(addr), sharedGRPCClientTLSOption())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tr.Close()

	err = tr.Connect(context.Background())
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != int(codes.Unauthenticated) {
		t.Fatalf("Connect error = %T %v, want a RequestError carrying Unauthenticated", err, err)
	}
	if !strings.Contains(err.Error(), "client certificate is invalid or unverified") {
		t.Errorf("Connect error = %v, want the server's refusal message", err)
	}
	var connErr *transport.ConnectionError
	if errors.As(err, &connErr) {
		t.Errorf("Connect error = %v, want the server's refusal, not a connection failure", err)
	}
}

// TestConnect_RateLimitedIsTheTypedRefusal pins that a node refusing the
// caller's address under the client-address rate limit answers Connect with
// its ResourceExhausted refusal and the wait it named, not "health check
// failed" (go-qrdb#1197).
func TestConnect_RateLimitedIsTheTypedRefusal(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.healthErr = &clientlimit.RateLimitedError{Client: "127.0.0.1", RetryAfter: 30 * time.Second}
	addr, stopSrv := startGRPCTestServer(t, newMockKVService(), adminSvc)
	defer stopSrv()
	tr, err := New(transport.WithAddress(addr), sharedGRPCClientTLSOption())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tr.Close()

	err = tr.Connect(context.Background())
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != int(codes.ResourceExhausted) {
		t.Fatalf("Connect error = %T %v, want a RequestError carrying ResourceExhausted", err, err)
	}
	if reqErr.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", reqErr.RetryAfter)
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("Connect error = %v, want the server's rate-limit refusal", err)
	}
	var connErr *transport.ConnectionError
	if errors.As(err, &connErr) {
		t.Errorf("Connect error = %v, want the server's refusal, not a connection failure", err)
	}
}

// TestConnect_HealthFailureIsAConnectionError: a Health failure that says
// nothing about the caller stays a connection failure.
func TestConnect_HealthFailureIsAConnectionError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.healthErr = errors.New("starting")
	addr, stopSrv := startGRPCTestServer(t, newMockKVService(), adminSvc)
	defer stopSrv()
	tr, err := New(transport.WithAddress(addr), sharedGRPCClientTLSOption())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tr.Close()

	err = tr.Connect(context.Background())
	var connErr *transport.ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("Connect error = %T %v, want a ConnectionError", err, err)
	}
}

func TestClose_Idempotent(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	if err := tr.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second close should succeed without error.
	if err := tr.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestHealthy_AfterClose(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	if tr.Healthy(context.Background()) {
		t.Fatal("expected Healthy=false after Close")
	}
}

func TestConnState_BeforeConnect(t *testing.T) {
	tr, err := New(transport.WithAddress("127.0.0.1:9999"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if state := tr.ConnState(); state != connectivity.Shutdown {
		t.Fatalf("expected Shutdown before connect, got %s", state)
	}
}

// TestConnState_AfterConnect verifies ConnState returns a non-Shutdown state
// once a connection has been established (exercises the non-nil conn branch).
func TestConnState_AfterConnect(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	state := tr.ConnState()
	if state == connectivity.Shutdown {
		t.Fatalf("expected non-Shutdown state after Connect, got %s", state)
	}
}

// ---------------------------------------------------------------------------
// KV operations tests
// ---------------------------------------------------------------------------

func TestPutGetDelete(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	ctx := context.Background()

	// Put
	if err := tr.Put(ctx, "hello", []byte("world")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Get
	val, err := tr.Get(ctx, "hello")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(val) != "world" {
		t.Fatalf("expected value %q, got %q", "world", string(val))
	}

	// Delete
	if err := tr.Delete(ctx, "hello"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Get after delete returns KeyNotFoundError.
	_, err = tr.Get(ctx, "hello")
	if err == nil {
		t.Fatal("expected error on Get after Delete")
	}
	var knfErr *transport.KeyNotFoundError
	if !errors.As(err, &knfErr) {
		t.Fatalf("expected *transport.KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestGet_KeyNotFound(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := tr.Get(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	var knfErr *transport.KeyNotFoundError
	if !errors.As(err, &knfErr) {
		t.Fatalf("expected *transport.KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestList(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	ctx := context.Background()

	// Insert keys with different prefixes.
	for _, key := range []string{"app:1", "app:2", "other:1"} {
		if err := tr.Put(ctx, key, []byte("v")); err != nil {
			t.Fatalf("Put %q: %v", key, err)
		}
	}

	// List all keys.
	keys, err := tr.List(ctx, "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}

	// List with prefix.
	keys, err = tr.List(ctx, "app:")
	if err != nil {
		t.Fatalf("List with prefix: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys with prefix 'app:', got %d", len(keys))
	}
}

func TestList_ServiceError(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.listErr = &service.ReadError{Err: errors.New("read failed")}
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	_, err := tr.List(context.Background(), "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestPut_ServiceError(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.putErr = &service.ProposeError{Err: errors.New("write failed")}
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	err := tr.Put(context.Background(), "key", []byte("val"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestDelete_ServiceError(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.delErr = &service.ProposeError{Err: errors.New("delete failed")}
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	err := tr.Delete(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Admin operations tests
// ---------------------------------------------------------------------------

func TestHealth(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	resp, err := tr.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("expected status %q, got %q", "ok", resp.Status)
	}
	if !resp.IsLeader {
		t.Fatal("expected IsLeader=true")
	}
}

func TestHealth_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()
	// Inject error after connect (Connect performs a health check on startup).
	adminSvc.healthErr = errors.New("health probe failed")

	_, err := tr.Health(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestStatus(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	resp, err := tr.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.NodeID != "node-1" {
		t.Fatalf("expected NodeID %q, got %q", "node-1", resp.NodeID)
	}
	if resp.RaftAddress != "127.0.0.1:63001" {
		t.Fatalf("expected RaftAddress %q, got %q", "127.0.0.1:63001", resp.RaftAddress)
	}
	if !resp.IsLeader {
		t.Fatal("expected IsLeader=true")
	}
	if resp.LeaderID != 1 {
		t.Fatalf("expected LeaderID=1, got %d", resp.LeaderID)
	}
	if resp.ShardCount != 1 {
		t.Fatalf("expected ShardCount=1, got %d", resp.ShardCount)
	}
	if resp.IsDraining {
		t.Fatal("expected IsDraining=false")
	}
}

func TestStatus_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()
	// Inject error after connect so the startup health check succeeds.
	adminSvc.statusErr = errors.New("status unavailable")

	_, err := tr.Status(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestPeers(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	resp, err := tr.Peers(context.Background())
	if err != nil {
		t.Fatalf("Peers: %v", err)
	}
	if len(resp.Shards) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(resp.Shards))
	}
	shard := resp.Shards[0]
	if shard.ShardID != 1 {
		t.Fatalf("expected ShardID=1, got %d", shard.ShardID)
	}
	if shard.LeaderID != 1 {
		t.Fatalf("expected LeaderID=1, got %d", shard.LeaderID)
	}
	if len(shard.Peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(shard.Peers))
	}
}

func TestAddMember(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.AddMember(context.Background(), 1, 3, "127.0.0.3:63001", "")
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
}

func TestAddMember_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.addMemberErr = &service.MembershipError{Op: "add", Err: errors.New("failed")}
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.AddMember(context.Background(), 1, 3, "127.0.0.3:63001", "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestRemoveMember(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.RemoveMember(context.Background(), 1, 2)
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
}

func TestRemoveMember_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.removeMemberErr = &service.MembershipError{Op: "remove", Err: errors.New("failed")}
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.RemoveMember(context.Background(), 1, 2)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestRequestSnapshot(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.RequestSnapshot(context.Background(), 1)
	if err != nil {
		t.Fatalf("RequestSnapshot: %v", err)
	}
}

func TestRequestSnapshot_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.snapshotErr = &service.SnapshotRequestError{Err: errors.New("snapshot failed")}
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.RequestSnapshot(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestDrain(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

func TestDrain_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.drainErr = &service.DrainError{Err: errors.New("drain failed")}
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.Drain(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Request method tests
// ---------------------------------------------------------------------------

func TestRequest_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	err := tr.Request(context.Background(), "/test.Method", nil, nil)
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

// TestRequest_UnknownMethod verifies that Request forwards the call to the
// underlying gRPC conn.Invoke and returns an error for an unknown method.
func TestRequest_UnknownMethod(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	// Invoking a method that does not exist on the server returns a gRPC
	// Unimplemented status error. The important thing is that the non-closed
	// path through Request() is exercised.
	err := tr.Request(context.Background(), "/qrdbv1.NoSuchService/NoSuchMethod", nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown method, got nil")
	}
}

// ---------------------------------------------------------------------------
// buildTLSConfig tests
// ---------------------------------------------------------------------------

// TestBuildTLSConfig_PrebuiltConfig verifies that a pre-built *tls.Config is
// returned immediately without reading any files.
func TestBuildTLSConfig_PrebuiltConfig(t *testing.T) {
	customCfg := &tls.Config{MinVersion: tls.VersionTLS12}

	tr := &Transport{
		config: &transport.Config{
			Address:   "localhost:9999",
			TLSConfig: customCfg,
		},
	}

	got, err := tr.buildTLSConfig()
	if err != nil {
		t.Fatalf("buildTLSConfig: unexpected error: %v", err)
	}
	if got == customCfg {
		t.Fatal("expected a clone of the pre-built TLSConfig, got the caller's pointer")
	}
	if got.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %#x, want %#x", got.MinVersion, tls.VersionTLS12)
	}
}

// TestBuildTLSConfig_WithCAFile verifies that a valid CA PEM file is loaded
// and populates the RootCAs pool.
func TestBuildTLSConfig_WithCAFile(t *testing.T) {
	bundle := generateTestCerts(t)
	defer bundle.cleanup()

	tr := &Transport{
		config: &transport.Config{
			Address:   "localhost:9999",
			TLSCAFile: bundle.caFile,
		},
	}

	got, err := tr.buildTLSConfig()
	if err != nil {
		t.Fatalf("buildTLSConfig: %v", err)
	}
	if got.RootCAs == nil {
		t.Fatal("expected RootCAs to be populated from CA file")
	}
	if got.MinVersion != tls.VersionTLS13 {
		t.Fatalf("expected MinVersion TLS 1.3, got %d", got.MinVersion)
	}
}

// TestBuildTLSConfig_WithCertAndKey verifies that a client certificate and key
// are loaded and carried via GetClientCertificate.
func TestBuildTLSConfig_WithCertAndKey(t *testing.T) {
	bundle := generateTestCerts(t)
	defer bundle.cleanup()

	tr := &Transport{
		config: &transport.Config{
			Address:     "localhost:9999",
			TLSCAFile:   bundle.caFile,
			TLSCertFile: bundle.certFile,
			TLSKeyFile:  bundle.keyFile,
		},
	}

	got, err := tr.buildTLSConfig()
	if err != nil {
		t.Fatalf("buildTLSConfig: %v", err)
	}
	// The static Certificates field stays empty: the client certificate
	// is carried via GetClientCertificate instead, so it is presented on
	// every handshake regardless of the server's advertised
	// AcceptableCAs (transport.BuildTLSConfig's own doc comment).
	if len(got.Certificates) != 0 {
		t.Fatalf("expected the static Certificates field to stay empty, got %d entries", len(got.Certificates))
	}
	if got.GetClientCertificate == nil {
		t.Fatal("expected GetClientCertificate to be set")
	}
	if got.RootCAs == nil {
		t.Fatal("expected RootCAs to be populated")
	}
}

// TestBuildTLSConfig_NonexistentCAFile verifies that a missing CA file
// produces a TLSError wrapping a transport.CAFileReadError.
func TestBuildTLSConfig_NonexistentCAFile(t *testing.T) {
	tr := &Transport{
		config: &transport.Config{
			Address:   "localhost:9999",
			TLSCAFile: "/nonexistent/path/ca.pem",
		},
	}

	_, err := tr.buildTLSConfig()
	if err == nil {
		t.Fatal("expected error for nonexistent CA file, got nil")
	}
	var tlsErr *transport.TLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("expected *transport.TLSError, got %T: %v", err, err)
	}
}

// TestBuildTLSConfig_InvalidCAPEM verifies that a CA file containing invalid
// PEM data produces a TLSError wrapping a transport.CAParseError.
func TestBuildTLSConfig_InvalidCAPEM(t *testing.T) {
	path := writeTempFile(t, "bad-ca-*.pem", []byte("this is not valid PEM data"))
	defer os.Remove(path)

	tr := &Transport{
		config: &transport.Config{
			Address:   "localhost:9999",
			TLSCAFile: path,
		},
	}

	_, err := tr.buildTLSConfig()
	if err == nil {
		t.Fatal("expected error for invalid CA PEM, got nil")
	}
	var tlsErr *transport.TLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("expected *transport.TLSError, got %T: %v", err, err)
	}
}

// TestBuildTLSConfig_InvalidCertKeyPair verifies that mismatched cert/key files
// produce a TLSError wrapping a transport.CertificateLoadError.
func TestBuildTLSConfig_InvalidCertKeyPair(t *testing.T) {
	bundle := generateTestCerts(t)
	defer bundle.cleanup()

	// Generate a second, unrelated key to intentionally mismatch.
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate other key: %v", err)
	}
	otherKeyDER, err := x509.MarshalECPrivateKey(otherKey)
	if err != nil {
		t.Fatalf("marshal other key: %v", err)
	}
	otherKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: otherKeyDER})
	mismatchedKeyFile := writeTempFile(t, "mismatched-key-*.pem", otherKeyPEM)
	defer os.Remove(mismatchedKeyFile)

	tr := &Transport{
		config: &transport.Config{
			Address:     "localhost:9999",
			TLSCAFile:   bundle.caFile,
			TLSCertFile: bundle.certFile,
			TLSKeyFile:  mismatchedKeyFile,
		},
	}

	_, err = tr.buildTLSConfig()
	if err == nil {
		t.Fatal("expected error for mismatched cert/key, got nil")
	}
	var tlsErr *transport.TLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("expected *transport.TLSError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// buildDialOptions tests
// ---------------------------------------------------------------------------

// TestBuildDialOptions_Insecure verifies the insecure (non-TLS) path returns
// options without error.
func TestBuildDialOptions_Insecure(t *testing.T) {
	tr := &Transport{
		config: &transport.Config{
			Address:    "localhost:9999",
			TLSEnabled: false,
		},
	}

	opts, err := tr.buildDialOptions()
	if err != nil {
		t.Fatalf("buildDialOptions (insecure): %v", err)
	}
	if len(opts) == 0 {
		t.Fatal("expected at least one dial option")
	}
}

// TestBuildDialOptions_TLS verifies that TLS dial options are constructed
// without error when a valid pre-built TLSConfig is provided.
func TestBuildDialOptions_TLS(t *testing.T) {
	bundle := generateTestCerts(t)
	defer bundle.cleanup()

	tr := &Transport{
		config: &transport.Config{
			Address:    "localhost:9999",
			TLSEnabled: true,
			TLSConfig:  bundle.clientTLSConfig,
		},
	}

	opts, err := tr.buildDialOptions()
	if err != nil {
		t.Fatalf("buildDialOptions (TLS): %v", err)
	}
	if len(opts) == 0 {
		t.Fatal("expected at least one dial option")
	}
}

// TestBuildDialOptions_TLSError verifies that buildDialOptions propagates
// buildTLSConfig errors (e.g., nonexistent CA file).
func TestBuildDialOptions_TLSError(t *testing.T) {
	tr := &Transport{
		config: &transport.Config{
			Address:    "localhost:9999",
			TLSEnabled: true,
			TLSCAFile:  "/nonexistent/ca.pem",
		},
	}

	_, err := tr.buildDialOptions()
	if err == nil {
		t.Fatal("expected error from buildDialOptions when CA file is missing")
	}
	var tlsErr *transport.TLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("expected *transport.TLSError, got %T: %v", err, err)
	}
}

// TestConnect_WithTLS verifies that a gRPC transport can connect to a TLS
// server using a pre-built client TLSConfig.
func TestConnect_WithTLS(t *testing.T) {
	bundle := generateTestCerts(t)
	defer bundle.cleanup()

	// Start a TLS-enabled gRPC server using the server-side TLS config.
	srv := grpcserver.NewServer(
		newMockKVService(),
		newMockAdminService(),
		"127.0.0.1:0",
		grpcserver.WithTLS(bundle.serverTLSConfig),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("start TLS server: %v", err)
	}
	defer srv.Stop()

	// Connect a client using the matching client-side TLS config. We must set
	// ServerName to "localhost" because the leaf cert has that SAN and the
	// random port address would not match otherwise.
	clientCfg := bundle.clientTLSConfig.Clone()
	clientCfg.ServerName = "localhost"

	tr, err := New(
		transport.WithAddress("localhost:"+portFrom(srv.Addr())),
		transport.WithTLSConfig(clientCfg),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = tr.Close() }()

	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect (TLS): %v", err)
	}

	if !tr.Healthy(context.Background()) {
		t.Fatal("expected Healthy=true after TLS connect")
	}
}

// portFrom extracts the port portion from an "addr:port" string.
func portFrom(addr string) string {
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		return addr
	}
	return addr[idx+1:]
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestDialError_Format(t *testing.T) {
	inner := errors.New("connection refused")
	err := &dialError{Err: inner}
	if !strings.Contains(err.Error(), "dial failed") {
		t.Fatalf("expected 'dial failed' in message, got %q", err.Error())
	}
	if err.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

// ---------------------------------------------------------------------------
// mapGRPCError tests
// ---------------------------------------------------------------------------

// TestMapGRPCError_NotFound verifies that a NotFound gRPC status maps to
// *transport.KeyNotFoundError.
func TestMapGRPCError_NotFound(t *testing.T) {
	grpcErr := status.Error(codes.NotFound, "mykey")
	mapped := mapGRPCError("Get", grpcErr)

	var knfErr *transport.KeyNotFoundError
	if !errors.As(mapped, &knfErr) {
		t.Fatalf("expected *transport.KeyNotFoundError, got %T: %v", mapped, mapped)
	}
	if knfErr.Key != "mykey" {
		t.Fatalf("expected Key=%q, got %q", "mykey", knfErr.Key)
	}
}

// TestMapGRPCError_Unavailable verifies that an Unavailable gRPC status maps
// to *transport.RequestError with the correct status code.
func TestMapGRPCError_Unavailable(t *testing.T) {
	grpcErr := status.Error(codes.Unavailable, "server down")
	mapped := mapGRPCError("Health", grpcErr)

	var reqErr *transport.RequestError
	if !errors.As(mapped, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", mapped, mapped)
	}
	if reqErr.StatusCode != int(codes.Unavailable) {
		t.Fatalf("expected StatusCode=%d, got %d", int(codes.Unavailable), reqErr.StatusCode)
	}
}

// TestMapGRPCError_NonStatusError verifies that a plain (non-gRPC status)
// error maps to *transport.RequestError with no status code.
func TestMapGRPCError_NonStatusError(t *testing.T) {
	plainErr := errors.New("plain network error")
	mapped := mapGRPCError("Put", plainErr)

	var reqErr *transport.RequestError
	if !errors.As(mapped, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", mapped, mapped)
	}
	if reqErr.StatusCode != 0 {
		t.Fatalf("expected StatusCode=0 for non-status error, got %d", reqErr.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// mapGRPCError and stripScheme tests
// ---------------------------------------------------------------------------

func TestStripScheme(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://localhost:9090", "localhost:9090"},
		{"http://localhost:9090", "localhost:9090"},
		{"localhost:9090", "localhost:9090"},
	}
	for _, tt := range tests {
		got := stripScheme(tt.input)
		if got != tt.expected {
			t.Errorf("stripScheme(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// Shard management tests
// ---------------------------------------------------------------------------

func TestStartShard(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.StartShard(context.Background(), &transport.StartShardRequest{
		ShardID:   1,
		ReplicaID: 1,
		Members:   map[uint64]string{1: "127.0.0.1:63001"},
	})
	if err != nil {
		t.Fatalf("StartShard: %v", err)
	}
}

func TestStartShard_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.startShardErr = errors.New("shard start failed")
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.StartShard(context.Background(), &transport.StartShardRequest{
		ShardID:   1,
		ReplicaID: 1,
		Members:   map[uint64]string{1: "127.0.0.1:63001"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestStopShard(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.StopShard(context.Background(), 1)
	if err != nil {
		t.Fatalf("StopShard: %v", err)
	}
}

func TestStopShard_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.stopShardErr = errors.New("shard stop failed")
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.StopShard(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestGetShardState(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	resp, err := tr.GetShardState(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetShardState: %v", err)
	}
	if resp.ShardID != 1 {
		t.Fatalf("expected ShardID=1, got %d", resp.ShardID)
	}
	if resp.ConfigChangeIndex != 1 {
		t.Fatalf("expected ConfigChangeIndex=1, got %d", resp.ConfigChangeIndex)
	}
	if resp.Term != 2 {
		t.Fatalf("expected Term=2, got %d", resp.Term)
	}
	if resp.LeaderID != 1 {
		t.Fatalf("expected LeaderID=1, got %d", resp.LeaderID)
	}
	if !resp.HasLeader {
		t.Fatal("expected HasLeader=true")
	}
}

func TestGetShardState_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.shardStateErr = errors.New("shard state unavailable")
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := tr.GetShardState(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestGetTopology(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	resp, err := tr.GetTopology(context.Background())
	if err != nil {
		t.Fatalf("GetTopology: %v", err)
	}
	if resp.NodeID != "test-node" {
		t.Fatalf("expected NodeID %q, got %q", "test-node", resp.NodeID)
	}
	if resp.RaftAddress != "127.0.0.1:63001" {
		t.Fatalf("expected RaftAddress %q, got %q", "127.0.0.1:63001", resp.RaftAddress)
	}
	if resp.ShardCount != 1 {
		t.Fatalf("expected ShardCount=1, got %d", resp.ShardCount)
	}
	if len(resp.Shards) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(resp.Shards))
	}
	if resp.Shards[0].ShardID != 1 {
		t.Fatalf("expected shard ID=1, got %d", resp.Shards[0].ShardID)
	}
	if !resp.Shards[0].IsLeader {
		t.Fatal("expected shard IsLeader=true")
	}
}

func TestGetTopology_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.topologyErr = errors.New("topology unavailable")
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := tr.GetTopology(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

// CompareAndSwap applies the guard for real, against the same map the other
// verbs use. It returned an unconditional (false, nil) until go-qrdb#121 gave
// the transports this verb, and a canned answer is worse than no double here:
// (false, nil) is precisely what a LOST swap looks like, so a client that
// dropped the request on the floor, or a server handler wired to the wrong
// field, would have produced an identical result and the test could not have
// failed.
func (m *mockKVService) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	current, ok := m.store[key]
	if !ok {
		// An absent key is left absent: a swap is not a create.
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	m.store[key] = next
	return true, nil
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (m *mockKVService) ListAll(a0 context.Context, prefix string) ([]string, error) {
	return m.List(a0, prefix)
}

// TestAddMember_CarriesIdentity pins that the operator-supplied member
// identity reaches the server's admin service over this transport.
func TestAddMember_CarriesIdentity(t *testing.T) {
	admin := newMockAdminService()
	tr, cleanup := connectTransport(t, newMockKVService(), admin)
	defer cleanup()

	identity := "spki:" + strings.Repeat("ab", 32)
	if err := tr.AddMember(context.Background(), 1, 4, "127.0.0.4:63001", identity); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if got, _ := admin.addIdentity.Load().(string); got != identity {
		t.Fatalf("server received identity %q, want %q", got, identity)
	}
}

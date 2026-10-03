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

package rest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

// mockKVService implements service.KVServicer for testing.
type mockKVService struct {
	putFn    func(ctx context.Context, key string, value []byte) error
	getFn    func(ctx context.Context, key string) ([]byte, error)
	deleteFn func(ctx context.Context, key string) error
	listFn   func(ctx context.Context, prefix string) ([]string, error)
	scanFn   func(ctx context.Context, prefix string) (map[string][]byte, error)

	// The guarded write verbs (go-qrdb#126). Injectable like every other
	// method here: the handlers exist to DELEGATE, so a test has to be able to
	// observe the arguments that arrived and choose the answer that goes back.
	// Stubbed constants would let a handler that dropped its request, or sent
	// `next` where `expected` belongs, return the same plausible boolean.
	putIfAbsentFn      func(ctx context.Context, key string, value []byte) (bool, error)
	compareAndDeleteFn func(ctx context.Context, key string, expected []byte) (bool, error)
	compareAndSwapFn   func(ctx context.Context, key string, expected, next []byte) (bool, error)
}

func (m *mockKVService) Put(ctx context.Context, key string, value []byte) error {
	if m.putFn != nil {
		return m.putFn(ctx, key, value)
	}
	return nil
}

// PutIfAbsent reports a successful create unless the test injects otherwise.
// This mock keeps no state, so there is nothing for the condition to consult.
func (m *mockKVService) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if m.putIfAbsentFn != nil {
		return m.putIfAbsentFn(ctx, key, value)
	}
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *mockKVService) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if m.compareAndDeleteFn != nil {
		return m.compareAndDeleteFn(ctx, key, expected)
	}
	return false, nil
}

func (m *mockKVService) Get(ctx context.Context, key string) ([]byte, error) {
	if m.getFn != nil {
		return m.getFn(ctx, key)
	}
	return nil, nil
}

func (m *mockKVService) Delete(ctx context.Context, key string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, key)
	}
	return nil
}

func (m *mockKVService) List(ctx context.Context, prefix string) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx, prefix)
	}
	return nil, nil
}

func (m *mockKVService) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	if m.scanFn != nil {
		return m.scanFn(ctx, prefix)
	}
	return nil, nil
}

func (m *mockKVService) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return nil, nil
}

func (m *mockKVService) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return nil, nil
}

func (m *mockKVService) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return nil, nil
}

func (m *mockKVService) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return nil, nil
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

func (m *mockKVService) PutWithMode(_ context.Context, _ string, _ []byte, _ writemode.Mode) error {
	return nil
}

func (m *mockKVService) DeleteWithMode(_ context.Context, _ string, _ writemode.Mode) error {
	return nil
}

func (m *mockKVService) GetWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]byte, error) {
	return nil, nil
}

func (m *mockKVService) ListWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]string, error) {
	return nil, nil
}

func (m *mockKVService) ScanWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return nil, nil
}

func (m *mockKVService) ExistsWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (bool, error) {
	return false, nil
}

// mockAdminService implements service.AdminServicer for testing.
type mockAdminService struct {
	// addIdentity records the member identity the last AddMember received.
	addIdentity       string
	healthFn          func(ctx context.Context) (*service.HealthStatus, error)
	statusFn          func(ctx context.Context) (*service.StatusInfo, error)
	peersFn           func(ctx context.Context) (*service.PeersInfo, error)
	addMemberFn       func(ctx context.Context, shardID, replicaID uint64, addr string) error
	removeMemberFn    func(ctx context.Context, shardID, replicaID uint64) error
	requestSnapshotFn func(ctx context.Context, shardID uint64) error
	drainFn           func(ctx context.Context) error
	capabilitiesFn    func(ctx context.Context) (*capabilities.Capabilities, error)
	startShardFn      func(ctx context.Context, req *service.StartShardRequest) error
	stopShardFn       func(ctx context.Context, shardID uint64) error
	getShardStateFn   func(ctx context.Context, shardID uint64) (*service.ShardStateResponse, error)
	getTopologyFn     func(ctx context.Context) (*service.TopologyResponse, error)
	mintJoinTokenFn   func(ctx context.Context, nodeID string, ttl time.Duration) (string, time.Time, error)
}

// MintJoinToken implements service.ClusterJoinTokenMinter. A mockAdminService
// with no mintJoinTokenFn behaves exactly like the real AdminService with no
// join-token store configured (*service.ClusterJoinTokenNotConfiguredError),
// so existing tests built against newDefaultAdminSvc see no behavior change
// from this mock additionally satisfying the interface.
func (m *mockAdminService) MintJoinToken(ctx context.Context, nodeID string, ttl time.Duration) (string, time.Time, error) {
	if m.mintJoinTokenFn != nil {
		return m.mintJoinTokenFn(ctx, nodeID, ttl)
	}
	return "", time.Time{}, &service.ClusterJoinTokenNotConfiguredError{}
}

func (m *mockAdminService) Health(ctx context.Context) (*service.HealthStatus, error) {
	if m.healthFn != nil {
		return m.healthFn(ctx)
	}
	return &service.HealthStatus{Status: "ok", Leader: true}, nil
}

func (m *mockAdminService) Status(ctx context.Context) (*service.StatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.StatusInfo{
		NodeID:      "test-node",
		RaftAddress: "127.0.0.1:63001",
		IsLeader:    true,
		LeaderID:    1,
		ShardCount:  1,
	}, nil
}

func (m *mockAdminService) Peers(ctx context.Context) (*service.PeersInfo, error) {
	if m.peersFn != nil {
		return m.peersFn(ctx)
	}
	return &service.PeersInfo{
		Shards: []service.ShardPeers{
			{
				ShardID:  1,
				LeaderID: 1,
				Peers: []service.PeerInfo{
					{ReplicaID: 1, Address: "127.0.0.1:63001", IsLeader: true},
				},
			},
		},
	}, nil
}

func (m *mockAdminService) AddMember(ctx context.Context, shardID, replicaID uint64, addr, identity string) error {
	m.addIdentity = identity
	if m.addMemberFn != nil {
		return m.addMemberFn(ctx, shardID, replicaID, addr)
	}
	return nil
}

func (m *mockAdminService) RemoveMember(ctx context.Context, shardID, replicaID uint64) error {
	if m.removeMemberFn != nil {
		return m.removeMemberFn(ctx, shardID, replicaID)
	}
	return nil
}

func (m *mockAdminService) RequestSnapshot(ctx context.Context, shardID uint64) error {
	if m.requestSnapshotFn != nil {
		return m.requestSnapshotFn(ctx, shardID)
	}
	return nil
}

func (m *mockAdminService) Drain(ctx context.Context) error {
	if m.drainFn != nil {
		return m.drainFn(ctx)
	}
	return nil
}

func (m *mockAdminService) Capabilities(ctx context.Context) (*capabilities.Capabilities, error) {
	if m.capabilitiesFn != nil {
		return m.capabilitiesFn(ctx)
	}
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

func (m *mockAdminService) StartShard(ctx context.Context, req *service.StartShardRequest) error {
	if m.startShardFn != nil {
		return m.startShardFn(ctx, req)
	}
	return nil
}

func (m *mockAdminService) StopShard(ctx context.Context, shardID uint64) error {
	if m.stopShardFn != nil {
		return m.stopShardFn(ctx, shardID)
	}
	return nil
}

func (m *mockAdminService) GetShardState(ctx context.Context, shardID uint64) (*service.ShardStateResponse, error) {
	if m.getShardStateFn != nil {
		return m.getShardStateFn(ctx, shardID)
	}
	return &service.ShardStateResponse{ShardID: shardID}, nil
}

func (m *mockAdminService) GetTopology(ctx context.Context) (*service.TopologyResponse, error) {
	if m.getTopologyFn != nil {
		return m.getTopologyFn(ctx)
	}
	return &service.TopologyResponse{}, nil
}

func (m *mockAdminService) NodeCompaction(_ context.Context) (*compaction.Policy, error) {
	return nil, nil
}

func (m *mockAdminService) SetNodeCompaction(_ context.Context, _ *compaction.Policy) error {
	return nil
}

// mockHost implements the Host interface for testing.
type mockHost struct {
	addr string
}

func (m *mockHost) RaftAddress() string { return m.addr }

// ---------------------------------------------------------------------------
// Server lifecycle tests
// ---------------------------------------------------------------------------

func TestServer_StartAndStop(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	tlsCfg, _ := generateTestTLSConfig(t)
	srv := NewServer(kvSvc, adminSvc, dualStackAddr(t), WithTLS(tlsCfg))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}

	addr := srv.Addr()
	if addr == "" {
		t.Fatal("Addr() returned empty string after Start()")
	}
	if addr == "127.0.0.1:0" {
		t.Fatal("Addr() returned unbound :0 address; expected resolved port")
	}

	srv.Stop()
}

func TestServer_AddrBeforeStart(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	srv := NewServer(kvSvc, adminSvc, "127.0.0.1:9999")

	// Before Start, Addr() should return the configured address.
	got := srv.Addr()
	if got != "127.0.0.1:9999" {
		t.Fatalf("Addr() before Start() = %q, want %q", got, "127.0.0.1:9999")
	}
}

func TestServer_StartError_InvalidAddress(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	tlsCfg, _ := generateTestTLSConfig(t)
	srv := NewServer(kvSvc, adminSvc, "invalid-addr-no-port", WithTLS(tlsCfg))

	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("Start() returned nil error for invalid address")
	}

	var startErr *StartError
	if !errors.As(err, &startErr) {
		t.Fatalf("Start() error type = %T, want *StartError", err)
	}
	if startErr.Addr != "invalid-addr-no-port" {
		t.Fatalf("StartError.Addr = %q, want %q", startErr.Addr, "invalid-addr-no-port")
	}
}

func TestServer_StopNilServer(t *testing.T) {
	// Stop on a zero-value server should not panic.
	s := &Server{}
	s.Stop()
}

// TestServer_Start_NoTLS_FailsClosed verifies the REST transport is TLS-only:
// Start without a WithTLS config returns *TLSNotConfiguredError rather than
// binding a plaintext listener.
func TestServer_Start_NoTLS_FailsClosed(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("Start() with no TLS returned nil; want *TLSNotConfiguredError")
	}
	var tlsErr *TLSNotConfiguredError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("Start() error type = %T, want *TLSNotConfiguredError", err)
	}
}

// TestServer_Start_WeakTLS_Rejected verifies the REST transport enforces the
// TLS 1.3 floor: an explicitly weaker MinVersion is rejected at Start rather
// than silently negotiating a weaker handshake.
func TestServer_Start_WeakTLS_Rejected(t *testing.T) {
	cfg, _ := generateTestTLSConfig(t)
	cfg.MinVersion = tls.VersionTLS12
	srv := NewServer(&mockKVService{}, &mockAdminService{}, dualStackAddr(t), WithTLS(cfg))
	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("Start() with TLS 1.2 MinVersion returned nil; want *WeakTLSVersionError")
	}
	var weak *WeakTLSVersionError
	if !errors.As(err, &weak) {
		t.Fatalf("Start() error type = %T, want *WeakTLSVersionError", err)
	}
}

// TestWithTLS_UnsetMinVersion_RaisedToTLS13 verifies WithTLS raises an unset
// MinVersion to TLS 1.3 rather than leaving it at the crypto/tls default of
// TLS 1.2.
func TestWithTLS_UnsetMinVersion_RaisedToTLS13(t *testing.T) {
	cfg, _ := generateTestTLSConfig(t)
	cfg.MinVersion = 0
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithTLS(cfg))
	if srv.tlsConfig == nil {
		t.Fatal("tlsConfig nil after WithTLS with valid config")
	}
	if srv.tlsConfig.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %x, want TLS 1.3 (%x)", srv.tlsConfig.MinVersion, tls.VersionTLS13)
	}
}

// TestWithTLS_MinVersionFloorSurvivesDynamicTrustPoolHook is the TLS 1.2
// downgrade proof (go-qrdb#560 review, B2-3): cfg already carries a
// GetConfigForClient hook -- standing in for pkg/clienttrust.Pool.Wire,
// shared across every client-facing listener to trust every tenant's Key B
// CA -- whose returned config was cloned from a TLS-1.2-floor snapshot
// taken before this listener's TLS 1.3 floor was ever applied. A bare
// clone-and-mutate (the pre-fix WithTLS) loses the floor on exactly the
// connections that take this dynamic path; WithTLS must re-assert it.
func TestWithTLS_MinVersionFloorSurvivesDynamicTrustPoolHook(t *testing.T) {
	cfg, _ := generateTestTLSConfig(t)
	keyBPool := &tls.Config{MinVersion: tls.VersionTLS12}
	cfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return keyBPool, nil
	}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithTLS(cfg))
	if srv.tlsConfig == nil || srv.tlsConfig.GetConfigForClient == nil {
		t.Fatal("expected the dynamic hook to be preserved, not dropped")
	}
	got, err := srv.tlsConfig.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetConfigForClient: %v", err)
	}
	if got.MinVersion != tls.VersionTLS13 {
		t.Fatalf("the config returned by the dynamic hook was NOT reasserted: MinVersion = %#x, want TLS 1.3", got.MinVersion)
	}
}

// ---------------------------------------------------------------------------
// Handler tests: health, status, peers
// ---------------------------------------------------------------------------

func TestHandler_Health_OK(t *testing.T) {
	adminSvc := &mockAdminService{
		healthFn: func(_ context.Context) (*service.HealthStatus, error) {
			return &service.HealthStatus{Status: "ok", Leader: true}, nil
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("health status = %q, want %q", resp.Status, "ok")
	}
	if !resp.IsLeader {
		t.Fatal("health is_leader = false, want true")
	}
}

func TestHandler_Health_Degraded(t *testing.T) {
	adminSvc := &mockAdminService{
		healthFn: func(_ context.Context) (*service.HealthStatus, error) {
			return &service.HealthStatus{Status: "degraded", Leader: false}, nil
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /health (degraded) status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestHandler_Health_ServiceError(t *testing.T) {
	adminSvc := &mockAdminService{
		healthFn: func(_ context.Context) (*service.HealthStatus, error) {
			return nil, &service.DrainError{Err: errors.New("test error")}
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /health (error) status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestHandler_Status_OK(t *testing.T) {
	adminSvc := &mockAdminService{
		statusFn: func(_ context.Context) (*service.StatusInfo, error) {
			return &service.StatusInfo{
				NodeID:      "node-1",
				RaftAddress: "127.0.0.1:63001",
				IsLeader:    true,
				LeaderID:    1,
				ShardCount:  2,
				IsDraining:  false,
			}, nil
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /status status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp statusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if resp.NodeID != "node-1" {
		t.Fatalf("status node_id = %q, want %q", resp.NodeID, "node-1")
	}
	if resp.ShardCount != 2 {
		t.Fatalf("status shard_count = %d, want %d", resp.ShardCount, 2)
	}
}

func TestHandler_Status_ServiceError(t *testing.T) {
	adminSvc := &mockAdminService{
		statusFn: func(_ context.Context) (*service.StatusInfo, error) {
			return nil, errors.New("internal failure")
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /status (error) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandler_Peers_OK(t *testing.T) {
	adminSvc := &mockAdminService{
		peersFn: func(_ context.Context) (*service.PeersInfo, error) {
			return &service.PeersInfo{
				Shards: []service.ShardPeers{
					{
						ShardID:  1,
						LeaderID: 2,
						Peers: []service.PeerInfo{
							{ReplicaID: 1, Address: "10.0.0.1:63001", IsLeader: false},
							{ReplicaID: 2, Address: "10.0.0.2:63001", IsLeader: true},
						},
					},
				},
			}, nil
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/peers", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /peers status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp peersResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode peers response: %v", err)
	}
	if len(resp.Shards) != 1 {
		t.Fatalf("peers shard count = %d, want 1", len(resp.Shards))
	}
	if len(resp.Shards[0].Peers) != 2 {
		t.Fatalf("peers[0] peer count = %d, want 2", len(resp.Shards[0].Peers))
	}
}

func TestHandler_Peers_ServiceError(t *testing.T) {
	adminSvc := &mockAdminService{
		peersFn: func(_ context.Context) (*service.PeersInfo, error) {
			return nil, errors.New("peers failure")
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/peers", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /peers (error) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------------------
// KV handler tests
// ---------------------------------------------------------------------------

func TestHandler_KVPut_OK(t *testing.T) {
	var gotKey string
	var gotValue []byte
	kvSvc := &mockKVService{
		putFn: func(_ context.Context, key string, value []byte) error {
			gotKey = key
			gotValue = make([]byte, len(value))
			copy(gotValue, value)
			return nil
		},
	}
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0")

	body := strings.NewReader("hello-value")
	req := httptest.NewRequest(http.MethodPut, "/kv/mykey", body)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT /kv/mykey status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if gotKey != "mykey" {
		t.Fatalf("Put key = %q, want %q", gotKey, "mykey")
	}
	if string(gotValue) != "hello-value" {
		t.Fatalf("Put value = %q, want %q", string(gotValue), "hello-value")
	}
}

func TestHandler_KVPut_ServiceError(t *testing.T) {
	kvSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return errors.New("propose timeout")
		},
	}
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0")

	body := strings.NewReader("val")
	req := httptest.NewRequest(http.MethodPut, "/kv/key1", body)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("PUT /kv/key1 (error) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandler_KVPut_NoKey(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")

	body := strings.NewReader("val")
	req := httptest.NewRequest(http.MethodPut, "/kv/", body)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT /kv/ (no key) status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandler_KVGet_OK(t *testing.T) {
	kvSvc := &mockKVService{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			if key == "mykey" {
				return []byte("my-value"), nil
			}
			return nil, &service.KeyNotFoundError{Key: key}
		},
	}
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /kv/mykey status = %d, want %d", rec.Code, http.StatusOK)
	}

	respBody, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if string(respBody) != "my-value" {
		t.Fatalf("GET /kv/mykey body = %q, want %q", string(respBody), "my-value")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want %q", ct, "application/octet-stream")
	}
}

func TestHandler_KVGet_NotFound(t *testing.T) {
	kvSvc := &mockKVService{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			return nil, &service.KeyNotFoundError{Key: key}
		},
	}
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/kv/missing", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /kv/missing status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandler_KVGet_ServiceError(t *testing.T) {
	kvSvc := &mockKVService{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, errors.New("read failure")
		},
	}
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/kv/key1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /kv/key1 (error) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandler_KVDelete_OK(t *testing.T) {
	var deletedKey string
	kvSvc := &mockKVService{
		deleteFn: func(_ context.Context, key string) error {
			deletedKey = key
			return nil
		},
	}
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodDelete, "/kv/delme", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /kv/delme status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if deletedKey != "delme" {
		t.Fatalf("Delete key = %q, want %q", deletedKey, "delme")
	}
}

func TestHandler_KVDelete_ServiceError(t *testing.T) {
	kvSvc := &mockKVService{
		deleteFn: func(_ context.Context, _ string) error {
			return errors.New("delete failed")
		},
	}
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodDelete, "/kv/key1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("DELETE /kv/key1 (error) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandler_KVDelete_NoKey(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodDelete, "/kv/", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("DELETE /kv/ (no key) status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandler_KVList_OK(t *testing.T) {
	kvSvc := &mockKVService{
		listFn: func(_ context.Context, prefix string) ([]string, error) {
			if prefix == "app:" {
				return []string{"app:config", "app:data"}, nil
			}
			return []string{"alpha", "beta"}, nil
		},
	}
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/kv?prefix=app:", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /kv?prefix=app: status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp kvListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode kv list response: %v", err)
	}
	if len(resp.Keys) != 2 {
		t.Fatalf("kv list key count = %d, want 2", len(resp.Keys))
	}
	if resp.Keys[0] != "app:config" || resp.Keys[1] != "app:data" {
		t.Fatalf("kv list keys = %v, want [app:config, app:data]", resp.Keys)
	}
}

func TestHandler_KVList_NoPrefix(t *testing.T) {
	kvSvc := &mockKVService{
		listFn: func(_ context.Context, prefix string) ([]string, error) {
			if prefix != "" {
				t.Fatalf("expected empty prefix, got %q", prefix)
			}
			return []string{"a", "b"}, nil
		},
	}
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/kv", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /kv status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHandler_KVList_ServiceError(t *testing.T) {
	kvSvc := &mockKVService{
		listFn: func(_ context.Context, _ string) ([]string, error) {
			return nil, errors.New("list failure")
		},
	}
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/kv", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /kv (error) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------------------
// Dispatch: 404 and 405 tests
// ---------------------------------------------------------------------------

func TestHandler_UnknownPath_404(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /unknown status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandler_WrongMethod_405(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /health status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}

	allow := rec.Header().Get("Allow")
	if allow != http.MethodGet {
		t.Fatalf("Allow header = %q, want %q", allow, http.MethodGet)
	}
}

func TestHandler_KV_InvalidMethod_405(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPatch, "/kv/key1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PATCH /kv/key1 status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// ---------------------------------------------------------------------------
// Membership and snapshot handler tests
// ---------------------------------------------------------------------------

func TestHandler_MembershipAdd_OK(t *testing.T) {
	var gotShardID, gotReplicaID uint64
	var gotAddr string
	adminSvc := &mockAdminService{
		addMemberFn: func(_ context.Context, shardID, replicaID uint64, addr string) error {
			gotShardID = shardID
			gotReplicaID = replicaID
			gotAddr = addr
			return nil
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	body := `{"shard_id":1,"replica_id":3,"address":"10.0.0.3:63001"}`
	req := httptest.NewRequest(http.MethodPost, "/membership/add", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /membership/add status = %d, want %d", rec.Code, http.StatusOK)
	}
	if gotShardID != 1 || gotReplicaID != 3 || gotAddr != "10.0.0.3:63001" {
		t.Fatalf("AddMember(%d, %d, %q) unexpected args", gotShardID, gotReplicaID, gotAddr)
	}
}

func TestHandler_MembershipAdd_BadBody(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/membership/add", strings.NewReader("not-json"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /membership/add (bad body) status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandler_MembershipAdd_ServiceError(t *testing.T) {
	adminSvc := &mockAdminService{
		addMemberFn: func(_ context.Context, _, _ uint64, _ string) error {
			return errors.New("add failed")
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	body := `{"shard_id":1,"replica_id":3,"address":"10.0.0.3:63001"}`
	req := httptest.NewRequest(http.MethodPost, "/membership/add", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST /membership/add (error) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// TestHandler_MembershipAdd_ForwardsOnNotLeader verifies that a membership
// add issued to a follower is forwarded to the leader instead of failing
// with a generic 500, mirroring the KV/scale forwarding paths.
func TestHandler_MembershipAdd_ForwardsOnNotLeader(t *testing.T) {
	leaderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/membership/add" {
			t.Fatalf("leader received path %s, want /membership/add", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer leaderSrv.Close()

	adminSvc := &mockAdminService{
		addMemberFn: func(_ context.Context, _, _ uint64, _ string) error {
			return &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "10.0.0.2:63001"}
		},
	}
	reg := NewPeerAdminRegistry("63002")
	reg.Register("10.0.0.2:63001", leaderSrv.URL)
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0", WithLeaderForwarding(reg))

	body := `{"shard_id":1,"replica_id":3,"address":"10.0.0.3:63001"}`
	req := httptest.NewRequest(http.MethodPost, "/membership/add", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /membership/add (not leader, forwarded) status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestHandler_MembershipAdd_NotLeader_NoResolver verifies that, absent
// leader forwarding configuration, a NotLeaderError from AddMember still
// surfaces as a non-panicking error response (not a 500 buried inside an
// opaque MembershipError with no leader routing information lost).
func TestHandler_MembershipAdd_NotLeader_NoResolver(t *testing.T) {
	adminSvc := &mockAdminService{
		addMemberFn: func(_ context.Context, _, _ uint64, _ string) error {
			return &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "10.0.0.2:63001"}
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	body := `{"shard_id":1,"replica_id":3,"address":"10.0.0.3:63001"}`
	req := httptest.NewRequest(http.MethodPost, "/membership/add", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST /membership/add (not leader, no resolver) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandler_MembershipRemove_OK(t *testing.T) {
	adminSvc := &mockAdminService{
		removeMemberFn: func(_ context.Context, shardID, replicaID uint64) error {
			if shardID != 1 || replicaID != 2 {
				t.Fatalf("RemoveMember(%d, %d) unexpected args", shardID, replicaID)
			}
			return nil
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	body := `{"shard_id":1,"replica_id":2}`
	req := httptest.NewRequest(http.MethodPost, "/membership/remove", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /membership/remove status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHandler_MembershipRemove_BadBody(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/membership/remove", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /membership/remove (bad body) status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandler_MembershipRemove_ServiceError(t *testing.T) {
	adminSvc := &mockAdminService{
		removeMemberFn: func(_ context.Context, _, _ uint64) error {
			return errors.New("remove failed")
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	body := `{"shard_id":1,"replica_id":2}`
	req := httptest.NewRequest(http.MethodPost, "/membership/remove", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST /membership/remove (error) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// TestHandler_MembershipRemove_ForwardsOnNotLeader verifies that a
// membership remove issued to a follower is forwarded to the leader instead
// of failing with a generic 500, mirroring the KV/scale forwarding paths.
func TestHandler_MembershipRemove_ForwardsOnNotLeader(t *testing.T) {
	leaderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/membership/remove" {
			t.Fatalf("leader received path %s, want /membership/remove", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer leaderSrv.Close()

	adminSvc := &mockAdminService{
		removeMemberFn: func(_ context.Context, _, _ uint64) error {
			return &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "10.0.0.2:63001"}
		},
	}
	reg := NewPeerAdminRegistry("63002")
	reg.Register("10.0.0.2:63001", leaderSrv.URL)
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0", WithLeaderForwarding(reg))

	body := `{"shard_id":1,"replica_id":2}`
	req := httptest.NewRequest(http.MethodPost, "/membership/remove", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /membership/remove (not leader, forwarded) status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestHandler_MembershipRemove_NotLeader_NoResolver verifies that, absent
// leader forwarding configuration, a NotLeaderError from RemoveMember still
// surfaces as a non-panicking error response.
func TestHandler_MembershipRemove_NotLeader_NoResolver(t *testing.T) {
	adminSvc := &mockAdminService{
		removeMemberFn: func(_ context.Context, _, _ uint64) error {
			return &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "10.0.0.2:63001"}
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	body := `{"shard_id":1,"replica_id":2}`
	req := httptest.NewRequest(http.MethodPost, "/membership/remove", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST /membership/remove (not leader, no resolver) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandler_Snapshot_OK(t *testing.T) {
	adminSvc := &mockAdminService{
		requestSnapshotFn: func(_ context.Context, shardID uint64) error {
			if shardID != 1 {
				t.Fatalf("RequestSnapshot(%d) unexpected arg", shardID)
			}
			return nil
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	body := `{"shard_id":1}`
	req := httptest.NewRequest(http.MethodPost, "/snapshot", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /snapshot status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHandler_Snapshot_BadBody(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/snapshot", strings.NewReader("invalid"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /snapshot (bad body) status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandler_Snapshot_ServiceError(t *testing.T) {
	adminSvc := &mockAdminService{
		requestSnapshotFn: func(_ context.Context, _ uint64) error {
			return errors.New("snapshot failed")
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	body := `{"shard_id":1}`
	req := httptest.NewRequest(http.MethodPost, "/snapshot", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST /snapshot (error) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------------------
// Drain handler tests
// ---------------------------------------------------------------------------

func TestHandler_Drain_OK(t *testing.T) {
	adminSvc := &mockAdminService{
		drainFn: func(_ context.Context) error { return nil },
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/drain", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /drain status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp drainResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode drain response: %v", err)
	}
	if resp.Status != "drained" {
		t.Fatalf("drain status = %q, want %q", resp.Status, "drained")
	}
}

func TestHandler_Drain_Error(t *testing.T) {
	adminSvc := &mockAdminService{
		drainFn: func(_ context.Context) error { return errors.New("drain timeout") },
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/drain", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /drain (error) status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var resp drainResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode drain response: %v", err)
	}
	if resp.Status != "drain_timeout" {
		t.Fatalf("drain error status = %q, want %q", resp.Status, "drain_timeout")
	}
}

// ---------------------------------------------------------------------------
// peerAdminRegistry tests
// ---------------------------------------------------------------------------

func TestPeerAdminRegistry_Register_And_Resolve(t *testing.T) {
	reg := NewPeerAdminRegistry("63002")
	reg.Register("10.0.0.1:63001", "http://10.0.0.1:63002")

	got := reg.ResolveAdminURL("10.0.0.1:63001")
	if got != "http://10.0.0.1:63002" {
		t.Fatalf("ResolveAdminURL(explicit) = %q, want %q", got, "http://10.0.0.1:63002")
	}
}

func TestPeerAdminRegistry_Fallback(t *testing.T) {
	reg := NewPeerAdminRegistry("63002")

	// No explicit registration; should derive from raft address.
	got := reg.ResolveAdminURL("10.0.0.5:63001")
	if got != "http://10.0.0.5:63002" {
		t.Fatalf("ResolveAdminURL(fallback) = %q, want %q", got, "http://10.0.0.5:63002")
	}
}

func TestPeerAdminRegistry_FallbackTLS(t *testing.T) {
	reg := NewPeerAdminRegistryTLS("63002")

	got := reg.ResolveAdminURL("10.0.0.5:63001")
	if got != "https://10.0.0.5:63002" {
		t.Fatalf("ResolveAdminURL(fallback TLS) = %q, want %q", got, "https://10.0.0.5:63002")
	}
}

func TestPeerAdminRegistry_NoFallbackPort(t *testing.T) {
	reg := NewPeerAdminRegistry("")

	got := reg.ResolveAdminURL("10.0.0.5:63001")
	if got != "" {
		t.Fatalf("ResolveAdminURL(no fallback) = %q, want empty", got)
	}
}

func TestPeerAdminRegistry_InvalidRaftAddr(t *testing.T) {
	reg := NewPeerAdminRegistry("63002")

	// No port separator -> SplitHostPort fails -> empty string.
	got := reg.ResolveAdminURL("badaddr")
	if got != "" {
		t.Fatalf("ResolveAdminURL(invalid) = %q, want empty", got)
	}
}

func TestPeerAdminRegistryTLS_Scheme(t *testing.T) {
	reg := NewPeerAdminRegistryTLS("8443")
	if reg.scheme != "https" {
		t.Fatalf("scheme = %q, want %q", reg.scheme, "https")
	}
}

func TestPeerAdminRegistry_Scheme(t *testing.T) {
	reg := NewPeerAdminRegistry("8080")
	if reg.scheme != "http" {
		t.Fatalf("scheme = %q, want %q", reg.scheme, "http")
	}
}

func TestPeerAdminRegistry_ExplicitOverridesFallback(t *testing.T) {
	reg := NewPeerAdminRegistry("63002")

	// Register explicit URL that differs from what fallback would produce.
	reg.Register("10.0.0.1:63001", "http://custom-host:9999")

	got := reg.ResolveAdminURL("10.0.0.1:63001")
	if got != "http://custom-host:9999" {
		t.Fatalf("ResolveAdminURL = %q, want %q", got, "http://custom-host:9999")
	}
}

// ---------------------------------------------------------------------------
// FetchJSON tests (via real HTTP server)
// ---------------------------------------------------------------------------

func TestFetchJSON_OK(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","is_leader":true}`))
	}))
	defer ts.Close()

	var resp healthResponse
	if err := FetchJSON(ts.URL, &resp, nil); err != nil {
		t.Fatalf("FetchJSON() error: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("FetchJSON status = %q, want %q", resp.Status, "ok")
	}
}

func TestFetchJSON_Non200(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	var resp healthResponse
	err := FetchJSON(ts.URL, &resp, nil)
	if err == nil {
		t.Fatal("FetchJSON() returned nil error for 500 response")
	}

	var fetchRespErr *FetchResponseError
	if !errors.As(err, &fetchRespErr) {
		t.Fatalf("error type = %T, want *FetchResponseError", err)
	}
	if fetchRespErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("FetchResponseError.StatusCode = %d, want %d", fetchRespErr.StatusCode, http.StatusInternalServerError)
	}
}

func TestFetchJSON_ConnectionRefused(t *testing.T) {
	var resp healthResponse
	err := FetchJSON("http://127.0.0.1:1", &resp, nil)
	if err == nil {
		t.Fatal("FetchJSON() returned nil error for connection refused")
	}

	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("error type = %T, want *FetchError", err)
	}
}

func TestFetchJSON_InvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer ts.Close()

	var resp healthResponse
	err := FetchJSON(ts.URL, &resp, nil)
	if err == nil {
		t.Fatal("FetchJSON() returned nil error for invalid JSON")
	}

	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("error type = %T, want *FetchError", err)
	}
}

// ---------------------------------------------------------------------------
// Server options tests
// ---------------------------------------------------------------------------

func TestWithGatherer_NilGatherer(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithGatherer(nil))
	if srv.gatherer != nil {
		t.Fatal("expected nil gatherer")
	}

	// Verify /metrics is not registered.
	if _, ok := srv.routes["/metrics"]; ok {
		t.Fatal("/metrics should not be registered when gatherer is nil")
	}
}

func TestWithHost(t *testing.T) {
	host := &mockHost{addr: "10.0.0.1:63001"}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithHost(host))
	if srv.host == nil {
		t.Fatal("host should not be nil after WithHost")
	}
	if srv.host.RaftAddress() != "10.0.0.1:63001" {
		t.Fatalf("host.RaftAddress() = %q, want %q", srv.host.RaftAddress(), "10.0.0.1:63001")
	}
}

func TestWithLeaderForwarding(t *testing.T) {
	reg := NewPeerAdminRegistry("63002")
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithLeaderForwarding(reg))
	if srv.resolver == nil {
		t.Fatal("resolver should not be nil after WithLeaderForwarding")
	}
	if srv.forwardClient == nil {
		t.Fatal("forwardClient should not be nil after WithLeaderForwarding")
	}
}

func TestWithTLS(t *testing.T) {
	// Just verify the option sets the field; no actual TLS needed.
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	if srv.tlsConfig != nil {
		t.Fatal("expected nil tlsConfig by default")
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestStartError(t *testing.T) {
	inner := errors.New("bind failed")
	err := &StartError{Addr: "127.0.0.1:80", Err: inner}
	want := "rest: server failed to start on 127.0.0.1:80: bind failed"
	if err.Error() != want {
		t.Fatalf("StartError.Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("StartError.Unwrap() did not return inner error")
	}
}

func TestReadBodyError(t *testing.T) {
	inner := errors.New("io error")
	err := &ReadBodyError{Err: inner}
	want := "rest: read body failed: io error"
	if err.Error() != want {
		t.Fatalf("ReadBodyError.Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("ReadBodyError.Unwrap() did not return inner error")
	}
}

func TestLeaderForwardError(t *testing.T) {
	inner := errors.New("connection refused")
	err := &LeaderForwardError{LeaderURL: "https://10.0.0.1:63002", Err: inner}
	want := "rest: leader forward failed to https://10.0.0.1:63002: connection refused"
	if err.Error() != want {
		t.Fatalf("LeaderForwardError.Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("LeaderForwardError.Unwrap() did not return inner error")
	}
}

func TestLeaderForwardLoopError(t *testing.T) {
	err := &LeaderForwardLoopError{LeaderURL: "https://10.0.0.1:63002"}
	want := "rest: leader forward loop detected, target https://10.0.0.1:63002"
	if err.Error() != want {
		t.Fatalf("LeaderForwardLoopError.Error() = %q, want %q", err.Error(), want)
	}
}

func TestHTTP3StartError(t *testing.T) {
	inner := errors.New("quic listen failed")
	err := &HTTP3StartError{Addr: "0.0.0.0:443", Err: inner}
	want := "rest: HTTP/3 server failed to start on 0.0.0.0:443: quic listen failed"
	if err.Error() != want {
		t.Fatalf("HTTP3StartError.Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("HTTP3StartError.Unwrap() did not return inner error")
	}
}

func TestFetchError(t *testing.T) {
	inner := errors.New("dial timeout")
	err := &FetchError{URL: "http://localhost:8080/health", Err: inner}
	want := "rest: failed to fetch http://localhost:8080/health: dial timeout"
	if err.Error() != want {
		t.Fatalf("FetchError.Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("FetchError.Unwrap() did not return inner error")
	}
}

func TestFetchResponseError(t *testing.T) {
	err := &FetchResponseError{URL: "http://localhost/status", StatusCode: 503}
	want := "rest: unexpected status 503 for http://localhost/status"
	if err.Error() != want {
		t.Fatalf("FetchResponseError.Error() = %q, want %q", err.Error(), want)
	}
}

func TestProposeError(t *testing.T) {
	inner := errors.New("timeout")
	err := &ProposeError{Err: inner}
	want := "rest: propose failed: timeout"
	if err.Error() != want {
		t.Fatalf("ProposeError.Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("ProposeError.Unwrap() did not return inner error")
	}
}

func TestReadError(t *testing.T) {
	inner := errors.New("not leader")
	err := &ReadError{Err: inner}
	want := "rest: read failed: not leader"
	if err.Error() != want {
		t.Fatalf("ReadError.Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("ReadError.Unwrap() did not return inner error")
	}
}

func TestMembershipError(t *testing.T) {
	inner := errors.New("rejected")
	err := &MembershipError{Op: "add", Err: inner}
	want := "rest: membership add failed: rejected"
	if err.Error() != want {
		t.Fatalf("MembershipError.Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("MembershipError.Unwrap() did not return inner error")
	}
}

func TestSnapshotRequestError(t *testing.T) {
	inner := errors.New("busy")
	err := &SnapshotRequestError{Err: inner}
	want := "rest: snapshot request failed: busy"
	if err.Error() != want {
		t.Fatalf("SnapshotRequestError.Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("SnapshotRequestError.Unwrap() did not return inner error")
	}
}

func TestHttpStatusText(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{200, "200"},
		{404, "404"},
		{503, "503"},
		{100, "100"},
	}
	for _, tt := range tests {
		got := httpStatusText(tt.code)
		if got != tt.want {
			t.Fatalf("httpStatusText(%d) = %q, want %q", tt.code, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Integration-style: actual server start on port 0, HTTP client tests
// ---------------------------------------------------------------------------

func TestServer_Integration_HealthViaHTTP(t *testing.T) {
	adminSvc := &mockAdminService{
		healthFn: func(_ context.Context) (*service.HealthStatus, error) {
			return &service.HealthStatus{Status: "ok", Leader: false}, nil
		},
	}
	tlsCfg, pool := generateTestTLSConfig(t)
	srv := NewServer(&mockKVService{}, adminSvc, dualStackAddr(t), WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer srv.Stop()

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13},
	}}
	url := "https://" + srv.Addr() + "/health"
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET /health error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /health status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var hr healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&hr); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if hr.Status != "ok" {
		t.Fatalf("health status = %q, want %q", hr.Status, "ok")
	}
}

func TestServer_Integration_PutGetDelete(t *testing.T) {
	store := make(map[string][]byte)
	kvSvc := &mockKVService{
		putFn: func(_ context.Context, key string, value []byte) error {
			cp := make([]byte, len(value))
			copy(cp, value)
			store[key] = cp
			return nil
		},
		getFn: func(_ context.Context, key string) ([]byte, error) {
			v, ok := store[key]
			if !ok {
				return nil, &service.KeyNotFoundError{Key: key}
			}
			return v, nil
		},
		deleteFn: func(_ context.Context, key string) error {
			delete(store, key)
			return nil
		},
	}

	tlsCfg, pool := generateTestTLSConfig(t)
	srv := NewServer(kvSvc, &mockAdminService{}, dualStackAddr(t), WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer srv.Stop()

	base := "https://" + srv.Addr()
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13},
	}}

	// PUT
	putReq, _ := http.NewRequest(http.MethodPut, base+"/kv/testkey", strings.NewReader("testval"))
	putResp, err := client.Do(putReq)
	if err != nil {
		t.Fatalf("PUT error: %v", err)
	}
	_ = putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want %d", putResp.StatusCode, http.StatusNoContent)
	}

	// GET
	getResp, err := client.Get(base + "/kv/testkey")
	if err != nil {
		t.Fatalf("GET error: %v", err)
	}
	body, _ := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", getResp.StatusCode, http.StatusOK)
	}
	if string(body) != "testval" {
		t.Fatalf("GET body = %q, want %q", string(body), "testval")
	}

	// DELETE
	delReq, _ := http.NewRequest(http.MethodDelete, base+"/kv/testkey", nil)
	delResp, err := client.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE error: %v", err)
	}
	_ = delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want %d", delResp.StatusCode, http.StatusNoContent)
	}

	// GET after DELETE -> 404
	getResp2, err := client.Get(base + "/kv/testkey")
	if err != nil {
		t.Fatalf("GET after DELETE error: %v", err)
	}
	_ = getResp2.Body.Close()
	if getResp2.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after DELETE status = %d, want %d", getResp2.StatusCode, http.StatusNotFound)
	}
}

// ---------------------------------------------------------------------------
// Leader forwarding tests
// ---------------------------------------------------------------------------

func TestHandler_KVPut_NotLeaderError_NoResolver(t *testing.T) {
	kvSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "10.0.0.2:63001"}
		},
	}
	// No leader forwarding configured.
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0")

	body := strings.NewReader("val")
	req := httptest.NewRequest(http.MethodPut, "/kv/key1", body)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// Without resolver, should return 500 (not forward).
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("PUT /kv/key1 (not leader, no resolver) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandler_KVPut_NotLeaderError_EmptyLeaderAddr(t *testing.T) {
	kvSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return &service.NotLeaderError{ShardID: 1, LeaderID: 0, LeaderAddress: ""}
		},
	}
	reg := NewPeerAdminRegistry("63002")
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0", WithLeaderForwarding(reg))

	body := strings.NewReader("val")
	req := httptest.NewRequest(http.MethodPut, "/kv/key1", body)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// Empty leader address -> cannot forward -> ProposeError.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("PUT /kv/key1 (empty leader) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandler_KVPut_ForwardLoopDetection(t *testing.T) {
	kvSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "10.0.0.2:63001"}
		},
	}
	reg := NewPeerAdminRegistry("63002")
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0", WithLeaderForwarding(reg))

	body := strings.NewReader("val")
	req := httptest.NewRequest(http.MethodPut, "/kv/key1", body)
	// Simulate a request that has already exhausted the bounded hop budget
	// (maxForwardHops); one more forward attempt must be rejected as a loop.
	req.Header.Set(headerForwardHops, strconv.Itoa(maxForwardHops))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusLoopDetected {
		t.Fatalf("PUT /kv/key1 (loop) status = %d, want %d", rec.Code, http.StatusLoopDetected)
	}
}

func TestHandler_KVPut_ForwardBelowMaxHops_NotRejected(t *testing.T) {
	leaderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(headerForwardHops); got != "2" {
			t.Fatalf("leader received hop count %q, want %q", got, "2")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer leaderSrv.Close()

	kvSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "10.0.0.2:63001"}
		},
	}
	reg := NewPeerAdminRegistry("63002")
	reg.Register("10.0.0.2:63001", leaderSrv.URL)
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0", WithLeaderForwarding(reg))

	body := strings.NewReader("val")
	req := httptest.NewRequest(http.MethodPut, "/kv/key1", body)
	// One hop already taken; this forward is the second of maxForwardHops.
	req.Header.Set(headerForwardHops, "1")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT /kv/key1 (below max hops) status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

// TestHandler_KVPut_ForwardToLeader_TwoHops exercises a genuine two-hop
// forwarding chain across three distinct *Server instances: a non-member
// entry node forwards to a guessed shard member that is itself not the
// leader, which in turn forwards to the real leader. This mirrors the
// scenario a rebalance MOVE or any write can hit when the entry node's
// resolver only knows a member address, not the current leader.
func TestHandler_KVPut_ForwardToLeader_TwoHops(t *testing.T) {
	var leaderSawHops string
	leaderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaderSawHops = r.Header.Get(headerForwardHops)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer leaderSrv.Close()

	// Intermediate node: a shard member, but not the leader. Its resolver
	// knows how to reach the real leader.
	memberKVSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return &service.NotLeaderError{ShardID: 1, LeaderID: 3, LeaderAddress: "10.0.0.3:63001"}
		},
	}
	memberReg := NewPeerAdminRegistry("63002")
	memberReg.Register("10.0.0.3:63001", leaderSrv.URL)
	memberSrv := NewServer(memberKVSvc, &mockAdminService{}, "127.0.0.1:0", WithLeaderForwarding(memberReg))
	memberHTTP := httptest.NewServer(http.HandlerFunc(memberSrv.dispatch))
	defer memberHTTP.Close()

	// Entry node: not a member of the shard at all. Its resolver only knows
	// how to reach the guessed member above, not the real leader.
	entryKVSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "10.0.0.2:63001"}
		},
	}
	entryReg := NewPeerAdminRegistry("63002")
	entryReg.Register("10.0.0.2:63001", memberHTTP.URL)
	entrySrv := NewServer(entryKVSvc, &mockAdminService{}, "127.0.0.1:0", WithLeaderForwarding(entryReg))

	body := strings.NewReader("val")
	req := httptest.NewRequest(http.MethodPut, "/kv/key1", body)
	rec := httptest.NewRecorder()
	entrySrv.dispatch(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("two-hop forwarded PUT status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if leaderSawHops != "2" {
		t.Fatalf("leader observed hop count %q, want %q", leaderSawHops, "2")
	}
}

// TestHandler_KVPut_ForwardToLeader_FourthHopRejected drives the same
// non-member -> member -> leader chain as the two-hop test, but starting
// from a request that has already taken maxForwardHops-1 hops, so the
// entry node's forward attempt is itself the hop that exceeds the budget
// and must be rejected before any network call is made.
func TestHandler_KVPut_ForwardToLeader_FourthHopRejected(t *testing.T) {
	kvSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "10.0.0.2:63001"}
		},
	}
	reg := NewPeerAdminRegistry("63002")
	// Deliberately unregistered -- if forwarding were attempted it would
	// have no admin URL to target, but the hop-budget check must reject
	// before resolution is even consulted.
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0", WithLeaderForwarding(reg))

	body := strings.NewReader("val")
	req := httptest.NewRequest(http.MethodPut, "/kv/key1", body)
	req.Header.Set(headerForwardHops, strconv.Itoa(maxForwardHops))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusLoopDetected {
		t.Fatalf("fourth-hop PUT status = %d, want %d", rec.Code, http.StatusLoopDetected)
	}
}

func TestHandler_KVDelete_NotLeaderError_NoResolver(t *testing.T) {
	kvSvc := &mockKVService{
		deleteFn: func(_ context.Context, _ string) error {
			return &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "10.0.0.2:63001"}
		},
	}
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodDelete, "/kv/key1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("DELETE /kv/key1 (not leader, no resolver) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------------------
// writeJSON and writeErrorJSON tests
// ---------------------------------------------------------------------------

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, okResponse{Status: "ok"})

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", ct, "application/json")
	}

	var resp okResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("status = %q, want %q", resp.Status, "ok")
	}
}

func TestWriteErrorJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErrorJSON(rec, http.StatusBadRequest, errors.New("bad input"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", ct, "application/json")
	}

	var resp errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != "bad input" {
		t.Fatalf("error = %q, want %q", resp.Error, "bad input")
	}
}

// ---------------------------------------------------------------------------
// WithTLS option test
// ---------------------------------------------------------------------------

func TestWithTLS_SetsConfig(t *testing.T) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithTLS(tlsCfg))
	if srv.tlsConfig == nil {
		t.Fatal("tlsConfig should not be nil after WithTLS")
	}
	if srv.tlsConfig.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %d, want %d", srv.tlsConfig.MinVersion, tls.VersionTLS13)
	}
}

// ---------------------------------------------------------------------------
// WithLeaderForwarding with TLS creates transport with TLS config
// ---------------------------------------------------------------------------

func TestWithLeaderForwarding_WithTLS(t *testing.T) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	reg := NewPeerAdminRegistry("63002")
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithTLS(tlsCfg), WithLeaderForwarding(reg))
	if srv.forwardClient == nil {
		t.Fatal("forwardClient should not be nil")
	}
	tr, ok := srv.forwardClient.Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport should be *http.Transport")
	}
	if tr.TLSClientConfig == nil {
		t.Fatal("TLSClientConfig should not be nil when TLS is configured")
	}
	if !tr.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 should be true when TLS is configured")
	}
}

// ---------------------------------------------------------------------------
// handleMetrics test (requires prometheus gatherer)
// ---------------------------------------------------------------------------

func TestHandler_Metrics_OK(t *testing.T) {
	reg := prometheus.NewRegistry()
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithGatherer(reg))

	if _, ok := srv.routes["/metrics"]; !ok {
		t.Fatal("/metrics route should be registered when gatherer is provided")
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHandler_Metrics_WrongMethod(t *testing.T) {
	reg := prometheus.NewRegistry()
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithGatherer(reg))

	req := httptest.NewRequest(http.MethodPost, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /metrics status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// ---------------------------------------------------------------------------
// forwardToLeader: successful forward via httptest leader server
// ---------------------------------------------------------------------------

func TestHandler_KVPut_ForwardToLeader_Success(t *testing.T) {
	// Spin up a fake leader server that accepts PUT /kv/{key}.
	leaderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("leader received method %s, want PUT", r.Method)
		}
		if r.URL.Path != "/kv/fwdkey" {
			t.Fatalf("leader received path %s, want /kv/fwdkey", r.URL.Path)
		}
		fwd := r.Header.Get(headerForwardedBy)
		if fwd == "" {
			t.Fatal("leader did not receive X-Quicraft-Forwarded-By header")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer leaderSrv.Close()

	kvSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return &service.NotLeaderError{
				ShardID:       1,
				LeaderID:      2,
				LeaderAddress: "10.0.0.2:63001",
			}
		},
	}

	reg := NewPeerAdminRegistry("63002")
	// Register the fake leader server as the admin URL for the leader address.
	reg.Register("10.0.0.2:63001", leaderSrv.URL)

	host := &mockHost{addr: "10.0.0.1:63001"}
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0",
		WithLeaderForwarding(reg), WithHost(host))

	body := strings.NewReader("fwdval")
	req := httptest.NewRequest(http.MethodPut, "/kv/fwdkey", body)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("forwarded PUT status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestHandler_KVDelete_ForwardToLeader_Success(t *testing.T) {
	leaderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("leader received method %s, want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer leaderSrv.Close()

	kvSvc := &mockKVService{
		deleteFn: func(_ context.Context, _ string) error {
			return &service.NotLeaderError{
				ShardID:       1,
				LeaderID:      2,
				LeaderAddress: "10.0.0.2:63001",
			}
		},
	}

	reg := NewPeerAdminRegistry("63002")
	reg.Register("10.0.0.2:63001", leaderSrv.URL)

	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0",
		WithLeaderForwarding(reg))

	req := httptest.NewRequest(http.MethodDelete, "/kv/delkey", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("forwarded DELETE status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestHandler_KVPut_ForwardToLeader_LeaderError(t *testing.T) {
	// Leader server that always returns 500.
	leaderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"leader busy"}`))
	}))
	defer leaderSrv.Close()

	kvSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return &service.NotLeaderError{
				ShardID:       1,
				LeaderID:      2,
				LeaderAddress: "10.0.0.2:63001",
			}
		},
	}

	reg := NewPeerAdminRegistry("63002")
	reg.Register("10.0.0.2:63001", leaderSrv.URL)

	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0", WithLeaderForwarding(reg))

	body := strings.NewReader("val")
	req := httptest.NewRequest(http.MethodPut, "/kv/key1", body)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// The leader's 500 should be proxied back.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("forwarded PUT (leader error) status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandler_KVPut_ForwardToLeader_UnresolvableAdmin(t *testing.T) {
	kvSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return &service.NotLeaderError{
				ShardID:       1,
				LeaderID:      2,
				LeaderAddress: "10.0.0.2:63001",
			}
		},
	}

	// Registry with no fallback port and no explicit registration.
	reg := NewPeerAdminRegistry("")
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0", WithLeaderForwarding(reg))

	body := strings.NewReader("val")
	req := httptest.NewRequest(http.MethodPut, "/kv/key1", body)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// Cannot resolve admin URL -> falls through to ProposeError -> 500.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("unresolvable admin status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandler_KVPut_ForwardToLeader_ConnectionRefused(t *testing.T) {
	kvSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return &service.NotLeaderError{
				ShardID:       1,
				LeaderID:      2,
				LeaderAddress: "10.0.0.2:63001",
			}
		},
	}

	reg := NewPeerAdminRegistry("63002")
	// Register a URL that will refuse connections.
	reg.Register("10.0.0.2:63001", "http://127.0.0.1:1")

	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0", WithLeaderForwarding(reg))

	body := strings.NewReader("val")
	req := httptest.NewRequest(http.MethodPut, "/kv/key1", body)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// Connection refused -> 502 Bad Gateway.
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("connection refused status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

func TestHandler_ForwardToLeader_WithoutHost(t *testing.T) {
	leaderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fwd := r.Header.Get(headerForwardedBy)
		if fwd != "unknown" {
			t.Fatalf("expected forwarded-by 'unknown' when no host, got %q", fwd)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer leaderSrv.Close()

	kvSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return &service.NotLeaderError{
				ShardID: 1, LeaderID: 2, LeaderAddress: "10.0.0.2:63001",
			}
		},
	}

	reg := NewPeerAdminRegistry("63002")
	reg.Register("10.0.0.2:63001", leaderSrv.URL)

	// No WithHost -- should use "unknown" as forwarded-by.
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0", WithLeaderForwarding(reg))

	body := strings.NewReader("val")
	req := httptest.NewRequest(http.MethodPut, "/kv/key1", body)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("forward without host status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

// ---------------------------------------------------------------------------
// tryForwardWrite: non-NotLeaderError should not attempt forwarding
// ---------------------------------------------------------------------------

func TestTryForwardWrite_NonNotLeaderError(t *testing.T) {
	kvSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return errors.New("generic error")
		},
	}
	reg := NewPeerAdminRegistry("63002")
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0", WithLeaderForwarding(reg))

	body := strings.NewReader("val")
	req := httptest.NewRequest(http.MethodPut, "/kv/key1", body)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// Generic error -> no forwarding -> ProposeError -> 500.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("non-NLE with resolver status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------------------
// Server Start with TLS (self-signed, port 0)
// ---------------------------------------------------------------------------

func TestServer_StartWithTLS(t *testing.T) {
	tlsCfg, caPool := generateTestTLSConfig(t)
	adminSvc := &mockAdminService{
		healthFn: func(_ context.Context) (*service.HealthStatus, error) {
			return &service.HealthStatus{Status: "ok", Leader: false}, nil
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, dualStackAddr(t), WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start() with TLS error: %v", err)
	}
	defer srv.Stop()

	addr := srv.Addr()
	if addr == "" || addr == "127.0.0.1:0" {
		t.Fatal("Addr() did not resolve after TLS Start")
	}

	// Make an HTTPS request verified against the test CA.
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    caPool,
				MinVersion: tls.VersionTLS13,
			},
		},
	}
	resp, err := client.Get("https://" + addr + "/health")
	if err != nil {
		t.Fatalf("HTTPS GET /health error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTPS GET /health status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// Verify Alt-Svc header is present for HTTP/3.
	altSvc := resp.Header.Get("Alt-Svc")
	if altSvc == "" {
		t.Fatal("expected Alt-Svc header for HTTP/3 advertisement")
	}
	if !strings.Contains(altSvc, "h3=") {
		t.Fatalf("Alt-Svc = %q, expected h3= prefix", altSvc)
	}
}

// generateTestTLSConfig creates an ephemeral ECDSA P-256 CA and a leaf
// certificate signed by that CA for tests. The returned CertPool contains the
// CA certificate and can be used by HTTP clients to verify the server without
// InsecureSkipVerify.
func generateTestTLSConfig(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()

	// Generate CA key pair.
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}

	caSerial, err := crand.Int(crand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate CA serial: %v", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber: caSerial,
		Subject: pkix.Name{
			Organization: []string{"go-quicraft-test"},
			CommonName:   "test-ca",
		},
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().Add(1 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	caCertDER, err := x509.CreateCertificate(crand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}

	// Generate leaf key pair.
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}

	leafSerial, err := crand.Int(crand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate leaf serial: %v", err)
	}

	leafTemplate := &x509.Certificate{
		SerialNumber: leafSerial,
		Subject: pkix.Name{
			Organization: []string{"go-quicraft-test"},
			CommonName:   "test-node",
		},
		DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{
			net.IPv4(127, 0, 0, 1),
			net.IPv6loopback,
			net.IPv4zero,
		},
		NotBefore: time.Now().Add(-1 * time.Minute),
		NotAfter:  time.Now().Add(1 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}

	leafCertDER, err := x509.CreateCertificate(crand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}

	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatalf("marshal leaf key: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafCertDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h3", "h2", "http/1.1"},
	}

	return tlsCfg, pool
}

// ---------------------------------------------------------------------------
// CA bootstrap endpoint tests
// ---------------------------------------------------------------------------

func TestHandler_CABootstrap_CertOnly(t *testing.T) {
	certPEM := []byte("-----BEGIN CERTIFICATE-----\nMIIBtest\n-----END CERTIFICATE-----\n")

	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithCABootstrap(certPEM),
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/ca/bootstrap", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/ca/bootstrap status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	if body != string(certPEM) {
		t.Fatalf("body = %q, want cert-only %q", body, string(certPEM))
	}
}

func TestHandler_CABootstrap_NotConfigured(t *testing.T) {
	// Server created without WithCABootstrap -- route should not be
	// registered, resulting in a 404 from dispatch.
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/v1/ca/bootstrap", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /v1/ca/bootstrap (not configured) status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandler_CABootstrap_WrongMethod(t *testing.T) {
	certPEM := []byte("-----BEGIN CERTIFICATE-----\nMIIBtest\n-----END CERTIFICATE-----\n")

	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithCABootstrap(certPEM),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/ca/bootstrap", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /v1/ca/bootstrap status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestWithCABootstrap_SetsFields(t *testing.T) {
	certPEM := []byte("-----BEGIN CERTIFICATE-----\nMIIBtest\n-----END CERTIFICATE-----\n")

	srv := &Server{}
	opt := WithCABootstrap(certPEM)
	opt(srv)

	if srv.caBootstrapSources != 1 || srv.caBootstrap == nil {
		t.Fatalf("sources = %d, want one", srv.caBootstrapSources)
	}
	got, err := srv.caBootstrap(context.Background())
	if err != nil || string(got) != string(certPEM) {
		t.Fatalf("bundle = %q, %v; want %q", got, err, certPEM)
	}
}

// TestWithCABootstrap_NonPEMInputLeavesNothingToServe: bytes that hold no
// CERTIFICATE block reduce to nothing, and the route stays unregistered.
func TestWithCABootstrap_NonPEMInputLeavesNothingToServe(t *testing.T) {
	srv := &Server{}
	WithCABootstrap([]byte("cert-data"))(srv)
	if srv.caBootstrapSources != 0 || srv.caBootstrap != nil {
		t.Fatalf("a non-PEM input configured the bootstrap route (sources = %d)", srv.caBootstrapSources)
	}
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (m *mockKVService) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if m.compareAndSwapFn != nil {
		return m.compareAndSwapFn(ctx, key, expected, next)
	}
	return false, nil
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (m *mockKVService) ListAll(ctx context.Context, prefix string) ([]string, error) {
	return m.List(ctx, prefix)
}

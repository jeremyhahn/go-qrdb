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
	"crypto/tls"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// mockKVService implements service.KVServicer for testing.
type mockKVService struct {
	store   map[string][]byte
	putErr  error
	getErr  error
	delErr  error
	listErr error
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

// PutIfAbsent stores only when the key is absent, matching the real
// engines. The map lookup is the same check they perform.
func (m *mockKVService) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
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
	result := make(map[string][]byte)
	for k, v := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return result, nil
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

func (m *mockKVService) PutWithMode(_ context.Context, key string, value []byte, _ writemode.Mode) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.store[key] = value
	return nil
}

func (m *mockKVService) DeleteWithMode(_ context.Context, key string, _ writemode.Mode) error {
	if m.delErr != nil {
		return m.delErr
	}
	delete(m.store, key)
	return nil
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
	v, err := m.Get(ctx, key)
	if err != nil {
		return false, nil
	}
	return v != nil, nil
}

// mockAdminService implements service.AdminServicer for testing.
type mockAdminService struct {
	healthResult    *service.HealthStatus
	statusResult    *service.StatusInfo
	peersResult     *service.PeersInfo
	capsResult      *capabilities.Capabilities
	healthErr       error
	statusErr       error
	peersErr        error
	addMemberErr    error
	removeMemberErr error
	snapshotErr     error
	drainErr        error
	capsErr         error
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
		capsResult: &capabilities.Capabilities{
			KV:          true,
			MVCC:        true,
			Transaction: true,
			Batch:       true,
			Watch:       true,
			Lease:       true,
			Index:       true,
			TimeSeries:  false,
			Vector:      false,
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

func (m *mockAdminService) AddMember(_ context.Context, _, _ uint64, _ string) error {
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
	if m.capsErr != nil {
		return nil, m.capsErr
	}
	return m.capsResult, nil
}

func (m *mockAdminService) StartShard(_ context.Context, _ *service.StartShardRequest) error {
	return nil
}

func (m *mockAdminService) StopShard(_ context.Context, _ uint64) error {
	return nil
}

func (m *mockAdminService) GetShardState(_ context.Context, shardID uint64) (*service.ShardStateResponse, error) {
	return &service.ShardStateResponse{
		ShardID:           shardID,
		ConfigChangeIndex: 0,
		Term:              0,
		LeaderID:          0,
		HasLeader:         false,
	}, nil
}

func (m *mockAdminService) GetTopology(_ context.Context) (*service.TopologyResponse, error) {
	return &service.TopologyResponse{}, nil
}

// ---------------------------------------------------------------------------
// mapServiceError tests
// ---------------------------------------------------------------------------

func TestMapServiceError_NilError(t *testing.T) {
	got := mapServiceError(nil)
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestMapServiceError_KeyNotFoundError(t *testing.T) {
	err := mapServiceError(&service.KeyNotFoundError{Key: "missing"})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
	if !strings.Contains(st.Message(), "missing") {
		t.Fatalf("expected message to contain key, got %q", st.Message())
	}
}

func TestMapServiceError_NotLeaderError(t *testing.T) {
	err := mapServiceError(&service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "addr"})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %s", st.Code())
	}
}

func TestMapServiceError_ProposeError(t *testing.T) {
	err := mapServiceError(&service.ProposeError{Err: errors.New("propose failed")})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestMapServiceError_ReadError(t *testing.T) {
	err := mapServiceError(&service.ReadError{Err: errors.New("read failed")})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestMapServiceError_MembershipError(t *testing.T) {
	err := mapServiceError(&service.MembershipError{Op: "add", Err: errors.New("membership failed")})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestMapServiceError_SnapshotRequestError(t *testing.T) {
	err := mapServiceError(&service.SnapshotRequestError{Err: errors.New("snapshot failed")})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestMapServiceError_DrainError(t *testing.T) {
	err := mapServiceError(&service.DrainError{Err: errors.New("drain failed")})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %s", st.Code())
	}
}

func TestMapServiceError_UnknownError(t *testing.T) {
	err := mapServiceError(errors.New("something unexpected"))
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
	if !strings.Contains(st.Message(), "something unexpected") {
		t.Fatalf("expected message to contain original error, got %q", st.Message())
	}
}

// ---------------------------------------------------------------------------
// StartError tests
// ---------------------------------------------------------------------------

func TestStartError_Error(t *testing.T) {
	se := &StartError{Addr: ":9999", Err: errors.New("bind failed")}
	msg := se.Error()
	if !strings.Contains(msg, ":9999") {
		t.Fatalf("expected address in error, got %q", msg)
	}
	if !strings.Contains(msg, "bind failed") {
		t.Fatalf("expected underlying error in message, got %q", msg)
	}
}

func TestStartError_Unwrap(t *testing.T) {
	inner := errors.New("underlying")
	se := &StartError{Addr: ":0", Err: inner}
	if se.Unwrap() != inner {
		t.Fatal("Unwrap did not return the underlying error")
	}
}

// ---------------------------------------------------------------------------
// Server lifecycle tests
// ---------------------------------------------------------------------------

func TestServer_StartStop(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	srv := NewServer(kvSvc, adminSvc, "127.0.0.1:0", WithTLS(testServerTLSConfig(t)))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	addr := srv.Addr()
	if addr == "" {
		t.Fatal("Addr returned empty string after Start")
	}
	if addr == "127.0.0.1:0" {
		t.Fatal("Addr returned unresolved :0 address")
	}
}

func TestServer_Addr_BeforeStart(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:12345")
	addr := srv.Addr()
	if addr != "127.0.0.1:12345" {
		t.Fatalf("expected configured address before start, got %q", addr)
	}
}

func TestServer_StartError_InvalidAddress(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "invalid-address-no-port", WithTLS(testServerTLSConfig(t)))
	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected error for invalid address")
	}
	var se *StartError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StartError, got %T: %v", err, err)
	}
}

func TestServer_Stop_NilServer(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0")
	// Stop before Start should not panic.
	srv.Stop()
}

func TestServer_WithTLS(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithTLS(nil))
	// The option is applied but nil TLS means no credentials.
	if srv.tls != nil {
		t.Fatal("expected nil tls when passed nil config")
	}
}

// TestServer_WithTLS_ClonesAndDefaultsMinVersion proves WithTLS clones the
// supplied config (never retaining the caller's pointer) and upgrades an
// unset MinVersion to TLS 1.3 (Codex #8).
func TestServer_WithTLS_ClonesAndDefaultsMinVersion(t *testing.T) {
	cfg := &tls.Config{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithTLS(cfg))
	if srv.tls == nil {
		t.Fatal("expected tls field to be set")
	}
	if srv.tls == cfg {
		t.Fatal("expected WithTLS to clone the config, not retain the caller's pointer")
	}
	if srv.tls.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %#x, want TLS 1.3", srv.tls.MinVersion)
	}
}

// TestServer_WithTLS_RejectsWeakExplicitMinVersion proves an explicit
// MinVersion below TLS 1.3 fails Start with a typed *WeakTLSVersionError
// (Codex #8).
func TestServer_WithTLS_RejectsWeakExplicitMinVersion(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithTLS(cfg))
	err := srv.Start()
	var weakErr *WeakTLSVersionError
	if !errors.As(err, &weakErr) {
		t.Fatalf("expected *WeakTLSVersionError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Full gRPC round-trip tests
// ---------------------------------------------------------------------------

// startTestServer creates a gRPC server on a random port and returns
// a connected client and a cleanup function.
func startTestServer(t *testing.T, kvSvc service.KVServicer, adminSvc service.AdminServicer) (pb.QRDBServiceClient, func()) {
	t.Helper()

	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(kvSvc, adminSvc, "127.0.0.1:0", WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	conn, err := grpc.NewClient(
		srv.Addr(),
		grpc.WithTransportCredentials(testClientCreds(t, tlsCfg)),
	)
	if err != nil {
		srv.Stop()
		t.Fatalf("grpc.NewClient failed: %v", err)
	}

	client := pb.NewQRDBServiceClient(conn)

	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
	}

	return client, cleanup
}

func TestGRPC_PutGetDelete(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	client, cleanup := startTestServer(t, kvSvc, adminSvc)
	defer cleanup()

	ctx := context.Background()

	// Put a key-value pair.
	_, err := client.Put(ctx, &pb.PutRequest{Key: "hello", Value: []byte("world")})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get the key back.
	getResp, err := client.Get(ctx, &pb.GetRequest{Key: "hello"})
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(getResp.GetValue()) != "world" {
		t.Fatalf("expected value 'world', got %q", string(getResp.GetValue()))
	}

	// Delete the key.
	_, err = client.Delete(ctx, &pb.DeleteRequest{Key: "hello"})
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Get should now return NotFound.
	_, err = client.Get(ctx, &pb.GetRequest{Key: "hello"})
	if err == nil {
		t.Fatal("expected error for deleted key, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_List(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	client, cleanup := startTestServer(t, kvSvc, adminSvc)
	defer cleanup()

	ctx := context.Background()

	// Insert several keys.
	for _, key := range []string{"app:1", "app:2", "other:1"} {
		if _, err := client.Put(ctx, &pb.PutRequest{Key: key, Value: []byte("v")}); err != nil {
			t.Fatalf("Put %q failed: %v", key, err)
		}
	}

	// List all keys.
	resp, err := client.List(ctx, &pb.ListRequest{Prefix: ""})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(resp.GetKeys()) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(resp.GetKeys()))
	}

	// List with prefix.
	resp, err = client.List(ctx, &pb.ListRequest{Prefix: "app:"})
	if err != nil {
		t.Fatalf("List with prefix failed: %v", err)
	}
	if len(resp.GetKeys()) != 2 {
		t.Fatalf("expected 2 keys with prefix 'app:', got %d", len(resp.GetKeys()))
	}
}

func TestGRPC_List_Error(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.listErr = &service.ReadError{Err: errors.New("list failed")}
	client, cleanup := startTestServer(t, kvSvc, newMockAdminService())
	defer cleanup()

	_, err := client.List(context.Background(), &pb.ListRequest{Prefix: ""})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_Health(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	client, cleanup := startTestServer(t, kvSvc, adminSvc)
	defer cleanup()

	resp, err := client.Health(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("Health failed: %v", err)
	}
	if resp.GetStatus() != "ok" {
		t.Fatalf("expected status 'ok', got %q", resp.GetStatus())
	}
	if !resp.GetLeader() {
		t.Fatal("expected leader=true")
	}
}

func TestGRPC_Health_Error(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.healthErr = &service.ReadError{Err: errors.New("health check failed")}
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := client.Health(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_Status(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	client, cleanup := startTestServer(t, kvSvc, adminSvc)
	defer cleanup()

	resp, err := client.Status(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if resp.GetNodeId() != "node-1" {
		t.Fatalf("expected node-1, got %q", resp.GetNodeId())
	}
	if resp.GetRaftAddress() != "127.0.0.1:63001" {
		t.Fatalf("expected raft address '127.0.0.1:63001', got %q", resp.GetRaftAddress())
	}
	if !resp.GetIsLeader() {
		t.Fatal("expected IsLeader=true")
	}
	if resp.GetLeaderId() != 1 {
		t.Fatalf("expected LeaderID=1, got %d", resp.GetLeaderId())
	}
	if resp.GetShardCount() != 1 {
		t.Fatalf("expected ShardCount=1, got %d", resp.GetShardCount())
	}
	if resp.GetIsDraining() {
		t.Fatal("expected IsDraining=false")
	}
}

func TestGRPC_Status_Error(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.statusErr = &service.ReadError{Err: errors.New("status failed")}
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := client.Status(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_Peers(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	client, cleanup := startTestServer(t, kvSvc, adminSvc)
	defer cleanup()

	resp, err := client.Peers(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("Peers failed: %v", err)
	}
	if len(resp.GetShards()) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(resp.GetShards()))
	}
	shard := resp.GetShards()[0]
	if shard.GetShardId() != 1 {
		t.Fatalf("expected shard_id=1, got %d", shard.GetShardId())
	}
	if shard.GetLeaderId() != 1 {
		t.Fatalf("expected leader_id=1, got %d", shard.GetLeaderId())
	}
	if len(shard.GetPeers()) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(shard.GetPeers()))
	}
}

func TestGRPC_Peers_Error(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.peersErr = &service.ReadError{Err: errors.New("peers failed")}
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := client.Peers(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_Peers_EmptyShards(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.peersResult = &service.PeersInfo{Shards: nil}
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	resp, err := client.Peers(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("Peers failed: %v", err)
	}
	if len(resp.GetShards()) != 0 {
		t.Fatalf("expected 0 shards, got %d", len(resp.GetShards()))
	}
}

func TestGRPC_AddMember(t *testing.T) {
	adminSvc := newMockAdminService()
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := client.AddMember(context.Background(), &pb.AddMemberRequest{
		ShardId:     1,
		ReplicaId:   3,
		RaftAddress: "127.0.0.3:63001",
	})
	if err != nil {
		t.Fatalf("AddMember failed: %v", err)
	}
}

func TestGRPC_AddMember_Error(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.addMemberErr = &service.MembershipError{Op: "add", Err: errors.New("failed")}
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := client.AddMember(context.Background(), &pb.AddMemberRequest{
		ShardId:     1,
		ReplicaId:   3,
		RaftAddress: "127.0.0.3:63001",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_RemoveMember(t *testing.T) {
	adminSvc := newMockAdminService()
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := client.RemoveMember(context.Background(), &pb.RemoveMemberRequest{
		ShardId:   1,
		ReplicaId: 2,
	})
	if err != nil {
		t.Fatalf("RemoveMember failed: %v", err)
	}
}

func TestGRPC_RemoveMember_Error(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.removeMemberErr = &service.MembershipError{Op: "remove", Err: errors.New("failed")}
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := client.RemoveMember(context.Background(), &pb.RemoveMemberRequest{
		ShardId:   1,
		ReplicaId: 2,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_RequestSnapshot(t *testing.T) {
	adminSvc := newMockAdminService()
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := client.RequestSnapshot(context.Background(), &pb.RequestSnapshotMessage{ShardId: 1})
	if err != nil {
		t.Fatalf("RequestSnapshot failed: %v", err)
	}
}

func TestGRPC_RequestSnapshot_Error(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.snapshotErr = &service.SnapshotRequestError{Err: errors.New("snapshot failed")}
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := client.RequestSnapshot(context.Background(), &pb.RequestSnapshotMessage{ShardId: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_Drain(t *testing.T) {
	adminSvc := newMockAdminService()
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := client.Drain(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("Drain failed: %v", err)
	}
}

func TestGRPC_Drain_Error(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.drainErr = &service.DrainError{Err: errors.New("drain failed")}
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := client.Drain(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %s", st.Code())
	}
}

func TestGRPC_Capabilities(t *testing.T) {
	adminSvc := newMockAdminService()
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	resp, err := client.Capabilities(context.Background(), &pb.CapabilitiesRequest{})
	if err != nil {
		t.Fatalf("Capabilities failed: %v", err)
	}
	if !resp.GetKv() {
		t.Fatal("expected KV=true")
	}
	if !resp.GetMvcc() {
		t.Fatal("expected MVCC=true")
	}
	if !resp.GetTransaction() {
		t.Fatal("expected Transaction=true")
	}
	if !resp.GetBatch() {
		t.Fatal("expected Batch=true")
	}
	if !resp.GetWatch() {
		t.Fatal("expected Watch=true")
	}
	if !resp.GetLease() {
		t.Fatal("expected Lease=true")
	}
	if !resp.GetIndex() {
		t.Fatal("expected Index=true")
	}
	if resp.GetTimeSeries() {
		t.Fatal("expected TimeSeries=false")
	}
	if resp.GetVector() {
		t.Fatal("expected Vector=false")
	}
}

func TestGRPC_Capabilities_Error(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.capsErr = &service.ReadError{Err: errors.New("capabilities failed")}
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := client.Capabilities(context.Background(), &pb.CapabilitiesRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_Put_Error_NotLeader(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.putErr = &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "127.0.0.2:63001"}
	client, cleanup := startTestServer(t, kvSvc, newMockAdminService())
	defer cleanup()

	_, err := client.Put(context.Background(), &pb.PutRequest{Key: "k", Value: []byte("v")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %s", st.Code())
	}
}

func TestGRPC_Get_Error_KeyNotFound(t *testing.T) {
	kvSvc := newMockKVService()
	client, cleanup := startTestServer(t, kvSvc, newMockAdminService())
	defer cleanup()

	_, err := client.Get(context.Background(), &pb.GetRequest{Key: "nonexistent"})
	if err == nil {
		t.Fatal("expected error for missing key, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_Delete_Error_ProposeError(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.delErr = &service.ProposeError{Err: errors.New("propose failed")}
	client, cleanup := startTestServer(t, kvSvc, newMockAdminService())
	defer cleanup()

	_, err := client.Delete(context.Background(), &pb.DeleteRequest{Key: "k"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// Interceptor tests
// ---------------------------------------------------------------------------

func TestRecoveryInterceptor_NoPanic(t *testing.T) {
	handler := func(_ context.Context, _ any) (any, error) {
		return "ok", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/test/Method"}

	resp, err := recoveryInterceptor(context.Background(), nil, info, handler)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected 'ok', got %v", resp)
	}
}

func TestRecoveryInterceptor_Panic(t *testing.T) {
	handler := func(_ context.Context, _ any) (any, error) {
		panic("test panic")
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/test/Method"}

	resp, err := recoveryInterceptor(context.Background(), nil, info, handler)
	if err == nil {
		t.Fatal("expected error after panic, got nil")
	}
	if resp != nil {
		t.Fatalf("expected nil response after panic, got %v", resp)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal code, got %s", st.Code())
	}
}

func TestLoggingInterceptor_Success(t *testing.T) {
	handler := func(_ context.Context, _ any) (any, error) {
		return "ok", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/test/Method"}

	resp, err := loggingInterceptor(context.Background(), nil, info, handler)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected 'ok', got %v", resp)
	}
}

func TestLoggingInterceptor_Error(t *testing.T) {
	handler := func(_ context.Context, _ any) (any, error) {
		return nil, status.Error(codes.NotFound, "not found")
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/test/Method"}

	resp, err := loggingInterceptor(context.Background(), nil, info, handler)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
}

func TestLoggingInterceptor_NonGRPCError(t *testing.T) {
	handler := func(_ context.Context, _ any) (any, error) {
		return nil, errors.New("plain error")
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/test/Method"}

	_, err := loggingInterceptor(context.Background(), nil, info, handler)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*mockKVService) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

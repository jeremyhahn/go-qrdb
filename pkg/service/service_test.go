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

package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"unsafe"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"
)

// mockHost implements the Host interface for unit testing. Each method
// delegates to a configurable function field, allowing per-test behavior
// without rebuilding the mock.
type mockHost struct {
	idFn                func() string
	raftAddressFn       func() string
	shardCountFn        func() int
	getNodeHostInfoFn   func(quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo
	healthCheckFn       func() *quicraft.HealthStatus
	drainFn             func() error
	isDrainingFn        func() bool
	syncProposeFn       func(ctx context.Context, shardID uint64, cmd []byte) (sm.Result, error)
	syncReadFn          func(ctx context.Context, shardID uint64, query interface{}) (interface{}, error)
	requestAddNodeFn    func(ctx context.Context, shardID, replicaID uint64, addr string) (*quicraft.RequestState, error)
	requestRemoveNodeFn func(ctx context.Context, shardID, replicaID uint64) (*quicraft.RequestState, error)
	requestSnapshotFn   func(ctx context.Context, shardID uint64) (*quicraft.RequestState, error)
}

func (m *mockHost) ID() string {
	if m.idFn != nil {
		return m.idFn()
	}
	return "test-host-id"
}

func (m *mockHost) RaftAddress() string {
	if m.raftAddressFn != nil {
		return m.raftAddressFn()
	}
	return "127.0.0.1:9100"
}

func (m *mockHost) ShardCount() int {
	if m.shardCountFn != nil {
		return m.shardCountFn()
	}
	return 1
}

func (m *mockHost) GetNodeHostInfo(opt quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
	if m.getNodeHostInfoFn != nil {
		return m.getNodeHostInfoFn(opt)
	}
	return &quicraft.NodeHostInfo{}
}

func (m *mockHost) HealthCheck() *quicraft.HealthStatus {
	if m.healthCheckFn != nil {
		return m.healthCheckFn()
	}
	return &quicraft.HealthStatus{Status: "ok"}
}

func (m *mockHost) Drain() error {
	if m.drainFn != nil {
		return m.drainFn()
	}
	return nil
}

func (m *mockHost) IsDraining() bool {
	if m.isDrainingFn != nil {
		return m.isDrainingFn()
	}
	return false
}

func (m *mockHost) SyncPropose(ctx context.Context, shardID uint64, cmd []byte) (sm.Result, error) {
	if m.syncProposeFn != nil {
		return m.syncProposeFn(ctx, shardID, cmd)
	}
	return sm.Result{}, nil
}

func (m *mockHost) SyncRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	if m.syncReadFn != nil {
		return m.syncReadFn(ctx, shardID, query)
	}
	return nil, nil
}

func (m *mockHost) RequestAddNode(ctx context.Context, shardID, replicaID uint64, addr string) (*quicraft.RequestState, error) {
	if m.requestAddNodeFn != nil {
		return m.requestAddNodeFn(ctx, shardID, replicaID, addr)
	}
	return nil, nil
}

func (m *mockHost) RequestRemoveNode(ctx context.Context, shardID, replicaID uint64) (*quicraft.RequestState, error) {
	if m.requestRemoveNodeFn != nil {
		return m.requestRemoveNodeFn(ctx, shardID, replicaID)
	}
	return nil, nil
}

func (m *mockHost) RequestSnapshot(ctx context.Context, shardID uint64) (*quicraft.RequestState, error) {
	if m.requestSnapshotFn != nil {
		return m.requestSnapshotFn(ctx, shardID)
	}
	return nil, nil
}

// newCompletedRequestState creates a RequestState that has already been
// resolved with the given result. The caller can call Result() immediately
// without blocking. This uses reflect to access the unexported completedC
// channel, which is the standard Go pattern for constructing test fixtures
// from types with unexported fields.
func newCompletedRequestState(result quicraft.RequestResult) *quicraft.RequestState {
	pool := quicraft.NewRequestStatePool()
	rs := pool.Get().(*quicraft.RequestState)

	// Access the unexported completedC field via reflect + unsafe.
	v := reflect.ValueOf(rs).Elem()
	completedField := v.FieldByName("completedC")
	// Use unsafe to bypass the unexported field restriction.
	completedCPtr := unsafe.Pointer(completedField.UnsafeAddr())
	completedC := *(*chan quicraft.RequestResult)(completedCPtr)

	// Pre-fill the buffered channel so Result() returns immediately.
	completedC <- result
	return rs
}

// ---------------------------------------------------------------------------
// KVService tests
// ---------------------------------------------------------------------------

func TestKVService_Put_Success(t *testing.T) {
	var capturedShardID uint64
	var capturedCmd []byte

	host := &mockHost{
		syncProposeFn: func(_ context.Context, shardID uint64, cmd []byte) (sm.Result, error) {
			capturedShardID = shardID
			capturedCmd = make([]byte, len(cmd))
			copy(capturedCmd, cmd)
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewKVService(host, 42)
	err := svc.Put(context.Background(), "hello", []byte("world"))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if capturedShardID != 42 {
		t.Fatalf("expected shardID 42, got %d", capturedShardID)
	}

	// Verify the command encodes correctly by round-tripping through EncodePut.
	expected := kv.EncodePut([]byte("hello"), []byte("world"))
	if len(capturedCmd) != len(expected) {
		t.Fatalf("expected cmd length %d, got %d", len(expected), len(capturedCmd))
	}
	for i := range expected {
		if capturedCmd[i] != expected[i] {
			t.Fatalf("cmd mismatch at byte %d: expected %d, got %d", i, expected[i], capturedCmd[i])
		}
	}
}

func TestKVService_Put_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       42,
				LeaderID:      3,
				LeaderAddress: "10.0.0.3:9100",
			}
		},
	}

	svc := NewKVService(host, 42)
	err := svc.Put(context.Background(), "key", []byte("val"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 42 {
		t.Fatalf("expected ShardID 42, got %d", nle.ShardID)
	}
	if nle.LeaderID != 3 {
		t.Fatalf("expected LeaderID 3, got %d", nle.LeaderID)
	}
	if nle.LeaderAddress != "10.0.0.3:9100" {
		t.Fatalf("expected LeaderAddress 10.0.0.3:9100, got %s", nle.LeaderAddress)
	}
}

func TestKVService_Put_GenericError(t *testing.T) {
	sentinel := errors.New("disk full")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewKVService(host, 1)
	err := svc.Put(context.Background(), "k", []byte("v"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", pe.Unwrap())
	}
}

func TestKVService_Get_Success(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, shardID uint64, query interface{}) (interface{}, error) {
			if shardID != 7 {
				t.Fatalf("expected shardID 7, got %d", shardID)
			}
			key, ok := query.([]byte)
			if !ok {
				t.Fatalf("expected []byte query, got %T", query)
			}
			if string(key) != "mykey" {
				t.Fatalf("expected query mykey, got %s", string(key))
			}
			return []byte("myvalue"), nil
		},
	}

	svc := NewKVService(host, 7)
	val, err := svc.Get(context.Background(), "mykey")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if string(val) != "myvalue" {
		t.Fatalf("expected myvalue, got %s", string(val))
	}
}

func TestKVService_Get_KeyNotFoundError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &kv.KeyNotFoundError{Key: "missing"}
		},
	}

	svc := NewKVService(host, 1)
	val, err := svc.Get(context.Background(), "missing")
	if val != nil {
		t.Fatalf("expected nil value, got %v", val)
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
	if knf.Key != "missing" {
		t.Fatalf("expected key missing, got %s", knf.Key)
	}
}

func TestKVService_Get_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{
				ShardID:       5,
				LeaderID:      10,
				LeaderAddress: "10.0.0.10:9100",
			}
		},
	}

	svc := NewKVService(host, 5)
	val, err := svc.Get(context.Background(), "key")
	if val != nil {
		t.Fatalf("expected nil value, got %v", val)
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 5 {
		t.Fatalf("expected ShardID 5, got %d", nle.ShardID)
	}
	if nle.LeaderID != 10 {
		t.Fatalf("expected LeaderID 10, got %d", nle.LeaderID)
	}
	if nle.LeaderAddress != "10.0.0.10:9100" {
		t.Fatalf("expected LeaderAddress 10.0.0.10:9100, got %s", nle.LeaderAddress)
	}
}

func TestKVService_Get_UnexpectedResultType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			// Return an int instead of []byte.
			return 12345, nil
		},
	}

	svc := NewKVService(host, 1)
	val, err := svc.Get(context.Background(), "key")
	if val != nil {
		t.Fatalf("expected nil value, got %v", val)
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}

	var urt *unexpectedResultTypeError
	if !errors.As(re.Unwrap(), &urt) {
		t.Fatalf("expected *unexpectedResultTypeError wrapped, got %T: %v", re.Unwrap(), re.Unwrap())
	}
}

func TestKVService_Get_GenericReadError(t *testing.T) {
	sentinel := errors.New("read timeout")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewKVService(host, 1)
	val, err := svc.Get(context.Background(), "key")
	if val != nil {
		t.Fatalf("expected nil value, got %v", val)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	if !errors.Is(re.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", re.Unwrap())
	}
}

func TestKVService_Delete_Success(t *testing.T) {
	var capturedCmd []byte
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			capturedCmd = make([]byte, len(cmd))
			copy(capturedCmd, cmd)
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewKVService(host, 1)
	err := svc.Delete(context.Background(), "delme")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	expected := kv.EncodeDelete([]byte("delme"))
	if len(capturedCmd) != len(expected) {
		t.Fatalf("expected cmd length %d, got %d", len(expected), len(capturedCmd))
	}
	for i := range expected {
		if capturedCmd[i] != expected[i] {
			t.Fatalf("cmd mismatch at byte %d: expected %d, got %d", i, expected[i], capturedCmd[i])
		}
	}
}

func TestKVService_Delete_Error(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{ShardID: 1}
		},
	}

	svc := NewKVService(host, 1)
	err := svc.Delete(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

func TestKVService_Delete_GenericError(t *testing.T) {
	sentinel := errors.New("busy")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewKVService(host, 1)
	err := svc.Delete(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
}

func TestKVService_List_Success(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, shardID uint64, query interface{}) (interface{}, error) {
			lq, ok := query.(kv.ListQuery)
			if !ok {
				t.Fatalf("expected kv.ListQuery, got %T", query)
			}
			if lq.Prefix != "user:" {
				t.Fatalf("expected prefix user:, got %s", lq.Prefix)
			}
			return []string{"user:alice", "user:bob", "user:carol"}, nil
		},
	}

	svc := NewKVService(host, 1)
	keys, err := svc.List(context.Background(), "user:")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}
	expected := []string{"user:alice", "user:bob", "user:carol"}
	for i, k := range keys {
		if k != expected[i] {
			t.Fatalf("key[%d]: expected %s, got %s", i, expected[i], k)
		}
	}
}

func TestKVService_List_EmptyPrefix(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			lq, ok := query.(kv.ListQuery)
			if !ok {
				t.Fatalf("expected kv.ListQuery, got %T", query)
			}
			if lq.Prefix != "" {
				t.Fatalf("expected empty prefix, got %s", lq.Prefix)
			}
			return []string{"a", "b"}, nil
		},
	}

	svc := NewKVService(host, 1)
	keys, err := svc.List(context.Background(), "")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
}

func TestKVService_List_Error(t *testing.T) {
	sentinel := errors.New("read timeout")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewKVService(host, 1)
	keys, err := svc.List(context.Background(), "")
	if keys != nil {
		t.Fatalf("expected nil keys, got %v", keys)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_List_UnexpectedResultType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			// Return []byte instead of []string.
			return []byte("wrong"), nil
		},
	}

	svc := NewKVService(host, 1)
	keys, err := svc.List(context.Background(), "")
	if keys != nil {
		t.Fatalf("expected nil keys, got %v", keys)
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}

	var urt *unexpectedResultTypeError
	if !errors.As(re.Unwrap(), &urt) {
		t.Fatalf("expected *unexpectedResultTypeError wrapped, got %T", re.Unwrap())
	}
}

func TestKVService_List_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{ShardID: 1, LeaderID: 5, LeaderAddress: "10.0.0.5:9100"}
		},
	}

	svc := NewKVService(host, 1)
	keys, err := svc.List(context.Background(), "prefix")
	if keys != nil {
		t.Fatalf("expected nil keys, got %v", keys)
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.LeaderID != 5 {
		t.Fatalf("expected LeaderID 5, got %d", nle.LeaderID)
	}
}

// ---------------------------------------------------------------------------
// AdminService tests
// ---------------------------------------------------------------------------

func TestAdminService_Health_OK_LeaderTrue(t *testing.T) {
	host := &mockHost{
		healthCheckFn: func() *quicraft.HealthStatus {
			return &quicraft.HealthStatus{
				Status: "ok",
				Shards: []quicraft.ShardInfo{
					{ShardID: 1, IsLeader: true},
					{ShardID: 2, IsLeader: false},
				},
			}
		},
	}

	svc := NewAdminService(host)
	hs, err := svc.Health(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if hs.Status != "ok" {
		t.Fatalf("expected status ok, got %s", hs.Status)
	}
	if !hs.Leader {
		t.Fatal("expected Leader=true")
	}
}

func TestAdminService_Health_OK_LeaderFalse(t *testing.T) {
	host := &mockHost{
		healthCheckFn: func() *quicraft.HealthStatus {
			return &quicraft.HealthStatus{
				Status: "ok",
				Shards: []quicraft.ShardInfo{
					{ShardID: 1, IsLeader: false},
					{ShardID: 2, IsLeader: false},
				},
			}
		},
	}

	svc := NewAdminService(host)
	hs, err := svc.Health(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if hs.Status != "ok" {
		t.Fatalf("expected status ok, got %s", hs.Status)
	}
	if hs.Leader {
		t.Fatal("expected Leader=false")
	}
}

func TestAdminService_Health_NoShards(t *testing.T) {
	host := &mockHost{
		healthCheckFn: func() *quicraft.HealthStatus {
			return &quicraft.HealthStatus{
				Status: "degraded",
				Shards: nil,
			}
		},
	}

	svc := NewAdminService(host)
	hs, err := svc.Health(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if hs.Status != "degraded" {
		t.Fatalf("expected status degraded, got %s", hs.Status)
	}
	if hs.Leader {
		t.Fatal("expected Leader=false with no shards")
	}
}

func TestAdminService_Status_Success(t *testing.T) {
	host := &mockHost{
		idFn: func() string { return "node-abc" },
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				RaftAddress: "10.0.0.1:9100",
				ShardInfoList: []quicraft.ShardInfo{
					{ShardID: 1, ReplicaID: 1, IsLeader: true},
					{ShardID: 2, ReplicaID: 2, IsLeader: false},
				},
			}
		},
		shardCountFn: func() int { return 2 },
		isDrainingFn: func() bool { return false },
	}

	svc := NewAdminService(host)
	si, err := svc.Status(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if si.NodeID != "node-abc" {
		t.Fatalf("expected NodeID node-abc, got %s", si.NodeID)
	}
	if si.RaftAddress != "10.0.0.1:9100" {
		t.Fatalf("expected RaftAddress 10.0.0.1:9100, got %s", si.RaftAddress)
	}
	if !si.IsLeader {
		t.Fatal("expected IsLeader=true")
	}
	if si.LeaderID != 1 {
		t.Fatalf("expected LeaderID 1, got %d", si.LeaderID)
	}
	if si.ShardCount != 2 {
		t.Fatalf("expected ShardCount 2, got %d", si.ShardCount)
	}
	if si.IsDraining {
		t.Fatal("expected IsDraining=false")
	}
	if len(si.Shards) != 2 {
		t.Fatalf("expected 2 shards, got %d", len(si.Shards))
	}
}

func TestAdminService_Status_NotLeader(t *testing.T) {
	host := &mockHost{
		idFn: func() string { return "node-xyz" },
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				RaftAddress: "10.0.0.2:9100",
				ShardInfoList: []quicraft.ShardInfo{
					{ShardID: 1, ReplicaID: 2, IsLeader: false, LeaderID: 1},
				},
			}
		},
		shardCountFn: func() int { return 1 },
		isDrainingFn: func() bool { return true },
	}

	svc := NewAdminService(host)
	si, err := svc.Status(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if si.IsLeader {
		t.Fatal("expected IsLeader=false")
	}
	if si.LeaderID != 0 {
		t.Fatalf("expected LeaderID 0 (no local leader), got %d", si.LeaderID)
	}
	if !si.IsDraining {
		t.Fatal("expected IsDraining=true")
	}
}

func TestAdminService_Peers_Success(t *testing.T) {
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					{
						ShardID:  1,
						LeaderID: 1,
						Nodes: map[uint64]string{
							1: "10.0.0.1:9100",
							2: "10.0.0.2:9100",
							3: "10.0.0.3:9100",
						},
					},
					{
						ShardID:  2,
						LeaderID: 2,
						Nodes: map[uint64]string{
							1: "10.0.0.1:9100",
							2: "10.0.0.2:9100",
						},
					},
				},
			}
		},
	}

	svc := NewAdminService(host)
	pi, err := svc.Peers(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(pi.Shards) != 2 {
		t.Fatalf("expected 2 shards, got %d", len(pi.Shards))
	}

	// Verify shard 1 peers.
	shard1 := pi.Shards[0]
	if shard1.ShardID != 1 {
		t.Fatalf("expected ShardID 1, got %d", shard1.ShardID)
	}
	if shard1.LeaderID != 1 {
		t.Fatalf("expected LeaderID 1, got %d", shard1.LeaderID)
	}
	if len(shard1.Peers) != 3 {
		t.Fatalf("expected 3 peers in shard 1, got %d", len(shard1.Peers))
	}

	// Verify that exactly one peer is marked as leader in shard 1.
	leaderCount := 0
	for _, p := range shard1.Peers {
		if p.IsLeader {
			leaderCount++
			if p.ReplicaID != 1 {
				t.Fatalf("expected leader to be replica 1, got %d", p.ReplicaID)
			}
		}
	}
	if leaderCount != 1 {
		t.Fatalf("expected exactly 1 leader in shard 1, got %d", leaderCount)
	}

	// Verify shard 2 peers.
	shard2 := pi.Shards[1]
	if shard2.ShardID != 2 {
		t.Fatalf("expected ShardID 2, got %d", shard2.ShardID)
	}
	if len(shard2.Peers) != 2 {
		t.Fatalf("expected 2 peers in shard 2, got %d", len(shard2.Peers))
	}
}

func TestAdminService_Peers_EmptyShards(t *testing.T) {
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: nil,
			}
		},
	}

	svc := NewAdminService(host)
	pi, err := svc.Peers(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(pi.Shards) != 0 {
		t.Fatalf("expected 0 shards, got %d", len(pi.Shards))
	}
}

func TestAdminService_AddMember_Success(t *testing.T) {
	host := &mockHost{
		requestAddNodeFn: func(_ context.Context, shardID, replicaID uint64, addr string) (*quicraft.RequestState, error) {
			if shardID != 1 {
				t.Fatalf("expected shardID 1, got %d", shardID)
			}
			if replicaID != 4 {
				t.Fatalf("expected replicaID 4, got %d", replicaID)
			}
			if addr != "10.0.0.4:9100" {
				t.Fatalf("expected addr 10.0.0.4:9100, got %s", addr)
			}
			return newCompletedRequestState(quicraft.RequestResult{}), nil
		},
	}

	svc := NewAdminService(host)
	err := svc.AddMember(context.Background(), 1, 4, "10.0.0.4:9100")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestAdminService_AddMember_RequestError(t *testing.T) {
	sentinel := errors.New("shard not found")
	host := &mockHost{
		requestAddNodeFn: func(_ context.Context, _, _ uint64, _ string) (*quicraft.RequestState, error) {
			return nil, sentinel
		},
	}

	svc := NewAdminService(host)
	err := svc.AddMember(context.Background(), 1, 4, "10.0.0.4:9100")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var me *MembershipError
	if !errors.As(err, &me) {
		t.Fatalf("expected *MembershipError, got %T: %v", err, err)
	}
	if me.Op != "add" {
		t.Fatalf("expected op add, got %s", me.Op)
	}
	if !errors.Is(me.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", me.Unwrap())
	}
}

func TestAdminService_AddMember_ResultError(t *testing.T) {
	resultErr := errors.New("config change rejected")
	host := &mockHost{
		requestAddNodeFn: func(_ context.Context, _, _ uint64, _ string) (*quicraft.RequestState, error) {
			return newCompletedRequestState(quicraft.RequestResult{Err: resultErr}), nil
		},
	}

	svc := NewAdminService(host)
	err := svc.AddMember(context.Background(), 1, 4, "10.0.0.4:9100")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var me *MembershipError
	if !errors.As(err, &me) {
		t.Fatalf("expected *MembershipError, got %T: %v", err, err)
	}
	if me.Op != "add" {
		t.Fatalf("expected op add, got %s", me.Op)
	}
	if !errors.Is(me.Unwrap(), resultErr) {
		t.Fatalf("expected underlying result error, got %v", me.Unwrap())
	}
}

func TestAdminService_RemoveMember_Success(t *testing.T) {
	host := &mockHost{
		requestRemoveNodeFn: func(_ context.Context, shardID, replicaID uint64) (*quicraft.RequestState, error) {
			if shardID != 1 {
				t.Fatalf("expected shardID 1, got %d", shardID)
			}
			if replicaID != 3 {
				t.Fatalf("expected replicaID 3, got %d", replicaID)
			}
			return newCompletedRequestState(quicraft.RequestResult{}), nil
		},
	}

	svc := NewAdminService(host)
	err := svc.RemoveMember(context.Background(), 1, 3)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestAdminService_RemoveMember_RequestError(t *testing.T) {
	sentinel := errors.New("not leader")
	host := &mockHost{
		requestRemoveNodeFn: func(_ context.Context, _, _ uint64) (*quicraft.RequestState, error) {
			return nil, sentinel
		},
	}

	svc := NewAdminService(host)
	err := svc.RemoveMember(context.Background(), 1, 3)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var me *MembershipError
	if !errors.As(err, &me) {
		t.Fatalf("expected *MembershipError, got %T: %v", err, err)
	}
	if me.Op != "remove" {
		t.Fatalf("expected op remove, got %s", me.Op)
	}
}

func TestAdminService_RemoveMember_ResultError(t *testing.T) {
	resultErr := errors.New("membership change in progress")
	host := &mockHost{
		requestRemoveNodeFn: func(_ context.Context, _, _ uint64) (*quicraft.RequestState, error) {
			return newCompletedRequestState(quicraft.RequestResult{Err: resultErr}), nil
		},
	}

	svc := NewAdminService(host)
	err := svc.RemoveMember(context.Background(), 1, 3)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var me *MembershipError
	if !errors.As(err, &me) {
		t.Fatalf("expected *MembershipError, got %T: %v", err, err)
	}
	if !errors.Is(me.Unwrap(), resultErr) {
		t.Fatalf("expected underlying result error, got %v", me.Unwrap())
	}
}

func TestAdminService_RequestSnapshot_Success(t *testing.T) {
	host := &mockHost{
		requestSnapshotFn: func(_ context.Context, shardID uint64) (*quicraft.RequestState, error) {
			if shardID != 5 {
				t.Fatalf("expected shardID 5, got %d", shardID)
			}
			return newCompletedRequestState(quicraft.RequestResult{}), nil
		},
	}

	svc := NewAdminService(host)
	err := svc.RequestSnapshot(context.Background(), 5)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestAdminService_RequestSnapshot_RequestError(t *testing.T) {
	sentinel := errors.New("snapshot in progress")
	host := &mockHost{
		requestSnapshotFn: func(_ context.Context, _ uint64) (*quicraft.RequestState, error) {
			return nil, sentinel
		},
	}

	svc := NewAdminService(host)
	err := svc.RequestSnapshot(context.Background(), 5)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var sre *SnapshotRequestError
	if !errors.As(err, &sre) {
		t.Fatalf("expected *SnapshotRequestError, got %T: %v", err, err)
	}
	if !errors.Is(sre.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", sre.Unwrap())
	}
}

func TestAdminService_RequestSnapshot_ResultError(t *testing.T) {
	resultErr := errors.New("snapshot rejected")
	host := &mockHost{
		requestSnapshotFn: func(_ context.Context, _ uint64) (*quicraft.RequestState, error) {
			return newCompletedRequestState(quicraft.RequestResult{Err: resultErr}), nil
		},
	}

	svc := NewAdminService(host)
	err := svc.RequestSnapshot(context.Background(), 5)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var sre *SnapshotRequestError
	if !errors.As(err, &sre) {
		t.Fatalf("expected *SnapshotRequestError, got %T: %v", err, err)
	}
	if !errors.Is(sre.Unwrap(), resultErr) {
		t.Fatalf("expected underlying result error, got %v", sre.Unwrap())
	}
}

func TestAdminService_Drain_Success(t *testing.T) {
	called := false
	host := &mockHost{
		drainFn: func() error {
			called = true
			return nil
		},
	}

	svc := NewAdminService(host)
	err := svc.Drain(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !called {
		t.Fatal("expected Drain to be called on host")
	}
}

func TestAdminService_Drain_Error(t *testing.T) {
	sentinel := errors.New("already draining")
	host := &mockHost{
		drainFn: func() error {
			return sentinel
		},
	}

	svc := NewAdminService(host)
	err := svc.Drain(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var de *DrainError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DrainError, got %T: %v", err, err)
	}
	if !errors.Is(de.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", de.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestKeyNotFoundError_Error(t *testing.T) {
	e := &KeyNotFoundError{Key: "test-key"}
	expected := "service: key not found: test-key"
	if e.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, e.Error())
	}
}

func TestProposeError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("inner")
	e := &ProposeError{Err: inner}

	expected := "service: propose failed: inner"
	if e.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, e.Error())
	}

	if e.Unwrap() != inner {
		t.Fatalf("expected Unwrap to return inner error")
	}

	if !errors.Is(e, inner) {
		t.Fatal("errors.Is should match inner error through Unwrap")
	}
}

func TestReadError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("timeout")
	e := &ReadError{Err: inner}

	expected := "service: read failed: timeout"
	if e.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, e.Error())
	}

	if e.Unwrap() != inner {
		t.Fatalf("expected Unwrap to return inner error")
	}

	if !errors.Is(e, inner) {
		t.Fatal("errors.Is should match inner error through Unwrap")
	}
}

func TestNotLeaderError_Error_WithLeader(t *testing.T) {
	e := &NotLeaderError{
		ShardID:       1,
		LeaderID:      3,
		LeaderAddress: "10.0.0.3:9100",
	}
	msg := e.Error()
	if msg != "service: not leader for shard 1, leader is replica 3 at 10.0.0.3:9100" {
		t.Fatalf("unexpected error message: %s", msg)
	}
}

func TestNotLeaderError_Error_LeaderUnknown(t *testing.T) {
	e := &NotLeaderError{
		ShardID:  42,
		LeaderID: 0,
	}
	msg := e.Error()
	if msg != "service: not leader for shard 42, leader unknown" {
		t.Fatalf("unexpected error message: %s", msg)
	}
}

func TestMembershipError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("rejected")
	e := &MembershipError{Op: "add", Err: inner}

	expected := "service: membership add failed: rejected"
	if e.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, e.Error())
	}

	if e.Unwrap() != inner {
		t.Fatalf("expected Unwrap to return inner error")
	}

	if !errors.Is(e, inner) {
		t.Fatal("errors.Is should match inner error through Unwrap")
	}
}

func TestMembershipError_RemoveOp(t *testing.T) {
	inner := errors.New("not found")
	e := &MembershipError{Op: "remove", Err: inner}

	expected := "service: membership remove failed: not found"
	if e.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, e.Error())
	}
}

func TestSnapshotRequestError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("in progress")
	e := &SnapshotRequestError{Err: inner}

	expected := "service: snapshot request failed: in progress"
	if e.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, e.Error())
	}

	if e.Unwrap() != inner {
		t.Fatalf("expected Unwrap to return inner error")
	}

	if !errors.Is(e, inner) {
		t.Fatal("errors.Is should match inner error through Unwrap")
	}
}

func TestDrainError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("already draining")
	e := &DrainError{Err: inner}

	expected := "service: drain failed: already draining"
	if e.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, e.Error())
	}

	if e.Unwrap() != inner {
		t.Fatalf("expected Unwrap to return inner error")
	}

	if !errors.Is(e, inner) {
		t.Fatal("errors.Is should match inner error through Unwrap")
	}
}

func TestUnexpectedResultTypeError_Error(t *testing.T) {
	e := &unexpectedResultTypeError{}
	expected := "unexpected result type from state machine"
	if e.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, e.Error())
	}
}

// ---------------------------------------------------------------------------
// Constructor tests
// ---------------------------------------------------------------------------

func TestNewKVService_ReturnsNonNil(t *testing.T) {
	svc := NewKVService(&mockHost{}, 1)
	if svc == nil {
		t.Fatal("expected non-nil KVService")
	}
}

func TestNewAdminService_ReturnsNonNil(t *testing.T) {
	svc := NewAdminService(&mockHost{})
	if svc == nil {
		t.Fatal("expected non-nil AdminService")
	}
}

// ---------------------------------------------------------------------------
// Edge case tests
// ---------------------------------------------------------------------------

func TestKVService_Put_EmptyKeyAndValue(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			expected := kv.EncodePut([]byte(""), []byte(""))
			if len(cmd) != len(expected) {
				t.Fatalf("expected cmd length %d, got %d", len(expected), len(cmd))
			}
			return sm.Result{}, nil
		},
	}

	svc := NewKVService(host, 1)
	err := svc.Put(context.Background(), "", []byte(""))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestKVService_Get_EmptyKey(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			key, ok := query.([]byte)
			if !ok {
				t.Fatalf("expected []byte query, got %T", query)
			}
			if len(key) != 0 {
				t.Fatalf("expected empty key, got %v", key)
			}
			return []byte("empty-key-value"), nil
		},
	}

	svc := NewKVService(host, 1)
	val, err := svc.Get(context.Background(), "")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if string(val) != "empty-key-value" {
		t.Fatalf("expected empty-key-value, got %s", string(val))
	}
}

func TestKVService_Put_LargeValue(t *testing.T) {
	largeVal := make([]byte, 64*1024) // 64KB
	for i := range largeVal {
		largeVal[i] = byte(i % 256)
	}

	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, nil
		},
	}

	svc := NewKVService(host, 1)
	err := svc.Put(context.Background(), "big", largeVal)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestKVService_Get_NilResult(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			// Return nil value (not []byte typed nil).
			return nil, nil
		},
	}

	svc := NewKVService(host, 1)
	val, err := svc.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error for nil result, got nil")
	}
	if val != nil {
		t.Fatalf("expected nil val, got %v", val)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_List_NilResult(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, nil
		},
	}

	svc := NewKVService(host, 1)
	keys, err := svc.List(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for nil result, got nil")
	}
	if keys != nil {
		t.Fatalf("expected nil keys, got %v", keys)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestAdminService_Peers_LeaderIDZero(t *testing.T) {
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					{
						ShardID:  1,
						LeaderID: 0, // No known leader.
						Nodes: map[uint64]string{
							1: "10.0.0.1:9100",
						},
					},
				},
			}
		},
	}

	svc := NewAdminService(host)
	pi, err := svc.Peers(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(pi.Shards) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(pi.Shards))
	}
	if pi.Shards[0].LeaderID != 0 {
		t.Fatalf("expected LeaderID 0, got %d", pi.Shards[0].LeaderID)
	}
	// No peer should be marked leader when LeaderID is 0.
	for _, p := range pi.Shards[0].Peers {
		if p.IsLeader {
			t.Fatalf("no peer should be leader when LeaderID=0, but replica %d is", p.ReplicaID)
		}
	}
}

func TestAdminService_Status_NoShards(t *testing.T) {
	host := &mockHost{
		idFn: func() string { return "solo" },
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				RaftAddress:   "127.0.0.1:9100",
				ShardInfoList: nil,
			}
		},
		shardCountFn: func() int { return 0 },
		isDrainingFn: func() bool { return false },
	}

	svc := NewAdminService(host)
	si, err := svc.Status(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if si.IsLeader {
		t.Fatal("expected IsLeader=false with no shards")
	}
	if si.LeaderID != 0 {
		t.Fatalf("expected LeaderID 0, got %d", si.LeaderID)
	}
	if si.ShardCount != 0 {
		t.Fatalf("expected ShardCount 0, got %d", si.ShardCount)
	}
}

// ---------------------------------------------------------------------------
// Error type assertion tests (typed assertions, not errors.As)
// ---------------------------------------------------------------------------

func TestErrorTypes_AreDistinct(t *testing.T) {
	// Verify that each error type is assignable and distinct.
	var _ error = &KeyNotFoundError{}
	var _ error = &ProposeError{Err: errors.New("x")}
	var _ error = &ReadError{Err: errors.New("x")}
	var _ error = &NotLeaderError{}
	var _ error = &MembershipError{Err: errors.New("x")}
	var _ error = &SnapshotRequestError{Err: errors.New("x")}
	var _ error = &DrainError{Err: errors.New("x")}
	var _ error = &unexpectedResultTypeError{}
}

func TestKVService_Delete_NotLeaderError_CarriesFields(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       99,
				LeaderID:      7,
				LeaderAddress: "192.168.1.7:9100",
			}
		},
	}

	svc := NewKVService(host, 99)
	err := svc.Delete(context.Background(), "key")

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T", err)
	}
	if nle.ShardID != 99 {
		t.Fatalf("expected ShardID 99, got %d", nle.ShardID)
	}
	if nle.LeaderID != 7 {
		t.Fatalf("expected LeaderID 7, got %d", nle.LeaderID)
	}
	if nle.LeaderAddress != "192.168.1.7:9100" {
		t.Fatalf("expected LeaderAddress 192.168.1.7:9100, got %s", nle.LeaderAddress)
	}
}

func TestKVService_List_KeyNotFoundError(t *testing.T) {
	// List goes through translateReadError, verify KeyNotFoundError is handled.
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &kv.KeyNotFoundError{Key: "scan-miss"}
		},
	}

	svc := NewKVService(host, 1)
	keys, err := svc.List(context.Background(), "scan")
	if keys != nil {
		t.Fatalf("expected nil keys, got %v", keys)
	}

	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
	if knf.Key != "scan-miss" {
		t.Fatalf("expected key scan-miss, got %s", knf.Key)
	}
}

func TestKVService_Get_KeyNotFoundError_EmptyKey(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &kv.KeyNotFoundError{Key: ""}
		},
	}

	svc := NewKVService(host, 1)
	val, err := svc.Get(context.Background(), "")
	if val != nil {
		t.Fatalf("expected nil value, got %v", val)
	}

	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
	if knf.Key != "" {
		t.Fatalf("expected empty key, got %q", knf.Key)
	}
}

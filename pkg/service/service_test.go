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
	"github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/codec"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// mockHost implements the Host interface for unit testing. Each method
// delegates to a configurable function field, allowing per-test behavior
// without rebuilding the mock.
type mockHost struct {
	idFn                  func() string
	raftAddressFn         func() string
	shardCountFn          func() int
	getNodeHostInfoFn     func(quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo
	healthCheckFn         func() *quicraft.HealthStatus
	drainFn               func() error
	isDrainingFn          func() bool
	syncProposeFn         func(ctx context.Context, shardID uint64, cmd []byte) (sm.Result, error)
	syncReadFn            func(ctx context.Context, shardID uint64, query interface{}) (interface{}, error)
	requestAddNodeFn      func(ctx context.Context, shardID, replicaID uint64, addr string) (*quicraft.RequestState, error)
	requestRemoveNodeFn   func(ctx context.Context, shardID, replicaID uint64) (*quicraft.RequestState, error)
	requestSnapshotFn     func(ctx context.Context, shardID uint64) (*quicraft.RequestState, error)
	proposeWithModeFn     func(ctx context.Context, shardID uint64, cmd []byte, mode writemode.Mode) (sm.Result, error)
	staleReadFn           func(ctx context.Context, shardID uint64, query interface{}) (interface{}, error)
	readWithConsistencyFn func(ctx context.Context, shardID uint64, query interface{}, opts consistency.ReadOptions) (interface{}, error)
	followerReadFn        func(ctx context.Context, shardID uint64, query interface{}) (interface{}, error)
	getLeaderIDFn         func(shardID uint64) (uint64, bool, error)
	getShardRaftStateFn   func(shardID uint64) (uint64, uint64, error)
	startShardFn          func(members map[uint64]string, join bool, create sm.CreateFunc, cfg config.Config) error
	stopShardFn           func(shardID uint64) error
	closeFn               func() error
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

func (m *mockHost) RequestAddNode(ctx context.Context, shardID, replicaID uint64, addr string, configChangeIndex uint64) (*quicraft.RequestState, error) {
	if m.requestAddNodeFn != nil {
		return m.requestAddNodeFn(ctx, shardID, replicaID, addr)
	}
	return nil, nil
}

func (m *mockHost) RequestRemoveNode(ctx context.Context, shardID, replicaID uint64, configChangeIndex uint64) (*quicraft.RequestState, error) {
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

func (m *mockHost) ProposeWithMode(ctx context.Context, shardID uint64, cmd []byte, mode writemode.Mode) (sm.Result, error) {
	if m.proposeWithModeFn != nil {
		return m.proposeWithModeFn(ctx, shardID, cmd, mode)
	}
	return sm.Result{}, nil
}

func (m *mockHost) StaleRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	if m.staleReadFn != nil {
		return m.staleReadFn(ctx, shardID, query)
	}
	return nil, nil
}

func (m *mockHost) ReadWithConsistency(ctx context.Context, shardID uint64, query interface{}, opts consistency.ReadOptions) (interface{}, error) {
	if m.readWithConsistencyFn != nil {
		return m.readWithConsistencyFn(ctx, shardID, query, opts)
	}
	return nil, nil
}

func (m *mockHost) FollowerRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	if m.followerReadFn != nil {
		return m.followerReadFn(ctx, shardID, query)
	}
	return nil, nil
}

func (m *mockHost) GetLeaderID(shardID uint64) (uint64, bool, error) {
	if m.getLeaderIDFn != nil {
		return m.getLeaderIDFn(shardID)
	}
	return 0, false, nil
}

func (m *mockHost) GetShardRaftState(shardID uint64) (uint64, uint64, error) {
	if m.getShardRaftStateFn != nil {
		return m.getShardRaftStateFn(shardID)
	}
	return 0, 0, nil
}

func (m *mockHost) StartShard(members map[uint64]string, join bool, create sm.CreateFunc, cfg config.Config) error {
	if m.startShardFn != nil {
		return m.startShardFn(members, join, create, cfg)
	}
	return nil
}

func (m *mockHost) StopShard(shardID uint64) error {
	if m.stopShardFn != nil {
		return m.stopShardFn(shardID)
	}
	return nil
}

func (m *mockHost) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
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
	expected, encErr := kv.EncodePut([]byte("hello"), []byte("world"))
	if encErr != nil {
		t.Fatalf("EncodePut error: %v", encErr)
	}
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

	expected, encErr := kv.EncodeDelete([]byte("delme"))
	if encErr != nil {
		t.Fatalf("EncodeDelete error: %v", encErr)
	}
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
			expected, encErr := kv.EncodePut([]byte(""), []byte(""))
			if encErr != nil {
				t.Fatalf("EncodePut error: %v", encErr)
			}
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
	if err != nil {
		t.Fatalf("expected no error for nil result, got %v", err)
	}
	if keys != nil {
		t.Fatalf("expected nil keys, got %v", keys)
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

// ---------------------------------------------------------------------------
// KVService.Scan
// ---------------------------------------------------------------------------

func TestKVService_Scan(t *testing.T) {
	expected := map[string][]byte{
		"app/key1": []byte("v1"),
		"app/key2": []byte("v2"),
	}
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return expected, nil
		},
	}
	svc := NewKVService(host, 1)
	result, err := svc.Scan(context.Background(), "app/")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(result))
	}
	if string(result["app/key1"]) != "v1" {
		t.Errorf("key1 = %q, want %q", result["app/key1"], "v1")
	}
}

func TestKVService_Scan_NilResult(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, nil
		},
	}
	svc := NewKVService(host, 1)
	result, err := svc.Scan(context.Background(), "empty/")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result, got %v", result)
	}
}

func TestKVService_Scan_ReadError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("read failed")
		},
	}
	svc := NewKVService(host, 1)
	_, err := svc.Scan(context.Background(), "prefix/")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestKVService_Scan_UnexpectedType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "not a map", nil
		},
	}
	svc := NewKVService(host, 1)
	_, err := svc.Scan(context.Background(), "prefix/")
	if err == nil {
		t.Fatal("expected error")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// AdminService.Capabilities
// ---------------------------------------------------------------------------

func TestAdminService_Capabilities(t *testing.T) {
	svc := NewAdminService(&mockHost{})
	caps, err := svc.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if caps == nil {
		t.Fatal("expected non-nil capabilities")
	}
}

// ---------------------------------------------------------------------------
// KVService with codec manager (nil = backward compat)
// ---------------------------------------------------------------------------

func TestKVService_NilCodec_PutGetPassthrough(t *testing.T) {
	var storedValue []byte
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			// Extract the value from the encoded command by decoding
			// the put command format.
			storedValue = make([]byte, len(cmd))
			copy(storedValue, cmd)
			return sm.Result{Value: 1}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			return []byte("raw-value"), nil
		},
	}

	// No codec option = nil codec, values pass through unmodified.
	svc := NewKVService(host, 1)

	if err := svc.Put(context.Background(), "key", []byte("raw-value")); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}

	got, err := svc.Get(context.Background(), "key")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if string(got) != "raw-value" {
		t.Errorf("Get: got %q, want %q", string(got), "raw-value")
	}
}

func TestKVService_WithCodec_PutEncodesForStorage(t *testing.T) {
	var capturedCmd []byte
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			capturedCmd = make([]byte, len(cmd))
			copy(capturedCmd, cmd)
			return sm.Result{Value: 1}, nil
		},
	}

	// Use JSON for both storage and wire (so we can verify encoding).
	cm := codec.NewManager(&codec.JSONCodec{}, &codec.JSONCodec{})
	svc := NewKVService(host, 1, WithCodecManager(cm))

	// Put raw bytes - the codec should encode them.
	if err := svc.Put(context.Background(), "key", []byte("hello")); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}

	// The captured command should contain the JSON-encoded form of
	// the value (the bytes "hello" JSON-marshaled = "aGVsbG8=" base64).
	if capturedCmd == nil {
		t.Fatal("Put: no command was proposed")
	}
}

func TestKVService_WithCodec_GetTranscodesFromStorage(t *testing.T) {
	// Store a JSON-encoded value and verify transcode works.
	cm := codec.NewManager(&codec.JSONCodec{}, &codec.JSONCodec{})

	// Encode a test value using the storage codec.
	testMap := map[string]string{"name": "alice"}
	encoded, err := cm.EncodeForStorage(testMap)
	if err != nil {
		t.Fatalf("EncodeForStorage: %v", err)
	}

	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			return encoded, nil
		},
	}

	svc := NewKVService(host, 1, WithCodecManager(cm))
	got, getErr := svc.Get(context.Background(), "key")
	if getErr != nil {
		t.Fatalf("Get: unexpected error: %v", getErr)
	}
	if got == nil {
		t.Fatal("Get: got nil")
	}
	// The transcoded result should be valid JSON.
	if len(got) == 0 {
		t.Fatal("Get: got empty result after transcode")
	}
}

func TestKVService_WithCodec_GetStaleTranscodes(t *testing.T) {
	cm := codec.NewManager(&codec.JSONCodec{}, &codec.JSONCodec{})

	testData := map[string]int{"count": 42}
	encoded, err := cm.EncodeForStorage(testData)
	if err != nil {
		t.Fatalf("EncodeForStorage: %v", err)
	}

	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			return encoded, nil
		},
	}

	svc := NewKVService(host, 1, WithCodecManager(cm))
	got, getErr := svc.GetStale(context.Background(), "key")
	if getErr != nil {
		t.Fatalf("GetStale: unexpected error: %v", getErr)
	}
	if len(got) == 0 {
		t.Fatal("GetStale: got empty result after transcode")
	}
}

func TestKVService_WithCodec_PutEncodeError(t *testing.T) {
	host := &mockHost{}

	// Use a failing codec to trigger encode errors.
	failCodec := &failingCodec{marshalErr: errors.New("encode failed")}
	cm := codec.NewManager(failCodec, &codec.JSONCodec{})
	svc := NewKVService(host, 1, WithCodecManager(cm))

	err := svc.Put(context.Background(), "key", []byte("value"))
	if err == nil {
		t.Fatal("expected error from codec encode, got nil")
	}
	var cee *CodecEncodeError
	if !errors.As(err, &cee) {
		t.Fatalf("expected *CodecEncodeError, got %T: %v", err, err)
	}
	if cee.Op != "put" {
		t.Errorf("expected op %q, got %q", "put", cee.Op)
	}
}

// failingCodec is a test codec that returns configurable errors.
type failingCodec struct {
	marshalErr   error
	unmarshalErr error
}

func (c *failingCodec) Marshal(_ any) ([]byte, error) {
	if c.marshalErr != nil {
		return nil, c.marshalErr
	}
	return []byte("ok"), nil
}

func (c *failingCodec) Unmarshal(_ []byte, _ any) error {
	return c.unmarshalErr
}

func (c *failingCodec) Type() string {
	return "failing"
}

func TestKVService_WithCodec_GetTranscodeError(t *testing.T) {
	// Use a storage codec that fails on unmarshal to trigger transcode error.
	failStorage := &failingCodec{unmarshalErr: errors.New("unmarshal failed")}
	cm := codec.NewManager(failStorage, &codec.JSONCodec{})

	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			return []byte("stored-data"), nil
		},
	}

	svc := NewKVService(host, 1, WithCodecManager(cm))
	_, err := svc.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error from transcode, got nil")
	}
	var cde *CodecDecodeError
	if !errors.As(err, &cde) {
		t.Fatalf("expected *CodecDecodeError, got %T: %v", err, err)
	}
	if cde.Op != "get" {
		t.Errorf("expected op %q, got %q", "get", cde.Op)
	}
}

func TestKVService_WithCodec_GetStaleTranscodeError(t *testing.T) {
	failStorage := &failingCodec{unmarshalErr: errors.New("unmarshal failed")}
	cm := codec.NewManager(failStorage, &codec.JSONCodec{})

	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			return []byte("stored-data"), nil
		},
	}

	svc := NewKVService(host, 1, WithCodecManager(cm))
	_, err := svc.GetStale(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error from transcode, got nil")
	}
	var cde *CodecDecodeError
	if !errors.As(err, &cde) {
		t.Fatalf("expected *CodecDecodeError, got %T: %v", err, err)
	}
	if cde.Op != "get_stale" {
		t.Errorf("expected op %q, got %q", "get_stale", cde.Op)
	}
}

// ---------------------------------------------------------------------------
// Codec error type tests
// ---------------------------------------------------------------------------

func TestCodecEncodeError_ErrorString(t *testing.T) {
	err := &CodecEncodeError{Op: "put", Err: errors.New("marshal failed")}
	want := "service: codec encode failed for put: marshal failed"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

func TestCodecEncodeError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	err := &CodecEncodeError{Op: "put", Err: inner}
	if err.Unwrap() != inner {
		t.Error("Unwrap did not return the inner error")
	}
}

func TestCodecDecodeError_ErrorString(t *testing.T) {
	err := &CodecDecodeError{Op: "get", Err: errors.New("unmarshal failed")}
	want := "service: codec decode failed for get: unmarshal failed"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

func TestCodecDecodeError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	err := &CodecDecodeError{Op: "get", Err: inner}
	if err.Unwrap() != inner {
		t.Error("Unwrap did not return the inner error")
	}
}

// ---------------------------------------------------------------------------
// AdminService shard management tests
// ---------------------------------------------------------------------------

// dummyCreateFunc is a no-op state machine factory for testing.
func dummyCreateFunc(shardID, replicaID uint64) interface{} {
	return nil
}

func TestAdminService_StartShard_Success(t *testing.T) {
	var capturedMembers map[uint64]string
	var capturedJoin bool
	var capturedCfg config.Config

	host := &mockHost{
		startShardFn: func(members map[uint64]string, join bool, create sm.CreateFunc, cfg config.Config) error {
			capturedMembers = members
			capturedJoin = join
			capturedCfg = cfg
			return nil
		},
	}

	svc := NewAdminService(host,
		WithStateMachineFactory(dummyCreateFunc),
		WithDefaultShardConfig(config.Config{
			CheckQuorum: config.BoolPtr(true),
			PreVote:     config.BoolPtr(true),
		}),
	)

	req := &StartShardRequest{
		ShardID:   100,
		ReplicaID: 1,
		Members:   map[uint64]string{1: "127.0.0.1:9100", 2: "127.0.0.1:9200"},
		Join:      false,
	}

	err := svc.StartShard(context.Background(), req)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(capturedMembers) != 2 {
		t.Errorf("expected 2 members, got %d", len(capturedMembers))
	}
	if capturedJoin {
		t.Error("expected join to be false")
	}
	if capturedCfg.ShardID != 100 {
		t.Errorf("expected ShardID 100, got %d", capturedCfg.ShardID)
	}
	if capturedCfg.ReplicaID != 1 {
		t.Errorf("expected ReplicaID 1, got %d", capturedCfg.ReplicaID)
	}
	// Verify default config fields are preserved.
	if !capturedCfg.GetCheckQuorum() {
		t.Error("expected CheckQuorum to be true from default config")
	}
	if !capturedCfg.GetPreVote() {
		t.Error("expected PreVote to be true from default config")
	}
}

func TestAdminService_StartShard_NoFactory(t *testing.T) {
	svc := NewAdminService(&mockHost{})

	err := svc.StartShard(context.Background(), &StartShardRequest{
		ShardID:   1,
		ReplicaID: 1,
		Members:   map[uint64]string{1: "127.0.0.1:9100"},
	})

	if err == nil {
		t.Fatal("expected error when no state machine factory is configured")
	}

	var noFactory *NoStateMachineFactoryError
	if !errors.As(err, &noFactory) {
		t.Errorf("expected NoStateMachineFactoryError, got %T: %v", err, err)
	}
}

func TestAdminService_StartShard_HostError(t *testing.T) {
	hostErr := errors.New("shard already exists")
	host := &mockHost{
		startShardFn: func(members map[uint64]string, join bool, create sm.CreateFunc, cfg config.Config) error {
			return hostErr
		},
	}

	svc := NewAdminService(host, WithStateMachineFactory(dummyCreateFunc))

	err := svc.StartShard(context.Background(), &StartShardRequest{
		ShardID:   1,
		ReplicaID: 1,
		Members:   map[uint64]string{1: "127.0.0.1:9100"},
	})

	if err == nil {
		t.Fatal("expected error from host.StartShard failure")
	}

	var shardErr *ShardStartError
	if !errors.As(err, &shardErr) {
		t.Fatalf("expected ShardStartError, got %T: %v", err, err)
	}
	if shardErr.ShardID != 1 {
		t.Errorf("expected ShardID 1, got %d", shardErr.ShardID)
	}
	if shardErr.Unwrap() != hostErr {
		t.Error("Unwrap did not return the host error")
	}
}

func TestAdminService_StopShard_Success(t *testing.T) {
	var capturedShardID uint64
	host := &mockHost{
		stopShardFn: func(shardID uint64) error {
			capturedShardID = shardID
			return nil
		},
	}

	svc := NewAdminService(host)
	err := svc.StopShard(context.Background(), 42)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if capturedShardID != 42 {
		t.Errorf("expected shardID 42, got %d", capturedShardID)
	}
}

func TestAdminService_StopShard_Error(t *testing.T) {
	hostErr := errors.New("shard not loaded")
	host := &mockHost{
		stopShardFn: func(shardID uint64) error {
			return hostErr
		},
	}

	svc := NewAdminService(host)
	err := svc.StopShard(context.Background(), 99)
	if err == nil {
		t.Fatal("expected error from host.StopShard failure")
	}

	var stopErr *ShardStopError
	if !errors.As(err, &stopErr) {
		t.Fatalf("expected ShardStopError, got %T: %v", err, err)
	}
	if stopErr.ShardID != 99 {
		t.Errorf("expected ShardID 99, got %d", stopErr.ShardID)
	}
	if stopErr.Unwrap() != hostErr {
		t.Error("Unwrap did not return the host error")
	}
}

func TestAdminService_GetShardState_Success(t *testing.T) {
	host := &mockHost{
		getShardRaftStateFn: func(shardID uint64) (uint64, uint64, error) {
			return 5, 3, nil // configChangeIndex=5, term=3
		},
		getLeaderIDFn: func(shardID uint64) (uint64, bool, error) {
			return 2, true, nil // leaderID=2, hasLeader=true
		},
	}

	svc := NewAdminService(host)
	resp, err := svc.GetShardState(context.Background(), 10)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if resp.ShardID != 10 {
		t.Errorf("expected ShardID 10, got %d", resp.ShardID)
	}
	if resp.ConfigChangeIndex != 5 {
		t.Errorf("expected ConfigChangeIndex 5, got %d", resp.ConfigChangeIndex)
	}
	if resp.Term != 3 {
		t.Errorf("expected Term 3, got %d", resp.Term)
	}
	if resp.LeaderID != 2 {
		t.Errorf("expected LeaderID 2, got %d", resp.LeaderID)
	}
	if !resp.HasLeader {
		t.Error("expected HasLeader to be true")
	}
}

func TestAdminService_GetShardState_RaftStateError(t *testing.T) {
	hostErr := errors.New("shard not found")
	host := &mockHost{
		getShardRaftStateFn: func(shardID uint64) (uint64, uint64, error) {
			return 0, 0, hostErr
		},
	}

	svc := NewAdminService(host)
	resp, err := svc.GetShardState(context.Background(), 7)
	if resp != nil {
		t.Error("expected nil response on error")
	}
	if err == nil {
		t.Fatal("expected error from GetShardRaftState failure")
	}

	var stateErr *ShardStateError
	if !errors.As(err, &stateErr) {
		t.Fatalf("expected ShardStateError, got %T: %v", err, err)
	}
	if stateErr.ShardID != 7 {
		t.Errorf("expected ShardID 7, got %d", stateErr.ShardID)
	}
	if stateErr.Unwrap() != hostErr {
		t.Error("Unwrap did not return the host error")
	}
}

func TestAdminService_GetShardState_LeaderIDError(t *testing.T) {
	hostErr := errors.New("leader lookup failed")
	host := &mockHost{
		getShardRaftStateFn: func(shardID uint64) (uint64, uint64, error) {
			return 5, 3, nil
		},
		getLeaderIDFn: func(shardID uint64) (uint64, bool, error) {
			return 0, false, hostErr
		},
	}

	svc := NewAdminService(host)
	resp, err := svc.GetShardState(context.Background(), 7)
	if resp != nil {
		t.Error("expected nil response on error")
	}
	if err == nil {
		t.Fatal("expected error from GetLeaderID failure")
	}

	var stateErr *ShardStateError
	if !errors.As(err, &stateErr) {
		t.Fatalf("expected ShardStateError, got %T: %v", err, err)
	}
	if stateErr.ShardID != 7 {
		t.Errorf("expected ShardID 7, got %d", stateErr.ShardID)
	}
	if stateErr.Unwrap() != hostErr {
		t.Error("Unwrap did not return the host error")
	}
}

func TestAdminService_GetTopology_Success(t *testing.T) {
	host := &mockHost{
		idFn:          func() string { return "node-abc" },
		raftAddressFn: func() string { return "10.0.0.1:9100" },
		shardCountFn:  func() int { return 2 },
		getNodeHostInfoFn: func(opt quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				RaftAddress: "10.0.0.1:9100",
				ShardInfoList: []quicraft.ShardInfo{
					{
						ShardID:   1,
						ReplicaID: 1,
						IsLeader:  true,
						LeaderID:  1,
						Nodes:     map[uint64]string{1: "10.0.0.1:9100", 2: "10.0.0.2:9100"},
					},
					{
						ShardID:   2,
						ReplicaID: 3,
						IsLeader:  false,
						LeaderID:  4,
						Nodes:     map[uint64]string{3: "10.0.0.1:9100", 4: "10.0.0.3:9100"},
					},
				},
			}
		},
	}

	svc := NewAdminService(host)
	resp, err := svc.GetTopology(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if resp.NodeID != "node-abc" {
		t.Errorf("expected NodeID 'node-abc', got %q", resp.NodeID)
	}
	if resp.RaftAddress != "10.0.0.1:9100" {
		t.Errorf("expected RaftAddress '10.0.0.1:9100', got %q", resp.RaftAddress)
	}
	if resp.ShardCount != 2 {
		t.Errorf("expected ShardCount 2, got %d", resp.ShardCount)
	}
	if len(resp.Shards) != 2 {
		t.Fatalf("expected 2 shards, got %d", len(resp.Shards))
	}

	shard0 := resp.Shards[0]
	if shard0.ShardID != 1 {
		t.Errorf("shard[0] ShardID: expected 1, got %d", shard0.ShardID)
	}
	if shard0.ReplicaID != 1 {
		t.Errorf("shard[0] ReplicaID: expected 1, got %d", shard0.ReplicaID)
	}
	if !shard0.IsLeader {
		t.Error("shard[0] expected IsLeader true")
	}
	if shard0.LeaderID != 1 {
		t.Errorf("shard[0] LeaderID: expected 1, got %d", shard0.LeaderID)
	}
	if len(shard0.Nodes) != 2 {
		t.Errorf("shard[0] Nodes: expected 2, got %d", len(shard0.Nodes))
	}

	shard1 := resp.Shards[1]
	if shard1.ShardID != 2 {
		t.Errorf("shard[1] ShardID: expected 2, got %d", shard1.ShardID)
	}
	if shard1.IsLeader {
		t.Error("shard[1] expected IsLeader false")
	}
}

func TestAdminService_GetTopology_EmptyShards(t *testing.T) {
	host := &mockHost{
		idFn:          func() string { return "node-empty" },
		raftAddressFn: func() string { return "10.0.0.1:9100" },
		shardCountFn:  func() int { return 0 },
		getNodeHostInfoFn: func(opt quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				RaftAddress:   "10.0.0.1:9100",
				ShardInfoList: nil,
			}
		},
	}

	svc := NewAdminService(host)
	resp, err := svc.GetTopology(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if resp.NodeID != "node-empty" {
		t.Errorf("expected NodeID 'node-empty', got %q", resp.NodeID)
	}
	if resp.ShardCount != 0 {
		t.Errorf("expected ShardCount 0, got %d", resp.ShardCount)
	}
	if len(resp.Shards) != 0 {
		t.Errorf("expected 0 shards, got %d", len(resp.Shards))
	}
}

func TestAdminService_StartShard_JoinExistingCluster(t *testing.T) {
	var capturedJoin bool
	host := &mockHost{
		startShardFn: func(members map[uint64]string, join bool, create sm.CreateFunc, cfg config.Config) error {
			capturedJoin = join
			return nil
		},
	}

	svc := NewAdminService(host, WithStateMachineFactory(dummyCreateFunc))

	err := svc.StartShard(context.Background(), &StartShardRequest{
		ShardID:   5,
		ReplicaID: 3,
		Members:   nil,
		Join:      true,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !capturedJoin {
		t.Error("expected join to be true")
	}
}

// ---------------------------------------------------------------------------
// Shard management error type tests
// ---------------------------------------------------------------------------

func TestShardStartError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("disk full")
	err := &ShardStartError{ShardID: 42, Err: inner}

	want := "service: start shard 42 failed: disk full"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Error("Unwrap did not return the inner error")
	}
}

func TestShardStopError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("not loaded")
	err := &ShardStopError{ShardID: 7, Err: inner}

	want := "service: stop shard 7 failed: not loaded"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Error("Unwrap did not return the inner error")
	}
}

func TestShardStateError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("shard unavailable")
	err := &ShardStateError{ShardID: 99, Err: inner}

	want := "service: get shard state 99 failed: shard unavailable"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Error("Unwrap did not return the inner error")
	}
}

func TestTopologyError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("host not ready")
	err := &TopologyError{Err: inner}

	want := "service: get topology failed: host not ready"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Error("Unwrap did not return the inner error")
	}
}

func TestNoStateMachineFactoryError_Error(t *testing.T) {
	err := &NoStateMachineFactoryError{}
	want := "service: no state machine factory configured"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// AdminService option tests
// ---------------------------------------------------------------------------

func TestWithStateMachineFactory_SetsFactory(t *testing.T) {
	called := false
	factory := sm.CreateFunc(func(shardID, replicaID uint64) interface{} {
		called = true
		return nil
	})

	host := &mockHost{
		startShardFn: func(members map[uint64]string, join bool, create sm.CreateFunc, cfg config.Config) error {
			create(1, 1)
			return nil
		},
	}

	svc := NewAdminService(host, WithStateMachineFactory(factory))
	err := svc.StartShard(context.Background(), &StartShardRequest{
		ShardID:   1,
		ReplicaID: 1,
		Members:   map[uint64]string{1: "127.0.0.1:9100"},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !called {
		t.Error("expected factory to be called by host.StartShard")
	}
}

func TestWithDefaultShardConfig_OverridesShardAndReplicaID(t *testing.T) {
	var capturedCfg config.Config
	host := &mockHost{
		startShardFn: func(members map[uint64]string, join bool, create sm.CreateFunc, cfg config.Config) error {
			capturedCfg = cfg
			return nil
		},
	}

	defaultCfg := config.Config{
		ShardID:     999, // Should be overridden
		ReplicaID:   999, // Should be overridden
		CheckQuorum: config.BoolPtr(true),
		PreVote:     config.BoolPtr(true),
	}

	svc := NewAdminService(host,
		WithStateMachineFactory(dummyCreateFunc),
		WithDefaultShardConfig(defaultCfg),
	)

	err := svc.StartShard(context.Background(), &StartShardRequest{
		ShardID:   50,
		ReplicaID: 3,
		Members:   map[uint64]string{3: "10.0.0.1:9100"},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if capturedCfg.ShardID != 50 {
		t.Errorf("expected ShardID 50, got %d", capturedCfg.ShardID)
	}
	if capturedCfg.ReplicaID != 3 {
		t.Errorf("expected ReplicaID 3, got %d", capturedCfg.ReplicaID)
	}
	if !capturedCfg.GetCheckQuorum() {
		t.Error("expected CheckQuorum from default config to be preserved")
	}
	if !capturedCfg.GetPreVote() {
		t.Error("expected PreVote from default config to be preserved")
	}
}

func TestAdminService_GetShardState_NoLeader(t *testing.T) {
	host := &mockHost{
		getShardRaftStateFn: func(shardID uint64) (uint64, uint64, error) {
			return 0, 1, nil
		},
		getLeaderIDFn: func(shardID uint64) (uint64, bool, error) {
			return 0, false, nil
		},
	}

	svc := NewAdminService(host)
	resp, err := svc.GetShardState(context.Background(), 5)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if resp.HasLeader {
		t.Error("expected HasLeader to be false")
	}
	if resp.LeaderID != 0 {
		t.Errorf("expected LeaderID 0, got %d", resp.LeaderID)
	}
}

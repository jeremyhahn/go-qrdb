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

//go:build !integration

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// mockQuicraftHost satisfies the quicraftHost interface for unit testing.
// ---------------------------------------------------------------------------

type mockQuicraftHost struct {
	syncProposeFn            func(ctx context.Context, shardID uint64, cmd []byte) (sm.Result, error)
	syncReadFn               func(ctx context.Context, shardID uint64, query interface{}) (interface{}, error)
	staleReadFn              func(ctx context.Context, shardID uint64, query interface{}) (interface{}, error)
	followerReadFn           func(ctx context.Context, shardID uint64, query interface{}) (interface{}, error)
	syncGetShardMembershipFn func(ctx context.Context, shardID uint64) (*quicraft.Membership, error)
	requestSnapshotFn        func(ctx context.Context, shardID uint64, opt quicraft.SnapshotOption) (*quicraft.RequestState, error)
	isShardJoiningFn         func(shardID uint64) bool
}

func (m *mockQuicraftHost) SyncPropose(ctx context.Context, shardID uint64, cmd []byte) (sm.Result, error) {
	if m.syncProposeFn != nil {
		return m.syncProposeFn(ctx, shardID, cmd)
	}
	return sm.Result{}, nil
}

func (m *mockQuicraftHost) SyncRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	if m.syncReadFn != nil {
		return m.syncReadFn(ctx, shardID, query)
	}
	return nil, nil
}

func (m *mockQuicraftHost) StaleRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	if m.staleReadFn != nil {
		return m.staleReadFn(ctx, shardID, query)
	}
	return nil, nil
}

func (m *mockQuicraftHost) FollowerRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	if m.followerReadFn != nil {
		return m.followerReadFn(ctx, shardID, query)
	}
	return nil, nil
}

func (m *mockQuicraftHost) SyncGetShardMembership(ctx context.Context, shardID uint64) (*quicraft.Membership, error) {
	if m.syncGetShardMembershipFn != nil {
		return m.syncGetShardMembershipFn(ctx, shardID)
	}
	return &quicraft.Membership{ConfigChangeID: 0}, nil
}

func (m *mockQuicraftHost) GetShardMembership(shardID uint64) (*quicraft.Membership, error) {
	if m.syncGetShardMembershipFn != nil {
		return m.syncGetShardMembershipFn(context.Background(), shardID)
	}
	return &quicraft.Membership{ConfigChangeID: 0}, nil
}

func (m *mockQuicraftHost) RequestSnapshot(ctx context.Context, shardID uint64, opt quicraft.SnapshotOption) (*quicraft.RequestState, error) {
	if m.requestSnapshotFn != nil {
		return m.requestSnapshotFn(ctx, shardID, opt)
	}
	return &quicraft.RequestState{}, nil
}

func (m *mockQuicraftHost) IsShardJoining(shardID uint64) bool {
	if m.isShardJoiningFn != nil {
		return m.isShardJoiningFn(shardID)
	}
	return false
}

// newTestHostAdapter creates a hostAdapter with a mock inner host.
// The Host field is left nil since test code only calls adapter methods
// that delegate to inner (not promoted Host methods).
func newTestHostAdapter(inner quicraftHost) *hostAdapter {
	return raftbackend.NewWithInner(nil, inner)
}

// ---------------------------------------------------------------------------
// ProposeWithMode tests
// ---------------------------------------------------------------------------

func TestHostAdapter_ProposeWithMode_Sync(t *testing.T) {
	var capturedCmd []byte
	mock := &mockQuicraftHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			capturedCmd = cmd
			return sm.Result{Value: 42, Data: []byte("ok")}, nil
		},
	}
	adapter := newTestHostAdapter(mock)

	result, err := adapter.ProposeWithMode(context.Background(), 1, []byte("cmd1"), writemode.Sync)
	require.NoError(t, err)
	assert.Equal(t, uint64(42), result.Value)
	assert.Equal(t, []byte("ok"), result.Data)
	assert.Equal(t, []byte("cmd1"), capturedCmd)
}

func TestHostAdapter_ProposeWithMode_WALDurable(t *testing.T) {
	var called bool
	mock := &mockQuicraftHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			called = true
			return sm.Result{Value: 10}, nil
		},
	}
	adapter := newTestHostAdapter(mock)

	result, err := adapter.ProposeWithMode(context.Background(), 1, []byte("wal"), writemode.WALDurable)
	require.NoError(t, err)
	assert.True(t, called, "WALDurable should fall through to SyncPropose")
	assert.Equal(t, uint64(10), result.Value)
}

func TestHostAdapter_ProposeWithMode_Batched(t *testing.T) {
	var called bool
	mock := &mockQuicraftHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			called = true
			return sm.Result{Value: 20}, nil
		},
	}
	adapter := newTestHostAdapter(mock)

	result, err := adapter.ProposeWithMode(context.Background(), 2, []byte("batch"), writemode.Batched)
	require.NoError(t, err)
	assert.True(t, called, "Batched should fall through to SyncPropose")
	assert.Equal(t, uint64(20), result.Value)
}

func TestHostAdapter_ProposeWithMode_DefaultMode(t *testing.T) {
	var called bool
	mock := &mockQuicraftHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			called = true
			return sm.Result{Value: 99}, nil
		},
	}
	adapter := newTestHostAdapter(mock)

	// Use an unrecognized mode value to exercise the default branch.
	result, err := adapter.ProposeWithMode(context.Background(), 1, []byte("x"), writemode.Mode(255))
	require.NoError(t, err)
	assert.True(t, called, "default case should fall through to SyncPropose")
	assert.Equal(t, uint64(99), result.Value)
}

func TestHostAdapter_ProposeWithMode_SyncError(t *testing.T) {
	sentinel := errors.New("raft timeout")
	mock := &mockQuicraftHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}
	adapter := newTestHostAdapter(mock)

	_, err := adapter.ProposeWithMode(context.Background(), 1, []byte("fail"), writemode.Sync)
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
}

func TestHostAdapter_ProposeWithMode_WALDurableError(t *testing.T) {
	sentinel := errors.New("propose failed")
	mock := &mockQuicraftHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}
	adapter := newTestHostAdapter(mock)

	_, err := adapter.ProposeWithMode(context.Background(), 1, []byte("fail"), writemode.WALDurable)
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
}

// ---------------------------------------------------------------------------
// StaleRead tests
// ---------------------------------------------------------------------------

func TestHostAdapter_StaleRead_Success(t *testing.T) {
	mock := &mockQuicraftHost{
		staleReadFn: func(_ context.Context, shardID uint64, query interface{}) (interface{}, error) {
			return map[string]interface{}{
				"shard": shardID,
				"query": query,
			}, nil
		},
	}
	adapter := newTestHostAdapter(mock)

	result, err := adapter.StaleRead(context.Background(), 5, "lookup-key")
	require.NoError(t, err)
	m, ok := result.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, uint64(5), m["shard"])
	assert.Equal(t, "lookup-key", m["query"])
}

func TestHostAdapter_StaleRead_Error(t *testing.T) {
	sentinel := errors.New("closed")
	mock := &mockQuicraftHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}
	adapter := newTestHostAdapter(mock)

	_, err := adapter.StaleRead(context.Background(), 1, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
}

// ---------------------------------------------------------------------------
// SyncRead tests (on hostAdapter)
// ---------------------------------------------------------------------------

func TestHostAdapter_SyncRead_Success(t *testing.T) {
	mock := &mockQuicraftHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			return []byte("data-" + query.(string)), nil
		},
	}
	adapter := newTestHostAdapter(mock)

	result, err := adapter.SyncRead(context.Background(), 1, "key1")
	require.NoError(t, err)
	assert.Equal(t, []byte("data-key1"), result)
}

func TestHostAdapter_SyncRead_Error(t *testing.T) {
	sentinel := errors.New("read index timeout")
	mock := &mockQuicraftHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}
	adapter := newTestHostAdapter(mock)

	_, err := adapter.SyncRead(context.Background(), 1, "key2")
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
}

// ---------------------------------------------------------------------------
// ReadWithConsistency tests
// ---------------------------------------------------------------------------

func TestHostAdapter_ReadWithConsistency_AllowStale(t *testing.T) {
	var staleReadCalled bool
	mock := &mockQuicraftHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			staleReadCalled = true
			return "stale-data", nil
		},
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			t.Fatal("SyncRead should not be called when AllowStale is true")
			return nil, nil
		},
	}
	adapter := newTestHostAdapter(mock)

	opts := consistency.ReadOptions{AllowStale: true}
	result, err := adapter.ReadWithConsistency(context.Background(), 1, "q", opts)
	require.NoError(t, err)
	assert.True(t, staleReadCalled)
	assert.Equal(t, "stale-data", result)
}

func TestHostAdapter_ReadWithConsistency_Strong(t *testing.T) {
	var syncReadCalled bool
	mock := &mockQuicraftHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			t.Fatal("StaleRead should not be called when AllowStale is false")
			return nil, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			syncReadCalled = true
			return "strong-data", nil
		},
	}
	adapter := newTestHostAdapter(mock)

	opts := consistency.ReadOptions{AllowStale: false}
	result, err := adapter.ReadWithConsistency(context.Background(), 1, "q", opts)
	require.NoError(t, err)
	assert.True(t, syncReadCalled)
	assert.Equal(t, "strong-data", result)
}

func TestHostAdapter_ReadWithConsistency_StaleError(t *testing.T) {
	sentinel := errors.New("stale read failed")
	mock := &mockQuicraftHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}
	adapter := newTestHostAdapter(mock)

	opts := consistency.ReadOptions{AllowStale: true}
	_, err := adapter.ReadWithConsistency(context.Background(), 1, "q", opts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
}

func TestHostAdapter_ReadWithConsistency_StrongError(t *testing.T) {
	sentinel := errors.New("sync read failed")
	mock := &mockQuicraftHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}
	adapter := newTestHostAdapter(mock)

	opts := consistency.ReadOptions{AllowStale: false}
	_, err := adapter.ReadWithConsistency(context.Background(), 1, "q", opts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
}

// ---------------------------------------------------------------------------
// GetShardRaftState tests
// ---------------------------------------------------------------------------

func TestHostAdapter_GetShardRaftState_Success(t *testing.T) {
	mock := &mockQuicraftHost{
		syncGetShardMembershipFn: func(_ context.Context, _ uint64) (*quicraft.Membership, error) {
			return &quicraft.Membership{
				ConfigChangeID: 42,
				Nodes:          map[uint64]string{1: "127.0.0.1:63001"},
			}, nil
		},
	}
	adapter := newTestHostAdapter(mock)

	ccIndex, term, err := adapter.GetShardRaftState(1)
	require.NoError(t, err)
	assert.Equal(t, uint64(42), ccIndex)
	assert.Equal(t, uint64(0), term)
}

func TestHostAdapter_GetShardRaftState_Error(t *testing.T) {
	sentinel := errors.New("shard not found")
	mock := &mockQuicraftHost{
		syncGetShardMembershipFn: func(_ context.Context, _ uint64) (*quicraft.Membership, error) {
			return nil, sentinel
		},
	}
	adapter := newTestHostAdapter(mock)

	ccIndex, term, err := adapter.GetShardRaftState(99)
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
	assert.Equal(t, uint64(0), ccIndex)
	assert.Equal(t, uint64(0), term)
}

func TestHostAdapter_GetShardRaftState_ZeroConfigChange(t *testing.T) {
	mock := &mockQuicraftHost{
		syncGetShardMembershipFn: func(_ context.Context, _ uint64) (*quicraft.Membership, error) {
			return &quicraft.Membership{ConfigChangeID: 0}, nil
		},
	}
	adapter := newTestHostAdapter(mock)

	ccIndex, term, err := adapter.GetShardRaftState(1)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), ccIndex)
	assert.Equal(t, uint64(0), term)
}

// ---------------------------------------------------------------------------
// bootstrapHostAdapter.SyncRead tests
// ---------------------------------------------------------------------------

func TestBootstrapHostAdapter_SyncRead_DelegatesToStaleRead(t *testing.T) {
	var staleReadCalled bool
	mock := &mockQuicraftHost{
		staleReadFn: func(_ context.Context, shardID uint64, query interface{}) (interface{}, error) {
			staleReadCalled = true
			return "stale-result", nil
		},
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			t.Fatal("bootstrapHostAdapter.SyncRead should not call inner.SyncRead")
			return nil, nil
		},
	}
	adapter := newTestHostAdapter(mock)
	boot := newBootstrapHostAdapter(adapter)

	result, err := boot.SyncRead(context.Background(), 1, "query")
	require.NoError(t, err)
	assert.True(t, staleReadCalled)
	assert.Equal(t, "stale-result", result)
}

func TestBootstrapHostAdapter_SyncRead_Error(t *testing.T) {
	sentinel := errors.New("stale read broken")
	mock := &mockQuicraftHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}
	adapter := newTestHostAdapter(mock)
	boot := newBootstrapHostAdapter(adapter)

	_, err := boot.SyncRead(context.Background(), 1, "q")
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
}

// ---------------------------------------------------------------------------
// newHostAdapter constructor test
// ---------------------------------------------------------------------------

func TestNewHostAdapter_NilHostPanicsOnPromotedMethods(t *testing.T) {
	// newHostAdapter with a real host is tested via initializeNode in
	// integration tests. Here we verify the constructor wires inner.
	// We cannot pass nil to newHostAdapter since it dereferences the
	// host, so test with the mock-based constructor instead.
	mock := &mockQuicraftHost{}
	adapter := newTestHostAdapter(mock)
	require.NotNil(t, adapter)
}

// ---------------------------------------------------------------------------
// ProposeWithMode: verify shardID is passed through
// ---------------------------------------------------------------------------

func TestHostAdapter_ProposeWithMode_PassesShardID(t *testing.T) {
	var capturedShardID uint64
	mock := &mockQuicraftHost{
		syncProposeFn: func(_ context.Context, shardID uint64, _ []byte) (sm.Result, error) {
			capturedShardID = shardID
			return sm.Result{}, nil
		},
	}
	adapter := newTestHostAdapter(mock)

	_, err := adapter.ProposeWithMode(context.Background(), 7, []byte("x"), writemode.Sync)
	require.NoError(t, err)
	assert.Equal(t, uint64(7), capturedShardID)
}

// ---------------------------------------------------------------------------
// FollowerRead tests
// ---------------------------------------------------------------------------

func TestHostAdapter_FollowerRead_Success(t *testing.T) {
	mock := &mockQuicraftHost{
		followerReadFn: func(_ context.Context, shardID uint64, query interface{}) (interface{}, error) {
			return map[string]interface{}{
				"shard": shardID,
				"query": query,
			}, nil
		},
	}
	adapter := newTestHostAdapter(mock)

	result, err := adapter.FollowerRead(context.Background(), 3, "follower-key")
	require.NoError(t, err)
	m, ok := result.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, uint64(3), m["shard"])
	assert.Equal(t, "follower-key", m["query"])
}

func TestHostAdapter_FollowerRead_Error(t *testing.T) {
	sentinel := errors.New("follower read failed")
	mock := &mockQuicraftHost{
		followerReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}
	adapter := newTestHostAdapter(mock)

	_, err := adapter.FollowerRead(context.Background(), 1, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
}

// ---------------------------------------------------------------------------
// RequestSnapshot tests
// ---------------------------------------------------------------------------

func TestHostAdapter_RequestSnapshot_Success(t *testing.T) {
	var capturedShardID uint64
	mock := &mockQuicraftHost{
		requestSnapshotFn: func(_ context.Context, shardID uint64, opt quicraft.SnapshotOption) (*quicraft.RequestState, error) {
			capturedShardID = shardID
			return nil, nil
		},
	}
	adapter := newTestHostAdapter(mock)

	_, err := adapter.RequestSnapshot(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, uint64(5), capturedShardID)
}

func TestHostAdapter_RequestSnapshot_Error(t *testing.T) {
	sentinel := errors.New("snapshot failed")
	mock := &mockQuicraftHost{
		requestSnapshotFn: func(_ context.Context, _ uint64, _ quicraft.SnapshotOption) (*quicraft.RequestState, error) {
			return nil, sentinel
		},
	}
	adapter := newTestHostAdapter(mock)

	_, err := adapter.RequestSnapshot(context.Background(), 1)
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
}

// ---------------------------------------------------------------------------
// ReadWithConsistency: additional consistency level branches
// ---------------------------------------------------------------------------

func TestHostAdapter_ReadWithConsistency_Eventual(t *testing.T) {
	var staleReadCalled bool
	mock := &mockQuicraftHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			staleReadCalled = true
			return "eventual-data", nil
		},
	}
	adapter := newTestHostAdapter(mock)

	opts := consistency.ReadOptions{Consistency: consistency.Eventual}
	result, err := adapter.ReadWithConsistency(context.Background(), 1, "q", opts)
	require.NoError(t, err)
	assert.True(t, staleReadCalled)
	assert.Equal(t, "eventual-data", result)
}

func TestHostAdapter_ReadWithConsistency_FollowerRead(t *testing.T) {
	var followerReadCalled bool
	mock := &mockQuicraftHost{
		followerReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			followerReadCalled = true
			return "follower-data", nil
		},
	}
	adapter := newTestHostAdapter(mock)

	opts := consistency.ReadOptions{Consistency: consistency.FollowerRead}
	result, err := adapter.ReadWithConsistency(context.Background(), 1, "q", opts)
	require.NoError(t, err)
	assert.True(t, followerReadCalled)
	assert.Equal(t, "follower-data", result)
}

func TestHostAdapter_ReadWithConsistency_Linearizable(t *testing.T) {
	var syncReadCalled bool
	mock := &mockQuicraftHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			syncReadCalled = true
			return "linearizable-data", nil
		},
	}
	adapter := newTestHostAdapter(mock)

	opts := consistency.ReadOptions{Consistency: consistency.Linearizable}
	result, err := adapter.ReadWithConsistency(context.Background(), 1, "q", opts)
	require.NoError(t, err)
	assert.True(t, syncReadCalled)
	assert.Equal(t, "linearizable-data", result)
}

func TestHostAdapter_ReadWithConsistency_LeaseRead(t *testing.T) {
	var syncReadCalled bool
	mock := &mockQuicraftHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			syncReadCalled = true
			return "lease-data", nil
		},
	}
	adapter := newTestHostAdapter(mock)

	opts := consistency.ReadOptions{Consistency: consistency.LeaseRead}
	result, err := adapter.ReadWithConsistency(context.Background(), 1, "q", opts)
	require.NoError(t, err)
	assert.True(t, syncReadCalled)
	assert.Equal(t, "lease-data", result)
}

func TestHostAdapter_ReadWithConsistency_DefaultFallback(t *testing.T) {
	var syncReadCalled bool
	mock := &mockQuicraftHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			syncReadCalled = true
			return "default-data", nil
		},
	}
	adapter := newTestHostAdapter(mock)

	// Use an unrecognized consistency level to exercise the default branch.
	opts := consistency.ReadOptions{Consistency: consistency.Level(99)}
	result, err := adapter.ReadWithConsistency(context.Background(), 1, "q", opts)
	require.NoError(t, err)
	assert.True(t, syncReadCalled)
	assert.Equal(t, "default-data", result)
}

func TestHostAdapter_ReadWithConsistency_FollowerRead_Error(t *testing.T) {
	sentinel := errors.New("follower read error")
	mock := &mockQuicraftHost{
		followerReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}
	adapter := newTestHostAdapter(mock)

	opts := consistency.ReadOptions{Consistency: consistency.FollowerRead}
	_, err := adapter.ReadWithConsistency(context.Background(), 1, "q", opts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
}

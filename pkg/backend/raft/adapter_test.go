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

package raft

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// mockQuicraftHost satisfies QuicraftHost for testing.
// ---------------------------------------------------------------------------

type mockQuicraftHost struct {
	syncProposeFn            func(ctx context.Context, shardID uint64, cmd []byte) (sm.Result, error)
	syncReadFn               func(ctx context.Context, shardID uint64, query interface{}) (interface{}, error)
	staleReadFn              func(ctx context.Context, shardID uint64, query interface{}) (interface{}, error)
	followerReadFn           func(ctx context.Context, shardID uint64, query interface{}) (interface{}, error)
	syncGetShardMembershipFn func(ctx context.Context, shardID uint64) (*quicraft.Membership, error)
	getShardMembershipFn     func(shardID uint64) (*quicraft.Membership, error)
	requestSnapshotFn        func(ctx context.Context, shardID uint64, opt quicraft.SnapshotOption) (*quicraft.RequestState, error)
	isShardJoiningFn         func(shardID uint64) bool
	getLeaderIDFn            func(shardID uint64) (uint64, bool, error)
}

var _ QuicraftHost = (*mockQuicraftHost)(nil)

func (m *mockQuicraftHost) SyncPropose(ctx context.Context, shardID uint64, cmd []byte) (sm.Result, error) {
	if m.syncProposeFn != nil {
		return m.syncProposeFn(ctx, shardID, cmd)
	}
	return sm.Result{Data: cmd}, nil
}

func (m *mockQuicraftHost) SyncRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	if m.syncReadFn != nil {
		return m.syncReadFn(ctx, shardID, query)
	}
	return query, nil
}

func (m *mockQuicraftHost) StaleRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	if m.staleReadFn != nil {
		return m.staleReadFn(ctx, shardID, query)
	}
	return query, nil
}

func (m *mockQuicraftHost) FollowerRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	if m.followerReadFn != nil {
		return m.followerReadFn(ctx, shardID, query)
	}
	return query, nil
}

func (m *mockQuicraftHost) SyncGetShardMembership(ctx context.Context, shardID uint64) (*quicraft.Membership, error) {
	if m.syncGetShardMembershipFn != nil {
		return m.syncGetShardMembershipFn(ctx, shardID)
	}
	return &quicraft.Membership{ConfigChangeID: 42}, nil
}

func (m *mockQuicraftHost) ShardMembership(shardID uint64) (*quicraft.Membership, error) {
	if m.getShardMembershipFn != nil {
		return m.getShardMembershipFn(shardID)
	}
	return &quicraft.Membership{ConfigChangeID: 42}, nil
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

func (m *mockQuicraftHost) LeaderID(shardID uint64) (uint64, bool, error) {
	if m.getLeaderIDFn != nil {
		return m.getLeaderIDFn(shardID)
	}
	return 1, true, nil
}

// ---------------------------------------------------------------------------
// HostAdapter tests
// ---------------------------------------------------------------------------

func newTestAdapter(mock *mockQuicraftHost) *HostAdapter {
	return NewWithInner(nil, mock)
}

func TestHostAdapter_ProposeWithMode(t *testing.T) {
	tests := []struct {
		name      string
		mode      writemode.Mode
		wantCalls int
	}{
		{"sync mode", writemode.Sync, 1},
		{"wal-durable falls through to sync", writemode.WALDurable, 1},
		{"batched falls through to sync", writemode.Batched, 1},
		{"default falls through to sync", writemode.Mode(99), 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			mock := &mockQuicraftHost{
				syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
					calls++
					return sm.Result{Data: cmd}, nil
				},
			}
			a := newTestAdapter(mock)
			cmd := []byte("test")
			res, err := a.ProposeWithMode(context.Background(), 1, cmd, tc.mode)
			require.NoError(t, err)
			assert.Equal(t, cmd, res.Data)
			assert.Equal(t, tc.wantCalls, calls)
		})
	}
}

func TestHostAdapter_StaleRead(t *testing.T) {
	called := false
	mock := &mockQuicraftHost{
		staleReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			called = true
			return query, nil
		},
	}
	a := newTestAdapter(mock)
	result, err := a.StaleRead(context.Background(), 1, "q")
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "q", result)
}

func TestHostAdapter_SyncRead(t *testing.T) {
	called := false
	mock := &mockQuicraftHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			called = true
			return query, nil
		},
	}
	a := newTestAdapter(mock)
	result, err := a.SyncRead(context.Background(), 1, "q")
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "q", result)
}

func TestHostAdapter_FollowerRead(t *testing.T) {
	called := false
	mock := &mockQuicraftHost{
		followerReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			called = true
			return query, nil
		},
	}
	a := newTestAdapter(mock)
	result, err := a.FollowerRead(context.Background(), 1, "q")
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "q", result)
}

func TestHostAdapter_ReadWithConsistency(t *testing.T) {
	tests := []struct {
		name     string
		opts     consistency.ReadOptions
		wantFunc string // which mock function is expected to be called
	}{
		{"allow-stale → StaleRead", consistency.ReadOptions{AllowStale: true}, "stale"},
		{"eventual → StaleRead", consistency.ReadOptions{Consistency: consistency.Eventual}, "stale"},
		{"follower-read → FollowerRead", consistency.ReadOptions{Consistency: consistency.FollowerRead}, "follower"},
		{"strong → SyncRead", consistency.ReadOptions{Consistency: consistency.Strong}, "sync"},
		{"linearizable → SyncRead", consistency.ReadOptions{Consistency: consistency.Linearizable}, "sync"},
		{"lease-read → SyncRead", consistency.ReadOptions{Consistency: consistency.LeaseRead}, "sync"},
		{"default → SyncRead", consistency.ReadOptions{}, "sync"},
		// An explicit non-default level wins over a stray AllowStale flag, so a
		// linearizable/lease/follower read is never silently downgraded.
		{"linearizable+allow-stale → SyncRead", consistency.ReadOptions{Consistency: consistency.Linearizable, AllowStale: true}, "sync"},
		{"lease-read+allow-stale → SyncRead", consistency.ReadOptions{Consistency: consistency.LeaseRead, AllowStale: true}, "sync"},
		{"follower-read+allow-stale → FollowerRead", consistency.ReadOptions{Consistency: consistency.FollowerRead, AllowStale: true}, "follower"},
		{"eventual+allow-stale → StaleRead", consistency.ReadOptions{Consistency: consistency.Eventual, AllowStale: true}, "stale"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var called string
			mock := &mockQuicraftHost{
				staleReadFn: func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
					called = "stale"
					return q, nil
				},
				syncReadFn: func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
					called = "sync"
					return q, nil
				},
				followerReadFn: func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
					called = "follower"
					return q, nil
				},
			}
			a := newTestAdapter(mock)
			_, err := a.ReadWithConsistency(context.Background(), 1, "q", tc.opts)
			require.NoError(t, err)
			assert.Equal(t, tc.wantFunc, called)
		})
	}
}

func TestHostAdapter_RequestSnapshot(t *testing.T) {
	called := false
	mock := &mockQuicraftHost{
		requestSnapshotFn: func(_ context.Context, shardID uint64, opt quicraft.SnapshotOption) (*quicraft.RequestState, error) {
			called = true
			return &quicraft.RequestState{}, nil
		},
	}
	a := newTestAdapter(mock)
	_, err := a.RequestSnapshot(context.Background(), 1)
	require.NoError(t, err)
	assert.True(t, called)
}

func TestHostAdapter_GetShardRaftState(t *testing.T) {
	t.Run("success returns config change id", func(t *testing.T) {
		// GetShardRaftState reads via the non-blocking ShardMembership
		// (local in-memory state), not the ReadIndex-blocking Sync variant.
		mock := &mockQuicraftHost{
			getShardMembershipFn: func(_ uint64) (*quicraft.Membership, error) {
				return &quicraft.Membership{ConfigChangeID: 77}, nil
			},
		}
		a := newTestAdapter(mock)
		ccIdx, term, err := a.GetShardRaftState(1)
		require.NoError(t, err)
		assert.Equal(t, uint64(77), ccIdx)
		assert.Equal(t, uint64(0), term)
	})

	t.Run("error propagates", func(t *testing.T) {
		mock := &mockQuicraftHost{
			getShardMembershipFn: func(_ uint64) (*quicraft.Membership, error) {
				return nil, errors.New("membership error")
			},
		}
		a := newTestAdapter(mock)
		_, _, err := a.GetShardRaftState(1)
		assert.Error(t, err)
	})
}

func TestHostAdapter_IsShardJoining(t *testing.T) {
	t.Run("delegates true", func(t *testing.T) {
		mock := &mockQuicraftHost{
			isShardJoiningFn: func(shardID uint64) bool {
				assert.Equal(t, uint64(1), shardID)
				return true
			},
		}
		a := newTestAdapter(mock)
		assert.True(t, a.IsShardJoining(1))
	})

	t.Run("delegates false", func(t *testing.T) {
		mock := &mockQuicraftHost{
			isShardJoiningFn: func(uint64) bool { return false },
		}
		a := newTestAdapter(mock)
		assert.False(t, a.IsShardJoining(1))
	})
}

func TestHostAdapter_SyncGetShardMembership(t *testing.T) {
	t.Run("returns the committed membership from inner", func(t *testing.T) {
		identity := "spki:" + strings.Repeat("ab", 32)
		mock := &mockQuicraftHost{
			syncGetShardMembershipFn: func(_ context.Context, shardID uint64) (*quicraft.Membership, error) {
				assert.Equal(t, uint64(7), shardID)
				return &quicraft.Membership{
					ConfigChangeID: 9,
					Identities:     map[uint64]string{2: identity},
				}, nil
			},
		}
		m, err := newTestAdapter(mock).SyncGetShardMembership(context.Background(), 7)
		require.NoError(t, err)
		assert.Equal(t, uint64(9), m.ConfigChangeID)
		assert.Equal(t, identity, m.Identities[2])
	})

	t.Run("error propagates", func(t *testing.T) {
		readErr := errors.New("no leader")
		mock := &mockQuicraftHost{
			syncGetShardMembershipFn: func(context.Context, uint64) (*quicraft.Membership, error) {
				return nil, readErr
			},
		}
		_, err := newTestAdapter(mock).SyncGetShardMembership(context.Background(), 7)
		assert.ErrorIs(t, err, readErr)
	})
}

func TestHostAdapter_NodeIdentity(t *testing.T) {
	t.Run("no concrete host has no identity", func(t *testing.T) {
		assert.Empty(t, newTestAdapter(&mockQuicraftHost{}).NodeIdentity())
	})

	t.Run("a host without a transport has no identity", func(t *testing.T) {
		dir := t.TempDir()
		host, err := quicraft.NewHost(quicraftconfig.HostConfig{
			WALDir:        filepath.Join(dir, "wal"),
			NodeHostDir:   filepath.Join(dir, "nodehost"),
			RaftAddress:   "127.0.0.1:63001",
			ListenAddress: "127.0.0.1:63001",
			DeploymentID:  1,
		}, quicraft.WithoutTransport(), quicraft.WithoutEncryption())
		require.NoError(t, err)
		t.Cleanup(func() { _ = host.Close() })

		assert.Empty(t, New(host).NodeIdentity())
	})
}

// ---------------------------------------------------------------------------
// BootstrapAdapter tests
// ---------------------------------------------------------------------------

func TestBootstrapAdapter_SyncReadUsesStaleRead(t *testing.T) {
	staleReadCalled := false
	syncReadCalled := false
	mock := &mockQuicraftHost{
		staleReadFn: func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
			staleReadCalled = true
			return q, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
			syncReadCalled = true
			return q, nil
		},
	}
	ba := NewBootstrapAdapter(newTestAdapter(mock))
	result, err := ba.SyncRead(context.Background(), 1, "bootstrap-query")
	require.NoError(t, err)
	assert.Equal(t, "bootstrap-query", result)
	assert.True(t, staleReadCalled, "BootstrapAdapter.SyncRead should delegate to StaleRead")
	assert.False(t, syncReadCalled, "BootstrapAdapter.SyncRead must NOT call inner.SyncRead")
}

func TestBootstrapAdapter_ProposeWithMode(t *testing.T) {
	calls := 0
	mock := &mockQuicraftHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			calls++
			return sm.Result{Data: cmd}, nil
		},
	}
	ba := NewBootstrapAdapter(newTestAdapter(mock))
	cmd := []byte("write")
	res, err := ba.ProposeWithMode(context.Background(), 1, cmd, writemode.Sync)
	require.NoError(t, err)
	assert.Equal(t, cmd, res.Data)
	assert.Equal(t, 1, calls)
}

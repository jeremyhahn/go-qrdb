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
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Mock types for startDynamicMembership success-path test
// ---------------------------------------------------------------------------

// mockLeaseServicer implements lease.LeaseServicer (= service.LeaseServicer).
// Grant returns a valid lease; Revoke and KeepAlive succeed silently.
type mockLeaseServicer struct{}

func (m *mockLeaseServicer) Grant(_ context.Context, ttl int64) (*lease.Lease, error) {
	return &lease.Lease{ID: 42, TTL: ttl}, nil
}

func (m *mockLeaseServicer) Revoke(_ context.Context, _ int64) error { return nil }

func (m *mockLeaseServicer) KeepAlive(_ context.Context, id int64) (*lease.Lease, error) {
	return &lease.Lease{ID: id}, nil
}

func (m *mockLeaseServicer) TimeToLive(_ context.Context, id int64) (*lease.Lease, error) {
	return &lease.Lease{ID: id}, nil
}

func (m *mockLeaseServicer) Leases(_ context.Context) ([]*lease.Lease, error) { return nil, nil }

// ---------------------------------------------------------------------------

// mockWatcher implements watch.Watcher. Its Events channel is closed when
// Close is called; Close uses sync.Once so it is safe to call multiple times.
type mockWatcher struct {
	events    chan watch.WatchResponse
	closeOnce sync.Once
}

func (w *mockWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *mockWatcher) WatchID() int64                     { return 1 }
func (w *mockWatcher) Close() error {
	w.closeOnce.Do(func() { close(w.events) })
	return nil
}
func (w *mockWatcher) Err() error { return nil }

// ---------------------------------------------------------------------------

// mockWatchServicer implements service.WatchServicer. WatchPrefix and Watch
// each create a mockWatcher whose Events channel is closed when ctx is
// canceled, so upstream forwarding goroutines exit cleanly.
type mockWatchServicer struct{}

func (m *mockWatchServicer) Watch(ctx context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	w := &mockWatcher{events: make(chan watch.WatchResponse)}
	go func() { <-ctx.Done(); _ = w.Close() }()
	return w, nil
}

func (m *mockWatchServicer) WatchPrefix(ctx context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	w := &mockWatcher{events: make(chan watch.WatchResponse)}
	go func() { <-ctx.Done(); _ = w.Close() }()
	return w, nil
}

func (m *mockWatchServicer) WatchEntity(_ context.Context, _ *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, nil
}

func (m *mockWatchServicer) WatchEntityType(_ context.Context, _ *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------

// mockMVCCKVServicer implements service.MVCCKVServicer by embedding
// *mockKVServicer (KVServicer) and adding no-op MVCC methods. PutWithLease
// returns a valid KeyValue so the membership store registration succeeds.
type mockMVCCKVServicer struct {
	*mockKVServicer
}

func (m *mockMVCCKVServicer) PutWithLease(_ context.Context, key string, value []byte, _ int64) (*service.KeyValue, error) {
	return &service.KeyValue{Key: []byte(key), Value: value}, nil
}

func (m *mockMVCCKVServicer) PutWithLeaseAndMode(_ context.Context, key string, value []byte, _ int64, _ writemode.Mode) (*service.KeyValue, error) {
	return &service.KeyValue{Key: []byte(key), Value: value}, nil
}

func (m *mockMVCCKVServicer) GetRevision(_ context.Context, _ string) (*service.KeyValue, error) {
	return nil, nil
}

func (m *mockMVCCKVServicer) GetRevisionWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (*service.KeyValue, error) {
	return nil, nil
}

func (m *mockMVCCKVServicer) GetAtRevision(_ context.Context, _ string, _ int64) (*service.KeyValue, error) {
	return nil, nil
}

func (m *mockMVCCKVServicer) GetAtRevisionWithConsistency(_ context.Context, _ string, _ int64, _ consistency.ReadOptions) (*service.KeyValue, error) {
	return nil, nil
}

func (m *mockMVCCKVServicer) Range(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	return nil, 0, 0, nil
}

func (m *mockMVCCKVServicer) RangeWithConsistency(_ context.Context, _, _ []byte, _ consistency.ReadOptions, _ ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	return nil, 0, 0, nil
}

func (m *mockMVCCKVServicer) Compact(_ context.Context, _ int64) error { return nil }

func (m *mockMVCCKVServicer) CurrentRevision(_ context.Context) (int64, error) { return 0, nil }

// ---------------------------------------------------------------------------
// startDynamicMembership success-path test
// ---------------------------------------------------------------------------

// TestStartDynamicMembership_SuccessPath covers start.go:1953.2,1953.17
// (the "return mgr, nil" success path). All three supplementary services
// (lease, MVCC, watch) are provided as in-memory mocks so embedded.New,
// sdkgo.NewClient, NewMembershipManager, and mgr.Start all succeed, and
// the function returns a non-nil manager without error.
func TestStartDynamicMembership_SuccessPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := make(map[string][]byte)
	kvMock := newMockKVStore(store)
	adminMock := &minimalAdminServicer{}
	leaseMock := &mockLeaseServicer{}
	mvccMock := &mockMVCCKVServicer{mockKVServicer: &mockKVServicer{}}
	watchMock := &mockWatchServicer{}

	f := &startFlags{
		replicaID:       1,
		raftAddress:     "127.0.0.1:63099",
		shardID:         1,
		clusterMinPeers: 3,
	}

	mgr, err := startDynamicMembership(ctx, f, kvMock, adminMock, mvccMock, leaseMock, watchMock)
	require.NoError(t, err, "startDynamicMembership must succeed with valid mock services")
	require.NotNil(t, mgr)

	// Signal shutdown; mgr.Close cancels the run context and waits for
	// background goroutines to drain before deregistering the member lease.
	cancel()
	if closeErr := mgr.Close(); closeErr != nil {
		t.Logf("mgr.Close() = %v (acceptable on test shutdown)", closeErr)
	}
}

// RestoreToRevision satisfies service.MVCCKVServicer.
func (m *mockMVCCKVServicer) RestoreToRevision(context.Context, *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	return nil, nil
}

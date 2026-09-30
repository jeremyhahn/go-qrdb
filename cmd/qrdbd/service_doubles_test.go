// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package main

import (
	"context"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// Service doubles shared by tests in every build of this package. They carry
// no build constraint so a caller under any tag set -- the integration build
// included -- finds them (see "Build constraints on test files" in AGENTS.md).

// minimalAdminServicer is a minimal implementation of service.AdminServicer
// that returns zero values for all methods. Used to cover the path through
// embedded.New (which requires non-nil AdminServicer).
type minimalAdminServicer struct{}

func (m *minimalAdminServicer) Health(_ context.Context) (*service.HealthStatus, error) {
	return &service.HealthStatus{Status: "ok"}, nil
}

func (m *minimalAdminServicer) Status(_ context.Context) (*service.StatusInfo, error) {
	return &service.StatusInfo{}, nil
}

func (m *minimalAdminServicer) Peers(_ context.Context) (*service.PeersInfo, error) {
	return &service.PeersInfo{}, nil
}

func (m *minimalAdminServicer) AddMember(_ context.Context, _, _ uint64, _, _ string) error {
	return nil
}

func (m *minimalAdminServicer) RemoveMember(_ context.Context, _, _ uint64) error { return nil }

func (m *minimalAdminServicer) RequestSnapshot(_ context.Context, _ uint64) error { return nil }

func (m *minimalAdminServicer) Drain(_ context.Context) error { return nil }

func (m *minimalAdminServicer) Capabilities(_ context.Context) (*capabilities.Capabilities, error) {
	return &capabilities.Capabilities{}, nil
}

func (m *minimalAdminServicer) StartShard(_ context.Context, _ *service.StartShardRequest) error {
	return nil
}

func (m *minimalAdminServicer) StopShard(_ context.Context, _ uint64) error { return nil }

func (m *minimalAdminServicer) GetShardState(_ context.Context, _ uint64) (*service.ShardStateResponse, error) {
	return &service.ShardStateResponse{}, nil
}

func (m *minimalAdminServicer) GetTopology(_ context.Context) (*service.TopologyResponse, error) {
	return &service.TopologyResponse{}, nil
}

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

// RestoreToRevision satisfies service.MVCCKVServicer.
func (m *mockMVCCKVServicer) RestoreToRevision(context.Context, *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	return nil, nil
}

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

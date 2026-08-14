//go:build !integration

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

package main

import (
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// blockingWatchService's Watch/WatchPrefix return a watcher whose Events()
// channel is never written to and never closed, simulating a watch on a key
// that never changes. It exists to prove `qrdb watch` honors --timeout
// instead of hanging forever (the bug fixed in cmd/qrdb/watch.go +
// cmd/qrdb/main.go).
type blockingWatchService struct {
	watcher *mockWatcher
}

func newBlockingWatchService() *blockingWatchService {
	return &blockingWatchService{
		watcher: &mockWatcher{events: make(chan watch.WatchResponse), id: 1},
	}
}

func (s *blockingWatchService) Watch(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	return s.watcher, nil
}

func (s *blockingWatchService) WatchPrefix(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	return s.watcher, nil
}

func (s *blockingWatchService) WatchEntity(_ context.Context, _ *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, nil
}

func (s *blockingWatchService) WatchEntityType(_ context.Context, _ *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, nil
}

// TestWatchCmd_TimeoutExitsGracefully verifies `qrdb watch --timeout <d>`
// stops and exits 0 once the deadline elapses, rather than hanging forever
// waiting on a watch that never delivers an event or closes its channel.
func TestWatchCmd_TimeoutExitsGracefully(t *testing.T) {
	svc := newBlockingWatchService()
	opts := newAllServiceOpts(newMockMVCCService(), &mockTransactionService{}, &mockBatchService{}, svc, &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(newMockMVCCService(), newMockAdminService(), opts...)

	start := time.Now()
	_, err := executeCommand(root, "watch", "watched-key", "--timeout", "150ms")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected nil error on a graceful --timeout exit, got: %v", err)
	}
	// Generous upper bound so this is robust under CI load while still
	// proving the command did not fall back to the default 10s bound (or
	// hang indefinitely, as it did before the fix).
	if elapsed > 5*time.Second {
		t.Fatalf("watch did not honor --timeout (150ms): took %v", elapsed)
	}
}

// TestWatchCmd_PrefixTimeoutExitsGracefully mirrors the above for the
// --prefix path.
func TestWatchCmd_PrefixTimeoutExitsGracefully(t *testing.T) {
	svc := newBlockingWatchService()
	opts := newAllServiceOpts(newMockMVCCService(), &mockTransactionService{}, &mockBatchService{}, svc, &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(newMockMVCCService(), newMockAdminService(), opts...)

	start := time.Now()
	_, err := executeCommand(root, "watch", "watched-prefix/", "--prefix", "--timeout", "150ms")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected nil error on a graceful --timeout exit, got: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("watch --prefix did not honor --timeout (150ms): took %v", elapsed)
	}
}

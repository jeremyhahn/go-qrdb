// Copyright (c) 2026 Jeremy Hahn
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

package barrierinit

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

// TestWatcher_DeliverHandsTheLatestStatementBeforeAnyPoll is the regression
// for go-qrdb#812: a node that is not a barrier's creator was asked about the
// barrier before its watcher polled, and answered from a registry the poll
// had not yet filled. Deliver brings it to the claimed statement on demand.
func TestWatcher_DeliverHandsTheLatestStatementBeforeAnyPoll(t *testing.T) {
	ctx, f := context.Background(), newWatchFixture(t)
	w := f.watcher(t)
	f.claim(t, clusterseal.TenantBarrier("acme"))

	if err := w.Deliver(ctx, clusterseal.TenantBarrier("acme")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	wantEpochs(t, f, 1)

	f.advance(t, 2)
	if err := w.Deliver(ctx, clusterseal.TenantBarrier("acme")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	wantEpochs(t, f, 1, 2)
}

// Deliver and Poll share one book of delivered epochs: neither hands the node
// a statement the other already handed it.
func TestWatcher_DeliverAndPollDeliverEachEpochOnce(t *testing.T) {
	ctx, f := context.Background(), newWatchFixture(t)
	w := f.watcher(t)
	f.claim(t, clusterseal.TenantBarrier("acme"))

	if err := w.Deliver(ctx, clusterseal.TenantBarrier("acme")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if err := w.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if err := w.Deliver(ctx, clusterseal.TenantBarrier("acme")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	wantEpochs(t, f, 1)
}

// A barrier with no claimed statement, or one this node does not serve,
// delivers nothing and is not an error: the request it serves answers as the
// registry stands.
func TestWatcher_DeliverWithNothingToDeliver(t *testing.T) {
	ctx, f := context.Background(), newWatchFixture(t)
	w := f.watcher(t)
	f.claim(t, clusterseal.TenantBarrier("globex")) // not served here

	for _, b := range []clusterseal.Barrier{clusterseal.TenantBarrier("acme"), clusterseal.TenantBarrier("globex")} {
		if err := w.Deliver(ctx, b); err != nil {
			t.Fatalf("Deliver(%s): %v", b, err)
		}
	}
	wantEpochs(t, f)
}

// A failed Deliver leaves the epoch undelivered, so the next Deliver or poll
// retries it; a store that cannot be read reports a *StoreError.
func TestWatcher_DeliverReportsFailuresAndRetries(t *testing.T) {
	ctx, f := context.Background(), newWatchFixture(t)
	w := f.watcher(t)
	f.claim(t, clusterseal.TenantBarrier("acme"))

	busy := errors.New("unseal in progress")
	f.handleErr = busy
	if err := w.Deliver(ctx, clusterseal.TenantBarrier("acme")); !errors.Is(err, busy) {
		t.Fatalf("Deliver = %v, want the handler's error", err)
	}
	f.handleErr = nil

	f.kv.fail(errors.New("unavailable"))
	var storeErr *StoreError
	if err := w.Deliver(ctx, clusterseal.TenantBarrier("acme")); !errors.As(err, &storeErr) {
		t.Fatalf("Deliver on a failing store = %v, want *StoreError", err)
	}
	wantEpochs(t, f)

	f.kv.fail(nil)
	if err := w.Deliver(ctx, clusterseal.TenantBarrier("acme")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	wantEpochs(t, f, 1)
}

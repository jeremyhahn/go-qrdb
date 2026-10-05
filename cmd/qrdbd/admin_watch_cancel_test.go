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

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// neverDeliveringWatcher is a watch.Watcher whose Events channel never
// sends anything and is never closed by anyone but the caller -- it blocks
// forever unless the context WatchForAdminBound was given is canceled.
type neverDeliveringWatcher struct {
	events chan watch.WatchResponse
}

func newNeverDeliveringWatcher() *neverDeliveringWatcher {
	return &neverDeliveringWatcher{events: make(chan watch.WatchResponse)}
}

func (w *neverDeliveringWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *neverDeliveringWatcher) WatchID() int64                    { return 1 }
func (w *neverDeliveringWatcher) Close() error                      { return nil }
func (w *neverDeliveringWatcher) Err() error                        { return nil }

// neverDeliveringAdminWatchSvc implements firstboot.AdminRecordWatcher by
// handing back a watcher that never delivers an event, so
// firstboot.WatchForAdminBound blocks in its own select loop until ctx is
// canceled -- exactly the real-world shape of a node that is still
// genuinely awaiting the admin's binding to replicate.
type neverDeliveringAdminWatchSvc struct{}

func (neverDeliveringAdminWatchSvc) Watch(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	return newNeverDeliveringWatcher(), nil
}

// TestBootstrapAuthEnforcement_AdminWatchCancelBeforeWaitPreventsHang is the
// go-qrdb#591 re-review item A fix: the admin-bound watch goroutine runs on
// its OWN child context (adminWatchCancel), not on the caller's ctx
// (sigCtx) directly. sigCtx is cancelled ONLY by a shutdown signal, so a
// caller that reaches its cleanup through a NON-signal error return
// elsewhere (runStart's later steps) must still be able to stop the watch
// goroutine deliberately -- otherwise the deferred adminWatchWG.Wait() the
// real call site performs blocks forever, since nothing would ever tell a
// goroutine watching for admin events to exit.
//
// This drives bootstrapAuthEnforcement exactly as the real daemon does
// (policy "required", no admin binding yet, so the probe enters "awaiting"
// and the watch goroutine actually starts against a watcher that never
// delivers an event), then reproduces the real call site's own cleanup
// order under ctx.Background() -- i.e. with NO signal ever cancelling the
// parent context -- and asserts adminWatchWG.Wait() returns promptly once
// adminWatchCancel runs, rather than hanging.
func TestBootstrapAuthEnforcement_AdminWatchCancelBeforeWaitPreventsHang(t *testing.T) {
	mgmt := bootstrapForBindingPolicy(t)

	authResult, err := bootstrapAuthEnforcement(
		context.Background(), mgmt, t.TempDir(), &mockRaftNode{},
		audit.NewNoopLogger(), false, "", qrdbconfig.AuthABACConfig{},
		nil, nil, "", "", "required",
		firstboot.NewAwaitingAdminCredentialProbe(), neverDeliveringAdminWatchSvc{},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = authResult.auditLogger.Close() })

	require.True(t, authResult.awaitingAdminCredentialProbe.AwaitingAdminCredential(),
		"the admin has no live binding yet, so this boot must be awaiting -- otherwise the watch below never started and this test would prove nothing")
	require.NotNil(t, authResult.adminWatchWG, "a watch goroutine must have started while awaiting")
	require.NotNil(t, authResult.adminWatchCancel)

	// The real call site's own cleanup order: cancel BEFORE waiting (the
	// fix). ctx here is context.Background() -- nothing is ever going to
	// cancel it the way a shutdown signal would cancel sigCtx in
	// production -- so if the watch goroutine still ran on ctx directly
	// instead of its own child context, this Wait() would never return.
	done := make(chan struct{})
	go func() {
		authResult.adminWatchCancel()
		authResult.adminWatchWG.Wait()
		close(done)
	}()

	select {
	case <-done:
		// The fix: Wait() returned promptly because adminWatchCancel
		// actually stopped the goroutine.
	case <-time.After(5 * time.Second):
		t.Fatal("adminWatchWG.Wait() did not return after adminWatchCancel: " +
			"the watch goroutine is still blocked, which means it is running on a context " +
			"only a shutdown signal cancels rather than its own cancelable child context")
	}
}

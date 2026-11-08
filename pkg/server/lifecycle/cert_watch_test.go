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

package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
)

// certProviderStub is a cert.CertProvider with every method left as the
// embedded nil interface -- enough to exist as a cert.CertProvider value
// without implementing certDeletionWatcher, which is all
// WatchCertProviderEvictions needs: it never calls any OTHER CertProvider
// method, only type-asserts.
type certProviderStub struct {
	cert.CertProvider
}

// certProviderWithWatch adds certDeletionWatcher to certProviderStub, so a
// test can control WatchForDeletions' outcome and observe the ctx it was
// called with. Like DefaultCertProvider's, its watchers run until that ctx
// is canceled and then close done.
type certProviderWithWatch struct {
	certProviderStub
	watchErr   error
	watchCalls int
	gotCtx     context.Context
	running    chan struct{} // closed once the watchers run
	exited     chan struct{} // closed once they have returned
}

func newCertProviderWithWatch(watchErr error) *certProviderWithWatch {
	return &certProviderWithWatch{watchErr: watchErr, running: make(chan struct{}), exited: make(chan struct{})}
}

func (s *certProviderWithWatch) WatchForDeletions(ctx context.Context) (<-chan struct{}, error) {
	s.watchCalls++
	s.gotCtx = ctx
	if s.watchErr != nil {
		return nil, s.watchErr
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		close(s.running)
		<-ctx.Done()
		close(s.exited)
	}()
	return done, nil
}

var _ certDeletionWatcher = (*certProviderWithWatch)(nil)

// TestWatchCertProviderEvictions_NilNodeOrProviderIsNoOp proves both a nil
// Node and a Node with no CertProv configured are documented no-ops.
func TestWatchCertProviderEvictions_NilNodeOrProviderIsNoOp(t *testing.T) {
	if err := WatchCertProviderEvictions(context.Background(), nil); err != nil {
		t.Fatalf("WatchCertProviderEvictions(nil Node) = %v, want nil", err)
	}
	if err := WatchCertProviderEvictions(context.Background(), &Node{}); err != nil {
		t.Fatalf("WatchCertProviderEvictions with a nil CertProv = %v, want nil", err)
	}
}

// TestWatchCertProviderEvictions_ProviderWithoutWatchSupportIsNoOp proves
// an embedder-supplied CertProvider that does not implement
// certDeletionWatcher is left alone rather than failing the type
// assertion loudly.
func TestWatchCertProviderEvictions_ProviderWithoutWatchSupportIsNoOp(t *testing.T) {
	n := &Node{CertProv: certProviderStub{}}
	if err := WatchCertProviderEvictions(context.Background(), n); err != nil {
		t.Fatalf("WatchCertProviderEvictions with a non-watching CertProv = %v, want nil", err)
	}
}

// TestWatchCertProviderEvictions_DelegatesToWatchForDeletions proves a
// CertProvider that DOES implement certDeletionWatcher has
// WatchForDeletions called once, under a ctx the caller's cancellation ends,
// and its error returned verbatim.
func TestWatchCertProviderEvictions_DelegatesToWatchForDeletions(t *testing.T) {
	watcher := newCertProviderWithWatch(nil)
	n := &Node{CertProv: watcher}
	ctx, cancel := context.WithCancel(context.Background())

	if err := WatchCertProviderEvictions(ctx, n); err != nil {
		t.Fatalf("WatchCertProviderEvictions: %v", err)
	}
	if watcher.watchCalls != 1 {
		t.Errorf("WatchForDeletions called %d times, want 1", watcher.watchCalls)
	}
	cancel()
	<-watcher.exited
	if watcher.gotCtx.Err() == nil {
		t.Error("canceling the caller's ctx did not cancel the watchers' ctx")
	}

	watcher2 := newCertProviderWithWatch(errors.New("hub unavailable"))
	n2 := &Node{CertProv: watcher2}
	if err := WatchCertProviderEvictions(context.Background(), n2); !errors.Is(err, watcher2.watchErr) {
		t.Errorf("WatchCertProviderEvictions = %v, want %v", err, watcher2.watchErr)
	}
}

// TestWatchCertProviderEvictions_StopWaitsForTheWatchers proves the eviction
// watchers are the node's own work: Stop cancels them, under a caller ctx
// that never ends, and returns only once they have.
func TestWatchCertProviderEvictions_StopWaitsForTheWatchers(t *testing.T) {
	watcher := newCertProviderWithWatch(nil)
	n := &Node{CertProv: watcher}
	if err := WatchCertProviderEvictions(context.Background(), n); err != nil {
		t.Fatalf("WatchCertProviderEvictions: %v", err)
	}
	<-watcher.running

	if err := n.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-watcher.exited:
	default:
		t.Fatal("a cert eviction watcher was still running when Stop returned")
	}
}

// TestWatchCertProviderEvictions_AfterStopLeavesNothingRunning proves
// watchers started on a node that has already stopped are canceled and
// awaited before the call returns, rather than left running unsupervised.
func TestWatchCertProviderEvictions_AfterStopLeavesNothingRunning(t *testing.T) {
	watcher := newCertProviderWithWatch(nil)
	n := &Node{CertProv: watcher}
	if err := n.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	err := watchCertEvictions(context.Background(), &n.work, watcher)
	if err != nil {
		t.Fatalf("watchCertEvictions: %v", err)
	}
	select {
	case <-watcher.exited:
	default:
		t.Fatal("a cert eviction watcher started after Stop was still running when the call returned")
	}
}

// TestWatchCertProviderEvictions_NoWatchServiceStartsNoWork proves a
// provider without a watch service (WatchForDeletions returns a nil done)
// adds nothing for Stop to wait on.
func TestWatchCertProviderEvictions_NoWatchServiceStartsNoWork(t *testing.T) {
	n := &Node{CertProv: noWatchServiceProvider{}}
	if err := WatchCertProviderEvictions(context.Background(), n); err != nil {
		t.Fatalf("WatchCertProviderEvictions: %v", err)
	}
	if err := n.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// noWatchServiceProvider is DefaultCertProvider built without
// WithWatchService: WatchForDeletions starts nothing and returns a nil done.
type noWatchServiceProvider struct{ certProviderStub }

func (noWatchServiceProvider) WatchForDeletions(context.Context) (<-chan struct{}, error) {
	return nil, nil
}

var _ certDeletionWatcher = noWatchServiceProvider{}

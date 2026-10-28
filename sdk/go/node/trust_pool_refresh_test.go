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

package node

import (
	"context"
	"crypto/x509"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/clienttrust"
)

// heldCertSource holds the first CACertPEMs read open -- the way a refresh
// holds a storage iterator open across its scan -- until the test releases it,
// and reports when that read began and when its context was cancelled. Later
// reads return at once. Only the CertSource methods are implemented; the
// trust pool calls nothing else on a Key B provider.
type heldCertSource struct {
	cert.CertProvider

	reads     atomic.Int32
	entered   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
}

func newHeldCertSource() *heldCertSource {
	return &heldCertSource{
		entered:   make(chan struct{}),
		cancelled: make(chan struct{}),
		release:   make(chan struct{}),
	}
}

func (s *heldCertSource) CACertPEMs(ctx context.Context) ([][]byte, error) {
	if s.reads.Add(1) != 1 {
		return nil, nil
	}
	close(s.entered)
	<-ctx.Done()
	close(s.cancelled)
	<-s.release
	return nil, ctx.Err()
}

func (s *heldCertSource) IsTenantIssuer(context.Context, string, *x509.Certificate) (bool, error) {
	return false, nil
}

// TestNode_CloseWaitsForAnInFlightTrustPoolRefresh pins the close order the
// trust pool's refresh needs (go-qrdb CI run 54787): Close must not return --
// and so must not reach the storage close -- while a refresh is still reading.
// Cancelling the refresh's context is not enough; the read in flight has to
// finish first, which Pebble otherwise refuses with "element has outstanding
// references".
func TestNode_CloseWaitsForAnInFlightTrustPoolRefresh(t *testing.T) {
	src := newHeldCertSource()
	pool := clienttrust.New()
	pool.SetKeyBProvider(src)
	n := &Node{}
	n.trustPool.Store(pool)
	n.startTrustPoolRefresh(pool)
	<-src.entered

	closed := make(chan error, 1)
	go func() { closed <- n.Close() }()
	<-src.cancelled
	select {
	case err := <-closed:
		t.Fatalf("Close returned (%v) while a trust-pool refresh was still reading", err)
	default:
	}
	close(src.release)
	require.NoError(t, <-closed)
	require.Nil(t, n.stopTrustPoolRefresh, "Close leaves no refresh to stop")
}

// TestNode_StopTrustPoolIsIdempotent pins that stopping twice, or with no
// refresh running, is a no-op rather than a second wait on a finished loop.
func TestNode_StopTrustPoolIsIdempotent(t *testing.T) {
	n := &Node{}
	n.stopTrustPool()

	var stops int
	n.stopTrustPoolRefresh = func() { stops++ }
	n.stopTrustPool()
	n.stopTrustPool()
	require.Equal(t, 1, stops)
}

// TestNode_TeardownDataPlaneStopsTrustPoolRefresh pins that a migration's
// teardown ends the refresh loop of the graph it tears down. Before the fix
// the loop ran on the node-lifetime context, which a teardown never cancels,
// so it kept reading through a storage backend the teardown had closed, and
// every rebuild started one more.
func TestNode_TeardownDataPlaneStopsTrustPoolRefresh(t *testing.T) {
	cfg := sealedStandaloneConfig(t, t.TempDir())
	n, err := NewStandaloneNode(context.Background(), cfg, nil, WithSealPassphrase([]byte("teardown-trust-pool")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	done := requireTrustPoolRefreshRunning(t, n)

	n.teardownDataPlane()
	requireTrustPoolRefreshExited(t, done)
}

// TestNode_CloseStopsTrustPoolRefresh pins the same for Close on a booted
// node.
func TestNode_CloseStopsTrustPoolRefresh(t *testing.T) {
	cfg := sealedStandaloneConfig(t, t.TempDir())
	n, err := NewStandaloneNode(context.Background(), cfg, nil, WithSealPassphrase([]byte("close-trust-pool")))
	require.NoError(t, err)
	done := requireTrustPoolRefreshRunning(t, n)

	require.NoError(t, n.Close())
	requireTrustPoolRefreshExited(t, done)
}

// requireTrustPoolRefreshRunning asserts that n's booted data plane runs its
// own trust-pool refresh loop and returns the channel closed when that loop
// exits. It reads n's loop alone: a process-wide goroutine count would also
// see loops of other tests' nodes, which exit asynchronously and some of
// which run in parallel.
func requireTrustPoolRefreshRunning(t *testing.T, n *Node) <-chan struct{} {
	t.Helper()
	require.NotNil(t, n.stopTrustPoolRefresh, "the booted data plane has a trust-pool refresh to stop")
	done := n.trustPoolRefreshDone
	require.NotNil(t, done, "the booted data plane started a trust-pool refresh loop")
	select {
	case <-done:
		t.Fatal("the booted data plane's trust-pool refresh loop has already exited")
	default:
	}
	return done
}

// requireTrustPoolRefreshExited asserts that the refresh loop whose exit done
// signals has ended. A stopped refresh has returned when its stop function
// returns, so no wait is needed: a loop still running was never stopped.
func requireTrustPoolRefreshExited(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	default:
		t.Fatal("the trust-pool refresh loop outlived the data plane it reads")
	}
}

var _ clienttrust.CertSource = (*heldCertSource)(nil)

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
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	n.trustPool = pool
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
	before := trustPoolRefreshes()
	n, err := NewStandaloneNode(context.Background(), cfg, nil, WithSealPassphrase([]byte("teardown-trust-pool")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	require.Equal(t, before+1, trustPoolRefreshes(), "the booted data plane runs one trust-pool refresh loop")

	n.teardownDataPlane()
	requireTrustPoolRefreshes(t, before)
}

// TestNode_CloseStopsTrustPoolRefresh pins the same for Close on a booted
// node.
func TestNode_CloseStopsTrustPoolRefresh(t *testing.T) {
	cfg := sealedStandaloneConfig(t, t.TempDir())
	before := trustPoolRefreshes()
	n, err := NewStandaloneNode(context.Background(), cfg, nil, WithSealPassphrase([]byte("close-trust-pool")))
	require.NoError(t, err)
	require.Equal(t, before+1, trustPoolRefreshes(), "the booted data plane runs one trust-pool refresh loop")

	require.NoError(t, n.Close())
	requireTrustPoolRefreshes(t, before)
}

// trustPoolRefreshOrigins are the stack lines a trust-pool refresh goroutine
// carries: the Run frame, and the starter's created-by line for one that has
// not reached Run yet.
var trustPoolRefreshOrigins = []string{
	"github.com/jeremyhahn/go-qrdb/pkg/clienttrust.(*Pool).Run(",
	"created by github.com/jeremyhahn/go-qrdb/sdk/go/node.(*Node).startTrustPoolRefresh",
}

// trustPoolRefreshes counts the goroutines running, or about to run, a trust
// pool's refresh loop.
func trustPoolRefreshes() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		for _, origin := range trustPoolRefreshOrigins {
			if strings.Contains(g, origin) {
				count++
				break
			}
		}
	}
	return count
}

// requireTrustPoolRefreshes waits at most sweeperExitBound for the refresh
// goroutines to drop to want. A stopped refresh has already returned from Run
// when its stop function returns; the bound covers only the goroutine's own
// exit after that, and matters only to a loop that was never stopped.
func requireTrustPoolRefreshes(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(sweeperExitBound)
	for {
		got := trustPoolRefreshes()
		if got <= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d trust-pool refresh loops still running, want %d: one outlived the data plane it reads", got, want)
		}
		runtime.Gosched()
	}
}

var _ clienttrust.CertSource = (*heldCertSource)(nil)

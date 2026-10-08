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
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// closeOvertakeWindow is how long the Unseal-vs-Close test gives Close to
// finish on its own before it releases the boot. It is NOT a budget the fixed
// code must meet: a correct Close is blocked on the boot for the whole window
// and the test passes whatever its length. It exists so an UNSYNCHRONISED Close
// — the defect — gets the chance to return first and let the boot republish
// after it. A loaded machine can only shorten the defect's chance, never fail a
// correct build.
const closeOvertakeWindow = 250 * time.Millisecond

// gatedStrategy is the real deferred-software (passphrase) strategy with a gate
// on UnsealRootKey: once armed, the next unseal reports that it has entered and
// then blocks until released. It changes WHEN the unseal completes, never what
// it computes, so the boot it gates is the production boot.
type gatedStrategy struct {
	seal.SealingStrategy

	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func newGatedStrategy() *gatedStrategy {
	return &gatedStrategy{
		SealingStrategy: seal.NewDeferredSoftwareStrategy(),
		entered:         make(chan struct{}),
		release:         make(chan struct{}),
	}
}

// UnsealRootKey blocks an armed unseal between "entered" and "release".
func (g *gatedStrategy) UnsealRootKey(ctx context.Context, sealed *seal.SealedRootKey, creds seal.Credentials) ([]byte, error) {
	if g.armed.CompareAndSwap(true, false) {
		close(g.entered)
		<-g.release
	}
	return g.SealingStrategy.UnsealRootKey(ctx, sealed, creds)
}

var _ seal.SealingStrategy = (*gatedStrategy)(nil)

// TestNode_CloseDuringUnseal_BlocksAndNeverRepublishes drives go-qrdb#226's
// first interleaving deterministically: Close is called while Unseal is inside
// the barrier unseal, i.e. before the boot has published anything. Close must
// wait for the boot and then tear it down; no accessor may read non-nil after
// both return. Without the lifecycle lock Close returns at once and the boot
// republishes the data plane onto a closed node.
func TestNode_CloseDuringUnseal_BlocksAndNeverRepublishes(t *testing.T) {
	const passphrase = "close-during-unseal"
	cfg := sealedStandaloneConfig(t, t.TempDir())
	gate := newGatedStrategy()

	// First boot initializes the barrier; the second comes back sealed.
	first, err := NewStandaloneNode(context.Background(), cfg, nil,
		WithSealPassphrase([]byte(passphrase)), WithSealStrategy(gate))
	require.NoError(t, err)
	require.NoError(t, first.Close())

	n, err := NewStandaloneNode(context.Background(), cfg, nil, WithSealStrategy(gate))
	require.NoError(t, err)
	require.True(t, n.IsSealed(), "second boot must return a sealed node")

	gate.armed.Store(true)
	unsealDone := make(chan error, 1)
	go func() {
		unsealDone <- n.Unseal(context.Background(), seal.MustCredentials(context.Background(), []byte(passphrase)))
	}()
	<-gate.entered

	closeDone := make(chan error, 1)
	go func() { closeDone <- n.Close() }()

	var closedEarly bool
	select {
	case err := <-closeDone:
		closedEarly = true
		closeDone <- err
	case <-time.After(closeOvertakeWindow):
	}
	close(gate.release)

	unsealErr := <-unsealDone
	require.NoError(t, <-closeDone)
	assert.False(t, closedEarly, "Close returned while Unseal was still booting; it must wait for the boot")
	if unsealErr != nil {
		require.ErrorIs(t, unsealErr, ErrNodeClosed)
	}
	assertAllAccessorsNil(t, n, nodeAccessors())
	require.NoError(t, n.Close(), "a second Close must be a no-op")
}

// TestNode_UnsealAfterClose_Refused pins the typed refusal: a caller can tell
// "closed" apart from a wrong credential.
func TestNode_UnsealAfterClose_Refused(t *testing.T) {
	const passphrase = "unseal-after-close"
	cfg := sealedStandaloneConfig(t, t.TempDir())
	first, err := NewStandaloneNode(context.Background(), cfg, nil, WithSealPassphrase([]byte(passphrase)))
	require.NoError(t, err)
	require.NoError(t, first.Close())

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.True(t, n.IsSealed())
	require.NoError(t, n.Close())

	err = n.Unseal(context.Background(), seal.MustCredentials(context.Background(), []byte(passphrase)))
	require.ErrorIs(t, err, ErrNodeClosed)
	var unsealErr *UnsealError
	require.False(t, errors.As(err, &unsealErr), "a closed node is not an unseal failure")
	assertAllAccessorsNil(t, n, nodeAccessors())
}

// TestNode_CloseDuringEnableEncryption_Ordered drives the second interleaving:
// a system-scope migration (tear down, re-key, rebuild the data plane) and
// Close started together. Either order is legal — the migration completes and
// Close tears the result down, or Close wins and the migration is refused with
// ErrNodeClosed — but the migration must never run against a barrier Close has
// retracted, and nothing may be published after Close returns. Unsynchronised,
// the two write the same data-plane fields concurrently, which -race reports,
// and the migration can dereference a retracted barrier.
func TestNode_CloseDuringEnableEncryption_Ordered(t *testing.T) {
	const rounds = 3
	for range rounds {
		cfg := sealedStandaloneConfig(t, t.TempDir())
		cfg.Seal.Strategy = "none"
		n, err := NewStandaloneNode(context.Background(), cfg, nil)
		require.NoError(t, err)
		require.False(t, n.IsSealed())
		require.NotNil(t, n.Client())

		opts := seal.MigrationOptions{
			TargetStrategy: seal.StrategyPassphrase,
			Mechanism:      seal.MechanismSwap,
			Availability:   seal.AvailabilityQuiesced,
			Credentials:    seal.MustCredentials(context.Background(), []byte("close-during-migration")),
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var migErr, closeErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			migErr = n.EnableEncryption(context.Background(), service.BarrierScope{Kind: service.ScopeSystem}, opts)
		}()
		go func() {
			defer wg.Done()
			<-start
			closeErr = n.Close()
		}()
		close(start)
		wg.Wait()

		require.NoError(t, closeErr)
		if migErr != nil {
			require.ErrorIs(t, migErr, ErrNodeClosed, "a migration racing Close either completes or is refused as closed")
		}
		assertAllAccessorsNil(t, n, nodeAccessors())
		require.ErrorIs(t, n.EnableEncryption(context.Background(), service.BarrierScope{Kind: service.ScopeSystem}, opts), ErrNodeClosed)
	}
}

// TestNode_CloseIdempotent pins that Close may be called any number of times,
// concurrently, and every call returns nil once the first has torn down.
func TestNode_CloseIdempotent(t *testing.T) {
	cfg := sealedStandaloneConfig(t, t.TempDir())
	n, err := NewStandaloneNode(context.Background(), cfg, nil, WithSealPassphrase([]byte("idempotent")))
	require.NoError(t, err)

	const callers = 4
	errs := make(chan error, callers)
	for range callers {
		go func() { errs <- n.Close() }()
	}
	for range callers {
		require.NoError(t, <-errs)
	}
	assertAllAccessorsNil(t, n, nodeAccessors())
}

// closeBackgroundWaitWindow is how long TestNode_Close_WaitsForBackgroundGoroutines
// gives Close() to have reached its bgWG.Wait and genuinely blocked on it,
// before concluding a too-early return means Close did not wait. A correct
// Close is parked on the gate held below for this whole window and the test
// passes regardless of its length; it exists only to give an UNSYNCHRONISED
// Close — the defect — the chance to return before the gate opens.
const closeBackgroundWaitWindow = 150 * time.Millisecond

// TestNode_Close_WaitsForBackgroundGoroutines proves Close does not return
// until every goroutine tracked by bgWG -- the lease-expiry sweeper, the
// reclaim sweeper and, when tenant isolation wires one, the
// TenantShardReconciler, all started on background() -- has actually
// exited, not merely been told to via a cancelled context. It exercises the
// exact Add/Done contract those callers use directly, rather than standing
// up a full cluster to drive a real sweeper through leadership, so the
// assertion is about the Close/bgWG contract itself and does not depend on
// election timing.
func TestNode_Close_WaitsForBackgroundGoroutines(t *testing.T) {
	cfg := sealedStandaloneConfig(t, t.TempDir())
	n, err := NewStandaloneNode(context.Background(), cfg, nil, WithSealPassphrase([]byte("close-bg-wait")))
	require.NoError(t, err)

	bgCtx := n.background()
	release := make(chan struct{})
	exited := make(chan struct{})
	n.bgWG.Add(1)
	go func() {
		defer n.bgWG.Done()
		<-bgCtx.Done() // Close cancels bgCancel first, same as every real caller sees.
		<-release      // Held open deliberately; Close must not return before this closes.
		close(exited)
	}()

	closeErr := make(chan error, 1)
	go func() { closeErr <- n.Close() }()

	select {
	case err := <-closeErr:
		t.Fatalf("Close returned (err=%v) while a bgWG-tracked goroutine was still held open; it must wait on bgWG before returning", err)
	case <-time.After(closeBackgroundWaitWindow):
	}

	close(release)
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the bgWG-tracked goroutine never observed its release")
	}
	select {
	case err := <-closeErr:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close never returned after the bgWG-tracked goroutine exited")
	}
}

// TestNode_CloseRefusesAWaitingMigration pins that the lifecycle gate is
// REFUSABLE: a system migration queued behind an in-flight transition is
// refused with ErrNodeClosed the moment Close begins, while the slot is still
// held. A plain mutex would queue it behind Close, and a Close that stops a
// server gracefully would wait on a request that is itself waiting on Close.
func TestNode_CloseRefusesAWaitingMigration(t *testing.T) {
	cfg := sealedStandaloneConfig(t, t.TempDir())
	cfg.Seal.Strategy = "none"
	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)

	// Stand in for an in-flight boot or migration: hold the slot.
	require.NoError(t, n.lifeGate.acquire(context.Background()))

	migDone := make(chan error, 1)
	go func() {
		migDone <- (&nodeSystemMigrator{node: n}).EnableEncryption(context.Background(),
			service.BarrierScope{Kind: service.ScopeSystem}, seal.MigrationOptions{})
	}()
	closeDone := make(chan error, 1)
	go func() { closeDone <- n.Close() }()

	select {
	case err := <-migDone:
		require.ErrorIs(t, err, ErrNodeClosed, "a waiting migration must be refused as closed")
	case <-time.After(closeRefusalBound):
		t.Fatal("a migration waiting on the gate was not refused when Close began")
	}
	select {
	case <-closeDone:
		t.Fatal("Close returned while the slot was still held by an in-flight transition")
	default:
	}

	n.lifeGate.release()
	require.NoError(t, <-closeDone)
	assertAllAccessorsNil(t, n, nodeAccessors())
}

// TestNode_CloseDuringTransportEnableEncryption drives the review's deadlock
// shape through a real transport: a system enable-encryption delivered over
// the node's embedded transport while a transition holds the gate, and Close
// called concurrently. The request must be answered with a closed refusal
// promptly — never "migration_started" for a migration left queued behind
// Close — and Close must finish once the transition does.
func TestNode_CloseDuringTransportEnableEncryption(t *testing.T) {
	cfg := sealedStandaloneConfig(t, t.TempDir())
	cfg.Seal.Strategy = "none"
	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	client := n.Client()
	require.NotNil(t, client)

	require.NoError(t, n.lifeGate.acquire(context.Background()))

	type rpcResult struct {
		resp *transport.BarrierEnableEncryptionResponse
		err  error
	}
	rpcDone := make(chan rpcResult, 1)
	go func() {
		resp, err := client.Barrier().BarrierEnableEncryption(context.Background(), &transport.BarrierEnableEncryptionRequest{
			Strategy:   string(seal.StrategyPassphrase),
			Passphrase: []byte("close-during-transport-migration"),
		})
		rpcDone <- rpcResult{resp, err}
	}()
	// Close must meet a migration already QUEUED on the gate — the shape
	// that deadlocks a graceful server stop — not one that would arrive after
	// Close began and be refused at the door.
	waitForQueuedTransition(t, n)
	closeDone := make(chan error, 1)
	go func() { closeDone <- n.Close() }()

	select {
	case r := <-rpcDone:
		var closedTransport *transport.ClosedError
		if !errors.Is(r.err, ErrNodeClosed) && !errors.As(r.err, &closedTransport) {
			t.Fatalf("enable-encryption racing Close = (%+v, %v), want a closed refusal", r.resp, r.err)
		}
	case <-time.After(closeRefusalBound):
		t.Fatal("enable-encryption over the transport was not answered while Close was pending")
	}

	n.lifeGate.release()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(closeRefusalBound):
		t.Fatal("Close did not finish once the in-flight transition released the gate")
	}
	assertAllAccessorsNil(t, n, nodeAccessors())
}

// TestNode_CloseDuringRESTEnableEncryption is the same deadlock shape over a
// real network transport: a system enable-encryption POSTed to the node's own
// REST server while a transition holds the gate, and Close called
// concurrently. Close stops that very server gracefully, so a migration left
// queued behind Close would hold the request open and Close with it. The
// request must instead be answered 503 — the node is closing, retry elsewhere —
// and never 202 "migration_started"; Close must finish once the transition
// does, and the handed-over listener must be closed with the node.
func TestNode_CloseDuringRESTEnableEncryption(t *testing.T) {
	tlsCfg, pool := preboundServerTLS(t)
	restLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	cfg := sealedStandaloneConfig(t, t.TempDir())
	cfg.Seal.Strategy = "none"
	cfg.Server.Host = "127.0.0.1"
	cfg.Protocols.REST = true
	// The route's authorization is not what is under test; the ordering of
	// the migration it starts against Close is.
	cfg.Auth = qrdbconfig.AuthConfig{Enabled: qrdbconfig.BoolPtr(false)}
	n, err := NewStandaloneNode(context.Background(), cfg, []server.ServerOption{
		server.WithTLS(tlsCfg), server.WithRESTListener(restLn),
	})
	require.NoError(t, err)
	addr := n.Server().RESTAddr()
	require.Equal(t, restLn.Addr().String(), addr)

	require.NoError(t, n.lifeGate.acquire(context.Background()))

	client := &http.Client{
		Timeout:   closeRefusalBound,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}},
	}
	defer client.CloseIdleConnections()
	type httpResult struct {
		code int
		body string
		err  error
	}
	postDone := make(chan httpResult, 1)
	go func() {
		passphrase := base64.StdEncoding.EncodeToString([]byte("close-during-rest-migration"))
		resp, err := client.Post("https://"+addr+"/v1/barrier/enable-encryption", "application/json",
			strings.NewReader(`{"strategy":"passphrase","passphrase":"`+passphrase+`"}`))
		if err != nil {
			postDone <- httpResult{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		postDone <- httpResult{code: resp.StatusCode, body: string(body), err: err}
	}()
	// Close must meet a migration already QUEUED on the gate — the shape
	// that deadlocks a graceful server stop — not one that would arrive after
	// Close began and be refused at the door.
	waitForQueuedTransition(t, n)
	closeDone := make(chan error, 1)
	go func() { closeDone <- n.Close() }()

	select {
	case r := <-postDone:
		require.NoError(t, r.err)
		require.Equal(t, http.StatusServiceUnavailable, r.code,
			"enable-encryption racing Close must be refused as retryable, got body %q", r.body)
		require.NotContains(t, r.body, "migration_started")
	case <-time.After(closeRefusalBound):
		t.Fatal("enable-encryption over REST was not answered while Close was pending")
	}

	n.lifeGate.release()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(closeRefusalBound):
		t.Fatal("Close did not finish once the in-flight transition released the gate")
	}
	assertAllAccessorsNil(t, n, nodeAccessors())
	_, err = restLn.Accept()
	require.ErrorIs(t, err, net.ErrClosed, "Close must close the handed-over listener")
}

// TestNode_UnsealHonoursContext pins the third way out of the gate: a caller
// whose context ends while it waits gets the context's error.
func TestNode_UnsealHonoursContext(t *testing.T) {
	var n Node
	require.NoError(t, n.lifeGate.acquire(context.Background()))
	defer n.lifeGate.release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, n.Unseal(ctx, seal.Credentials{}), context.Canceled)
}

// TestNode_CloseWaitsForTenantMigration pins that Close is serialised with a
// tenant-scope migration too: a migration admitted before Close holds Close
// until it leaves, and one that arrives after Close has begun is refused with
// ErrNodeClosed. Tenant migrations hold the gate SHARED, so two run at once.
func TestNode_CloseWaitsForTenantMigration(t *testing.T) {
	cfg := sealedStandaloneConfig(t, t.TempDir())
	cfg.Seal.Strategy = "none"
	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)

	releaseA, err := n.admitTenantMigration(context.Background())
	require.NoError(t, err)
	releaseB, err := n.admitTenantMigration(context.Background())
	require.NoError(t, err, "tenant migrations are admitted alongside each other")

	closeDone := make(chan error, 1)
	go func() { closeDone <- n.Close() }()

	// Once Close has begun it refuses new tenant migrations — while the two
	// admitted ones still hold it open.
	// A probe that lands before Close has begun is admitted like any other
	// migration, and must leave again or Close waits for it forever.
	require.Eventually(t, func() bool {
		release, err := n.admitTenantMigration(context.Background())
		if err == nil {
			release()
			return false
		}
		return errors.Is(err, ErrNodeClosed)
	}, closeRefusalBound, time.Millisecond)
	select {
	case <-closeDone:
		t.Fatal("Close returned while tenant migrations were still in flight")
	default:
	}

	releaseA()
	select {
	case <-closeDone:
		t.Fatal("Close returned while one tenant migration was still in flight")
	default:
	}
	releaseB()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(closeRefusalBound):
		t.Fatal("Close did not finish once the tenant migrations left")
	}
}

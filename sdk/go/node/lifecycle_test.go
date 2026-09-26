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
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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
		unsealDone <- n.Unseal(context.Background(), seal.MustCredentials([]byte(passphrase)))
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

	err = n.Unseal(context.Background(), seal.MustCredentials([]byte(passphrase)))
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
			Credentials:    seal.MustCredentials([]byte("close-during-migration")),
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

// closeRefusalBound bounds how long a boot or migration WAITING on the
// lifecycle gate may take to be refused once Close begins. A failure bound: a
// correct gate refuses at once, and the waiter the test watches is otherwise
// parked on a slot the test itself holds, so without the refusal it does not
// return until the test gives up.
const closeRefusalBound = 30 * time.Second

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

// waitForQueuedTransition waits until a transition is blocked on n's gate.
func waitForQueuedTransition(t *testing.T, n *Node) {
	t.Helper()
	require.Eventually(t, func() bool { return n.lifeGate.waiting.Load() > 0 },
		closeRefusalBound, time.Millisecond, "the migration never queued on the lifecycle gate")
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

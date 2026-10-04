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

package node

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
)

// TestServeSealedWindow_NilNode verifies the typed error on a nil node.
func TestServeSealedWindow_NilNode(t *testing.T) {
	err := ServeSealedWindow(context.Background(), nil)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
}

// TestServeSealedWindow_UnsealedNodeIsNoop verifies that a node without a
// seal barrier (seal disabled) returns nil immediately — there is no
// sealed window to serve.
func TestServeSealedWindow_UnsealedNodeIsNoop(t *testing.T) {
	n := &Node{} // no barrier → IsSealed() == false
	require.NoError(t, ServeSealedWindow(context.Background(), n))
}

// TestServeSealedWindow_CanceledContext verifies that canceling ctx while
// the barrier is still sealed tears the listener down and returns
// *server.SealedWindowCanceledError.
func TestServeSealedWindow_CanceledContext(t *testing.T) {
	dataDir := t.TempDir()
	cfg := sealedStandaloneConfig(t, dataDir)

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.True(t, n.IsSealed())
	defer func() { require.NoError(t, n.Close()) }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-canceled: the wait loop must exit on its first select

	err = ServeSealedWindow(ctx, n)
	var canceled *server.SealedWindowCanceledError
	require.ErrorAs(t, err, &canceled)
	assert.True(t, n.IsSealed(), "node must remain sealed after canceled wait")
}

// TestServeSealedWindow_ReturnsOnUnseal verifies the full sealed-window
// flow: the listener blocks while sealed, observes the operator-driven
// unseal, tears down, and returns nil; the node then completes its boot
// via Unseal with the barrier already unsealed.
func TestServeSealedWindow_ReturnsOnUnseal(t *testing.T) {
	dataDir := t.TempDir()
	cfg := sealedStandaloneConfig(t, dataDir)

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.True(t, n.IsSealed())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- ServeSealedWindow(ctx, n)
	}()

	// Drive the unseal the way an operator would through the barrier API
	// (Initialize on a fresh barrier both initializes and unseals it).
	creds := seal.MustCredentials(context.Background(), []byte("window-passphrase"))
	initErr := n.barrier.Load().Initialize(context.Background(), creds)
	if initErr != nil && !errors.Is(initErr, seal.ErrAlreadyInitialized) {
		t.Fatalf("barrier initialize: %v", initErr)
	}
	require.False(t, n.barrier.Load().IsSealed(), "barrier must be unsealed")

	select {
	case err := <-serveErr:
		require.NoError(t, err, "ServeSealedWindow must return nil once unsealed")
	case <-ctx.Done():
		t.Fatal("ServeSealedWindow did not observe the unseal in time")
	}

	// Complete the boot: the barrier is already unsealed, so Unseal runs
	// straight into the pending data-plane build.
	require.NoError(t, n.Unseal(context.Background(), creds))
	assert.False(t, n.IsSealed())
	assert.NotNil(t, n.Server(), "server must be wired after boot completes")
	require.NoError(t, n.Close())
}

// TestSealedWindowOptions_CarryThePINBackend proves the sealed-window
// listener serves the node's PIN backend, as the daemon's does: the node's
// own seal-store backend by default, the caller's when one is injected.
func TestSealedWindowOptions_CarryThePINBackend(t *testing.T) {
	cfg := sealedStandaloneConfig(t, t.TempDir())
	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	require.True(t, n.IsSealed())

	got := server.ResolveCryptoOptions(n.sealedWindowOptions(nil)...).PINBackend
	require.NotNil(t, got, "the sealed window must serve a PIN backend")
	assert.Same(t, n.sealPINBackend, got, "by default the node's own seal-store backend")

	// An injected backend wins, as the one decorated instance its option
	// carries: the option installs the node's SO PIN lockout around it once.
	injected := server.WithPINBackend(pintest.NewBackend(t))
	want := server.ResolveCryptoOptions(injected).PINBackend
	got = server.ResolveCryptoOptions(n.sealedWindowOptions([]server.ServerOption{injected})...).PINBackend
	assert.Same(t, want, got, "an injected backend wins")
	assert.Same(t, got, pin.NewLockoutBackend(got), "the injected backend carries the SO PIN lockout")
}

// TestSealedWindowConfig verifies the pending-boot config resolution used
// by ServeSealedWindow, including the no-pending-boot error path.
func TestSealedWindowConfig(t *testing.T) {
	n := &Node{}
	assert.Nil(t, n.sealedWindowConfig(), "no pending boot → nil config")

	dataDir := t.TempDir()
	cfg := sealedStandaloneConfig(t, dataDir)
	n2, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, n2.Close()) }()
	require.True(t, n2.IsSealed())
	assert.Same(t, cfg, n2.sealedWindowConfig(), "sealed standalone node must expose its pending boot config")
}

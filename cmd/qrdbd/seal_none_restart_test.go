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

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// bootSealSubsystem opens the seal subsystem on dir under strategy, the way
// each qrdbd start does, and closes it when the test ends.
func bootSealSubsystem(t *testing.T, dir, strategy string) *sealSubsystem {
	t.Helper()
	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig:   qrdbconfig.SealConfig{Strategy: strategy, RootKeyPath: "seal-root"},
	}
	sub, err := initSealSubsystem(f, context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Close() })
	return sub
}

// TestInitSealSubsystem_NoneBarrierUnsealsItselfOnRestart proves the
// contract in docs/seal/README.md and docs/cluster-verification: a node whose
// barrier was initialized under `none` opens it at boot, with no unseal call.
func TestInitSealSubsystem_NoneBarrierUnsealsItselfOnRestart(t *testing.T) {
	dir := t.TempDir()
	first := bootSealSubsystem(t, dir, string(seal.StrategyNone))
	require.True(t, first.SealBarrier.IsSealed(), "an uninitialized barrier stays sealed until it is initialized")
	require.NoError(t, first.SealBarrier.Initialize(context.Background(), seal.Credentials{}))
	require.False(t, first.SealBarrier.IsSealed())
	require.NoError(t, first.Close())

	restarted := bootSealSubsystem(t, dir, string(seal.StrategyNone))
	require.False(t, restarted.SealBarrier.IsSealed(), "a none barrier must unseal itself at boot")
}

// TestInitSealSubsystem_PassphraseBarrierStaysSealedOnRestart is the
// counterpart: a keyed barrier is never opened without its credential.
func TestInitSealSubsystem_PassphraseBarrierStaysSealedOnRestart(t *testing.T) {
	dir := t.TempDir()
	first := bootSealSubsystem(t, dir, string(seal.StrategyPassphrase))
	creds, err := seal.CredentialsFor(context.Background(), []byte("correct horse battery staple"))
	require.NoError(t, err)
	defer seal.DestroyCredentials(creds)
	require.NoError(t, first.SealBarrier.Initialize(context.Background(), creds))
	require.NoError(t, first.Close())

	restarted := bootSealSubsystem(t, dir, string(seal.StrategyPassphrase))
	require.True(t, restarted.SealBarrier.IsSealed(), "a passphrase barrier must stay sealed at boot")
}

// TestUnsealNoneBarrier_LeavesUninitializedBarrierSealed proves the boot-time
// unseal never initializes: first boot belongs to the operator's init.
func TestUnsealNoneBarrier_LeavesUninitializedBarrierSealed(t *testing.T) {
	sub := bootSealSubsystem(t, t.TempDir(), string(seal.StrategyNone))
	require.NoError(t, unsealNoneBarrier(context.Background(), sub.SealBarrier))
	require.True(t, sub.SealBarrier.IsSealed())
	require.Empty(t, sub.SealBarrier.PersistedStrategy(context.Background()), "no root key may be persisted")
}

// TestUnsealNoneBarrier_AlreadyUnsealedIsNoop proves a second call is harmless.
func TestUnsealNoneBarrier_AlreadyUnsealedIsNoop(t *testing.T) {
	sub := bootSealSubsystem(t, t.TempDir(), string(seal.StrategyNone))
	require.NoError(t, sub.SealBarrier.Initialize(context.Background(), seal.Credentials{}))
	require.NoError(t, unsealNoneBarrier(context.Background(), sub.SealBarrier))
	require.False(t, sub.SealBarrier.IsSealed())
}

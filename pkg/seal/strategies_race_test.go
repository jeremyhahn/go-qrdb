// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStrategiesMap_ConcurrentStatusAndChangePassword_NoRace proves the fix
// for the b.strategies concurrent read/write defect: Status() (and the other
// unlocked readers, Strategies() and ShamirStrategy()) used to read the
// strategies map with no lock while ChangePassword mutated it, which is a
// genuine "fatal error: concurrent map read and map write" (a process crash,
// not merely a benign data race) whenever a Status() poll landed concurrently
// with a password change. Run under `-race`: the detector must report
// nothing, and (independent of -race) the test itself must never hit Go's
// fatal concurrent-map-access crash.
func TestStrategiesMap_ConcurrentStatusAndChangePassword_NoRace(t *testing.T) {
	ctx := context.Background()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, testSoftwareStrategy(t))
	require.NoError(t, err)
	require.NoError(t, b.Initialize(ctx, Credentials{Secret: []byte("initial-password")}))

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers: hammer every code path that reads b.strategies —
	// Status/strategyOrPersisted, Strategies (full-map snapshot), and
	// ShamirStrategy (map lookup) — concurrently with the writer below.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = b.Status()
				_ = b.Strategies()
				_ = b.ShamirStrategy()
			}
		}()
	}

	// Writer: ChangePassword replaces the StrategyPassphrase entry in
	// b.strategies on every call (see change_password.go's setStrategy).
	for i := 0; i < 60; i++ {
		pw := fmt.Sprintf("password-%d", i)
		require.NoError(t, b.ChangePassword(ctx, Credentials{Secret: []byte(pw)}))
	}

	close(stop)
	wg.Wait()

	// The barrier must still be fully functional after the concurrent
	// hammering: readable, unseal-able with the final password.
	require.False(t, b.IsSealed())
	status := b.Status()
	require.Equal(t, StrategyPassphrase, status.Strategy)
}

// TestStrategiesMap_ConcurrentStatusAndUnseal_NoRace proves the same fix from
// the Unseal side: Unseal reads b.strategies (strategyByID) to resolve the
// sealing strategy for the persisted blob's Strategy field. This runs a
// sealed/unsealed cycle concurrently with Status() polling to prove the read
// path in Unseal is race-free against any concurrent strategies-map access.
func TestStrategiesMap_ConcurrentStatusAndUnseal_NoRace(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	strat := testSoftwareStrategy(t)
	b, err := NewStorageBarrier(testLogger(), base, cfg, strat)
	require.NoError(t, err)
	require.NoError(t, b.Initialize(ctx, Credentials{Secret: []byte("password")}))
	require.NoError(t, b.Seal())

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = b.Status()
			}
		}()
	}

	for i := 0; i < 60; i++ {
		require.NoError(t, b.Unseal(ctx, Credentials{Secret: []byte("password")}))
		require.NoError(t, b.Seal())
	}

	close(stop)
	wg.Wait()
}

// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: AGPL-3.0-only

package ceremony_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
)

// mapPendingStore is a minimal in-memory firstboot.PendingAdminCertStore.
type mapPendingStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *mapPendingStore) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[key]; ok {
		return false, nil
	}
	s.m[key] = append([]byte(nil), value...)
	return true, nil
}

func (s *mapPendingStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[key], nil
}

func (s *mapPendingStore) Put(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = append([]byte(nil), value...)
	return nil
}

func (s *mapPendingStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}

// TestShamirSOWriteFault_HoldsAdminPublishAndSealedWindowUntilRetry pins
// go-qrdb#1579 Codex round 1 P1 (2): after an M-of-N Initialize failed its
// SO record write, which rolls the initialization back, first boot
// publishes no admin credential (it would bind an admin with no SO), the
// sealed window stays held and admits the retried Initialize, and once the
// retry records the SO first boot publishes both and the window closes.
func TestShamirSOWriteFault_HoldsAdminPublishAndSealedWindowUntilRetry(t *testing.T) {
	ctx := context.Background()
	// f.failed is the core whose 2-of-3 Initialize issued a system SO and
	// failed the SO record write.
	f := newShamirSOWriteFault(t)
	core := f.failed
	store := &mapPendingStore{m: map[string][]byte{}}

	adminPEM, _ := core.LastIssuedAdminCertPEM()
	assert.Empty(t, adminPEM, "the rolled-back Initialize hands first boot no admin credential")
	assert.Nil(t, core.LastIssuedSystemSO(), "nor an SO")
	assert.True(t, core.HoldsSealedWindow(ctx), "the sealed window stays up for the retry")
	assert.True(t, core.EnrollmentOpen(ctx), "the sealed gate admits the retried Initialize")

	require.NoError(t, firstboot.WriteIssuedAdminCertIfAny(ctx, core, store, nil))
	recorded, _ := store.Get(ctx, firstboot.PendingAdminCertKey)
	assert.Empty(t, recorded, "no admin credential is published without the SO")

	// The rolled-back barrier initializes again only after a restart.
	core = f.newService(f.newBarrier())
	assert.True(t, core.HoldsSealedWindow(ctx), "the restarted node holds the sealed window")
	result, err := core.Initialize(ctx, f.ca, f.options())
	require.NoError(t, err, "the retry records the SO")
	require.NotEmpty(t, result.SOCertPEM)

	assert.False(t, core.SystemSOHandoffPending(ctx))
	assert.False(t, core.HoldsSealedWindow(ctx), "the window closes once the SO is durable")
	require.NoError(t, firstboot.WriteIssuedSystemSOIfAny(ctx, core, store, nil))
	require.NoError(t, firstboot.WriteIssuedAdminCertIfAny(ctx, core, store, nil))
	so, _ := store.Get(ctx, firstboot.PendingSystemSOKey)
	assert.NotEmpty(t, so, "first boot holds the SO")
	recorded, _ = store.Get(ctx, firstboot.PendingAdminCertKey)
	assert.NotEmpty(t, recorded, "and then the admin credential")
}

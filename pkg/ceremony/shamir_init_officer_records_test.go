// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: AGPL-3.0-only

package ceremony

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

var errWrappedKeyWrite = errors.New("injected wrapped unseal key write failure")

// wrappedKeyWriteFailBackend fails the first write of the wrapped unseal
// key: the barrier's first write after Distribute returns, before the
// Shamir initialization commits.
type wrappedKeyWriteFailBackend struct {
	seal.StorageBackend
	armed bool
}

func (b *wrappedKeyWriteFailBackend) Put(ctx context.Context, key string, value []byte) error {
	if b.armed && strings.HasSuffix(key, ".wrapped") {
		b.armed = false
		return errWrappedKeyWrite
	}
	return b.StorageBackend.Put(ctx, key, value)
}

// An M-of-N Initialize that fails after PrepareRoster wrote the officer
// records into the freshly unsealed barrier rolls the barrier back, which
// leaves those records in storage encrypted under the discarded root key.
// The retry after the restart must not read them: it removes them before it
// writes its own, so it completes and every stored officer record is one it
// wrote (go-qrdb#1579 Codex review 5960981468).
func TestInitialize_ShamirFailureAfterRosterRemovesTheOfficerRecords(t *testing.T) {
	ctx := context.Background()
	backend := &wrappedKeyWriteFailBackend{StorageBackend: seal.NewMemoryBackend(), armed: true}
	e := &holderEnv{t: t, backend: backend, ca: testCA(t), pins: pintest.NewBackend(t), factors: testFactors(t)}
	e.barrier = shamirBarrier(t, e.backend, 2, 3)
	e.svc = e.newService(e.barrier)

	alice := newHolder(t, "alice", seal.RoleSecurityOfficer)
	bob := newHolder(t, "bob", seal.RoleSecurityOfficer)
	carol := newHolder(t, "carol", seal.RoleSecurityOfficer)
	opts := func() *InitializeOptions { return initOpts(2, alice, bob, carol) }

	_, err := e.svc.Initialize(ctx, e.ca, opts())
	require.ErrorIs(t, err, errWrappedKeyWrite)
	require.False(t, backend.armed, "the fault hit the write after Distribute")
	require.True(t, e.barrier.IsSealed(), "the initialization rolled back to sealed")
	stale, err := backend.StorageBackend.List(ctx, officerRegistryPrefix)
	require.NoError(t, err)
	require.NotEmpty(t, stale, "the unsealed PrepareRoster wrote the officer records")

	b2, svc2 := e.restart(2, 3)
	e.barrier, e.svc = b2, svc2
	result, err := svc2.Initialize(ctx, e.ca, opts())
	require.NoError(t, err, "the retry reads no record under the rolled-back key")
	require.Len(t, result.Holders, 3)

	keys, err := backend.StorageBackend.List(ctx, officerRegistryPrefix)
	require.NoError(t, err)
	assert.Len(t, keys, 3, "no officer record of the rolled-back attempt remains")
	officers, err := svc2.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err, "every stored officer record opens under the new root key")
	assert.Len(t, officers, 3)

	// The retried ceremony completes: every holder claims its envelope.
	for _, o := range officers {
		for _, h := range []*testHolder{alice, bob, carol} {
			if o.Username == h.username {
				h.thumb = o.CertThumbprint
			}
		}
	}
	e.claim(seal.DomainUnseal, alice)
	e.claim(seal.DomainUnseal, bob)
	e.claim(seal.DomainUnseal, carol)
	assert.Equal(t, StateOperational, svc2.State())
}

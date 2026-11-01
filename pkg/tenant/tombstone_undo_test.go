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

package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// errUndoInactive stands in for the composer's feature.NotActiveError.
var errUndoInactive = errors.New("tenant-undo not active")

// undoActive is a tenant-undo activation check that reports the feature on.
func undoActive() error { return nil }

// mustReadTombstone decodes the tombstone read stores under id's key.
func mustReadTombstone(t *testing.T, read tombstoneReader, id string) *Tombstone {
	t.Helper()
	data, err := read(context.Background(), tombstoneKey(id))
	require.NoError(t, err)
	require.NotNil(t, data, "no tombstone for %q", id)
	var ts Tombstone
	require.NoError(t, json.Unmarshal(data, &ts))
	return &ts
}

// TestUndoCreateTenant_ActiveGateLeavesAnUndoneTombstone pins that an undo
// with the feature active fences the id with an undone tombstone, not a
// retired one, and still tears the tenant down.
func TestUndoCreateTenant_ActiveGateLeavesAnUndoneTombstone(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc, _ := newTestServiceWithStore(t, store)
	WithTenantUndoGate(undoActive)(svc)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	require.NoError(t, err)
	require.NoError(t, svc.UndoCreateTenant(ctx, "acme"))

	ts := mustReadTombstone(t, store.Get, "acme")
	assert.True(t, ts.Undone())
	assert.NotEmpty(t, ts.Undo)
	_, err = svc.GetTenant(ctx, "acme")
	assert.ErrorIs(t, err, ErrTenantNotFound)
}

// TestUndoCreateTenant_InactiveGateRetiresTheId pins the fallback: with the
// feature not active, or no check injected at all, an undo is a deletion and
// the id is retired for good, exactly as before the feature existed.
func TestUndoCreateTenant_InactiveGateRetiresTheId(t *testing.T) {
	for name, gate := range map[string]func() error{
		"not active":  func() error { return errUndoInactive },
		"no gate set": nil,
	} {
		t.Run(name, func(t *testing.T) {
			store := seal.NewMemoryBackend()
			svc, _ := newTestServiceWithStore(t, store)
			WithTenantUndoGate(gate)(svc)
			ctx := context.Background()

			_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
			require.NoError(t, err)
			require.NoError(t, svc.UndoCreateTenant(ctx, "acme"))

			assert.False(t, mustReadTombstone(t, store.Get, "acme").Undone())
			var deleted *TenantDeletedError
			_, err = svc.CreateTenant(ctx, "acme", "Acme Two", IsolationPrefix)
			assert.True(t, errors.As(err, &deleted), "got %v", err)
		})
	}
}

// TestUndoCreateTenant_RefusesAnUnknownTenant pins that an undo of an id
// that was never created is not-found, as a deletion of one is.
func TestUndoCreateTenant_RefusesAnUnknownTenant(t *testing.T) {
	svc, _ := newTestServiceWithStore(t, seal.NewMemoryBackend())
	WithTenantUndoGate(undoActive)(svc)
	assert.ErrorIs(t, svc.UndoCreateTenant(context.Background(), "never-existed"), ErrTenantNotFound)
}

// TestKVTenantService_UndoCreateTenant pins the no-seal service's undo on
// both sides of the gate.
func TestKVTenantService_UndoCreateTenant(t *testing.T) {
	for name, tc := range map[string]struct {
		gate   func() error
		undone bool
	}{
		"active":     {gate: undoActive, undone: true},
		"not active": {gate: func() error { return errUndoInactive }, undone: false},
	} {
		t.Run(name, func(t *testing.T) {
			store := kvstore.NewMemoryKVStore()
			svcI, err := NewKVTenantService(store, testLogger())
			require.NoError(t, err)
			svcI.(interface{ SetTenantUndoGate(func() error) }).SetTenantUndoGate(tc.gate)
			ctx := context.Background()

			_, err = svcI.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
			require.NoError(t, err)
			require.NoError(t, svcI.(CreateUndoer).UndoCreateTenant(ctx, "acme"))
			assert.Equal(t, tc.undone, mustReadTombstone(t, store.Get, "acme").Undone())
		})
	}
}

// TestTombstone_UndoneStateRoundTrips pins that an undone tombstone records its
// state and its undo nonce, and that both survive the store's encoding.
func TestTombstone_UndoneStateRoundTrips(t *testing.T) {
	ts := newUndoneTombstone(&TenantInfo{ID: "acme", Name: "Acme"}, time.Now().UTC())
	require.True(t, ts.Undone())
	require.NotEmpty(t, ts.Undo)

	data, err := json.Marshal(ts)
	require.NoError(t, err)
	var back Tombstone
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, TombstoneUndone, back.State)
	assert.Equal(t, ts.Undo, back.Undo)
	assert.True(t, back.Undone())
	assert.Equal(t, "acme", back.ID)
}

// TestTombstone_EachUndoHasItsOwnNonce pins that two undos of one id are told
// apart, so an acknowledgement of the first never counts toward the second.
func TestTombstone_EachUndoHasItsOwnNonce(t *testing.T) {
	info := &TenantInfo{ID: "acme"}
	a := newUndoneTombstone(info, time.Now().UTC())
	b := newUndoneTombstone(info, time.Now().UTC())
	assert.NotEqual(t, a.Undo, b.Undo)
}

// TestTombstone_DeletionTombstoneIsRetired pins that a deletion's tombstone,
// and every tombstone written before the state existed, reads as retired.
func TestTombstone_DeletionTombstoneIsRetired(t *testing.T) {
	ts := newTombstone(&TenantInfo{ID: "acme"}, time.Now().UTC())
	assert.False(t, ts.Undone())
	data, err := json.Marshal(ts)
	require.NoError(t, err)
	assert.NotContains(t, string(data), `"state"`)
	assert.NotContains(t, string(data), `"undo"`)

	var legacy Tombstone
	require.NoError(t, json.Unmarshal([]byte(`{"id":"acme","deleted_at":"2026-01-02T03:04:05Z"}`), &legacy))
	assert.Equal(t, TombstoneRetired, legacy.State)
	assert.False(t, legacy.Undone())
}

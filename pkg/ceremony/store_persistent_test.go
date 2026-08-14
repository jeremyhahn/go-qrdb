// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ceremony

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// unsealedTestBarrier returns a fully unsealed StorageBarrier suitable for
// persistent-registry tests.
func unsealedTestBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	b := testBarrier(t)
	require.NoError(t, b.Initialize(context.Background(), seal.Credentials{Secret: []byte("test-master-password")}))
	require.False(t, b.IsSealed())
	return b
}

// TestNewOfficerRegistry_NilBackend verifies the constructor rejects a nil
// backend with ErrNilStore.
func TestNewOfficerRegistry_NilBackend(t *testing.T) {
	_, err := NewOfficerRegistry(nil)
	assert.ErrorIs(t, err, ErrNilStore)
}

// TestNewOfficerRegistry_Valid verifies the constructor returns a non-nil
// registry when given a valid backend.
func TestNewOfficerRegistry_Valid(t *testing.T) {
	reg, err := NewOfficerRegistry(unsealedTestBarrier(t))
	require.NoError(t, err)
	assert.NotNil(t, reg)
}

// TestOfficerRegistry_SaveLoad_RoundTrip verifies that a saved record can
// be retrieved with the same fields.
func TestOfficerRegistry_SaveLoad_RoundTrip(t *testing.T) {
	reg, err := NewOfficerRegistry(unsealedTestBarrier(t))
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	officer := &PersistentOfficer{
		Username:       "alice",
		CertThumbprint: "abc123",
		PubKeyPEM:      "-----BEGIN PUBLIC KEY-----\n...\n-----END PUBLIC KEY-----\n",
		ShareIndex:     1,
		AuthFactor:     "mtls",
		EnrolledAt:     now,
	}
	require.NoError(t, reg.Save(context.Background(), officer))

	got, err := reg.Load(context.Background(), "alice")
	require.NoError(t, err)
	assert.Equal(t, officer.Username, got.Username)
	assert.Equal(t, officer.CertThumbprint, got.CertThumbprint)
	assert.Equal(t, officer.PubKeyPEM, got.PubKeyPEM)
	assert.Equal(t, officer.ShareIndex, got.ShareIndex)
	assert.Equal(t, officer.AuthFactor, got.AuthFactor)
	assert.True(t, got.EnrolledAt.Equal(now))
	assert.True(t, got.ClaimedAt.IsZero())
}

// TestOfficerRegistry_Save_NilRecord verifies a nil record is rejected.
func TestOfficerRegistry_Save_NilRecord(t *testing.T) {
	reg, err := NewOfficerRegistry(unsealedTestBarrier(t))
	require.NoError(t, err)

	err = reg.Save(context.Background(), nil)
	assert.ErrorIs(t, err, ErrInvalidOfficerRecord)
}

// TestOfficerRegistry_Save_EmptyUsername verifies an empty username is rejected.
func TestOfficerRegistry_Save_EmptyUsername(t *testing.T) {
	reg, err := NewOfficerRegistry(unsealedTestBarrier(t))
	require.NoError(t, err)

	err = reg.Save(context.Background(), &PersistentOfficer{})
	assert.ErrorIs(t, err, ErrMissingUsername)
}

// TestOfficerRegistry_Save_DefaultsEnrolledAt verifies that a zero
// EnrolledAt is replaced with the current UTC time.
func TestOfficerRegistry_Save_DefaultsEnrolledAt(t *testing.T) {
	reg, err := NewOfficerRegistry(unsealedTestBarrier(t))
	require.NoError(t, err)

	officer := &PersistentOfficer{Username: "bob", ShareIndex: 2, AuthFactor: "piv"}
	require.NoError(t, reg.Save(context.Background(), officer))

	got, err := reg.Load(context.Background(), "bob")
	require.NoError(t, err)
	assert.False(t, got.EnrolledAt.IsZero())
}

// TestOfficerRegistry_Load_EmptyUsername verifies an empty username is rejected.
func TestOfficerRegistry_Load_EmptyUsername(t *testing.T) {
	reg, err := NewOfficerRegistry(unsealedTestBarrier(t))
	require.NoError(t, err)

	_, err = reg.Load(context.Background(), "")
	assert.ErrorIs(t, err, ErrMissingUsername)
}

// TestOfficerRegistry_Load_NotFound verifies missing records surface as
// ErrUserNotFound.
func TestOfficerRegistry_Load_NotFound(t *testing.T) {
	reg, err := NewOfficerRegistry(unsealedTestBarrier(t))
	require.NoError(t, err)

	_, err = reg.Load(context.Background(), "no-such-user")
	assert.ErrorIs(t, err, ErrUserNotFound)
}

// TestOfficerRegistry_ListAll_Empty verifies that an empty registry
// returns an empty slice (and not nil-as-error).
func TestOfficerRegistry_ListAll_Empty(t *testing.T) {
	reg, err := NewOfficerRegistry(unsealedTestBarrier(t))
	require.NoError(t, err)

	all, err := reg.ListAll(context.Background())
	require.NoError(t, err)
	assert.Empty(t, all)
}

// TestOfficerRegistry_ListAll_OrdersByEnrolledAt verifies records come
// back in ascending enrollment order.
func TestOfficerRegistry_ListAll_OrdersByEnrolledAt(t *testing.T) {
	reg, err := NewOfficerRegistry(unsealedTestBarrier(t))
	require.NoError(t, err)

	base := time.Now().UTC().Truncate(time.Second)
	officers := []*PersistentOfficer{
		{Username: "carol", ShareIndex: 3, AuthFactor: "mtls", EnrolledAt: base.Add(2 * time.Second)},
		{Username: "alice", ShareIndex: 1, AuthFactor: "mtls", EnrolledAt: base},
		{Username: "bob", ShareIndex: 2, AuthFactor: "piv", EnrolledAt: base.Add(1 * time.Second)},
	}
	for _, o := range officers {
		require.NoError(t, reg.Save(context.Background(), o))
	}

	all, err := reg.ListAll(context.Background())
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, "alice", all[0].Username)
	assert.Equal(t, "bob", all[1].Username)
	assert.Equal(t, "carol", all[2].Username)
}

// TestOfficerRegistry_MarkClaimed_Success verifies MarkClaimed updates
// the timestamp.
func TestOfficerRegistry_MarkClaimed_Success(t *testing.T) {
	reg, err := NewOfficerRegistry(unsealedTestBarrier(t))
	require.NoError(t, err)

	require.NoError(t, reg.Save(context.Background(), &PersistentOfficer{
		Username:   "dave",
		ShareIndex: 1,
		AuthFactor: "mtls",
	}))

	when := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, reg.MarkClaimed(context.Background(), "dave", when))

	got, err := reg.Load(context.Background(), "dave")
	require.NoError(t, err)
	assert.True(t, got.ClaimedAt.Equal(when))
}

// TestOfficerRegistry_MarkClaimed_DefaultsToNow verifies that a zero
// timestamp is replaced with the current UTC time.
func TestOfficerRegistry_MarkClaimed_DefaultsToNow(t *testing.T) {
	reg, err := NewOfficerRegistry(unsealedTestBarrier(t))
	require.NoError(t, err)

	require.NoError(t, reg.Save(context.Background(), &PersistentOfficer{
		Username:   "erin",
		ShareIndex: 1,
		AuthFactor: "mtls",
	}))

	before := time.Now().UTC().Add(-time.Second)
	require.NoError(t, reg.MarkClaimed(context.Background(), "erin", time.Time{}))

	got, err := reg.Load(context.Background(), "erin")
	require.NoError(t, err)
	assert.False(t, got.ClaimedAt.IsZero())
	assert.False(t, got.ClaimedAt.Before(before))
}

// TestOfficerRegistry_MarkClaimed_EmptyUsername rejects empty username.
func TestOfficerRegistry_MarkClaimed_EmptyUsername(t *testing.T) {
	reg, err := NewOfficerRegistry(unsealedTestBarrier(t))
	require.NoError(t, err)

	err = reg.MarkClaimed(context.Background(), "", time.Now())
	assert.ErrorIs(t, err, ErrMissingUsername)
}

// TestOfficerRegistry_MarkClaimed_NotFound verifies the error path.
func TestOfficerRegistry_MarkClaimed_NotFound(t *testing.T) {
	reg, err := NewOfficerRegistry(unsealedTestBarrier(t))
	require.NoError(t, err)

	err = reg.MarkClaimed(context.Background(), "nobody", time.Now())
	assert.ErrorIs(t, err, ErrUserNotFound)
}

// TestOfficerRegistry_Sealed_GetReturnsErrSealed verifies that with a
// sealed barrier, all backend-touching methods surface ErrSealed.
func TestOfficerRegistry_Sealed_GetReturnsErrSealed(t *testing.T) {
	barrier := unsealedTestBarrier(t)
	reg, err := NewOfficerRegistry(barrier)
	require.NoError(t, err)

	require.NoError(t, reg.Save(context.Background(), &PersistentOfficer{
		Username:   "frank",
		ShareIndex: 1,
		AuthFactor: "mtls",
	}))

	// Seal the barrier.
	require.NoError(t, barrier.Seal())
	assert.True(t, barrier.IsSealed())

	// Cache may serve a hit, so test ListAll/MarkClaimed which hit the
	// backend unconditionally.
	_, err = reg.ListAll(context.Background())
	assert.ErrorIs(t, err, seal.ErrSealed)

	err = reg.MarkClaimed(context.Background(), "frank", time.Now())
	assert.ErrorIs(t, err, seal.ErrSealed)

	// A Save attempt against a sealed barrier must also fail.
	err = reg.Save(context.Background(), &PersistentOfficer{
		Username:   "sealed-user",
		ShareIndex: 1,
		AuthFactor: "mtls",
	})
	assert.ErrorIs(t, err, seal.ErrSealed)
}

// TestOfficerRegistry_Sealed_LoadFromBackend verifies that a Load that
// misses the cache surfaces ErrSealed when the backend is sealed.
func TestOfficerRegistry_Sealed_LoadFromBackend(t *testing.T) {
	barrier := unsealedTestBarrier(t)
	reg, err := NewOfficerRegistry(barrier)
	require.NoError(t, err)

	// Save an officer (warms cache).
	require.NoError(t, reg.Save(context.Background(), &PersistentOfficer{
		Username:   "gina",
		ShareIndex: 1,
		AuthFactor: "mtls",
	}))

	// Drop the cache so Load is forced to consult the backend.
	reg.mu.Lock()
	reg.cache = make(map[string]*PersistentOfficer)
	reg.mu.Unlock()

	require.NoError(t, barrier.Seal())
	_, err = reg.Load(context.Background(), "gina")
	assert.ErrorIs(t, err, seal.ErrSealed)
}

// TestOfficerRegistry_Reload_AfterUnseal verifies the registry restores
// state after barrier unseal.
func TestOfficerRegistry_Reload_AfterUnseal(t *testing.T) {
	barrier := unsealedTestBarrier(t)

	reg, err := NewOfficerRegistry(barrier)
	require.NoError(t, err)

	officers := []*PersistentOfficer{
		{Username: "h-alice", ShareIndex: 1, AuthFactor: "mtls"},
		{Username: "h-bob", ShareIndex: 2, AuthFactor: "piv"},
	}
	for _, o := range officers {
		require.NoError(t, reg.Save(context.Background(), o))
	}

	// Seal then unseal.
	require.NoError(t, barrier.Seal())
	require.NoError(t, barrier.Unseal(context.Background(), seal.Credentials{Secret: []byte("test-master-password")}))

	// Drop the in-memory cache to simulate a fresh process.
	reg.mu.Lock()
	reg.cache = make(map[string]*PersistentOfficer)
	reg.mu.Unlock()

	// Reload should rebuild the cache.
	require.NoError(t, reg.Reload(context.Background()))

	// Cache snapshot should match what we saved.
	cached := reg.Cached()
	require.Len(t, cached, 2)
	usernames := []string{cached[0].Username, cached[1].Username}
	assert.Contains(t, usernames, "h-alice")
	assert.Contains(t, usernames, "h-bob")
}

// TestOfficerRegistry_Reload_Sealed verifies Reload propagates ErrSealed.
func TestOfficerRegistry_Reload_Sealed(t *testing.T) {
	barrier := unsealedTestBarrier(t)
	reg, err := NewOfficerRegistry(barrier)
	require.NoError(t, err)
	require.NoError(t, barrier.Seal())

	err = reg.Reload(context.Background())
	assert.ErrorIs(t, err, seal.ErrSealed)
}

// TestOfficerRegistry_Cached_DefensiveCopy verifies that mutating a value
// returned by Cached does not change the registry's view.
func TestOfficerRegistry_Cached_DefensiveCopy(t *testing.T) {
	reg, err := NewOfficerRegistry(unsealedTestBarrier(t))
	require.NoError(t, err)

	require.NoError(t, reg.Save(context.Background(), &PersistentOfficer{
		Username:   "ivan",
		ShareIndex: 5,
		AuthFactor: "mtls",
	}))

	cached := reg.Cached()
	require.Len(t, cached, 1)
	cached[0].ShareIndex = 999

	got, err := reg.Load(context.Background(), "ivan")
	require.NoError(t, err)
	assert.Equal(t, 5, got.ShareIndex)
}

// TestOfficerRegistry_Save_OverwritesExisting verifies that re-saving
// updates the stored record.
func TestOfficerRegistry_Save_OverwritesExisting(t *testing.T) {
	reg, err := NewOfficerRegistry(unsealedTestBarrier(t))
	require.NoError(t, err)

	require.NoError(t, reg.Save(context.Background(), &PersistentOfficer{
		Username:   "judy",
		ShareIndex: 1,
		AuthFactor: "mtls",
	}))

	require.NoError(t, reg.Save(context.Background(), &PersistentOfficer{
		Username:   "judy",
		ShareIndex: 7,
		AuthFactor: "piv",
	}))

	got, err := reg.Load(context.Background(), "judy")
	require.NoError(t, err)
	assert.Equal(t, 7, got.ShareIndex)
	assert.Equal(t, "piv", got.AuthFactor)
}

// TestOfficerRegistry_LoadFromBackend_AfterCacheDrop verifies that a Load
// going through the backend correctly populates the cache.
func TestOfficerRegistry_LoadFromBackend_AfterCacheDrop(t *testing.T) {
	reg, err := NewOfficerRegistry(unsealedTestBarrier(t))
	require.NoError(t, err)

	require.NoError(t, reg.Save(context.Background(), &PersistentOfficer{
		Username:   "ken",
		ShareIndex: 4,
		AuthFactor: "webauthn",
	}))

	// Drop the cache.
	reg.mu.Lock()
	reg.cache = make(map[string]*PersistentOfficer)
	reg.mu.Unlock()

	got, err := reg.Load(context.Background(), "ken")
	require.NoError(t, err)
	assert.Equal(t, "ken", got.Username)
	assert.Equal(t, 4, got.ShareIndex)

	// Now the cache should have been repopulated.
	cached := reg.Cached()
	require.Len(t, cached, 1)
	assert.Equal(t, "ken", cached[0].Username)
}

// failingBackend is a StorageBackend stub used to drive error paths.
type failingBackend struct {
	getErr  error
	putErr  error
	listErr error
}

func (f *failingBackend) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, f.getErr
}

func (f *failingBackend) Put(_ context.Context, _ string, _ []byte) error {
	return f.putErr
}

func (f *failingBackend) PutIfAbsent(_ context.Context, _ string, _ []byte) (bool, error) {
	if f.putErr != nil {
		return false, f.putErr
	}
	return true, nil
}

func (f *failingBackend) Delete(_ context.Context, _ string) error { return nil }

func (f *failingBackend) List(_ context.Context, _ string) ([]string, error) {
	return nil, f.listErr
}

func (f *failingBackend) Scan(_ context.Context, _ string, _ func(string, []byte) error) error {
	return nil
}

func (f *failingBackend) Exists(_ context.Context, _ string) (bool, error) { return false, nil }

func (f *failingBackend) Close() error { return nil }

// TestOfficerRegistry_Save_BackendError covers the Put error path.
func TestOfficerRegistry_Save_BackendError(t *testing.T) {
	wantErr := errors.New("disk full")
	reg, err := NewOfficerRegistry(&failingBackend{putErr: wantErr})
	require.NoError(t, err)

	err = reg.Save(context.Background(), &PersistentOfficer{
		Username:   "lin",
		ShareIndex: 1,
		AuthFactor: "mtls",
	})
	assert.ErrorIs(t, err, wantErr)
}

// TestOfficerRegistry_Load_BackendError covers a non-NotFound error from Get.
func TestOfficerRegistry_Load_BackendError(t *testing.T) {
	wantErr := errors.New("io error")
	reg, err := NewOfficerRegistry(&failingBackend{getErr: wantErr})
	require.NoError(t, err)

	_, err = reg.Load(context.Background(), "mary")
	assert.ErrorIs(t, err, wantErr)
}

// TestOfficerRegistry_ListAll_BackendError covers List error propagation.
func TestOfficerRegistry_ListAll_BackendError(t *testing.T) {
	wantErr := errors.New("io error")
	reg, err := NewOfficerRegistry(&failingBackend{listErr: wantErr})
	require.NoError(t, err)

	_, err = reg.ListAll(context.Background())
	assert.ErrorIs(t, err, wantErr)
}

// TestOfficerRegistry_Reload_BackendError covers Reload's List error path.
func TestOfficerRegistry_Reload_BackendError(t *testing.T) {
	wantErr := errors.New("io error")
	reg, err := NewOfficerRegistry(&failingBackend{listErr: wantErr})
	require.NoError(t, err)

	err = reg.Reload(context.Background())
	assert.ErrorIs(t, err, wantErr)
}

// TestOfficerRegistryError_ErrorAndUnwrap exercises the wrapping error type.
func TestOfficerRegistryError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("inner")
	e := &OfficerRegistryError{Op: "marshal", Err: inner}
	assert.Contains(t, e.Error(), "marshal")
	assert.ErrorIs(t, e, inner)
}

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

package firstboot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
)

// soCeremonyCore runs a ceremony that names a system SO by CSR.
func soCeremonyCore(t *testing.T) *ceremony.CeremonyService {
	t.Helper()
	core := testCeremonyCore(t)
	ca, _ := ceremonytest.NewCA(t, "test-ca")
	core.SetPINBackend(pintest.NewBackend(t))
	_, err := core.Initialize(context.Background(), ca, &ceremony.InitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
		SOUsername: "secofficer", SOCSRPEM: testCSR(t),
	})
	require.NoError(t, err)
	require.NotNil(t, core.LastIssuedSystemSO())
	return core
}

// The ceremony's SO crosses to first boot as a record carrying the
// username, the certificate and the PIN's hash, and is consumed once.
func TestPendingSystemSO_RoundTrip(t *testing.T) {
	ctx := context.Background()
	core := soCeremonyCore(t)
	store := newMemPendingAdminCertStore()

	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, core, store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)
	issued := core.LastIssuedSystemSO()
	assert.Equal(t, "secofficer", so.Username)
	assert.Equal(t, issued.CertPEM, so.CertPEM)
	assert.Equal(t, issued.PINRecord, so.PINRecord)
	assert.Nil(t, so.PIN, "the record never carries the PIN")
	raw, err := store.Get(ctx, PendingSystemSOKey)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "test-so-pin")
	require.NoError(t, so.Validate())

	assert.False(t, SystemSOConsumed(ctx, store))
	require.NoError(t, ClearPendingSystemSO(ctx, store))
	assert.True(t, SystemSOConsumed(ctx, store))
	assert.False(t, SystemSOConsumed(ctx, nil))
	so, err = PendingSystemSO(ctx, store)
	require.NoError(t, err)
	assert.Nil(t, so, "a consumed record names no SO")
	require.NoError(t, ClearPendingSystemSO(ctx, store), "clearing twice is a no-op")

	// A later boot of the same node proposes nothing over the consumed record.
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, core, store, nil))
	so, err = PendingSystemSO(ctx, store)
	require.NoError(t, err)
	assert.Nil(t, so)
}

// The first ceremony's SO wins; another node's later write is not an error
// and changes nothing.
func TestWriteIssuedSystemSOIfAny_CreateIfAbsent(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	first := soCeremonyCore(t)
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, first, store, nil))
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	assert.Equal(t, first.LastIssuedSystemSO().CertPEM, so.CertPEM)
}

// A ceremony that named no SO writes no record.
func TestWriteIssuedSystemSOIfAny_NoSOWritesNothing(t *testing.T) {
	ctx := context.Background()
	core, _ := issuedCeremonyCore(t)
	store := newMemPendingAdminCertStore()
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, core, store, nil))
	_, err := store.Get(ctx, PendingSystemSOKey)
	assert.Error(t, err, "no record was written")
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, nil, store, nil))
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, core, nil, nil))
	so, err := PendingSystemSO(ctx, nil)
	require.NoError(t, err)
	assert.Nil(t, so)
	require.NoError(t, ClearPendingSystemSO(ctx, nil))
}

// The write waits out an election like the admin's; a fault stands.
func TestWriteIssuedSystemSOIfAny_SettlesLikeTheAdmin(t *testing.T) {
	shortenSettleBudget(t, 2*time.Second)
	ctx := context.Background()
	core := soCeremonyCore(t)

	store := newElectingStore(2, errElecting)
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, core, store, electing))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)

	faulty := newElectingStore(1, errStoreFault)
	err = WriteIssuedSystemSOIfAny(ctx, core, faulty, electing)
	var storeErr *PendingAdminCertStoreError
	require.ErrorAs(t, err, &storeErr)
	assert.True(t, errors.Is(err, errStoreFault))
	assert.Contains(t, err.Error(), "write system SO")
}

func TestPendingSystemSO_UndecodableRecordIsAFault(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	require.NoError(t, store.Put(ctx, PendingSystemSOKey, []byte("{not json")))
	_, err := PendingSystemSO(ctx, store)
	var storeErr *PendingAdminCertStoreError
	require.ErrorAs(t, err, &storeErr)
	assert.Equal(t, "decode system SO", storeErr.Op)
}

// A store that cannot mark the record consumed reports it.
func TestClearPendingSystemSO_StoreFault(t *testing.T) {
	ctx := context.Background()
	store := &failingPutStore{memPendingAdminCertStore: newMemPendingAdminCertStore()}
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), store, nil))
	err := ClearPendingSystemSO(ctx, store)
	var storeErr *PendingAdminCertStoreError
	require.ErrorAs(t, err, &storeErr)
	assert.Equal(t, "clear system SO", storeErr.Op)
}

type failingPutStore struct{ *memPendingAdminCertStore }

func (s *failingPutStore) Put(context.Context, string, []byte) error { return errStoreFault }

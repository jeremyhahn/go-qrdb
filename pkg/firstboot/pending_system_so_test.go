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
	"crypto/x509"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
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

// TestClearPendingSystemSOIfEnrolled_KeepsTheRecordUntilTheSOIsEnrolled
// proves a leadership handoff that left the admin but not the SO -- and an SO
// whose PIN is not committed yet -- keeps the pending record, so the next
// start's first boot enrols the SO; only an SO with its account and PIN both
// committed consumes it.
func TestClearPendingSystemSOIfEnrolled_KeepsTheRecordUntilTheSOIsEnrolled(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	users := newSOUsers()
	pins, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), audit.NewRingBuffer(8), discardLogger())
	require.NoError(t, err)

	cleared, err := ClearPendingSystemSOIfEnrolled(ctx, store, users, pins)
	require.NoError(t, err)
	assert.True(t, cleared, "no record leaves nothing to consume")

	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)
	keeps := func(why string) {
		t.Helper()
		cleared, err := ClearPendingSystemSOIfEnrolled(ctx, store, users, pins)
		require.NoError(t, err)
		assert.False(t, cleared, why)
		assert.False(t, SystemSOConsumed(ctx, store), why)
		pending, err := PendingSystemSO(ctx, store)
		require.NoError(t, err)
		assert.NotNil(t, pending, why)
	}

	// The handoff: the leader created the admin, then lost leadership.
	_, err = users.CreateUser(ctx, &user.CreateUserRequest{Username: adminUsername, TenantID: tenantid.System,
		RoleIDs: []string{role.SystemRoleID(tenantid.System, role.RoleNameAdmin)}})
	require.NoError(t, err)
	keeps("the admin alone must not consume the SO's record")

	cleared, err = ClearPendingSystemSOIfEnrolled(ctx, store, users, nil)
	require.NoError(t, err)
	assert.False(t, cleared, "no PIN seam can verify nothing")

	_, err = users.CreateUser(ctx, &user.CreateUserRequest{Username: so.Username, TenantID: tenantid.System,
		RoleIDs: []string{role.SystemRoleID(tenantid.System, role.RoleNameSO)}})
	require.NoError(t, err)
	keeps("an SO account without its PIN is not enrolled")

	require.NoError(t, pins.CommitSOPINRecord(ctx, tenantid.System, so.PINRecord))
	keeps("an SO account with its PIN but without its certificate binding is not enrolled")

	bindPendingSO(t, users, so)
	cleared, err = ClearPendingSystemSOIfEnrolled(ctx, store, users, pins)
	require.NoError(t, err)
	assert.True(t, cleared)
	assert.True(t, SystemSOConsumed(ctx, store))
}

// bindPendingSO gives the pending SO's account a live binding to the
// certificate its record carries, as a successful enrolment does.
func bindPendingSO(t *testing.T, users *soUsers, so *SOConfig) {
	t.Helper()
	thumbprint, err := spkiThumbprintFromCertPEM(so.CertPEM)
	require.NoError(t, err)
	account := users.byName[so.Username]
	require.NotNil(t, account)
	account.CertBindings = append(account.CertBindings, user.CertBinding{Thumbprint: thumbprint, NotAfter: time.Now().Add(time.Hour)})
}

// TestClearPendingSystemSOIfEnrolled_KeepsTheRecordAfterAForeignPIN
// covers a follower's cleanup after an enrolment that created and bound
// the SO's account but failed because a different PIN was already
// committed: some PIN exists, but not this record's, so the record stays
// pending and the cleanup reports why.
func TestClearPendingSystemSOIfEnrolled_KeepsTheRecordAfterAForeignPIN(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	users := newSOUsers()
	pins, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), audit.NewRingBuffer(8), discardLogger())
	require.NoError(t, err)

	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)

	_, err = users.CreateUser(ctx, &user.CreateUserRequest{Username: so.Username, TenantID: tenantid.System,
		RoleIDs: []string{role.SystemRoleID(tenantid.System, role.RoleNameSO)}})
	require.NoError(t, err)
	bindPendingSO(t, users, so)
	require.NoError(t, pins.SetSOPIN(ctx, tenantid.System, []byte("someone-else's-pin")))

	cleared, err := ClearPendingSystemSOIfEnrolled(ctx, store, users, pins)
	var unverified *SystemSOUnverifiedError
	require.ErrorAs(t, err, &unverified)
	assert.False(t, cleared)
	assert.False(t, SystemSOConsumed(ctx, store), "a foreign PIN must not consume the record")
	pending, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	assert.NotNil(t, pending, "the pending credential is kept for the enrolment to retry")
}

// TestClearPendingSystemSOIfEnrolled_KeepsTheRecordForAnotherCertificate:
// the record's PIN is committed, but the SO's account is bound to some
// other certificate, so the record's own certificate never worked.
func TestClearPendingSystemSOIfEnrolled_KeepsTheRecordForAnotherCertificate(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	users := newSOUsers()
	pins, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), audit.NewRingBuffer(8), discardLogger())
	require.NoError(t, err)

	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)

	account, err := users.CreateUser(ctx, &user.CreateUserRequest{Username: so.Username, TenantID: tenantid.System,
		RoleIDs: []string{role.SystemRoleID(tenantid.System, role.RoleNameSO)}})
	require.NoError(t, err)
	users.byName[so.Username].CertBindings = []user.CertBinding{{Thumbprint: "sha256:another-certificate", NotAfter: time.Now().Add(time.Hour)}}
	require.NotZero(t, account.ID)
	require.NoError(t, pins.CommitSOPINRecord(ctx, tenantid.System, so.PINRecord))

	cleared, err := ClearPendingSystemSOIfEnrolled(ctx, store, users, pins)
	var unverified *SystemSOUnverifiedError
	require.ErrorAs(t, err, &unverified)
	assert.False(t, cleared)
	assert.False(t, SystemSOConsumed(ctx, store))
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

// ceremonyCoreOver builds a ceremony core whose system barrier persists in
// backend, unsealing it when an earlier core has initialised it: the same
// node, started again.
func ceremonyCoreOver(t *testing.T, backend *seal.MemoryBackend) *ceremony.CeremonyService {
	t.Helper()
	ctx := context.Background()
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte("test-master-password"))
	require.NoError(t, err)
	barrier, err := seal.NewStorageBarrier(slog.New(slog.DiscardHandler), backend,
		seal.BarrierConfig{RootKeyPath: "test/root-key"}, strategy)
	require.NoError(t, err)
	if initialized, err := barrier.Initialized(ctx); err == nil && initialized {
		creds, err := seal.CredentialsFor(ctx, nil)
		require.NoError(t, err)
		defer seal.DestroyCredentials(creds)
		require.NoError(t, barrier.Unseal(ctx, creds))
	}
	core, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: t.TempDir()}, barrier, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	core.SetTrustDomain("example.com")
	return core
}

// A node that crashes after Initialize reported success, but before
// startup copied the issued SO into the replicated store, still completes
// strict first boot when it restarts: Initialize recorded the SO in the
// system barrier before it returned.
func TestWriteIssuedSystemSOIfAny_SurvivesACrashAfterInitialize(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	first := ceremonyCoreOver(t, backend)
	ca, caPEM := ceremonytest.NewCA(t, "test-ca")
	first.SetPINBackend(pintest.NewBackend(t))
	_, err := first.Initialize(ctx, ca, &ceremony.InitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
		SOUsername: "secofficer", SOCSRPEM: testCSR(t),
	})
	require.NoError(t, err)
	issued := first.LastIssuedSystemSO()
	require.NotNil(t, issued)

	// The crash: nothing reached the replicated store, and the restarted
	// core's memory is empty.
	restarted := ceremonyCoreOver(t, backend)
	require.Nil(t, restarted.LastIssuedSystemSO())
	store := newMemPendingAdminCertStore()
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, restarted, store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)
	assert.Equal(t, issued.CertPEM, so.CertPEM)
	assert.Equal(t, issued.PINRecord, so.PINRecord)

	// First boot enrols the recovered SO, PIN and all.
	h := newSOHarness(t, true)
	pins, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), audit.NewRingBuffer(8), discardLogger())
	require.NoError(t, err)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(caPEM))
	h.deps.SO = so
	h.deps.SOPINs = pins
	h.deps.AdminCertPool = pool
	_, err = Initialize(ctx, h.deps)
	require.NoError(t, err)
	require.NoError(t, pins.VerifySOPIN(ctx, tenantid.System, []byte("test-so-pin")))

	// Once consumed, the next start forgets the barrier's copy.
	require.NoError(t, ClearPendingSystemSO(ctx, store))
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, restarted, store, nil))
	left, err := ceremonyCoreOver(t, backend).PendingSystemSO(ctx)
	require.NoError(t, err)
	assert.Nil(t, left, "the consumed SO's record is gone from the barrier")
}

// An encryption migration re-keys the barrier, so the barrier's copy of the
// SO, sealed by the boot that issued it, no longer decrypts. Once the
// replicated store holds the SO (or its consumed marker), a restart must
// settle on that record and never read the barrier's copy.
func TestWriteIssuedSystemSOIfAny_ReplicatedRecordWinsOverAnUndecryptableBarrierCopy(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	first := ceremonyCoreOver(t, backend)
	ca, _ := ceremonytest.NewCA(t, "test-ca")
	first.SetPINBackend(pintest.NewBackend(t))
	_, err := first.Initialize(ctx, ca, &ceremony.InitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
		SOUsername: "secofficer", SOCSRPEM: testCSR(t),
	})
	require.NoError(t, err)
	store := newMemPendingAdminCertStore()
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, first, store, nil))
	recorded, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, recorded)

	// The barrier's copy no longer decrypts.
	keys, err := backend.List(ctx, "")
	require.NoError(t, err)
	corrupted := 0
	for _, key := range keys {
		if strings.Contains(key, "pending-system-so") {
			require.NoError(t, backend.Put(ctx, key, []byte("sealed under a retired key")))
			corrupted++
		}
	}
	require.Equal(t, 1, corrupted, "the barrier holds the SO's copy")
	restarted := ceremonyCoreOver(t, backend)
	_, err = restarted.PendingSystemSO(ctx)
	require.Error(t, err, "the barrier's copy is unreadable")

	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, restarted, store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)
	assert.Equal(t, recorded.CertPEM, so.CertPEM)

	// Consumed: the restart forgets the barrier's copy without reading it.
	require.NoError(t, ClearPendingSystemSO(ctx, store))
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, restarted, store, nil))
	left, err := ceremonyCoreOver(t, backend).PendingSystemSO(ctx)
	require.NoError(t, err)
	assert.Nil(t, left, "the consumed SO's record is gone from the barrier")
}

// failingGetStore answers every read of the pending system SO record with
// a store fault, not absence.
type failingGetStore struct {
	*memPendingAdminCertStore
}

func (s *failingGetStore) Get(ctx context.Context, key string) ([]byte, error) {
	if key == PendingSystemSOKey {
		return nil, errStoreFault
	}
	return s.memPendingAdminCertStore.Get(ctx, key)
}

// A store that cannot be read is never taken for one with no SO: startup
// fails, consumption refuses, and the record stays intact.
func TestPendingSystemSO_StoreReadFaultFailsTheBoot(t *testing.T) {
	ctx := context.Background()
	mem := newMemPendingAdminCertStore()
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), mem, nil))
	before, err := mem.Get(ctx, PendingSystemSOKey)
	require.NoError(t, err)
	store := &failingGetStore{memPendingAdminCertStore: mem}

	so, err := PendingSystemSO(ctx, store)
	assert.Nil(t, so)
	var storeErr *PendingAdminCertStoreError
	require.ErrorAs(t, err, &storeErr)
	assert.Equal(t, "read system SO", storeErr.Op)
	assert.ErrorIs(t, err, errStoreFault)

	require.ErrorIs(t, ClearPendingSystemSO(ctx, store), errStoreFault)
	require.ErrorIs(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), store, nil), errStoreFault)
	assert.False(t, SystemSOConsumed(ctx, store))

	after, err := mem.Get(ctx, PendingSystemSOKey)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the record is intact")
}

type failingPutStore struct{ *memPendingAdminCertStore }

func (s *failingPutStore) Put(context.Context, string, []byte) error { return errStoreFault }

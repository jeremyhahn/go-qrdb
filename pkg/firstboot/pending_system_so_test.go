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

// TestConsumeEnrolledSystemSO_ConsumesTheExactRecord: the record the SO was
// read from is marked consumed.
func TestConsumeEnrolledSystemSO_ConsumesTheExactRecord(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)

	consumed, err := ConsumeEnrolledSystemSO(ctx, store, so)
	require.NoError(t, err)
	assert.True(t, consumed)
	assert.True(t, SystemSOConsumed(ctx, store))
}

// TestConsumeEnrolledSystemSO_LeavesADifferentRecordPending: an SO whose
// certificate or PIN record differs from the pending record's is not the SO
// the record names, so the record stays pending.
func TestConsumeEnrolledSystemSO_LeavesADifferentRecordPending(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)

	otherCert := *so
	otherCert.CertPEM = append([]byte("other-"), so.CertPEM...)
	otherPIN := *so
	otherPIN.PINRecord = so.PINRecord + "-other"
	otherUser := *so
	otherUser.Username = so.Username + "-other"
	for name, other := range map[string]*SOConfig{"cert": &otherCert, "pin record": &otherPIN, "username": &otherUser} {
		consumed, err := ConsumeEnrolledSystemSO(ctx, store, other)
		require.NoError(t, err, name)
		assert.False(t, consumed, name)
		assert.False(t, SystemSOConsumed(ctx, store), name)
	}
	still, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, still)
	assert.Equal(t, so.Username, still.Username)
}

// TestConsumeEnrolledSystemSO_StoreFault: a store that cannot write the
// consumed marker is a fault the caller sees.
func TestConsumeEnrolledSystemSO_StoreFault(t *testing.T) {
	ctx := context.Background()
	store := &failingPutStore{memPendingAdminCertStore: newMemPendingAdminCertStore()}
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)

	consumed, err := ConsumeEnrolledSystemSO(ctx, store, so)
	var storeErr *PendingAdminCertStoreError
	require.ErrorAs(t, err, &storeErr)
	assert.Equal(t, "consume system SO", storeErr.Op)
	assert.False(t, consumed)
	assert.False(t, SystemSOConsumed(ctx, store))
}

// TestConsumeEnrolledSystemSO_NothingToConsume: no store or no SO consumes
// nothing, and a record that cannot be read is a fault, never a consume.
func TestConsumeEnrolledSystemSO_NothingToConsume(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)

	consumed, err := ConsumeEnrolledSystemSO(ctx, nil, so)
	require.NoError(t, err)
	assert.False(t, consumed, "no store")
	consumed, err = ConsumeEnrolledSystemSO(ctx, store, nil)
	require.NoError(t, err)
	assert.False(t, consumed, "no SO")
	assert.False(t, SystemSOConsumed(ctx, store))

	consumed, err = ConsumeEnrolledSystemSO(ctx, &failingGetStore{memPendingAdminCertStore: newMemPendingAdminCertStore()}, so)
	require.ErrorIs(t, err, errStoreFault)
	assert.False(t, consumed)
}

// TestClearPendingSystemSOIfEnrolled_Faults: a record that cannot be read
// and a PIN seam that cannot answer are faults that keep the record.
func TestClearPendingSystemSOIfEnrolled_Faults(t *testing.T) {
	ctx := context.Background()
	users := newSOUsers()

	cleared, err := ClearPendingSystemSOIfEnrolled(ctx, &failingGetStore{memPendingAdminCertStore: newMemPendingAdminCertStore()}, users, scriptedPINChecker{})
	require.ErrorIs(t, err, errStoreFault)
	assert.False(t, cleared)

	store := newMemPendingAdminCertStore()
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)
	_, err = users.CreateUser(ctx, &user.CreateUserRequest{Username: so.Username, TenantID: tenantid.System,
		RoleIDs: []string{role.SystemRoleID(tenantid.System, role.RoleNameSO)}})
	require.NoError(t, err)

	cleared, err = ClearPendingSystemSOIfEnrolled(ctx, store, users, scriptedPINChecker{committedErr: errBranchFault})
	require.ErrorIs(t, err, errBranchFault)
	assert.False(t, cleared)
	pending, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	assert.NotNil(t, pending, "a PIN fault keeps the record")
}

// soNotFoundLookup never finds the SO: its account is gone.
type soNotFoundLookup struct{}

func (soNotFoundLookup) GetUserByUsername(context.Context, string, string) (*user.User, error) {
	return nil, &user.UserNotFoundError{}
}

// TestPendingSOAccountDeleted_Branches: deciding whether an enrolled SO was
// deleted needs a PIN record matcher and a user lookup, and any fault while
// deciding is an enrollment error, never "deleted".
func TestPendingSOAccountDeleted_Branches(t *testing.T) {
	so := &SOConfig{Username: "so", PINRecord: "record"}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name    string
		ctx     context.Context
		deps    Dependencies
		wantErr func(t *testing.T, err error)
	}{
		{"no record matcher", context.Background(), Dependencies{SO: so, SOPINs: pinSetterOnly{}}, func(t *testing.T, err error) {
			var missing *MissingSOSeamError
			require.ErrorAs(t, err, &missing)
		}},
		{"matcher fault", context.Background(), Dependencies{SO: so, SOPINs: recordMatcherPINs{err: errBranchFault}}, func(t *testing.T, err error) {
			var enrollErr *SystemSOEnrollmentError
			require.ErrorAs(t, err, &enrollErr)
			assert.Equal(t, "PIN check", enrollErr.Op)
			require.ErrorIs(t, err, errBranchFault)
		}},
		{"no user resolver", context.Background(), Dependencies{SO: so, SOPINs: recordMatcherPINs{matches: true}}, func(t *testing.T, err error) {
			var missing *MissingSOSeamError
			require.ErrorAs(t, err, &missing)
		}},
		{"lookup fault", context.Background(), Dependencies{SO: so, SOPINs: recordMatcherPINs{matches: true},
			UserResolver: failingLookup{err: errBranchFault}}, func(t *testing.T, err error) {
			var enrollErr *SystemSOEnrollmentError
			require.ErrorAs(t, err, &enrollErr)
			assert.Equal(t, "account", enrollErr.Op)
			require.ErrorIs(t, err, errBranchFault)
		}},
		{"cancelled while converging", cancelled, Dependencies{SO: so, SOPINs: recordMatcherPINs{matches: true},
			UserResolver: soNotFoundLookup{}}, func(t *testing.T, err error) {
			require.ErrorIs(t, err, context.Canceled)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deleted, err := pendingSOAccountDeleted(tt.ctx, tt.deps, tenantid.System)
			tt.wantErr(t, err)
			assert.False(t, deleted)

			var result Result
			tt.wantErr(t, enrollSystemSO(tt.ctx, tt.deps, tenantid.System, &result))
			assert.False(t, result.SORetired)
			assert.False(t, result.SOCreated)
		})
	}
}

// TestCheckSystemSO_AdminAsSONeedsAnUpdater: the bootstrap admin enrolled
// as SO is granted the role on its own account, which needs UpdateUser.
func TestCheckSystemSO_AdminAsSONeedsAnUpdater(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)
	so.Username = adminUsername

	err = checkSystemSO(ctx, Dependencies{SO: so, Users: creatorOnlyUsers{}})
	var missing *MissingSOSeamError
	require.ErrorAs(t, err, &missing)
	assert.Contains(t, missing.Seam, "UpdateUser")
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

// retainedSOHarness enrols the ceremony's issued SO through a first boot
// fed from its replicated pending record, and returns the harness, the
// store still holding that record (never consumed), and the SO's username.
func retainedSOHarness(t *testing.T) (*soHarness, *memPendingAdminCertStore, string) {
	t.Helper()
	ctx := context.Background()
	core := ceremonyCoreOver(t, seal.NewMemoryBackend())
	ca, caPEM := ceremonytest.NewCA(t, "test-ca")
	core.SetPINBackend(pintest.NewBackend(t))
	_, err := core.Initialize(ctx, ca, &ceremony.InitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
		SOUsername: "secofficer", SOCSRPEM: testCSR(t),
	})
	require.NoError(t, err)
	store := newMemPendingAdminCertStore()
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, core, store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)

	h := newSOHarness(t, true)
	pins, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), audit.NewRingBuffer(8), discardLogger())
	require.NoError(t, err)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(caPEM))
	h.deps.SO = so
	h.deps.SOPINs = pins
	h.deps.AdminCertPool = pool
	result, err := Initialize(ctx, h.deps)
	require.NoError(t, err)
	require.True(t, result.SOCreated)
	require.False(t, result.SORetired)
	require.NotNil(t, h.users.byName[so.Username])
	require.Equal(t, 1, h.binder.calls[h.users.byName[so.Username].ID])
	return h, store, so.Username
}

// restartWithRetainedSO re-reads the still-pending record, as the next
// start does, and runs first boot again with it.
func restartWithRetainedSO(t *testing.T, h *soHarness, store *memPendingAdminCertStore) *Result {
	t.Helper()
	ctx := context.Background()
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so, "the unconsumed record is still pending")
	h.deps.SO = so
	result, err := Initialize(ctx, h.deps)
	require.NoError(t, err)
	return result
}

// A run whose enrolment bound the SO's PIN and certificate but stopped before
// activateSystemSO -- the activation failed, or leadership moved -- leaves a
// bound account still LifecyclePending. That is not an enrolled SO: the
// background cleanup must keep the pending record, so the next boot finishes
// the activation.
func TestClearPendingSystemSOIfEnrolled_KeepsTheRecordUntilTheSOIsActivated(t *testing.T) {
	ctx := context.Background()
	h, store, username := retainedSOHarness(t)
	pins, ok := h.deps.SOPINs.(SOPINChecker)
	require.True(t, ok)
	account := h.users.byName[username]
	require.NotEmpty(t, account.CertBindings, "the enrolment bound the SO")
	account.Lifecycle = user.LifecyclePending // activateSystemSO did not commit

	cleared, err := ClearPendingSystemSOIfEnrolled(ctx, store, h.users, pins)
	require.NoError(t, err)
	assert.False(t, cleared, "a bound but unactivated SO is not enrolled")
	assert.False(t, SystemSOConsumed(ctx, store))

	restartWithRetainedSO(t, h, store)
	assert.Equal(t, user.LifecycleActive, h.users.byName[username].Lifecycle, "the next boot finishes the activation")

	cleared, err = ClearPendingSystemSOIfEnrolled(ctx, store, h.users, pins)
	require.NoError(t, err)
	assert.True(t, cleared)
	assert.True(t, SystemSOConsumed(ctx, store))
}

// TestInitialize_DeletedPendingSOIsNotRecreatedFromItsRetainedRecord (PR
// #1579 review round 2, P1): the SO was enrolled, its pending record was
// never consumed, and the SO was deleted since. The next start must not
// re-create the account from the retained record: withdrawn access does
// not return on restart.
func TestInitialize_DeletedPendingSOIsNotRecreatedFromItsRetainedRecord(t *testing.T) {
	h, store, username := retainedSOHarness(t)
	soID := h.users.byName[username].ID
	delete(h.users.byName, username)
	shrinkAdminConvergeWindow(t, 50*time.Millisecond, time.Millisecond)

	result := restartWithRetainedSO(t, h, store)
	assert.True(t, result.SORetired)
	assert.False(t, result.SOCreated)
	assert.Nil(t, h.users.byName[username], "the deleted SO was re-created")
	assert.Equal(t, 1, h.users.creates[username])
	assert.Equal(t, 1, h.binder.calls[soID])
}

// TestInitialize_PendingSOWithItsPINAndItsAccountStillEnrols: a retained
// record whose committed PIN is its own and whose account still exists is
// an interrupted enrolment, not a deleted SO, and is completed.
func TestInitialize_PendingSOWithItsPINAndItsAccountStillEnrols(t *testing.T) {
	h, store, username := retainedSOHarness(t)
	soID := h.users.byName[username].ID
	// Interrupted before its binding: no binding, nor its record.
	h.users.byName[username].CertBindings = nil
	h.users.byName[username].Attributes = nil

	result := restartWithRetainedSO(t, h, store)
	assert.False(t, result.SORetired)
	assert.Equal(t, soID, result.SOUserID)
	assert.Equal(t, 2, h.binder.calls[soID], "the unbound SO is bound again")
	assert.False(t, result.SOCreated, "the standing account is reused, not re-created")
}

// TestEnrollSystemSO_WithdrawnKeyNotRebound (PR #1579 Codex round 1, P1):
// the SO was enrolled, its pending record was never consumed, and an
// operator withdrew its key's binding since. The next start must not bind
// the key again from the retained record.
func TestEnrollSystemSO_WithdrawnKeyNotRebound(t *testing.T) {
	h, store, username := retainedSOHarness(t)
	soID := h.users.byName[username].ID
	h.users.byName[username].CertBindings = nil // the operator's withdrawal

	result := restartWithRetainedSO(t, h, store)
	assert.Equal(t, 1, h.binder.calls[soID], "the withdrawn key was bound again")
	assert.Empty(t, h.users.byName[username].CertBindings)
	assert.Empty(t, result.SOCertPEM)
	assert.False(t, result.SOCreated)
	assert.Equal(t, soID, result.SOUserID, "the record is still consumed")
}

// soBindFailer fails BindAdminCert for one account: a node that stops
// between creating the SO and binding its key.
type soBindFailer struct {
	inner *soBinder
	fail  string
}

func (b soBindFailer) BindAdminCert(ctx context.Context, tenantID string, userID uint64, thumbprint string) error {
	if u := b.inner.users.byID(userID); u != nil && u.Username == b.fail {
		return errors.New("stopped before the binding")
	}
	return b.inner.BindAdminCert(ctx, tenantID, userID, thumbprint)
}

// TestEnrollSystemSO_UnfinishedEnrollmentStillBinds: a run that stopped
// before the SO's binding left no record of one, so the next run binds it.
func TestEnrollSystemSO_UnfinishedEnrollmentStillBinds(t *testing.T) {
	h := newSOHarness(t, true)
	h.deps.Binder = soBindFailer{inner: h.binder, fail: "officer"}
	_, err := Initialize(context.Background(), h.deps)
	require.Error(t, err)
	so := h.users.byName["officer"]
	require.NotNil(t, so, "the SO account was created before the binding")
	require.Empty(t, so.CertBindings)

	h.deps.Binder = h.binder
	result, err := Initialize(context.Background(), h.deps)
	require.NoError(t, err)
	assert.False(t, result.SOCreated)
	assert.Len(t, so.CertBindings, 1, "the unfinished enrolment was not completed")
	assert.NotEmpty(t, result.SOCertThumbprint)
	assert.Equal(t, result.SOCertThumbprint, so.Attributes[soBoundKeyAttribute])
}

// recordSetterOnlyPINs commits a PIN record but cannot compare one.
type recordSetterOnlyPINs struct{}

func (recordSetterOnlyPINs) SetSOPIN(context.Context, string, []byte) error {
	return errors.New("recordSetterOnlyPINs commits records only")
}

func (recordSetterOnlyPINs) CommitSOPINRecord(context.Context, string, string) error { return nil }

// TestInitialize_PendingSOWithoutPINRecordMatcherIsRefusedBeforeAnyWrite:
// an SO handed over as a PIN record needs an SOPINs that can prove the
// committed record is its own, refused in the pre-flight before any write.
func TestInitialize_PendingSOWithoutPINRecordMatcherIsRefusedBeforeAnyWrite(t *testing.T) {
	h := newSOHarness(t, true)
	h.deps.SO.PIN = nil
	h.deps.SO.PINRecord = "A:00"
	h.deps.SOPINs = recordSetterOnlyPINs{}
	_, err := Initialize(context.Background(), h.deps)
	var e *MissingSOSeamError
	require.ErrorAs(t, err, &e)
	assert.Contains(t, e.Seam, "SOPINRecordMatcher")
	assert.Empty(t, h.users.creates)
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

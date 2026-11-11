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
	"encoding/json"
	"encoding/pem"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// fakeSOPINs is an SOPINChecker whose answers and faults the test sets.
type fakeSOPINs struct {
	committed    bool
	committedErr error
	matches      bool
	matchesErr   error
}

func (f *fakeSOPINs) SOPINCommitted(context.Context, string) (bool, error) {
	return f.committed, f.committedErr
}

func (f *fakeSOPINs) SOPINRecordMatches(context.Context, string, string) (bool, error) {
	return f.matches, f.matchesErr
}

// fakeSOLister serves pages[Page-1] and a nil page past the end.
type fakeSOLister struct {
	pages []*user.ListUsersResponse
	err   error
	asked []int
}

func (f *fakeSOLister) ListUsers(_ context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	f.asked = append(f.asked, req.Page)
	if f.err != nil {
		return nil, f.err
	}
	if req.Page < 1 || req.Page > len(f.pages) {
		return nil, nil
	}
	return f.pages[req.Page-1], nil
}

// enrolledSystemSO is a system account that holds the so role and can log
// in with a live binding.
func enrolledSystemSO(id uint64) *user.User {
	return &user.User{
		ID:           id,
		Username:     "so",
		TenantID:     tenantid.System,
		RoleIDs:      []string{role.SystemRoleID(tenantid.System, role.RoleNameSO)},
		Lifecycle:    user.LifecycleActive,
		CertBindings: []user.CertBinding{{Thumbprint: "t", NotAfter: time.Now().Add(time.Hour)}},
	}
}

func TestRecognizeEnrolledSystemSO_NilSeamsRecognizeNothing(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	ok, err := RecognizeEnrolledSystemSO(ctx, nil, &fakeSOLister{}, &fakeSOPINs{}, 1)
	require.NoError(t, err)
	assert.False(t, ok)
	ok, err = RecognizeEnrolledSystemSO(ctx, store, nil, &fakeSOPINs{}, 1)
	require.NoError(t, err)
	assert.False(t, ok)
	ok, err = RecognizeEnrolledSystemSO(ctx, store, &fakeSOLister{}, nil, 1)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestRecognizeEnrolledSystemSO_StoreReadFault(t *testing.T) {
	store := &failingGetStore{memPendingAdminCertStore: newMemPendingAdminCertStore()}
	ok, err := RecognizeEnrolledSystemSO(context.Background(), store, &fakeSOLister{}, &fakeSOPINs{committed: true}, 1)
	require.ErrorIs(t, err, errStoreFault)
	assert.False(t, ok)
}

func TestRecognizeEnrolledSystemSO_ConsumedMarkerIsRecognized(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	require.NoError(t, store.Put(ctx, PendingSystemSOKey, pendingAdminCertConsumedMarker))
	users := &fakeSOLister{}
	ok, err := RecognizeEnrolledSystemSO(ctx, store, users, &fakeSOPINs{}, 1)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Empty(t, users.asked, "a consumed record needs no account scan")
}

func TestRecognizeEnrolledSystemSO_PendingRecordIsNotRecognized(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	require.NoError(t, store.Put(ctx, PendingSystemSOKey, []byte(`{"username":"so"}`)))
	users := &fakeSOLister{pages: []*user.ListUsersResponse{{Users: []*user.User{enrolledSystemSO(2)}}}}
	ok, err := RecognizeEnrolledSystemSO(ctx, store, users, &fakeSOPINs{committed: true}, 1)
	require.NoError(t, err)
	assert.False(t, ok, "a pending record is consumed only by ClearPendingSystemSOIfEnrolled")
	assert.Empty(t, users.asked)
}

func TestRecognizeEnrolledSystemSO_NeedsACommittedPIN(t *testing.T) {
	ctx := context.Background()
	users := &fakeSOLister{pages: []*user.ListUsersResponse{{Users: []*user.User{enrolledSystemSO(2)}}}}

	ok, err := RecognizeEnrolledSystemSO(ctx, newMemPendingAdminCertStore(), users, &fakeSOPINs{}, 1)
	require.NoError(t, err)
	assert.False(t, ok, "no committed PIN, no enrolled SO")

	ok, err = RecognizeEnrolledSystemSO(ctx, newMemPendingAdminCertStore(), users, &fakeSOPINs{committedErr: errStoreFault}, 1)
	require.ErrorIs(t, err, errStoreFault)
	assert.False(t, ok)
	assert.Empty(t, users.asked)
}

func TestRecognizeEnrolledSystemSO_ListFaultWraps(t *testing.T) {
	ok, err := RecognizeEnrolledSystemSO(context.Background(), newMemPendingAdminCertStore(),
		&fakeSOLister{err: errStoreFault}, &fakeSOPINs{committed: true}, 1)
	var enrollErr *SystemSOEnrollmentError
	require.ErrorAs(t, err, &enrollErr)
	assert.Equal(t, "list accounts", enrollErr.Op)
	require.ErrorIs(t, err, errStoreFault)
	assert.False(t, ok)
}

func TestRecognizeEnrolledSystemSO_NilPageEndsTheScan(t *testing.T) {
	ok, err := RecognizeEnrolledSystemSO(context.Background(), newMemPendingAdminCertStore(),
		&fakeSOLister{}, &fakeSOPINs{committed: true}, 1)
	require.NoError(t, err)
	assert.False(t, ok)
}

// Only a live, active system account holding the so role, other than the
// admin, is the SO; it may sit on a later page.
func TestRecognizeEnrolledSystemSO_FindsTheSOOnALaterPage(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	admin := enrolledSystemSO(1)
	otherTenant := enrolledSystemSO(3)
	otherTenant.TenantID = "tenant-a"
	noRole := enrolledSystemSO(4)
	noRole.RoleIDs = []string{role.SystemRoleID(tenantid.System, role.RoleNameAdmin)}
	unbound := enrolledSystemSO(5)
	unbound.CertBindings = nil
	users := &fakeSOLister{pages: []*user.ListUsersResponse{
		{Users: []*user.User{nil, admin, otherTenant, noRole, unbound}, HasMore: true},
		{Users: []*user.User{enrolledSystemSO(6)}},
	}}
	ok, err := RecognizeEnrolledSystemSO(ctx, store, users, &fakeSOPINs{committed: true}, admin.ID)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []int{1, 2}, users.asked)
	assert.True(t, SystemSOConsumed(ctx, store), "recognition records the consumed marker")
}

func TestRecognizeEnrolledSystemSO_NoSOAmongTheAccounts(t *testing.T) {
	ctx := context.Background()
	admin := enrolledSystemSO(1)
	for name, pages := range map[string][]*user.ListUsersResponse{
		"last page":            {{Users: []*user.User{admin}}},
		"empty page with more": {{Users: []*user.User{admin}, HasMore: true}, {HasMore: true}},
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemPendingAdminCertStore()
			ok, err := RecognizeEnrolledSystemSO(ctx, store, &fakeSOLister{pages: pages}, &fakeSOPINs{committed: true}, admin.ID)
			require.NoError(t, err)
			assert.False(t, ok)
			assert.False(t, SystemSOConsumed(ctx, store))
		})
	}
}

func TestRecognizeEnrolledSystemSO_MarkerWriteFaultWraps(t *testing.T) {
	store := &failingPutStore{memPendingAdminCertStore: newMemPendingAdminCertStore()}
	users := &fakeSOLister{pages: []*user.ListUsersResponse{{Users: []*user.User{enrolledSystemSO(2)}}}}
	ok, err := RecognizeEnrolledSystemSO(context.Background(), store, users, &fakeSOPINs{committed: true}, 1)
	var storeErr *PendingAdminCertStoreError
	require.ErrorAs(t, err, &storeErr)
	assert.Equal(t, "record enrolled system SO", storeErr.Op)
	assert.False(t, ok)
}

// pendingSOFixture writes a pending system SO record over certPEM into a
// store and creates its system so account in users.
func pendingSOFixture(t *testing.T, store PendingAdminCertStore, certPEM []byte, pinRecord string) *soUsers {
	t.Helper()
	ctx := context.Background()
	raw, err := json.Marshal(pendingSystemSO{Username: "so", CertPEM: certPEM, PINRecord: pinRecord})
	require.NoError(t, err)
	require.NoError(t, store.Put(ctx, PendingSystemSOKey, raw))
	users := newSOUsers()
	_, err = users.CreateUser(ctx, &user.CreateUserRequest{Username: "so", TenantID: tenantid.System,
		RoleIDs: []string{role.SystemRoleID(tenantid.System, role.RoleNameSO)}})
	require.NoError(t, err)
	return users
}

func TestClearPendingSystemSOIfEnrolled_StoreReadFault(t *testing.T) {
	store := &failingGetStore{memPendingAdminCertStore: newMemPendingAdminCertStore()}
	cleared, err := ClearPendingSystemSOIfEnrolled(context.Background(), store, newSOUsers(), &fakeSOPINs{})
	require.ErrorIs(t, err, errStoreFault)
	assert.False(t, cleared)
}

func TestClearPendingSystemSOIfEnrolled_PINFaultsAreReturned(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	users := pendingSOFixture(t, store, realCertPEM(t), "record")

	cleared, err := ClearPendingSystemSOIfEnrolled(ctx, store, users, &fakeSOPINs{committedErr: errStoreFault})
	require.ErrorIs(t, err, errStoreFault)
	assert.False(t, cleared)

	cleared, err = ClearPendingSystemSOIfEnrolled(ctx, store, users, &fakeSOPINs{committed: true, matchesErr: errStoreFault})
	require.ErrorIs(t, err, errStoreFault)
	assert.False(t, cleared)
}

func TestClearPendingSystemSOIfEnrolled_RecordWithoutPINIsUnverified(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	users := pendingSOFixture(t, store, realCertPEM(t), "")
	cleared, err := ClearPendingSystemSOIfEnrolled(ctx, store, users, &fakeSOPINs{committed: true, matches: true})
	var unverified *SystemSOUnverifiedError
	require.ErrorAs(t, err, &unverified)
	assert.Contains(t, err.Error(), "the committed SO PIN is not the record's")
	assert.False(t, cleared)
}

func TestClearPendingSystemSOIfEnrolled_UnreadableCertificateIsUnverified(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	users := pendingSOFixture(t, store, []byte("not a certificate"), "record")
	cleared, err := ClearPendingSystemSOIfEnrolled(ctx, store, users, &fakeSOPINs{committed: true, matches: true})
	var unverified *SystemSOUnverifiedError
	require.ErrorAs(t, err, &unverified)
	assert.Equal(t, "so", unverified.Username)
	assert.Contains(t, err.Error(), "certificate is unreadable")
	assert.False(t, cleared)
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	assert.NotNil(t, so, "an unverified record stays")
}

func TestClearPendingSystemSOIfEnrolled_ClearFaultIsReturned(t *testing.T) {
	ctx := context.Background()
	mem := newMemPendingAdminCertStore()
	certPEM := realCertPEM(t)
	users := pendingSOFixture(t, mem, certPEM, "record")
	thumbprint, err := spkiThumbprintFromCertPEM(certPEM)
	require.NoError(t, err)
	users.byName["so"].CertBindings = []user.CertBinding{{Thumbprint: thumbprint, NotAfter: time.Now().Add(time.Hour)}}

	store := &failingPutStore{memPendingAdminCertStore: mem}
	cleared, err := ClearPendingSystemSOIfEnrolled(ctx, store, users, &fakeSOPINs{committed: true, matches: true})
	var storeErr *PendingAdminCertStoreError
	require.ErrorAs(t, err, &storeErr)
	assert.Equal(t, "clear system SO", storeErr.Op)
	assert.False(t, cleared)
}

func (f *fakeSOPINs) SetSOPIN(context.Context, string, []byte) error { return nil }

// setOnlySOPINs commits a PIN but can neither verify nor compare one.
type setOnlySOPINs struct{}

func (setOnlySOPINs) SetSOPIN(context.Context, string, []byte) error { return nil }

func TestCommittedSOPINIsOurs_RecordComparison(t *testing.T) {
	ctx := context.Background()
	so := &SOConfig{Username: "so", PINRecord: "record"}

	var seamErr *MissingSOSeamError
	require.ErrorAs(t, committedSOPINIsOurs(ctx, setOnlySOPINs{}, tenantid.System, so), &seamErr)
	assert.Contains(t, seamErr.Seam, "SOPINRecordMatcher")

	err := committedSOPINIsOurs(ctx, &fakeSOPINs{matchesErr: errStoreFault}, tenantid.System, so)
	require.ErrorIs(t, err, errStoreFault)
}

func TestSpkiThumbprintOf_UnparseableCSR(t *testing.T) {
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: []byte("not DER")})
	_, err := spkiThumbprintOf(csrPEM, nil)
	require.Error(t, err)
}

func TestCheckSOCertLogsInAsUsername_UnreadableCertificate(t *testing.T) {
	err := checkSOCertLogsInAsUsername(&SOConfig{Username: "so", CertPEM: []byte("not PEM")})
	require.ErrorContains(t, err, "no CERTIFICATE block")

	bad := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")})
	require.Error(t, checkSOCertLogsInAsUsername(&SOConfig{Username: "so", CertPEM: bad}))
}

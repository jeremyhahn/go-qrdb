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
	"encoding/pem"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

var errBranchFault = errors.New("branch fault")

// pinSetterOnly sets an SO PIN but neither verifies nor compares one.
type pinSetterOnly struct{ SOPINSetter }

// recordMatcherPINs compares a committed SO PIN record with a fixed answer.
type recordMatcherPINs struct {
	SOPINSetter
	matches bool
	err     error
}

func (p recordMatcherPINs) SOPINRecordMatches(context.Context, string, string) (bool, error) {
	return p.matches, p.err
}

func TestCommittedSOPINIsOurs_RecordBranches(t *testing.T) {
	ctx := context.Background()
	so := &SOConfig{PINRecord: "record"}
	tests := []struct {
		name    string
		pins    SOPINSetter
		wantErr func(t *testing.T, err error)
	}{
		{"no record matcher", pinSetterOnly{}, func(t *testing.T, err error) {
			var missing *MissingSOSeamError
			require.ErrorAs(t, err, &missing)
		}},
		{"matcher fault", recordMatcherPINs{err: errBranchFault}, func(t *testing.T, err error) {
			require.ErrorIs(t, err, errBranchFault)
		}},
		{"foreign record", recordMatcherPINs{}, func(t *testing.T, err error) {
			require.ErrorIs(t, err, auth.ErrSOPINInvalid)
		}},
		{"our record", recordMatcherPINs{matches: true}, func(t *testing.T, err error) {
			require.NoError(t, err)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.wantErr(t, committedSOPINIsOurs(ctx, tt.pins, tenantid.System, so))
		})
	}
}

// creatorOnlyUsers creates users but cannot update them.
type creatorOnlyUsers struct{ UserCreator }

// updatingUsers answers UpdateUser with a fixed result.
type updatingUsers struct {
	UserCreator
	updated *user.User
	err     error
}

func (u updatingUsers) UpdateUser(context.Context, *user.UpdateUserRequest) (*user.User, error) {
	return u.updated, u.err
}

func TestGrantAdminSORole_ErrorBranches(t *testing.T) {
	ctx := context.Background()
	admin := &user.User{ID: 7, TenantID: tenantid.System, RoleIDs: []string{"admin"}}
	soRole := role.SystemRoleID(tenantid.System, role.RoleNameSO)
	tests := []struct {
		name    string
		users   UserCreator
		wantErr func(t *testing.T, err error)
	}{
		{"no updater", creatorOnlyUsers{}, func(t *testing.T, err error) {
			var missing *MissingSOSeamError
			require.ErrorAs(t, err, &missing)
		}},
		{"update fault", updatingUsers{err: errBranchFault}, func(t *testing.T, err error) {
			var enroll *SystemSOEnrollmentError
			require.ErrorAs(t, err, &enroll)
			assert.Equal(t, "account", enroll.Op)
			require.ErrorIs(t, err, errBranchFault)
		}},
		{"no account returned", updatingUsers{}, func(t *testing.T, err error) {
			var enroll *SystemSOEnrollmentError
			require.ErrorAs(t, err, &enroll)
			assert.Equal(t, "account", enroll.Op)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := grantAdminSORole(ctx, Dependencies{Users: tt.users, Logger: discardLogger()}, admin, soRole)
			assert.Nil(t, got)
			tt.wantErr(t, err)
		})
	}
	assert.Equal(t, []string{"admin"}, admin.RoleIDs, "the grant must not mutate the caller's account")
}

func TestCertBound(t *testing.T) {
	u := &user.User{CertBindings: []user.CertBinding{{Thumbprint: "a"}, {Thumbprint: "b"}}}
	assert.True(t, certBound(u, "b"))
	assert.False(t, certBound(u, "c"))
	assert.False(t, certBound(&user.User{}, "a"))
}

// creatorOnlyRoles creates roles but does not resolve names.
type creatorOnlyRoles struct{ RoleCreator }

// fixedResolver is a role.NameResolver double; it is only compared by identity.
type fixedResolver struct{ role.NameResolver }

func TestRoleResolver_Sources(t *testing.T) {
	explicit := fixedResolver{}
	assert.Equal(t, role.NameResolver(explicit), roleResolver(Dependencies{RoleResolver: explicit, Roles: creatorOnlyRoles{}}))
	assert.Nil(t, roleResolver(Dependencies{Roles: creatorOnlyRoles{}}))
}

func TestSystemSOUnverifiedError_Message(t *testing.T) {
	err := &SystemSOUnverifiedError{Username: "so-user", Reason: "a reason"}
	assert.Contains(t, err.Error(), "so-user")
	assert.Contains(t, err.Error(), "a reason")
}

// scriptedPINChecker answers SOPINChecker with fixed results.
type scriptedPINChecker struct {
	committed    bool
	committedErr error
	matches      bool
	matchesErr   error
}

func (c scriptedPINChecker) SOPINCommitted(context.Context, string) (bool, error) {
	return c.committed, c.committedErr
}

func (c scriptedPINChecker) SOPINRecordMatches(context.Context, string, string) (bool, error) {
	return c.matches, c.matchesErr
}

func TestClearPendingSystemSOIfEnrolled_PINCheckerFaults(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	users := newSOUsers()
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)
	_, err = users.CreateUser(ctx, &user.CreateUserRequest{Username: so.Username, TenantID: tenantid.System,
		RoleIDs: []string{role.SystemRoleID(tenantid.System, role.RoleNameSO)}})
	require.NoError(t, err)

	tests := []struct {
		name string
		pins scriptedPINChecker
	}{
		{"committed fault", scriptedPINChecker{committedErr: errBranchFault}},
		{"record match fault", scriptedPINChecker{committed: true, matchesErr: errBranchFault}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleared, err := ClearPendingSystemSOIfEnrolled(ctx, store, users, tt.pins)
			require.ErrorIs(t, err, errBranchFault)
			assert.False(t, cleared)
			assert.False(t, SystemSOConsumed(ctx, store))
		})
	}
}

func TestSOCertParsing_RejectsUnreadableInput(t *testing.T) {
	notPEM := []byte("not pem")
	badCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage")})
	badCSR := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: []byte("garbage")})
	tests := []struct {
		name string
		run  func() error
	}{
		{"cert check: no PEM block", func() error {
			return checkSOCertLogsInAsUsername(&SOConfig{Username: "so", CertPEM: notPEM})
		}},
		{"cert check: bad DER", func() error {
			return checkSOCertLogsInAsUsername(&SOConfig{Username: "so", CertPEM: badCert})
		}},
		{"thumbprint: no CSR block", func() error {
			_, err := spkiThumbprintOf(notPEM, nil)
			return err
		}},
		{"thumbprint: bad CSR DER", func() error {
			_, err := spkiThumbprintOf(badCSR, nil)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, tt.run())
		})
	}
	require.NoError(t, checkSOCertLogsInAsUsername(&SOConfig{Username: "so"}), "no certificate has nothing to check")
}

func TestIssueSOCert_RejectsAnUnusableTrustPool(t *testing.T) {
	deps := Dependencies{SO: &SOConfig{Username: "so", CertPEM: []byte("cert"), PoolPEM: []byte("not a pool")}}
	_, err := issueSOCert(context.Background(), deps, tenantid.System)
	var rejected *AdminCertRejectedError
	require.ErrorAs(t, err, &rejected)
}

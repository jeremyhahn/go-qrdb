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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestPermissionSetsEqual pins the set semantics the built-in role provenance
// check relies on: order and repetition do not change what a role grants, but
// a missing or an extra permission does.
func TestPermissionSetsEqual(t *testing.T) {
	get, put, del := permission.KVGet, permission.KVPut, permission.KVDelete
	for _, tc := range []struct {
		name string
		a, b []permission.Permission
		want bool
	}{
		{name: "identical", a: []permission.Permission{get, put}, b: []permission.Permission{get, put}, want: true},
		{name: "order does not matter", a: []permission.Permission{get, put}, b: []permission.Permission{put, get}, want: true},
		{name: "repetition does not matter", a: []permission.Permission{get, get, put}, b: []permission.Permission{put, get}, want: true},
		{name: "both empty", a: nil, b: []permission.Permission{}, want: true},
		{name: "an extra permission", a: []permission.Permission{get, put, del}, b: []permission.Permission{get, put}, want: false},
		{name: "a missing permission", a: []permission.Permission{get}, b: []permission.Permission{get, put}, want: false},
		{name: "same size, different members", a: []permission.Permission{get, del}, b: []permission.Permission{get, put}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := permissionSetsEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("permissionSetsEqual(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// usersOnlyCreator implements UserCreator but NOT UserResolver.
type usersOnlyCreator struct{}

func (usersOnlyCreator) CreateUser(context.Context, *user.CreateUserRequest) (*user.User, error) {
	return nil, nil
}

// TestUserResolver_Precedence pins the three answers userResolver gives: an
// explicit Dependencies.UserResolver wins, Users is used when it can resolve
// too, and nil — which createAdminUser treats as "cannot verify" — otherwise.
func TestUserResolver_Precedence(t *testing.T) {
	explicit := &fakeUsers{}
	users := &fakeUsers{}

	if got := userResolver(Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled, Users: users, UserResolver: explicit}); got != UserResolver(explicit) {
		t.Errorf("explicit resolver not preferred: got %v", got)
	}
	if got := userResolver(Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled, Users: users}); got != UserResolver(users) {
		t.Errorf("Users that can resolve was not used: got %v", got)
	}
	if got := userResolver(Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled, Users: usersOnlyCreator{}}); got != nil {
		t.Errorf("want nil for a Users that cannot resolve, got %v", got)
	}
}

// scriptedAdminUsers answers CreateUser from a script and GetUserByUsername
// with a fixed record, so createAdminUser's collision handling can be driven
// through each branch.
type scriptedAdminUsers struct {
	createErrs []error // consumed one per call; exhausted means success
	lookup     *user.User
	calls      int

	// unresolvedLookups is how many lookups report not-found before lookup
	// is returned: a winner whose record lands after its username claim.
	unresolvedLookups int
	lookups           int
}

func (s *scriptedAdminUsers) CreateUser(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
	s.calls++
	if len(s.createErrs) > 0 {
		err := s.createErrs[0]
		s.createErrs = s.createErrs[1:]
		return nil, err
	}
	return &user.User{ID: 99, Username: req.Username, TenantID: req.TenantID, RoleIDs: req.RoleIDs, Lifecycle: user.LifecycleActive}, nil
}

func (s *scriptedAdminUsers) GetUserByUsername(context.Context, string, string) (*user.User, error) {
	s.lookups++
	if s.lookup == nil || s.lookups <= s.unresolvedLookups {
		return nil, &user.UserNotFoundError{Lookup: "username"}
	}
	return s.lookup, nil
}

func allBuiltInRolesPresent() *fakeRoles {
	return &fakeRoles{existsByName: map[string]bool{
		role.RoleNameSO: true, role.RoleNameAdmin: true, role.RoleNameOperator: true,
		role.RoleNameUser: true, role.RoleNameAuditor: true,
		role.RoleNameOwner: true,
	}}
}

// TestCreateAdminUser_IDCollisionWithAnotherUserRetries pins that a generated
// id that repeated some OTHER user's — no admin behind it — is retried with a
// fresh id instead of aborting first boot.
func TestCreateAdminUser_IDCollisionWithAnotherUserRetries(t *testing.T) {
	users := &scriptedAdminUsers{createErrs: []error{&user.UserIDExistsError{ID: 7, TenantID: tenantid.System}}}
	result, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{createAlreadyErr: true}, Users: users, Roles: allBuiltInRolesPresent(),
		CA: &fakeCA{cert: realCertPEM(t), key: []byte("KEY")}, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !result.AdminCreated || users.calls != 2 {
		t.Errorf("AdminCreated = %v after %d CreateUser calls, want true after 2 (one collision, one retry)", result.AdminCreated, users.calls)
	}
}

// TestCreateAdminUser_IDCollisionRetriesAreBounded: a store that reports the
// collision for every id cannot loop first boot forever.
func TestCreateAdminUser_IDCollisionRetriesAreBounded(t *testing.T) {
	collide := &user.UserIDExistsError{ID: 7, TenantID: tenantid.System}
	users := &scriptedAdminUsers{createErrs: []error{collide, collide, collide, collide, collide}}
	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{createAlreadyErr: true}, Users: users, Roles: allBuiltInRolesPresent(),
		CA: &fakeCA{cert: []byte("CERT"), key: []byte("KEY")}, Logger: discardLogger(),
	})
	var ace *AdminUserCreationError
	if !errors.As(err, &ace) || !errors.As(err, &collide) {
		t.Fatalf("Initialize err = %v, want AdminUserCreationError wrapping the id collision", err)
	}
	if users.calls != adminCreateAttempts {
		t.Errorf("CreateUser calls = %d, want %d", users.calls, adminCreateAttempts)
	}
}

// TestCreateAdminUser_UsernameSquatterIsRefusedWithoutRetry: a username held
// by a record that is not the admin is a squatter. A fresh id cannot fix it.
func TestCreateAdminUser_UsernameSquatterIsRefusedWithoutRetry(t *testing.T) {
	users := &scriptedAdminUsers{
		createErrs: []error{&user.UsernameExistsError{Username: adminUsername, TenantID: tenantid.System}},
		lookup:     &user.User{ID: 5, Username: adminUsername, TenantID: tenantid.System, RoleIDs: []string{"some-ordinary-role"}, Lifecycle: user.LifecycleActive},
	}
	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{createAlreadyErr: true}, Users: users, Roles: allBuiltInRolesPresent(),
		CA: &fakeCA{cert: []byte("CERT"), key: []byte("KEY")}, Logger: discardLogger(),
	})
	var ace *AdminUserCreationError
	if !errors.As(err, &ace) {
		t.Fatalf("Initialize err = %v, want AdminUserCreationError", err)
	}
	if users.calls != 1 {
		t.Errorf("CreateUser calls = %d, want 1: a squatted username is not retried", users.calls)
	}
}

// shrinkAdminConvergeWindow swaps createAdminUser's convergence bounds for
// test-sized ones, restoring the production values on cleanup.
func shrinkAdminConvergeWindow(t *testing.T, window, interval time.Duration) {
	t.Helper()
	origWindow, origInterval := adminConvergeWindow, adminConvergeInterval
	adminConvergeWindow, adminConvergeInterval = window, interval
	t.Cleanup(func() {
		adminConvergeWindow, adminConvergeInterval = origWindow, origInterval
	})
}

// initializeWithScriptedUsers runs Initialize over users with every other
// dependency already settled, so only the admin create is exercised.
func initializeWithScriptedUsers(ctx context.Context, t *testing.T, users *scriptedAdminUsers) (*Result, error) {
	t.Helper()
	return Initialize(ctx, Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{createAlreadyErr: true}, Users: users, Roles: allBuiltInRolesPresent(),
		CA: &fakeCA{cert: realCertPEM(t), key: []byte("KEY")}, Logger: discardLogger(),
	})
}

// TestCreateAdminUser_ConvergesOnAWinnerNotYetVisible is the lost race whose
// winner has claimed the admin username but whose record this node cannot
// read yet. The loser must wait for it and converge on that one admin, not
// refuse first boot and not create a second account.
func TestCreateAdminUser_ConvergesOnAWinnerNotYetVisible(t *testing.T) {
	shrinkAdminConvergeWindow(t, 5*time.Second, time.Millisecond)
	exists := &user.UsernameExistsError{Username: adminUsername, TenantID: tenantid.System}
	users := &scriptedAdminUsers{
		createErrs:        []error{exists, exists, exists},
		lookup:            &user.User{ID: 5, Username: adminUsername, TenantID: tenantid.System, RoleIDs: []string{role.SystemRoleID(tenantid.System, role.RoleNameAdmin)}, Lifecycle: user.LifecycleActive},
		unresolvedLookups: 2,
	}
	res, err := initializeWithScriptedUsers(context.Background(), t, users)
	if err != nil {
		t.Fatalf("Initialize: %v, want convergence on the winner's admin", err)
	}
	if res.AdminCreated {
		t.Error("AdminCreated = true, want false: the winner created the admin")
	}
	if users.calls != 3 {
		t.Errorf("CreateUser calls = %d, want 3: retried until the winner's record was visible", users.calls)
	}
}

// TestCreateAdminUser_CreatesTheAdminWhenTheWinnerRolledBack is the other
// way an unresolved claim settles: the winner failed after claiming the
// username and released it, so the retried create succeeds.
func TestCreateAdminUser_CreatesTheAdminWhenTheWinnerRolledBack(t *testing.T) {
	shrinkAdminConvergeWindow(t, 5*time.Second, time.Millisecond)
	users := &scriptedAdminUsers{
		createErrs: []error{&user.UsernameExistsError{Username: adminUsername, TenantID: tenantid.System}},
	}
	res, err := initializeWithScriptedUsers(context.Background(), t, users)
	if err != nil {
		t.Fatalf("Initialize: %v, want the retried create to succeed", err)
	}
	if !res.AdminCreated {
		t.Error("AdminCreated = false, want true: the released username was claimed by this run")
	}
	if users.calls != 2 {
		t.Errorf("CreateUser calls = %d, want 2", users.calls)
	}
}

// TestCreateAdminUser_UnresolvedUsernameIsRefusedAfterTheWindow bounds the
// wait: a username claim whose record never becomes readable still fails
// first boot, with the typed collision, once the window has passed.
func TestCreateAdminUser_UnresolvedUsernameIsRefusedAfterTheWindow(t *testing.T) {
	shrinkAdminConvergeWindow(t, 20*time.Millisecond, time.Millisecond)
	exists := &user.UsernameExistsError{Username: adminUsername, TenantID: tenantid.System}
	errs := make([]error, 10000)
	for i := range errs {
		errs[i] = exists
	}
	users := &scriptedAdminUsers{createErrs: errs}
	_, err := initializeWithScriptedUsers(context.Background(), t, users)
	var ace *AdminUserCreationError
	if !errors.As(err, &ace) || !errors.As(err, &exists) {
		t.Fatalf("Initialize err = %v, want AdminUserCreationError wrapping the username collision", err)
	}
	if users.calls < 2 || users.calls == len(errs) {
		t.Errorf("CreateUser calls = %d, want a bounded retry (more than 1, fewer than %d)", users.calls, len(errs))
	}
}

// TestCreateAdminUser_ConvergenceWaitHonoursCancellation: a caller that
// gives up while the loser is waiting gets its context's error back, typed.
func TestCreateAdminUser_ConvergenceWaitHonoursCancellation(t *testing.T) {
	shrinkAdminConvergeWindow(t, time.Hour, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	users := &scriptedAdminUsers{
		createErrs: []error{&user.UsernameExistsError{Username: adminUsername, TenantID: tenantid.System}},
	}
	_, err := initializeWithScriptedUsers(ctx, t, users)
	var ace *AdminUserCreationError
	if !errors.As(err, &ace) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Initialize err = %v, want AdminUserCreationError wrapping context.DeadlineExceeded", err)
	}
	if users.calls != 1 {
		t.Errorf("CreateUser calls = %d, want 1: cancelled during the first wait", users.calls)
	}
}

// collidingCreator implements UserCreator but NOT UserResolver, and answers
// every create with err.
type collidingCreator struct {
	err   error
	calls int
}

func (c *collidingCreator) CreateUser(context.Context, *user.CreateUserRequest) (*user.User, error) {
	c.calls++
	return nil, c.err
}

// TestCreateAdminUser_UnverifiableUsernameCollisionIsRefusedAtOnce: with no
// way to read the record back, waiting cannot verify anything, so the
// collision is refused without a retry.
func TestCreateAdminUser_UnverifiableUsernameCollisionIsRefusedAtOnce(t *testing.T) {
	shrinkAdminConvergeWindow(t, time.Hour, time.Hour)
	creator := &collidingCreator{err: &user.UsernameExistsError{Username: adminUsername, TenantID: tenantid.System}}
	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{createAlreadyErr: true}, Users: creator, Roles: allBuiltInRolesPresent(),
		CA: &fakeCA{cert: []byte("CERT"), key: []byte("KEY")}, Logger: discardLogger(),
	})
	var ace *AdminUserCreationError
	if !errors.As(err, &ace) {
		t.Fatalf("Initialize err = %v, want AdminUserCreationError", err)
	}
	if creator.calls != 1 {
		t.Errorf("CreateUser calls = %d, want 1", creator.calls)
	}
}

// TestAdminUsernameHolder pins what "the admin" means when a collision is
// verified: every property first boot gives the account, with the lifecycle
// allowed to have moved on to any defined, non-terminal state. A record that
// is anything else is another holder; a lookup that finds nothing is
// unresolved, never another holder.
func TestAdminUsernameHolder(t *testing.T) {
	const tenantID, adminRoleID = "system", "role-admin"
	admin := func(mut func(*user.User)) *user.User {
		u := &user.User{ID: 1, Username: adminUsername, TenantID: tenantID, RoleIDs: []string{adminRoleID, "role-owner"}, Lifecycle: user.LifecycleActive}
		if mut != nil {
			mut(u)
		}
		return u
	}
	for _, tc := range []struct {
		name string
		u    *user.User
		want adminHolder
	}{
		{name: "the admin", u: admin(nil), want: adminHolderAdmin},
		{name: "suspended since by an operator", u: admin(func(u *user.User) { u.Lifecycle = user.LifecycleSuspended }), want: adminHolderAdmin},
		{name: "disabled since by an operator", u: admin(func(u *user.User) { u.Lifecycle = user.LifecycleDisabled }), want: adminHolderAdmin},
		{name: "missing the admin role", u: admin(func(u *user.User) { u.RoleIDs = []string{"role-owner"} }), want: adminHolderOther},
		{name: "the admin as the System user", u: admin(func(u *user.User) { u.System = true }), want: adminHolderAdmin},
		{name: "being deleted", u: admin(func(u *user.User) { u.Lifecycle = user.LifecycleDeleting }), want: adminHolderOther},
		{name: "unknown lifecycle", u: admin(func(u *user.User) { u.Lifecycle = "hibernating" }), want: adminHolderOther},
		{name: "no lifecycle", u: admin(func(u *user.User) { u.Lifecycle = "" }), want: adminHolderOther},
		{name: "another tenant's record", u: admin(func(u *user.User) { u.TenantID = "acme" }), want: adminHolderOther},
		{name: "another username", u: admin(func(u *user.User) { u.Username = "root" }), want: adminHolderOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := adminUsernameHolder(context.Background(), &scriptedAdminUsers{lookup: tc.u}, tenantID, adminRoleID)
			if got != tc.want {
				t.Errorf("adminUsernameHolder = %v, want %v", got, tc.want)
			}
		})
	}
	if got := adminUsernameHolder(context.Background(), nil, tenantID, adminRoleID); got != adminHolderUnverifiable {
		t.Errorf("nil resolver: adminUsernameHolder = %v, want adminHolderUnverifiable: it cannot verify anything", got)
	}
	if got := adminUsernameHolder(context.Background(), &scriptedAdminUsers{}, tenantID, adminRoleID); got != adminHolderUnresolved {
		t.Errorf("failed lookup: adminUsernameHolder = %v, want adminHolderUnresolved: the claim's record is not visible yet", got)
	}
}

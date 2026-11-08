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

package authtest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// The binding fixture. A RoleBinding may name only its holder's own tenant,
// and the fixture is the one every transport's test drives to hold the six
// transports to one answer:
//
//   - an administrator of BindingTenant holding that tenant's Admin role
//     through its own RoleIDs, the caller that administers BindingTenant's
//     users and roles;
//   - a user in BindingTenant, and a user in OtherTenant the administrator may
//     not reach;
//   - a system-tenant user holding nothing, so a test can show a binding on a
//     system-tenant account is held to the same own-tenant rule.
//
// The role store is BuiltInRoles(t, tenantid.System, BindingTenant,
// OtherTenant).
const (
	// BindingTenant is the administrator's tenant.
	BindingTenant = "acme"
	// OtherTenant is a tenant the administrator holds nothing in.
	OtherTenant = "globex"
	// BindingTenantAdminName is the BindingTenant administrator's username
	// and certificate subject.
	BindingTenantAdminName = "acme-admin"

	// SystemUserID, BindingTenantAdminID, UserInBindingTenant and
	// UserInOtherTenant are the fixture's user ids.
	SystemUserID         uint64 = 100
	BindingTenantAdminID uint64 = 400
	UserInBindingTenant  uint64 = 200
	UserInOtherTenant    uint64 = 300
)

// BindingTenantAdmin returns the identity BindingTenant's administrator
// authenticates as, before the middleware enriches it with the Admin role its
// record holds.
func BindingTenantAdmin() *auth.Identity {
	return &auth.Identity{Subject: BindingTenantAdminName, TenantID: BindingTenant}
}

// BindingUsers is the fixture's user store. GetUser answers as the routed
// production service does: a caller outside the system tenant cannot see
// another tenant's user and is told it does not exist.
type BindingUsers struct {
	user.UserServicer

	mu      sync.Mutex
	records map[uint64]*user.User
	updated []uint64
	created []string
	nextID  uint64
}

// NewBindingUsers returns the fixture's user store.
func NewBindingUsers() *BindingUsers {
	now := time.Now().UTC()
	rec := func(id uint64, name, tenantID string) *user.User {
		return &user.User{ID: id, Username: name, TenantID: tenantID, Lifecycle: user.LifecycleActive, RoleIDs: []string{}, CreatedAt: now, UpdatedAt: now}
	}
	admin := rec(BindingTenantAdminID, BindingTenantAdminName, BindingTenant)
	admin.RoleIDs = []string{role.SystemRoleID(BindingTenant, role.RoleNameAdmin)}
	return &BindingUsers{nextID: 1000, records: map[uint64]*user.User{
		SystemUserID:         rec(SystemUserID, "operator", tenantid.System),
		BindingTenantAdminID: admin,
		UserInBindingTenant:  rec(UserInBindingTenant, "alice", BindingTenant),
		UserInOtherTenant:    rec(UserInOtherTenant, "bob", OtherTenant),
	}}
}

// GetUser returns the record for id, subject to tenant visibility.
func (u *BindingUsers) GetUser(ctx context.Context, id uint64) (*user.User, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	r, ok := u.records[id]
	caller := tenant.FromContext(ctx)
	if !ok || (!tenantid.IsSystem(caller) && r.TenantID != caller) {
		return nil, &user.UserNotFoundError{ID: id}
	}
	cp := *r
	return &cp, nil
}

// GetUserByUsername resolves a login within its tenant.
func (u *BindingUsers) GetUserByUsername(_ context.Context, tenantID, username string) (*user.User, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, r := range u.records {
		if r.Username == username && r.TenantID == tenantid.Normalize(tenantID) {
			cp := *r
			return &cp, nil
		}
	}
	return nil, &user.UserNotFoundError{Lookup: "username " + username}
}

// UpdateUser records the update and returns the record.
func (u *BindingUsers) UpdateUser(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	rec, err := u.GetUser(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	// The store's own rule, applied before anything is recorded so a refused
	// write leaves no trace, exactly as production refuses before writing.
	bindings, berr := user.NormalizeRoleBindings(rec.ID, rec.TenantID, req.RoleBindings)
	if berr != nil {
		return nil, berr
	}
	// The caller's guard, judged against the record this write replaces, as
	// the store judges it against the base of its conditional commit.
	if req.Guard != nil {
		next := *rec
		if req.RoleIDs != nil {
			next.RoleIDs = req.RoleIDs
		}
		if req.RoleBindings != nil {
			next.RoleBindings = bindings
		}
		if req.Attributes != nil {
			next.Attributes = req.Attributes
		}
		if gerr := req.Guard(rec, &next); gerr != nil {
			return nil, gerr
		}
	}
	u.mu.Lock()
	u.updated = append(u.updated, req.ID)
	u.mu.Unlock()
	if req.Name != "" {
		rec.Name = req.Name
	}
	if req.RoleIDs != nil {
		rec.RoleIDs = append([]string(nil), req.RoleIDs...)
	}
	if req.RoleBindings != nil {
		rec.RoleBindings = bindings
	}
	u.mu.Lock()
	u.records[rec.ID] = rec
	u.mu.Unlock()
	cp := *rec
	return &cp, nil
}

// CreateUser records a user in the tenant the request names and returns it.
// The tenant is resolved as the routed production service resolves it,
// tenant.EffectiveTenant: the caller's own when the request names none, and
// a non-system caller naming another tenant is refused.
func (u *BindingUsers) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	tenantID, err := tenant.EffectiveTenant(ctx, req.TenantID, "create user")
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.nextID++
	rec := &user.User{ID: u.nextID, Username: req.Username, TenantID: tenantID, RoleIDs: append([]string{}, req.RoleIDs...), Lifecycle: user.LifecycleActive}
	u.records[rec.ID] = rec
	u.created = append(u.created, tenantID)
	cp := *rec
	return &cp, nil
}

// ListUsers lists the users of the tenant the request names, resolved as
// CreateUser resolves it.
func (u *BindingUsers) ListUsers(ctx context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	tenantID, err := tenant.EffectiveTenant(ctx, req.TenantID, "list users")
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	resp := &user.ListUsersResponse{}
	for _, r := range u.records {
		if r.TenantID == tenantID {
			cp := *r
			resp.Users = append(resp.Users, &cp)
		}
	}
	resp.Total = len(resp.Users)
	return resp, nil
}

// Created returns the tenant of every user CreateUser recorded, in order.
func (u *BindingUsers) Created() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.created...)
}

// User returns a copy of the stored record for id, bypassing visibility.
func (u *BindingUsers) User(id uint64) *user.User {
	u.mu.Lock()
	defer u.mu.Unlock()
	r, ok := u.records[id]
	if !ok {
		return nil
	}
	cp := *r
	return &cp
}

// Updated returns the ids of every update that reached the store.
func (u *BindingUsers) Updated() []uint64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]uint64(nil), u.updated...)
}

// BindingRoles returns the fixture's role store: the real built-in roles of
// the system tenant, BindingTenant and OtherTenant.
func BindingRoles(t testing.TB) role.RoleServicer {
	t.Helper()
	return BuiltInRoles(t, tenantid.System, BindingTenant, OtherTenant)
}

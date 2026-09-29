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

// The binding fixture: a system-tenant operator whose ONLY grant is a
// RoleBinding for the built-in Admin role of BindingTenant, a user in that
// tenant, and a user in OtherTenant the binding does not reach. Every
// transport's target-tenant test drives its own request shape through its own
// route-level check against this one fixture, over a role store built by
// BuiltInRoles(t, tenantid.System, BindingTenant, OtherTenant), so the
// six transports are held to one answer: updating the BindingTenant user is
// authorized, updating the OtherTenant user is refused.
const (
	// BindingTenant is the tenant the operator's binding names.
	BindingTenant = "acme"
	// OtherTenant is a tenant the operator holds nothing in.
	OtherTenant = "globex"
	// BindingOperatorName is the operator's username and certificate subject.
	BindingOperatorName = "operator"

	// BindingOperatorID, UserInBindingTenant and UserInOtherTenant are the
	// fixture's user ids.
	BindingOperatorID   uint64 = 100
	UserInBindingTenant uint64 = 200
	UserInOtherTenant   uint64 = 300
)

// BindingOperator returns the identity the operator authenticates as, before
// the middleware enriches it from the user store: a system-tenant subject
// holding no roles of its own.
func BindingOperator() *auth.Identity {
	return &auth.Identity{Subject: BindingOperatorName, TenantID: tenantid.System}
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

// NewBindingUsers returns the fixture's user store, the operator's record
// carrying its one binding.
func NewBindingUsers() *BindingUsers {
	now := time.Now().UTC()
	rec := func(id uint64, name, tenantID string) *user.User {
		return &user.User{ID: id, Username: name, TenantID: tenantID, Lifecycle: user.LifecycleActive, RoleIDs: []string{}, CreatedAt: now, UpdatedAt: now}
	}
	op := rec(BindingOperatorID, BindingOperatorName, tenantid.System)
	op.RoleBindings = []user.RoleBinding{{RoleID: role.SystemRoleID(BindingTenant, role.RoleNameAdmin), TenantID: BindingTenant}}
	return &BindingUsers{nextID: 1000, records: map[uint64]*user.User{
		BindingOperatorID:   op,
		UserInBindingTenant: rec(UserInBindingTenant, "alice", BindingTenant),
		UserInOtherTenant:   rec(UserInOtherTenant, "bob", OtherTenant),
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

// CreateUser records a user in the tenant the request names (the caller's
// own tenant when it names none) and returns it.
func (u *BindingUsers) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = tenant.FromContext(ctx)
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

// ListUsers lists the users of the tenant the request names.
func (u *BindingUsers) ListUsers(ctx context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = tenant.FromContext(ctx)
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

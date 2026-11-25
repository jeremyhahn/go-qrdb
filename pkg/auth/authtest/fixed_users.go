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
	"hash/fnv"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// Users returns a read-only user store holding exactly one record: the
// account of the identity the authenticator CURRENTLY authenticates as (so
// Set keeps working). Pair it with the authenticator
// (authmw.WithUserService(fa.Users())): the authentication chain discards
// every role an authenticator returns and takes Roles, RoleBindings and
// Attributes only from the bound user record (SPEC authz-claim-roles), so a
// fixed identity's roles reach the authorizer only through this record.
//
// The record is active, carries a deterministic non-zero ID (the
// identity's UserID when set, else one derived from its tenant and
// subject), Username = the identity's Subject, its TenantID, RoleIDs = its
// Roles, and copies of its RoleBindings and Attributes. Any other account
// answers *user.UserNotFoundError, as the real store does.
func (a *FixedAuthenticator) Users() user.UserServicer { return fixedUsers{a: a} }

// fixedUsers is FixedAuthenticator.Users' store. It embeds the interface
// so a write or listing a test never expects panics instead of silently
// succeeding.
type fixedUsers struct {
	user.UserServicer
	a *FixedAuthenticator
}

var _ user.UserServicer = fixedUsers{}

// record returns the current identity's user record, or nil when the
// authenticator has no identity.
func (u fixedUsers) record() *user.User {
	id := u.a.identity.Load()
	if id == nil {
		return nil
	}
	rec := &user.User{
		ID:        fixedUserID(id),
		Username:  id.Subject,
		TenantID:  tenantid.Normalize(id.TenantID),
		Lifecycle: user.LifecycleActive,
		RoleIDs:   append([]string(nil), id.Roles...),
	}
	for _, b := range id.RoleBindings {
		rec.RoleBindings = append(rec.RoleBindings, user.RoleBinding{RoleID: b.RoleID, TenantID: b.TenantID})
	}
	if id.Attributes != nil {
		rec.Attributes = make(map[string]string, len(id.Attributes))
		for k, v := range id.Attributes {
			rec.Attributes[k] = v
		}
	}
	return rec
}

// fixedUserID is the identity's UserID when set, else a stable non-zero id
// derived from its tenant and subject.
func fixedUserID(id *auth.Identity) uint64 {
	if id.UserID != 0 {
		return id.UserID
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(tenantid.Normalize(id.TenantID) + "\x00" + id.Subject))
	if v := h.Sum64() >> 1; v != 0 {
		return v
	}
	return 1
}

// GetUser resolves the current identity's record by id.
func (u fixedUsers) GetUser(_ context.Context, id uint64) (*user.User, error) {
	if rec := u.record(); rec != nil && rec.ID == id {
		return rec, nil
	}
	return nil, &user.UserNotFoundError{ID: id}
}

// GetUserByUsername resolves the current identity's record by its login
// within its tenant.
func (u fixedUsers) GetUserByUsername(_ context.Context, tenantID, username string) (*user.User, error) {
	if rec := u.record(); rec != nil && rec.Username == username && rec.TenantID == tenantid.Normalize(tenantID) {
		return rec, nil
	}
	return nil, &user.UserNotFoundError{Lookup: "username " + username}
}

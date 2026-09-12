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

package user

import "context"

// UserServicer defines user management operations. All implementations
// must be safe for concurrent use.
type UserServicer interface {
	// CreateUser creates a new passwordless user. The username must be
	// unique within the tenant. The ID is assigned by the store.
	CreateUser(ctx context.Context, req *CreateUserRequest) (*User, error)

	// GetUser retrieves a user by its globally unique ID.
	GetUser(ctx context.Context, id uint64) (*User, error)

	// GetUserByUsername retrieves a user by tenant-scoped username.
	GetUserByUsername(ctx context.Context, tenantID, username string) (*User, error)

	// GetUserByEmail retrieves a user by tenant-scoped email — the canonical
	// login key. This is the lookup a login ceremony performs to join a
	// verified factor to the record that says what its holder may do.
	GetUserByEmail(ctx context.Context, tenantID, email string) (*User, error)

	// UserByCertThumbprint resolves a user by an mTLS certificate's SPKI
	// thumbprint via the secondary index. This is the mTLS-auth lookup:
	// given a presented client cert, compute ComputeSPKIThumbprint and
	// resolve the bound user. Returns UserNotFoundError when no user is
	// bound to the thumbprint.
	UserByCertThumbprint(ctx context.Context, thumbprint string) (*User, error)

	// AddCertBinding binds an mTLS certificate's SPKI thumbprint to the user
	// so UserByCertThumbprint resolves it. A notAfterUnix > 0 sets the
	// binding's expiry; otherwise the binding never expires. This is the
	// server-side enrollment seam used to bind an admin's cert to a user.
	AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*User, error)

	// RemoveCertBinding withdraws the binding for thumbprint from the user
	// so UserByCertThumbprint no longer resolves it and the presented
	// certificate no longer authenticates. It is the counterpart of
	// AddCertBinding and the only way to unenroll a key.
	RemoveCertBinding(ctx context.Context, userID uint64, thumbprint string) (*User, error)

	// UpdateUser applies partial updates to an existing user.
	UpdateUser(ctx context.Context, req *UpdateUserRequest) (*User, error)

	// DeleteUser removes a non-system user by ID.
	DeleteUser(ctx context.Context, id uint64) error

	// ListUsers returns a paginated list of users for the given tenant.
	ListUsers(ctx context.Context, req *ListUsersRequest) (*ListUsersResponse, error)

	// EnableUser activates a user account.
	EnableUser(ctx context.Context, id uint64) error

	// DisableUser deactivates a user account.
	DisableUser(ctx context.Context, id uint64) error
}

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
	// unique within the tenant.
	CreateUser(ctx context.Context, req *CreateUserRequest) (*User, error)

	// GetUser retrieves a user by its globally unique ID.
	GetUser(ctx context.Context, id string) (*User, error)

	// GetUserByUsername retrieves a user by tenant-scoped username.
	GetUserByUsername(ctx context.Context, tenantID, username string) (*User, error)

	// UpdateUser applies partial updates to an existing user.
	UpdateUser(ctx context.Context, req *UpdateUserRequest) (*User, error)

	// DeleteUser removes a non-system user by ID.
	DeleteUser(ctx context.Context, id string) error

	// ListUsers returns a paginated list of users for the given tenant.
	ListUsers(ctx context.Context, req *ListUsersRequest) (*ListUsersResponse, error)

	// EnableUser activates a user account.
	EnableUser(ctx context.Context, id string) error

	// DisableUser deactivates a user account.
	DisableUser(ctx context.Context, id string) error
}

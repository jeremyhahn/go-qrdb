// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// UserBinding exposes user management operations to the Wails frontend.
type UserBinding struct {
	logger *sdk.ConcreteLogger
	conn   *ConnectionBinding
}

// NewUserBinding creates a new UserBinding backed by the given connection.
func NewUserBinding(log *sdk.ConcreteLogger, conn *ConnectionBinding) *UserBinding {
	return &UserBinding{logger: log, conn: conn}
}

// Create creates a new passwordless user.
func (b *UserBinding) Create(req *transport.CreateUserRequest) (*transport.User, error) {
	b.logger.Debug("Create called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Create failed", "error", err)
		return nil, err
	}
	user, err := cl.Users().CreateUser(context.Background(), req)
	if err != nil {
		b.logger.Error("Create failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Create completed", "userID", user.ID)
	return user, nil
}

// Get retrieves a user by its globally unique ID.
func (b *UserBinding) Get(id string) (*transport.User, error) {
	b.logger.Debug("Get called", "id", id)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Get failed", "error", err)
		return nil, err
	}
	uid, err := parseUserID(id)
	if err != nil {
		b.logger.Error("Get failed", "id", id, "error", err)
		return nil, err
	}
	user, err := cl.Users().GetUser(context.Background(), uid)
	if err != nil {
		b.logger.Error("Get failed", "id", id, "error", err)
		return nil, err
	}
	b.logger.Debug("Get completed", "id", id)
	return user, nil
}

// Update applies partial updates to an existing user.
func (b *UserBinding) Update(req *transport.UpdateUserRequest) (*transport.User, error) {
	b.logger.Debug("Update called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Update failed", "error", err)
		return nil, err
	}
	user, err := cl.Users().UpdateUser(context.Background(), req)
	if err != nil {
		b.logger.Error("Update failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Update completed")
	return user, nil
}

// Delete removes a non-system user by ID.
func (b *UserBinding) Delete(id string) error {
	b.logger.Debug("Delete called", "id", id)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Delete failed", "error", err)
		return err
	}
	uid, err := parseUserID(id)
	if err != nil {
		b.logger.Error("Delete failed", "id", id, "error", err)
		return err
	}
	if err := cl.Users().DeleteUser(context.Background(), uid); err != nil {
		b.logger.Error("Delete failed", "id", id, "error", err)
		return err
	}
	b.logger.Debug("Delete completed", "id", id)
	return nil
}

// List returns a paginated list of users for the given tenant.
func (b *UserBinding) List(req *transport.ListUsersRequest) (*transport.ListUsersResponse, error) {
	b.logger.Debug("List called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	resp, err := cl.Users().ListUsers(context.Background(), req)
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	b.logger.Debug("List completed")
	return resp, nil
}

// Enable activates a user account.
func (b *UserBinding) Enable(id string) error {
	b.logger.Debug("Enable called", "id", id)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Enable failed", "error", err)
		return err
	}
	uid, err := parseUserID(id)
	if err != nil {
		b.logger.Error("Enable failed", "id", id, "error", err)
		return err
	}
	if err := cl.Users().EnableUser(context.Background(), uid); err != nil {
		b.logger.Error("Enable failed", "id", id, "error", err)
		return err
	}
	b.logger.Debug("Enable completed", "id", id)
	return nil
}

// Disable deactivates a user account.
func (b *UserBinding) Disable(id string) error {
	b.logger.Debug("Disable called", "id", id)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Disable failed", "error", err)
		return err
	}
	uid, err := parseUserID(id)
	if err != nil {
		b.logger.Error("Disable failed", "id", id, "error", err)
		return err
	}
	if err := cl.Users().DisableUser(context.Background(), uid); err != nil {
		b.logger.Error("Disable failed", "id", id, "error", err)
		return err
	}
	b.logger.Debug("Disable completed", "id", id)
	return nil
}

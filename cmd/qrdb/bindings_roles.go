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

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// RoleBinding exposes role management operations to the Wails frontend.
type RoleBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
}

// NewRoleBinding creates a new RoleBinding backed by the given connection.
func NewRoleBinding(log *logger.Logger, conn *ConnectionBinding) *RoleBinding {
	return &RoleBinding{logger: log, conn: conn}
}

// Create creates a new role with a generated UUID.
func (b *RoleBinding) Create(req *transport.CreateRoleRequest) (*transport.Role, error) {
	b.logger.Debug("Create called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Create failed", "error", err)
		return nil, err
	}
	role, err := cl.Roles().CreateRole(context.Background(), req)
	if err != nil {
		b.logger.Error("Create failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Create completed", "roleID", role.ID)
	return role, nil
}

// Get retrieves a role by its globally unique ID.
func (b *RoleBinding) Get(id string) (*transport.Role, error) {
	b.logger.Debug("Get called", "id", id)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Get failed", "error", err)
		return nil, err
	}
	role, err := cl.Roles().GetRole(context.Background(), id)
	if err != nil {
		b.logger.Error("Get failed", "id", id, "error", err)
		return nil, err
	}
	b.logger.Debug("Get completed", "id", id)
	return role, nil
}

// Update applies partial updates to an existing role.
func (b *RoleBinding) Update(req *transport.UpdateRoleRequest) (*transport.Role, error) {
	b.logger.Debug("Update called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Update failed", "error", err)
		return nil, err
	}
	role, err := cl.Roles().UpdateRole(context.Background(), req)
	if err != nil {
		b.logger.Error("Update failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Update completed")
	return role, nil
}

// Delete removes a non-system role by ID.
func (b *RoleBinding) Delete(id string) error {
	b.logger.Debug("Delete called", "id", id)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Delete failed", "error", err)
		return err
	}
	if err := cl.Roles().DeleteRole(context.Background(), id); err != nil {
		b.logger.Error("Delete failed", "id", id, "error", err)
		return err
	}
	b.logger.Debug("Delete completed", "id", id)
	return nil
}

// List returns a paginated list of roles for the given tenant.
func (b *RoleBinding) List(req *transport.ListRolesRequest) (*transport.ListRolesResponse, error) {
	b.logger.Debug("List called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	resp, err := cl.Roles().ListRoles(context.Background(), req)
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	b.logger.Debug("List completed")
	return resp, nil
}

// Assign assigns a role to a user.
func (b *RoleBinding) Assign(userID, roleID string) error {
	b.logger.Debug("Assign called", "userID", userID, "roleID", roleID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Assign failed", "error", err)
		return err
	}
	uid, err := parseUserID(userID)
	if err != nil {
		b.logger.Error("Assign failed", "error", err)
		return err
	}
	if err := cl.Roles().AssignRole(context.Background(), uid, roleID); err != nil {
		b.logger.Error("Assign failed", "error", err)
		return err
	}
	b.logger.Debug("Assign completed")
	return nil
}

// Unassign removes a role assignment from a user.
func (b *RoleBinding) Unassign(userID, roleID string) error {
	b.logger.Debug("Unassign called", "userID", userID, "roleID", roleID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Unassign failed", "error", err)
		return err
	}
	uid, err := parseUserID(userID)
	if err != nil {
		b.logger.Error("Unassign failed", "error", err)
		return err
	}
	if err := cl.Roles().UnassignRole(context.Background(), uid, roleID); err != nil {
		b.logger.Error("Unassign failed", "error", err)
		return err
	}
	b.logger.Debug("Unassign completed")
	return nil
}

// ListPermissions returns all available permission categories for UI rendering.
func (b *RoleBinding) ListPermissions() (*transport.ListPermissionsResponse, error) {
	b.logger.Debug("ListPermissions called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("ListPermissions failed", "error", err)
		return nil, err
	}
	resp, err := cl.Roles().ListPermissions(context.Background())
	if err != nil {
		b.logger.Error("ListPermissions failed", "error", err)
		return nil, err
	}
	b.logger.Debug("ListPermissions completed")
	return resp, nil
}

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

package group

import (
	"errors"
	"fmt"
)

// ErrStoreNotAtomic is returned when the backing kvstore cannot claim a
// group name atomically (kvstore.AtomicKVStore).
var ErrStoreNotAtomic = errors.New("group: kvstore does not support atomic operations")

// GroupNotFoundError is returned when a group cannot be found, by ID or by
// name. A group of another tenant is reported as not found.
type GroupNotFoundError struct {
	ID       string
	Name     string
	TenantID string
}

// Error returns a human-readable description of the error.
func (e *GroupNotFoundError) Error() string {
	if e.Name != "" {
		return fmt.Sprintf("group %q not found in tenant %q", e.Name, e.TenantID)
	}
	return fmt.Sprintf("group %q not found", e.ID)
}

// GroupNameTakenError is returned when a group name is already in use within
// the tenant.
type GroupNameTakenError struct {
	Name     string
	TenantID string
}

// Error returns a human-readable description of the error.
func (e *GroupNameTakenError) Error() string {
	return fmt.Sprintf("group name %q already exists in tenant %q", e.Name, e.TenantID)
}

// RoleNotInTenantError is returned when a group would carry a role that does
// not resolve in the group's own tenant: a group may carry only roles of its
// own tenant.
type RoleNotInTenantError struct {
	RoleID   string
	TenantID string
}

// Error returns a human-readable description of the error.
func (e *RoleNotInTenantError) Error() string {
	return fmt.Sprintf("role %q is not a role of tenant %q", e.RoleID, e.TenantID)
}

// ValidationError is returned when a request fails validation.
type ValidationError struct {
	Field  string
	Reason string
}

// Error returns a human-readable description of the error.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("group: invalid %s: %s", e.Field, e.Reason)
}

// StoreError wraps a failure of the backing kvstore.
type StoreError struct {
	Op     string
	Detail string
	Err    error
}

// Error returns a human-readable description of the error.
func (e *StoreError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("group store %s: %s: %v", e.Op, e.Detail, e.Err)
	}
	return fmt.Sprintf("group store %s: %s", e.Op, e.Detail)
}

// Unwrap returns the underlying error.
func (e *StoreError) Unwrap() error { return e.Err }

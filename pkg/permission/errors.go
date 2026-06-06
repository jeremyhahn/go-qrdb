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

package permission

import "errors"

// ErrNilStoreResolver is returned by NewTenantScopeStore when the supplied
// StoreResolver is nil.
var ErrNilStoreResolver = errors.New("permission: nil store resolver")

// InvalidPermissionError is returned when a permission string does not
// conform to the required "resource:action" format.
type InvalidPermissionError struct {
	Value string
}

// Error returns a human-readable description of the malformed permission.
func (e *InvalidPermissionError) Error() string {
	return "permission: invalid format: " + e.Value + " (expected resource:action)"
}

// UnregisteredPermissionError is returned when an operation references a
// permission that has not been registered in the global registry.
type UnregisteredPermissionError struct {
	Permission Permission
}

// Error returns a human-readable description of the unregistered permission.
func (e *UnregisteredPermissionError) Error() string {
	return "permission: not registered: " + string(e.Permission)
}

// InvalidScopeError is returned by ParseScope when a stored scope label is
// not one of the recognized scope constants. It signals a corrupt or
// forward-incompatible persisted value; callers fail closed to ScopeSelf.
type InvalidScopeError struct {
	Value string
}

// Error returns a human-readable description of the invalid scope label.
func (e *InvalidScopeError) Error() string {
	return "permission: invalid scope label: " + e.Value
}

// EmptyPermissionError is returned by TenantScopeStore operations when an
// empty permission name is supplied. A per-tenant scope definition must
// name a non-empty permission.
type EmptyPermissionError struct{}

// Error returns a human-readable description of the empty-permission error.
func (e *EmptyPermissionError) Error() string {
	return "permission: empty permission name"
}

// ScopeDecodeError wraps a failure to decode a persisted per-tenant scope
// definition. It carries the offending permission and the underlying
// cause. Resolution fails closed when this occurs.
type ScopeDecodeError struct {
	Permission Permission
	Err        error
}

// Error returns a human-readable description of the decode failure.
func (e *ScopeDecodeError) Error() string {
	return "permission: decode scope for " + string(e.Permission) + ": " + e.Err.Error()
}

// Unwrap returns the underlying cause for errors.Is / errors.As.
func (e *ScopeDecodeError) Unwrap() error {
	return e.Err
}

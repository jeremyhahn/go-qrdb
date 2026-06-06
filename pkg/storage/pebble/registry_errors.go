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

package pebble

import "fmt"

// EmptyTenantIDError is returned when a registry method is called with
// an empty tenant id.
type EmptyTenantIDError struct{}

// Error implements the error interface.
func (e *EmptyTenantIDError) Error() string {
	return "qrdb/storage/pebble: tenant id must not be empty"
}

// EmptyPathError is returned when Open is called with an empty path.
type EmptyPathError struct{}

// Error implements the error interface.
func (e *EmptyPathError) Error() string {
	return "qrdb/storage/pebble: per-tenant path must not be empty"
}

// RegistryClosedError is returned by Open after CloseAll has been
// invoked. The registry is single-cycle: once shut down it cannot be
// re-used.
type RegistryClosedError struct{}

// Error implements the error interface.
func (e *RegistryClosedError) Error() string {
	return "qrdb/storage/pebble: per-tenant registry is closed"
}

// TenantPathConflictError is returned when Open is called twice for
// the same tenant id but with different on-disk paths. This indicates
// either a programming error or an attempted live migration; both
// require explicit Close + re-Open.
type TenantPathConflictError struct {
	TenantID      string
	ExistingPath  string
	RequestedPath string
}

// Error implements the error interface.
func (e *TenantPathConflictError) Error() string {
	return fmt.Sprintf(
		"qrdb/storage/pebble: tenant %q already opened at %q, refused to re-open at %q",
		e.TenantID, e.ExistingPath, e.RequestedPath)
}

// NotRegisteredError is returned by Get when the tenant id has not
// been registered (or has been closed and removed).
type NotRegisteredError struct {
	TenantID string
}

// Error implements the error interface.
func (e *NotRegisteredError) Error() string {
	return fmt.Sprintf("qrdb/storage/pebble: tenant %q is not registered", e.TenantID)
}

// OpenError wraps an underlying Pebble open failure with the tenant
// id and path that triggered it.
type OpenError struct {
	TenantID string
	Path     string
	Err      error
}

// Error implements the error interface.
func (e *OpenError) Error() string {
	return fmt.Sprintf("qrdb/storage/pebble: open tenant %q at %q: %v",
		e.TenantID, e.Path, e.Err)
}

// Unwrap returns the underlying error.
func (e *OpenError) Unwrap() error { return e.Err }

// CloseError wraps an underlying Pebble close failure with the tenant
// id and path that triggered it.
type CloseError struct {
	TenantID string
	Path     string
	Err      error
}

// Error implements the error interface.
func (e *CloseError) Error() string {
	return fmt.Sprintf("qrdb/storage/pebble: close tenant %q at %q: %v",
		e.TenantID, e.Path, e.Err)
}

// Unwrap returns the underlying error.
func (e *CloseError) Unwrap() error { return e.Err }

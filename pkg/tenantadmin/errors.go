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

package tenantadmin

import "strconv"

// InvalidTenantError is returned for a tenant id that cannot have a pending
// admin: one that cannot name a keyspace, or the system tenant.
type InvalidTenantError struct {
	TenantID string
	Err      error
}

// Error names the tenant.
func (e *InvalidTenantError) Error() string {
	msg := "tenantadmin: tenant " + strconv.Quote(e.TenantID) + " cannot have a pending admin"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the validation cause.
func (e *InvalidTenantError) Unwrap() error { return e.Err }

// IncompleteError is returned by Put for a record without a username or a
// thumbprint.
type IncompleteError struct{}

// Error describes the refusal.
func (e *IncompleteError) Error() string {
	return "tenantadmin: a pending admin needs a username and a certificate thumbprint"
}

// StoreError wraps a failure of the backing store.
type StoreError struct {
	Op  string
	Err error
}

// Error names the operation.
func (e *StoreError) Error() string {
	return "tenantadmin: store " + e.Op + ": " + e.Err.Error()
}

// Unwrap returns the store failure.
func (e *StoreError) Unwrap() error { return e.Err }

// AlreadyPendingError is returned by Create when the tenant already has a
// pending admin: the record is created once and never replaced.
type AlreadyPendingError struct {
	TenantID string
}

// Error names the tenant.
func (e *AlreadyPendingError) Error() string {
	return "tenantadmin: tenant " + e.TenantID + " already has a pending admin"
}

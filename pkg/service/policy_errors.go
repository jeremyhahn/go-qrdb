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

package service

import "fmt"

// PolicyInvalidError reports an ABAC policy document the policy service
// refuses to store: a name outside the accepted charset, or a document that
// does not validate and compile (pkg/auth/abac). Err carries the abac error
// when there is one.
type PolicyInvalidError struct {
	Name   string
	Reason string
	Err    error
}

func (e *PolicyInvalidError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("service: invalid abac policy %q: %s: %v", e.Name, e.Reason, e.Err)
	}
	return fmt.Sprintf("service: invalid abac policy %q: %s", e.Name, e.Reason)
}

func (e *PolicyInvalidError) Unwrap() error { return e.Err }

// PolicyNotFoundError reports that the tenant holds no ABAC policy of the
// given name.
type PolicyNotFoundError struct {
	Tenant string
	Name   string
}

func (e *PolicyNotFoundError) Error() string {
	return fmt.Sprintf("service: abac policy %q not found in tenant %q", e.Name, e.Tenant)
}

// PolicyExistsError reports a create of an ABAC policy whose name the tenant
// already holds.
type PolicyExistsError struct {
	Tenant string
	Name   string
}

func (e *PolicyExistsError) Error() string {
	return fmt.Sprintf("service: abac policy %q already exists in tenant %q", e.Name, e.Tenant)
}

// PolicyConflictError reports an update or delete that lost a race: the
// stored document changed between the read and the guarded write, so the
// store is unchanged and the caller may read again and retry.
type PolicyConflictError struct {
	Tenant string
	Name   string
}

func (e *PolicyConflictError) Error() string {
	return fmt.Sprintf("service: abac policy %q in tenant %q changed concurrently", e.Name, e.Tenant)
}

// PolicyCrossTenantError reports a request naming a tenant other than the
// caller's own. The policy service acts on the caller's tenant only, and
// refuses another even for a system-tenant caller.
type PolicyCrossTenantError struct {
	CallerTenant    string
	RequestedTenant string
}

func (e *PolicyCrossTenantError) Error() string {
	return fmt.Sprintf("service: abac policies of tenant %q are not reachable from tenant %q", e.RequestedTenant, e.CallerTenant)
}

// PolicyCorruptError reports a stored ABAC policy record that does not decode.
// The record is persisted state the service cannot trust, so it is refused
// rather than read as some other document.
type PolicyCorruptError struct {
	Tenant string
	Key    string
	Err    error
}

func (e *PolicyCorruptError) Error() string {
	return fmt.Sprintf("service: corrupt abac policy record %q in tenant %q: %v", e.Key, e.Tenant, e.Err)
}

func (e *PolicyCorruptError) Unwrap() error { return e.Err }

// PolicyStoreUnsupportedError reports a tenant store that cannot perform the
// guarded writes the policy service needs (kvstore.AtomicKVStore). Without
// them a create could overwrite and an update could lose a concurrent write,
// so the service refuses instead.
type PolicyStoreUnsupportedError struct {
	Tenant string
}

func (e *PolicyStoreUnsupportedError) Error() string {
	return fmt.Sprintf("service: the store of tenant %q does not support guarded writes for abac policies", e.Tenant)
}

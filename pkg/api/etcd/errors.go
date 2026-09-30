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

package etcd

// StartError is returned when the etcd gRPC server fails to start.
type StartError struct {
	Addr string
	Err  error
}

// Error returns a human-readable description.
func (e *StartError) Error() string {
	return "etcd: failed to start server on " + e.Addr + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *StartError) Unwrap() error { return e.Err }

// InvalidRequestError is returned when an etcd request is malformed.
type InvalidRequestError struct {
	Detail string
}

// Error returns a human-readable description.
func (e *InvalidRequestError) Error() string {
	return "etcd: invalid request: " + e.Detail
}

// AuthRequiredError is returned when an etcd listener is asked to start
// without the node's request authentication: TLS to present client
// certificates over and an authorizer to check them. The etcd Auth service is
// not implemented, so such a listener would serve its database to every
// client that can reach the port.
type AuthRequiredError struct {
	Database string
}

// Error returns a human-readable description.
func (e *AuthRequiredError) Error() string {
	return "etcd: refusing to serve database " + e.Database +
		" without authentication: configure TLS and the node's authentication"
}

// ScopeUnavailableError is returned when an etcd listener cannot be confined
// to its database's keyspace -- the node has no database catalog or no
// per-(tenant, database) service registry to resolve it through -- and is
// refused rather than served over the raw, unscoped store.
type ScopeUnavailableError struct {
	Database string
	Reason   string
}

// Error returns a human-readable description.
func (e *ScopeUnavailableError) Error() string {
	return "etcd: cannot confine the listener to database " + e.Database + ": " + e.Reason
}

// MethodNotAllowedError is returned for an etcd RPC the listener does not
// serve (see MethodPermission).
type MethodNotAllowedError struct {
	Method string
}

// Error returns a human-readable description.
func (e *MethodNotAllowedError) Error() string {
	return "etcd: method not served: " + e.Method
}

// TenantMismatchError records an authenticated caller from one tenant
// addressing an etcd listener serving another tenant's database. It names the
// owning tenant and the database, so it is for the server's log only; the
// caller is answered with PermissionDeniedError.
type TenantMismatchError struct {
	CallerTenant string
	OwnerTenant  string
	Database     string
}

// Error returns a human-readable description.
func (e *TenantMismatchError) Error() string {
	return "etcd: tenant " + e.CallerTenant + " may not access database " + e.Database +
		" of tenant " + e.OwnerTenant
}

// PermissionDeniedError is the refusal an etcd caller receives when it may
// not use the listener. It deliberately names neither the database nor its
// owning tenant, so a refusal does not reveal what the listener serves.
type PermissionDeniedError struct{}

// Error returns a human-readable description.
func (e *PermissionDeniedError) Error() string {
	return "etcd: permission denied"
}

// AuthNotEnabledError is returned for all Auth service RPCs.
type AuthNotEnabledError struct{}

// Error returns a human-readable description.
func (e *AuthNotEnabledError) Error() string {
	return "etcd: authentication is not enabled"
}

// RevisionHistoryRequiredError refuses to serve the etcd API over a store
// that keeps no MVCC revision history: every etcd answer carries a revision,
// and such a store has none to give.
type RevisionHistoryRequiredError struct{}

// Error returns a human-readable description.
func (e *RevisionHistoryRequiredError) Error() string {
	return "etcd: the etcd API requires a state machine with MVCC revision history (raft.sm_type: mvcc)"
}

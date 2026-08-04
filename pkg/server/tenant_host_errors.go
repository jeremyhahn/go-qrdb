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

package server

import "errors"

// ErrTenantHostUnavailable is returned when a per-tenant volume Host is
// requested but the registry was not configured with a barrier registry and
// tenant metadata source.
var ErrTenantHostUnavailable = errors.New("server: tenant host registry not configured")

// ErrTenantDataRootUnset is returned when an IsolationVolume tenant has no
// DataRoot recorded in its metadata (it was never allocated from the pool).
var ErrTenantDataRootUnset = errors.New("server: tenant data root is unset")

// ErrTenantMigrationInProgress is returned by Services while a tenant's barrier
// is mid encryption-migration recycle. It is RETRYABLE: once the migration
// commits and replaces the barrier, the next call builds the Host under the
// keyed barrier. Building during the window would wrap the host with the stale
// source barrier and fail against the already-keyed at-rest stores.
var ErrTenantMigrationInProgress = errors.New("server: tenant encryption migration in progress")

// ErrTenantShardNoLeader is returned when a per-tenant Host's local shard fails
// to elect a leader within the readiness timeout.
var ErrTenantShardNoLeader = errors.New("server: tenant shard did not elect a leader")

// TenantHostError wraps a failure building, resolving, or closing a per-tenant
// volume Raft Host. It identifies the tenant and the operation that failed.
type TenantHostError struct {
	TenantID string
	Op       string
	Err      error
}

// Error implements the error interface.
func (e *TenantHostError) Error() string {
	return "server: tenant host " + e.Op + " for tenant " + e.TenantID + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for errors.Is/As.
func (e *TenantHostError) Unwrap() error { return e.Err }

// VolumeReattachError is returned when an IsolationVolume tenant cannot be
// re-bound to its on-disk state during boot (barrier re-registration, unseal,
// or per-tenant Host construction failed).
type VolumeReattachError struct {
	TenantID string
	Err      error
}

// Error implements the error interface.
func (e *VolumeReattachError) Error() string {
	return "server: re-attach volume tenant " + e.TenantID + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for errors.Is/As.
func (e *VolumeReattachError) Unwrap() error { return e.Err }

// RaftReattachError is returned when an IsolationRaft tenant cannot be re-bound
// to its on-disk state during boot (barrier re-registration, unseal, or a
// dedicated database shard re-start failed).
type RaftReattachError struct {
	TenantID string
	Database string
	Err      error
}

// Error implements the error interface.
func (e *RaftReattachError) Error() string {
	if e.Database != "" {
		return "server: re-attach raft tenant " + e.TenantID + "/" + e.Database + ": " + e.Err.Error()
	}
	return "server: re-attach raft tenant " + e.TenantID + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for errors.Is/As.
func (e *RaftReattachError) Unwrap() error { return e.Err }

// TenantInfraConfigError is returned by BuildTenantInfra when the caller
// supplies an incompatible combination of TenantInfraConfig fields. It is a
// programmer / configuration error — the node must not proceed.
//
// The canonical case is Host != nil && Manager == nil: IsolationRaft requires
// a database.Manager to track per-tenant shard metadata and to walk existing
// shards during reattach. Without one the factory would panic inside
// NewRaftBackendFactoryFromManager or reattachRaftTenants.
type TenantInfraConfigError struct {
	// Field is the name of the misconfigured TenantInfraConfig field.
	Field string
	// Message describes the constraint that was violated.
	Message string
}

// Error implements the error interface.
func (e *TenantInfraConfigError) Error() string {
	return "server: tenant infra config: " + e.Field + ": " + e.Message
}

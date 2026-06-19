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

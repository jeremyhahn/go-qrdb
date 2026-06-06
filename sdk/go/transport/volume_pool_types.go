// Copyright 2026 Jeremy Hahn
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

package transport

import "context"

// ---------------------------------------------------------------------------
// VolumePoolClient types
// ---------------------------------------------------------------------------
//
// The volume pool primitive lives in go-objstore (Layer 0). qrdb wires
// its configured Manager into the seal.BarrierRegistry so per-tenant
// barrier-mode tenants land on independent Pebble instances at
// pool-assigned local paths. Phase 3 exposes the read surface (List /
// Pick) plus an admin RegisterPool gate so SDK consumers can drive the
// pool without depending on the objstore package directly.

// VolumePool is the SDK projection of an objstore pool.Pool.
type VolumePool struct {
	// Name is the pool's unique identifier.
	Name string `json:"name"`

	// Strategy is the canonical strategy name (e.g. "round_robin",
	// "least_utilization").
	Strategy string `json:"strategy"`

	// BackendCount is the number of registered candidates in the pool.
	BackendCount int `json:"backendCount"`

	// LocalPaths is the set of LocalPath() values returned by the
	// pool's candidates that satisfy the PathAccessor interface.
	// Cloud-only pools return an empty slice. Useful for operator
	// diagnostics — answering "where will the next tenant land?".
	LocalPaths []string `json:"localPaths,omitempty"`
}

// VolumePoolHint mirrors objstore pool.Hint. Every field is optional;
// strategies ignore fields they do not consume.
type VolumePoolHint struct {
	// TenantID is a sticky-placement signal a future strategy could use
	// to keep the same tenant on the same backend across restarts.
	TenantID string `json:"tenantID,omitempty"`

	// SizeHint is the expected payload size in bytes.
	SizeHint int64 `json:"sizeHint,omitempty"`

	// Region is a preferred region tag (e.g. "us-east-1").
	Region string `json:"region,omitempty"`
}

// VolumePoolBackend is the SDK projection of an objstore Storage that
// was returned from Pick. Only the local path is exposed today — pool
// consumers that need a richer view should compose with a deeper
// objstore SDK call.
type VolumePoolBackend struct {
	// PoolName is the pool that produced this backend.
	PoolName string `json:"poolName"`

	// LocalPath is the absolute filesystem path backing the picked
	// storage. Empty for cloud-only candidates.
	LocalPath string `json:"localPath,omitempty"`
}

// RegisterVolumePoolRequest is the input to VolumePoolClient.RegisterPool.
// It is admin-gated by the cross-scope volumepool:create permission and
// is therefore only callable by identities in the system tenant.
type RegisterVolumePoolRequest struct {
	// Name is the pool's unique identifier.
	Name string `json:"name"`

	// Paths is the set of local-filesystem paths that back the pool.
	// Each path is wrapped in an objstore local-filesystem Storage and
	// added as a candidate.
	Paths []string `json:"paths"`

	// Strategy is the canonical strategy name. An empty value defaults
	// to "round_robin" — the only strategy with a concrete implementation
	// today.
	Strategy string `json:"strategy,omitempty"`
}

// ---------------------------------------------------------------------------
// VolumePoolClient interface
// ---------------------------------------------------------------------------

// VolumePoolClient exposes the objstore volume-pool primitive used by
// qrdb to place per-tenant barrier-mode Pebble instances.
type VolumePoolClient interface {
	// List returns every registered pool sorted by name.
	List(ctx context.Context) ([]VolumePool, error)

	// Pick selects one backend from the named pool using its
	// configured strategy. Returns PoolNotConfiguredError when the
	// pool name is unknown and PoolEmptyError when the pool has no
	// candidates.
	Pick(ctx context.Context, poolName string, hint VolumePoolHint) (*VolumePoolBackend, error)

	// RegisterPool adds a new pool. Admin-only — the underlying
	// implementation may gate the call behind volumepool:create which
	// is a cross-scope permission reserved for the system tenant.
	RegisterPool(ctx context.Context, req RegisterVolumePoolRequest) error
}

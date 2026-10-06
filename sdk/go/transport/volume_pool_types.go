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

package transport

import (
	"context"
	"time"
)

// VolumePoolNodeCounts tallies a pool's per-node health: how many of the
// nodes that have reported it are ready, locked, unavailable, or (always 0
// in this release) unmapped.
type VolumePoolNodeCounts struct {
	Ready       int `json:"ready"`
	Locked      int `json:"locked"`
	Unavailable int `json:"unavailable"`
	Unmapped    int `json:"unmapped"`
}

// VolumePool is one pool's catalog record plus the aggregate view every
// list/get response reports: measured usage, assigned-tenant count, and a
// per-node health tally (docs/volume-pools/api.md).
type VolumePool struct {
	Name          string               `json:"name"`
	Provider      string               `json:"provider"`
	Description   string               `json:"description,omitempty"`
	CapacityBytes int64                `json:"capacityBytes"`
	ReservedBytes int64                `json:"reservedBytes"`
	UsedBytes     int64                `json:"usedBytes"`
	TenantCount   int                  `json:"tenantCount"`
	Nodes         VolumePoolNodeCounts `json:"nodes"`
	Overcommit    float64              `json:"overcommit"`
	CreatedAt     time.Time            `json:"createdAt,omitempty"`
	UpdatedAt     time.Time            `json:"updatedAt,omitempty"`
}

// VolumePoolNodeReport is one node's report, scoped to a single pool.
// State is "ready", "locked", "unavailable", "unmapped" (the node's last
// report said it does not map the pool) or "no_report" (the report is
// older than the catalog's lease TTL; SizeBytes, FreeBytes and Detail are
// zeroed rather than shown stale).
type VolumePoolNodeReport struct {
	NodeID     string    `json:"nodeId"`
	State      string    `json:"state"`
	SizeBytes  int64     `json:"sizeBytes,omitempty"`
	FreeBytes  int64     `json:"freeBytes,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	ReportedAt time.Time `json:"reportedAt,omitempty"`
}

// VolumePoolDetail is VolumePoolGet's response: a VolumePool plus one row
// per node that has reported it.
type VolumePoolDetail struct {
	VolumePool
	NodeReports []VolumePoolNodeReport `json:"nodeReports"`
}

// CreateVolumePoolRequest adds a pool to the catalog.
type CreateVolumePoolRequest struct {
	Name string `json:"name"`
	// Provider defaults to "dir" when empty.
	Provider    string `json:"provider,omitempty"`
	Description string `json:"description,omitempty"`
	// CapacityBytes of 0 means derived from health reports.
	CapacityBytes int64 `json:"capacityBytes,omitempty"`
	// Overcommit defaults to 1.0 when 0; must be >= 1.0 otherwise.
	Overcommit float64 `json:"overcommit,omitempty"`
}

// UpdateVolumePoolRequest changes a pool's description, capacity or
// overcommit. The provider is immutable. A nil field leaves the
// corresponding property unchanged; CapacityBytes of a non-nil, zero
// value returns the pool to the derived capacity.
type UpdateVolumePoolRequest struct {
	Name          string   `json:"name"`
	Description   *string  `json:"description,omitempty"`
	CapacityBytes *int64   `json:"capacityBytes,omitempty"`
	Overcommit    *float64 `json:"overcommit,omitempty"`
}

// VolumePoolTenant is one row of ListVolumePoolTenantsResponse: an
// assigned tenant's reservation and measured use on the pool.
type VolumePoolTenant struct {
	TenantID      string `json:"tenantId"`
	ReservedBytes int64  `json:"reservedBytes"`
}

// ListVolumePoolTenantsRequest pages through the tenants assigned to a
// pool. PageSize 0 means the server default; PageToken is the previous
// response's NextPageToken.
type ListVolumePoolTenantsRequest struct {
	Pool      string `json:"pool"`
	PageToken string `json:"page_token,omitempty"`
	PageSize  int    `json:"page_size,omitempty"`
}

// ListVolumePoolTenantsResponse is one page of a pool's assigned tenants.
type ListVolumePoolTenantsResponse struct {
	Tenants       []VolumePoolTenant `json:"tenants"`
	NextPageToken string             `json:"next_page_token,omitempty"`
}

// VolumePoolClient is the volume pool management surface
// (docs/volume-pools/api.md): list, inspect, create, update, delete a
// pool, and list a pool's assigned tenants. Tier 1: every transport
// implements it. Every capability is ScopeCross (system tenant only).
type VolumePoolClient interface {
	// ListPools lists every pool in the catalog. Not paginated: a cluster
	// has a handful of pools.
	ListPools(ctx context.Context) ([]VolumePool, error)

	// GetPool inspects one pool, with one row per node that has reported
	// it.
	GetPool(ctx context.Context, name string) (*VolumePoolDetail, error)

	// CreatePool adds a pool to the catalog. The pool is not served until
	// each node maps it in its own configuration and restarts or reloads.
	CreatePool(ctx context.Context, req *CreateVolumePoolRequest) (*VolumePool, error)

	// UpdatePool changes a pool's description, capacity or overcommit.
	UpdatePool(ctx context.Context, req *UpdateVolumePoolRequest) (*VolumePool, error)

	// DeletePool removes a pool from the catalog. Refused while any
	// tenant is assigned to it.
	DeletePool(ctx context.Context, name string) error

	// ListPoolTenants lists the tenants assigned to a pool, with each
	// one's reservation and measured use.
	ListPoolTenants(ctx context.Context, req *ListVolumePoolTenantsRequest) (*ListVolumePoolTenantsResponse, error)
}

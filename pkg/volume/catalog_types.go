// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"context"
	"time"
)

// Pool is the catalog record for one named volume pool, persisted at
// PoolKey(Name) ("__sys:volumepool/pools/{name}").
type Pool struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	// Description is free operator text.
	Description string `json:"description,omitempty"`
	// CapacityBytes is the pool's allocatable capacity. Zero means it is
	// derived from health reports (decision D4) rather than set by the
	// operator.
	CapacityBytes int64 `json:"capacityBytes"`
	// Overcommit is the factor reservations may reach: capacity *
	// overcommit. Must be >= 1.0; defaults to 1.0.
	Overcommit float64 `json:"overcommit"`
	// ReservedBytes is the sum of every assignment's reservation on this
	// pool. Maintained only by Catalog.Reserve / Catalog.Release, never
	// written directly by CreatePool or UpdatePool.
	ReservedBytes int64     `json:"reservedBytes"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Assignment is a tenant's location: persisted at AssignmentKey(TenantID)
// ("__sys:volumepool/assign/{tenant}"). A tenant has at most one
// Assignment (invariant 3); its absence means the tenant is not placed on
// any pool.
type Assignment struct {
	TenantID string `json:"tenantId"`
	Pool     string `json:"pool"`
	// Path is tenant-relative: one clean path component (invariant 2),
	// joined onto the pool's opened Volume.Root() by the resolving layer.
	Path          string `json:"path"`
	ReservedBytes int64  `json:"reservedBytes"`
}

// NodeReport is one node's health report: persisted at
// NodeReportKey(NodeID) ("__sys:volumepool/nodes/{node}"), owned
// exclusively by that node (no other writer touches it).
type NodeReport struct {
	NodeID string
	// Pools holds one entry per pool THIS node's configuration maps --
	// never every pool in the catalog. A pool absent from Pools is a pool
	// this node does not map at all, which reads the same as the node
	// never having reported it.
	Pools      map[string]PoolOnNode
	ReportedAt time.Time
}

// PoolOnNode is one node's view of one pool it maps. Internal: never
// returned by volume.Service directly (NodeReportRow, built from it, is
// the wire-facing per-node row -- see GetPool/PoolDetail).
type PoolOnNode struct {
	// Mapped is true when this node's configuration maps the pool.
	// CreatePool/UpdatePool never set this; only the reporting node does.
	Mapped bool
	Usage  Usage
	Health Health
}

// NodeHealthCounts tallies PoolView.NodeCounts, matching the
// ready/locked/unavailable/unmapped breakdown `qrdb volume-pool list` and
// `get` print.
//
// Unmapped is always 0 in this implementation: computing it needs the
// cluster's full node membership (to count a node that has never reported
// this pool at all, not merely a node whose report is stale), which this
// package does not depend on. It is populated once the health-reporting
// daemon's membership cross-reference lands.
type NodeHealthCounts struct {
	Ready       int `json:"ready"`
	Locked      int `json:"locked"`
	Unavailable int `json:"unavailable"`
	Unmapped    int `json:"unmapped"`
}

// PoolView is a Pool plus the aggregate view ListPools and GetPool report:
// measured usage, how many tenants are assigned, and a per-node health
// tally.
type PoolView struct {
	Pool
	// UsedBytes approximates physical usage from node health reports:
	// the busiest StateReady node's measured usage for this pool (see
	// usedBytesAcrossNodes), not a cluster-wide sum -- decision D1's one-
	// volume-per-pool-per-node model means there is no shared filesystem
	// to sum across. It is only as fresh as the last health report, and
	// is NOT the storage-quota reconciler's own directory walk
	// (quota.NewReconciler), which measures a specific tenant's usage,
	// not a pool's.
	UsedBytes   int64            `json:"usedBytes"`
	TenantCount int              `json:"tenantCount"`
	NodeCounts  NodeHealthCounts `json:"nodes"`
}

// PoolDetail is a PoolView plus one row per node that has reported the
// pool, for `qrdb volume-pool get`.
type PoolDetail struct {
	PoolView
	NodeReports []NodeReportRow `json:"nodeReports"`
}

// NodeReportRow is one node's report, scoped to a single pool: PoolDetail's
// per-node row.
type NodeReportRow struct {
	NodeID string `json:"nodeId"`
	// State is "ready", "locked", "unavailable", "unmapped" (the node's
	// last report said it does not map the pool) or "no_report" (the
	// report is older than the catalog's lease TTL; Detail, SizeBytes and
	// FreeBytes are zeroed rather than shown stale, per user.md).
	State      string    `json:"state"`
	SizeBytes  int64     `json:"sizeBytes,omitempty"`
	FreeBytes  int64     `json:"freeBytes,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	ReportedAt time.Time `json:"reportedAt,omitempty"`
}

// Node report row states. See NodeReportRow.State.
const (
	NodeStateReady       = "ready"
	NodeStateLocked      = "locked"
	NodeStateUnavailable = "unavailable"
	NodeStateUnmapped    = "unmapped"
	NodeStateNoReport    = "no_report"
)

// CreatePoolRequest is CreatePool's input.
type CreatePoolRequest struct {
	Name string `json:"name"`
	// Provider defaults to "dir" when empty.
	Provider    string `json:"provider,omitempty"`
	Description string `json:"description,omitempty"`
	// CapacityBytes of 0 means derived from health reports (decision D4).
	CapacityBytes int64 `json:"capacityBytes,omitempty"`
	// Overcommit defaults to 1.0 when <= 0. Must be >= 1.0 otherwise.
	Overcommit float64 `json:"overcommit,omitempty"`
}

// UpdatePoolRequest is UpdatePool's input. A nil field leaves that
// property unchanged; Provider is immutable and has no field here.
type UpdatePoolRequest struct {
	Description *string `json:"description,omitempty"`
	// CapacityBytes of a non-nil, zero value returns the pool to the
	// derived capacity (decision D4).
	CapacityBytes *int64   `json:"capacityBytes,omitempty"`
	Overcommit    *float64 `json:"overcommit,omitempty"`
}

// AssignmentPage is one page of ListAssignments, in ascending tenant-id
// order, following the same paging contract as tenant.DeletedTenantsPage:
// NextPageToken is opaque, empty on the last page, and resumes strictly
// after the tenant id it names.
type AssignmentPage struct {
	Assignments   []Assignment `json:"assignments"`
	NextPageToken string       `json:"next_page_token,omitempty"`
}

// Move is the Phase 2 move-journal record (decision D5). Nothing in this
// build populates it: MoveTenant and GetMove return
// *MoveNotImplementedError until the Phase 2 journal lands. The type
// exists now only so volume.Service's signature compiles against it.
type Move struct {
	TenantID     string   `json:"tenantId"`
	FromPool     string   `json:"fromPool"`
	ToPool       string   `json:"toPool"`
	State        string   `json:"state"`
	NodesCopied  []string `json:"nodesCopied,omitempty"`
	NodesPending []string `json:"nodesPending,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// Service is the management surface every transport delegates to (REST,
// gRPC, QUIC, Unix, MCP, embedded -- see docs/volume-pools/api.md).
//
// MoveTenant and GetMove are Phase 2 (decision D5); their journal and wire
// surface are not part of this build, and NewCatalog's implementation of
// them returns *MoveNotImplementedError.
type Service interface {
	ListPools(ctx context.Context) ([]PoolView, error)
	GetPool(ctx context.Context, name string) (*PoolDetail, error)
	CreatePool(ctx context.Context, req CreatePoolRequest) (*Pool, error)
	UpdatePool(ctx context.Context, name string, req UpdatePoolRequest) (*Pool, error)
	DeletePool(ctx context.Context, name string) error
	ListAssignments(ctx context.Context, pool, pageToken string, pageSize int) (*AssignmentPage, error)
	MoveTenant(ctx context.Context, tenantID, toPool string) (*Move, error)
	GetMove(ctx context.Context, tenantID string) (*Move, error)
}

// Allocator is the narrow seam pkg/tenant (placing a tenant at create time)
// and pkg/seal's BarrierRegistry (resolving an already-placed tenant's
// location) depend on, instead of the full management Service -- neither
// package needs to list pools or run move/catalog administration, and
// BarrierRegistry in particular must not import this package's
// Service-level composition. *Catalog satisfies both Service and
// Allocator.
type Allocator interface {
	// Reserve places tenantID on pool, reserving reserveBytes of its
	// capacity, and returns the resulting Assignment. It is idempotent
	// per invariant 3: a tenantID that already has an Assignment for
	// pool returns that Assignment unchanged rather than reserving again;
	// one that already has an Assignment for a DIFFERENT pool is refused
	// with *TenantAlreadyAssignedError.
	Reserve(ctx context.Context, tenantID, pool, path string, reserveBytes int64) (*Assignment, error)

	// Resolve returns tenantID's current Assignment. Returns
	// *AssignmentNotFoundError when the tenant has none.
	Resolve(ctx context.Context, tenantID string) (*Assignment, error)

	// Release releases tenantID's reservation and removes its Assignment,
	// atomically with each other (invariant 4). A tenant with no
	// Assignment is left exactly as is: nil error, nothing changes.
	Release(ctx context.Context, tenantID string) error

	// ListAll returns every tenant's Assignment, for the boot-time
	// restore walk (seal.BarrierRegistry.RestoreVolumeTenants). Unlike
	// Service.ListAssignments it is unpaginated: an internal caller that
	// must see everything to decide what to re-register has no use for
	// a page token.
	ListAll(ctx context.Context) ([]Assignment, error)

	// AdjustReservation changes tenantID's ReservedBytes to newBytes,
	// crediting or debiting the DELTA against its pool's own
	// ReservedBytes atomically with the Assignment update (invariant 4,
	// the same atomicity Release's reservation/Assignment pair keeps).
	// An increase is checked against the pool's capacity exactly as
	// Reserve checks a new reservation, refusing with
	// *PoolCapacityExceededError if it would not fit; a decrease is
	// never refused. Returns *AssignmentNotFoundError when tenantID has
	// no Assignment -- this adjusts an EXISTING reservation, it does not
	// create one.
	AdjustReservation(ctx context.Context, tenantID string, newBytes int64) (*Assignment, error)
}

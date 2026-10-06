// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import "strconv"

// PoolNotFoundError is returned when a request names a pool the catalog
// does not have.
type PoolNotFoundError struct {
	Pool string
}

// Error implements the error interface.
func (e *PoolNotFoundError) Error() string {
	return "volume: pool not found: " + strconv.Quote(e.Pool)
}

// PoolAlreadyExistsError is returned by CreatePool when the name is
// already in the catalog.
type PoolAlreadyExistsError struct {
	Pool string
}

// Error implements the error interface.
func (e *PoolAlreadyExistsError) Error() string {
	return "volume: pool already exists: " + strconv.Quote(e.Pool)
}

// PoolInUseError is returned by DeletePool while tenants are assigned to
// the pool. Tenants carries the first page of their ids.
type PoolInUseError struct {
	Pool    string
	Tenants []string
}

// Error implements the error interface.
func (e *PoolInUseError) Error() string {
	msg := "volume: pool " + strconv.Quote(e.Pool) + " is in use by: "
	for i, t := range e.Tenants {
		if i > 0 {
			msg += ", "
		}
		msg += t
	}
	return msg
}

// ProviderMismatchError is returned when a node's own configuration opens
// a named pool with a different provider than the catalog recorded for
// it at CreatePool time. The catalog's record is the cluster-wide source
// of truth for what a pool's data is encoded as (e.g. "luks2" vs plain
// "dir"); a node that locally opened it as something else would silently
// serve the pool's tenants under the wrong storage semantics -- most
// seriously, an operator expecting hardware encryption getting plaintext
// "dir" instead on a misconfigured node. The node refuses to serve that
// pool at all (invariant 6: only that pool's tenants on this node, never
// the whole node's boot) rather than opening it anyway.
type ProviderMismatchError struct {
	Pool               string
	CatalogProvider    string
	ConfiguredProvider string
}

// Error implements the error interface.
func (e *ProviderMismatchError) Error() string {
	return "volume: pool " + strconv.Quote(e.Pool) + " provider mismatch: catalog says " +
		strconv.Quote(e.CatalogProvider) + ", this node's configuration says " + strconv.Quote(e.ConfiguredProvider)
}

// PoolUnparseableAssignmentError is returned by DeletePool when an
// assignment record exists that could not be parsed well enough to prove
// it does NOT name the pool being deleted (invariant 9). Unlike
// PoolInUseError, this is DeletePool failing closed on a record it cannot
// read at all -- it genuinely does not know whether the record names this
// pool or some other one -- rather than confirming a specific tenant is
// assigned to it.
type PoolUnparseableAssignmentError struct {
	Pool string
}

// Error implements the error interface.
func (e *PoolUnparseableAssignmentError) Error() string {
	return "volume: pool " + strconv.Quote(e.Pool) +
		" cannot be deleted: an assignment record exists that could not be read well enough to prove it does not name this pool"
}

// PoolCapacityExceededError is returned by Reserve when reserveBytes would
// push a pool's ReservedBytes past CapacityBytes*Overcommit.
type PoolCapacityExceededError struct {
	Pool      string
	Requested int64
	Available int64
}

// Error implements the error interface.
func (e *PoolCapacityExceededError) Error() string {
	return "volume: pool " + strconv.Quote(e.Pool) + " capacity exceeded: requested " +
		strconv.FormatInt(e.Requested, 10) + " bytes, " + strconv.FormatInt(e.Available, 10) + " available"
}

// InvalidPoolRequestError is returned by CreatePool/UpdatePool when the
// request itself is malformed: an empty name, an overcommit below 1.0, or
// (CreatePool only) a negative capacity.
type InvalidPoolRequestError struct {
	Reason string
}

// Error implements the error interface.
func (e *InvalidPoolRequestError) Error() string {
	return "volume: invalid pool request: " + e.Reason
}

// InvalidAssignmentPathError is returned when an assignment's Path is not
// one clean path component (invariant 2): empty, containing a path
// separator, or "." / "..".
type InvalidAssignmentPathError struct {
	TenantID string
	Path     string
}

// Error implements the error interface.
func (e *InvalidAssignmentPathError) Error() string {
	return "volume: invalid assignment path for tenant " + strconv.Quote(e.TenantID) + ": " + strconv.Quote(e.Path)
}

// AssignmentNotFoundError is returned by Resolve when tenantID has no
// Assignment.
type AssignmentNotFoundError struct {
	TenantID string
}

// Error implements the error interface.
func (e *AssignmentNotFoundError) Error() string {
	return "volume: tenant " + strconv.Quote(e.TenantID) + " has no volume pool assignment"
}

// TenantAlreadyAssignedError is returned by Reserve when tenantID already
// has an Assignment naming a DIFFERENT pool than the one requested.
type TenantAlreadyAssignedError struct {
	TenantID      string
	CurrentPool   string
	RequestedPool string
}

// Error implements the error interface.
func (e *TenantAlreadyAssignedError) Error() string {
	return "volume: tenant " + strconv.Quote(e.TenantID) + " is already assigned to pool " +
		strconv.Quote(e.CurrentPool) + ", not " + strconv.Quote(e.RequestedPool)
}

// ReservationConflictError is returned by Reserve or Release when the
// pool's reservation CAS lost a bounded number of consecutive races.
// Retrying is always safe: no partial state is left behind.
type ReservationConflictError struct {
	Pool string
}

// Error implements the error interface.
func (e *ReservationConflictError) Error() string {
	return "volume: reservation conflict on pool " + strconv.Quote(e.Pool) + ": too many concurrent racers, retry"
}

// MoveNotImplementedError is returned by MoveTenant and GetMove: tenant
// moves are Phase 2 (decision D5) and are not implemented by this build.
type MoveNotImplementedError struct{}

// Error implements the error interface.
func (e *MoveNotImplementedError) Error() string {
	return "volume: tenant moves are not implemented in this release (decision D5, Phase 2)"
}

// PoolUnavailableError is returned when this node cannot open or resolve
// the named pool: a missing mount, an absent device, an unregistered
// provider, or a Lockable volume that failed to unlock. It is never
// returned by Catalog itself (which has no node-local concept of "this
// node"); OpenVolumes and the composition layer that opens volumes at
// boot return it.
type PoolUnavailableError struct {
	Pool   string
	NodeID string
	Err    error
}

// Error implements the error interface.
func (e *PoolUnavailableError) Error() string {
	msg := "volume: pool " + strconv.Quote(e.Pool) + " is unavailable on node " + strconv.Quote(e.NodeID)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap supports errors.Is / errors.As against the wrapped cause.
func (e *PoolUnavailableError) Unwrap() error { return e.Err }

// VolumeAlreadyOpenError is returned by OpenVolumes.Add when the pool is
// already tracked. The caller must Remove it first to replace it.
type VolumeAlreadyOpenError struct {
	Pool string
}

// Error implements the error interface.
func (e *VolumeAlreadyOpenError) Error() string {
	return "volume: pool " + strconv.Quote(e.Pool) + " is already open"
}

// InvalidCatalogDependencyError is returned by NewCatalog when a required
// dependency is nil.
type InvalidCatalogDependencyError struct {
	Reason string
}

// Error implements the error interface.
func (e *InvalidCatalogDependencyError) Error() string {
	return "volume: invalid catalog dependency: " + e.Reason
}

// EncodeError is returned when a record cannot be marshaled to JSON.
type EncodeError struct {
	Err error
}

// Error implements the error interface.
func (e *EncodeError) Error() string { return "volume: encode failed: " + e.Err.Error() }

// Unwrap supports errors.Is / errors.As against the wrapped cause.
func (e *EncodeError) Unwrap() error { return e.Err }

// CorruptRecordError is returned when a stored record cannot be decoded.
// It never includes the raw bytes: a corrupt record may carry anything.
type CorruptRecordError struct {
	Key string
	Err error
}

// Error implements the error interface.
func (e *CorruptRecordError) Error() string {
	return "volume: corrupt record at " + strconv.Quote(e.Key) + ": " + e.Err.Error()
}

// Unwrap supports errors.Is / errors.As against the wrapped cause.
func (e *CorruptRecordError) Unwrap() error { return e.Err }

// InvalidPageRequestError is returned by ListAssignments for a malformed
// pageToken or a negative pageSize.
type InvalidPageRequestError struct {
	Field  string
	Reason string
}

// Error implements the error interface.
func (e *InvalidPageRequestError) Error() string {
	return "volume: invalid " + e.Field + ": " + e.Reason
}

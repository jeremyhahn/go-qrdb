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

import "strconv"

// Every error here fails the whole drop rather than being logged and skipped,
// the way CollectionMetadataError{Op: "delete-sequence"} fails a collection
// drop (go-qrdb#411). Reporting a successful DROP DATABASE over data that is
// still in the store is the defect go-qrdb#412 reports; a failure that leaves
// the database, its catalog record and its data exactly as they were is a
// retry.

// ReclaimScopeError is returned when a reclamation was asked for without a
// scope to confine it to -- an empty database name, or an empty key prefix.
// It is refused rather than widened: the unscoped form of "delete everything
// this database owns" is "delete everything".
type ReclaimScopeError struct {
	TenantID string
	Database string
}

// Error returns a human-readable description of the missing scope.
func (e *ReclaimScopeError) Error() string {
	return "service: refusing to reclaim an unscoped keyspace (tenant: " +
		strconv.Quote(e.TenantID) + ", database: " + strconv.Quote(e.Database) + ")"
}

// ReclaimStoreUnavailableError is returned when the store holding a
// database's keys could not be resolved, so the drop cannot know whether it
// reclaimed anything.
type ReclaimStoreUnavailableError struct {
	TenantID string
	Err      error
}

// Error returns a human-readable description of the unresolvable store.
func (e *ReclaimStoreUnavailableError) Error() string {
	msg := "service: cannot resolve the store holding the keyspace of tenant " +
		strconv.Quote(e.TenantID)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ReclaimStoreUnavailableError) Unwrap() error { return e.Err }

// ReclaimPrefixError is returned when a database's keys under one prefix
// could not be enumerated or deleted.
type ReclaimPrefixError struct {
	Prefix string
	Op     string
	Err    error
}

// Error returns a human-readable description of the prefix failure.
func (e *ReclaimPrefixError) Error() string {
	return "service: failed to " + e.Op + " keys under prefix " +
		strconv.Quote(e.Prefix) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ReclaimPrefixError) Unwrap() error { return e.Err }

// ReclaimIndexError is returned when a database's secondary indexes could not
// be enumerated or dropped.
type ReclaimIndexError struct {
	Name string
	Op   string
	Err  error
}

// Error returns a human-readable description of the index failure.
func (e *ReclaimIndexError) Error() string {
	return "service: failed to " + e.Op + " secondary index " +
		strconv.Quote(e.Name) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ReclaimIndexError) Unwrap() error { return e.Err }

// ReclaimVectorError is returned when a database's vector namespaces could
// not be enumerated or deleted.
type ReclaimVectorError struct {
	Namespace string
	Op        string
	Err       error
}

// Error returns a human-readable description of the vector failure.
func (e *ReclaimVectorError) Error() string {
	return "service: failed to " + e.Op + " vector namespace " +
		strconv.Quote(e.Namespace) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ReclaimVectorError) Unwrap() error { return e.Err }

// ReclaimJobEncodeError is returned when a ReclaimJob record could not be
// marshaled for persistence (see ReclaimJobOp).
type ReclaimJobEncodeError struct {
	ID  string
	Err error
}

// Error returns a human-readable description of the encoding failure.
func (e *ReclaimJobEncodeError) Error() string {
	return "service: failed to encode reclaim job " + strconv.Quote(e.ID) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ReclaimJobEncodeError) Unwrap() error { return e.Err }

// ReclaimStoreGoneError is returned by a ReclaimStoreResolver when the
// (tenant, database) a pending reclaim job names definitively no longer
// exists. The reclamation that removed the database or tenant owns
// everything that was in its store -- the job's record and orphaned keys
// included -- so the sweeper treats the job as finished.
type ReclaimStoreGoneError struct {
	TenantID string
	Database string
}

// Error returns a human-readable description of the missing store.
func (e *ReclaimStoreGoneError) Error() string {
	return "service: the store of tenant " + strconv.Quote(e.TenantID) +
		", database " + strconv.Quote(e.Database) + " no longer exists"
}

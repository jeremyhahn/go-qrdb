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

// PrivilegeEmptyDatabaseError is returned when a privilege request names no
// database. The catalog is per-database by construction, so there is no
// database-less form of any of its operations.
type PrivilegeEmptyDatabaseError struct{}

// Error returns a human-readable description.
func (e *PrivilegeEmptyDatabaseError) Error() string {
	return "service: privilege request must name a database"
}

// PrivilegeInvalidGranteeError is returned when a grantee name cannot be
// used as a catalog key.
type PrivilegeInvalidGranteeError struct {
	Grantee string
	Reason  string
}

// Error returns a human-readable description.
func (e *PrivilegeInvalidGranteeError) Error() string {
	return "service: invalid privilege grantee " + strconv.Quote(e.Grantee) + ": " + e.Reason
}

// PrivilegeNoGrantsError is returned when a grant or revoke request carries
// no privilege assignments at all. An empty request is a caller bug, not a
// no-op: silently accepting it would arm the activation latch without
// granting anything.
type PrivilegeNoGrantsError struct{}

// Error returns a human-readable description.
func (e *PrivilegeNoGrantsError) Error() string {
	return "service: privilege request must carry at least one grant"
}

// PrivilegeColumnWithoutTableError is returned when a grant names a column
// but no table. There is no database-wide column tier.
type PrivilegeColumnWithoutTableError struct {
	Column string
}

// Error returns a human-readable description.
func (e *PrivilegeColumnWithoutTableError) Error() string {
	return "service: privilege grant names column " + strconv.Quote(e.Column) + " without a table"
}

// PrivilegeUnknownNameError is returned when a stored record names a
// privilege this build cannot represent. It is an error rather than a silent
// drop: a record that cannot be fully decoded must not be interpreted as a
// different set of privileges than it grants.
type PrivilegeUnknownNameError struct {
	Name string
}

// Error returns a human-readable description.
func (e *PrivilegeUnknownNameError) Error() string {
	return "service: unknown privilege name " + strconv.Quote(e.Name)
}

// PrivilegeMetadataError wraps a marshal, unmarshal, or enumeration failure
// against the privilege catalog.
type PrivilegeMetadataError struct {
	Op  string
	Err error
}

// Error returns a human-readable description.
func (e *PrivilegeMetadataError) Error() string {
	return "service: privilege catalog " + e.Op + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *PrivilegeMetadataError) Unwrap() error { return e.Err }

// PrivilegeConflictError is returned when a privilege compare-and-swap loses
// its race repeatedly enough to exhaust the retry budget. It carries the
// (database, grantee) pair whose record could not be committed.
type PrivilegeConflictError struct {
	Database string
	Grantee  string
	Err      error
}

// Error returns a human-readable description.
func (e *PrivilegeConflictError) Error() string {
	return "service: privilege catalog conflict for " + e.Database + "/" + e.Grantee
}

// Unwrap returns the underlying conflict error.
func (e *PrivilegeConflictError) Unwrap() error { return e.Err }

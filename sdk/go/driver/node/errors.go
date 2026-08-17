//go:build qrdb_sql

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

package node

import "strconv"

// StartError is returned when the in-process node cannot be started.
type StartError struct {
	// Database is the DSN's database name.
	Database string

	// Err is the underlying failure from sdk/go/node.
	Err error
}

// Error returns a human-readable description of the start failure.
func (e *StartError) Error() string {
	return "driver/node: cannot start in-process node for database " +
		strconv.Quote(e.Database) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *StartError) Unwrap() error { return e.Err }

// DataDirError is returned when the scratch directory an in-memory node
// still needs (for the vector store and the database catalog) cannot be
// created.
type DataDirError struct {
	// Err is the underlying filesystem failure.
	Err error
}

// Error returns a human-readable description of the data-directory failure.
func (e *DataDirError) Error() string {
	return "driver/node: cannot create node data directory: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *DataDirError) Unwrap() error { return e.Err }

// SealedError is returned when the node booted into the sealed state, so it
// has no data plane and no multi-protocol server to resolve services from.
// A node:// DSN carries no unseal credentials, so this is terminal for the
// connection rather than something the driver can wait out.
type SealedError struct {
	// Database is the DSN's database name.
	Database string
}

// Error returns a human-readable description of the sealed node.
func (e *SealedError) Error() string {
	return "driver/node: in-process node for database " + strconv.Quote(e.Database) +
		" booted sealed; a node:// DSN carries no unseal credentials"
}

// RegistryUnavailableError is returned when the started node's server has no
// service.Registry.
//
// It is reported rather than tolerated on purpose. The registry is the entire
// reason the node:// scheme exists: without it the connection would still
// open and still serve the KV/entity/collection tables, and would then fail
// every CREATE TABLE with a relational-catalog error far away from the actual
// cause. Degrading silently to KV-only is precisely the failure mode this
// scheme was added to eliminate.
type RegistryUnavailableError struct {
	// Database is the DSN's database name.
	Database string
}

// Error returns a human-readable description of the missing registry.
func (e *RegistryUnavailableError) Error() string {
	return "driver/node: the in-process node started for database " +
		strconv.Quote(e.Database) + " produced no service registry, so it has no " +
		"relational table catalog; refusing to serve a KV-only connection over a node:// DSN"
}

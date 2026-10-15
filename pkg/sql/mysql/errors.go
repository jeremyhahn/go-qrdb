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

//go:build qrdb_mysql

// Package mysql provides a MySQL wire protocol server for QRDB. It uses
// go-mysql-server's built-in MySQL protocol implementation to expose the
// QRDB key-value store through standard SQL queries over the MySQL wire
// protocol. mTLS authentication is integrated through the auth middleware
// chain when configured.
package mysql

import (
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

// ServerStartError is returned when the MySQL server fails to start.
type ServerStartError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ServerStartError) Error() string {
	if e.Err != nil {
		return "sql/mysql: server start failed: " + e.Err.Error()
	}
	return "sql/mysql: server start failed"
}

// Unwrap returns the underlying error.
func (e *ServerStartError) Unwrap() error {
	return e.Err
}

// AuthenticationError is returned when mTLS authentication fails for a
// MySQL connection.
type AuthenticationError struct {
	Err error
}

// Error returns a human-readable description.
func (e *AuthenticationError) Error() string {
	if e.Err != nil {
		return "sql/mysql: authentication failed: " + e.Err.Error()
	}
	return "sql/mysql: authentication failed"
}

// Unwrap returns the underlying error.
func (e *AuthenticationError) Unwrap() error {
	return e.Err
}

// BarrierSealedError is returned when a MySQL connection is attempted
// while the storage barrier is sealed. The server is fail-closed during
// the sealed window: only the unseal-flow endpoints on the multi-protocol
// API surface accept requests until the barrier is unsealed.
type BarrierSealedError struct{}

// Error returns a human-readable description.
func (e *BarrierSealedError) Error() string {
	return "sql/mysql: barrier sealed"
}

// SealStateUnconfirmedError is returned when a MySQL connection or query
// reaches a node whose barrier is unsealed but which cannot confirm the
// cluster seal state: its lease on the seal records ran out, typically while
// the system shard has no leader. It reaches the client as error 1105 with its
// own message, distinct from BarrierSealedError, and is retryable: the node
// serves again once it reads the records.
type SealStateUnconfirmedError struct{}

// Error returns a human-readable description.
func (e *SealStateUnconfirmedError) Error() string {
	return "sql/mysql: seal state unconfirmed: this node cannot confirm the cluster seal state; retry"
}

// Unwrap exposes the cluster-seal refusal it reports.
func (e *SealStateUnconfirmedError) Unwrap() error { return &clusterseal.SealStateUnconfirmedError{} }

// QueryTimeoutError reports a statement cancelled at the query timeout
// (WithQueryTimeout, sql.query_timeout). On the wire it is ER_QUERY_TIMEOUT.
type QueryTimeoutError struct {
	Timeout time.Duration
}

// Error implements error.
func (e *QueryTimeoutError) Error() string {
	return "sql/mysql: statement exceeded the query timeout of " + e.Timeout.String() + " (sql.query_timeout)"
}

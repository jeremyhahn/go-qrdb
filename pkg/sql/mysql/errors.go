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

// SessionBuildError is returned when building a SQL session fails.
type SessionBuildError struct {
	Err error
}

// Error returns a human-readable description.
func (e *SessionBuildError) Error() string {
	if e.Err != nil {
		return "sql/mysql: session build failed: " + e.Err.Error()
	}
	return "sql/mysql: session build failed"
}

// Unwrap returns the underlying error.
func (e *SessionBuildError) Unwrap() error {
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

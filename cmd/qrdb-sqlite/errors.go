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

package main

// ConfigError is returned when the gateway configuration is invalid.
// Field identifies which configuration field failed validation;
// Reason describes why.
type ConfigError struct {
	Field  string
	Reason string
}

// Error returns a human-readable description of the configuration error.
func (e *ConfigError) Error() string {
	return "qrdb-sqlite: invalid configuration for " + e.Field + ": " + e.Reason
}

// ConnectionError is returned when the gateway fails to connect to a QRDB
// server. Address is the target address that was attempted; Err is the
// underlying connection failure.
type ConnectionError struct {
	Address string
	Err     error
}

// Error returns a human-readable description of the connection error.
func (e *ConnectionError) Error() string {
	return "qrdb-sqlite: connect to " + e.Address + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying connection error.
func (e *ConnectionError) Unwrap() error {
	return e.Err
}

// TransportError is returned when the transport layer cannot be constructed.
// Transport names the transport type (e.g. "grpc", "rest", "unix");
// Err is the underlying failure.
type TransportError struct {
	Transport string
	Err       error
}

// Error returns a human-readable description of the transport error.
func (e *TransportError) Error() string {
	return "qrdb-sqlite: transport " + e.Transport + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying transport error.
func (e *TransportError) Unwrap() error {
	return e.Err
}

// VirtualTableError is returned when a SQLite virtual table cannot be
// registered. Table is the virtual table name; Err is the underlying failure.
type VirtualTableError struct {
	Table string
	Err   error
}

// Error returns a human-readable description of the virtual table error.
func (e *VirtualTableError) Error() string {
	return "qrdb-sqlite: virtual table " + e.Table + " registration failed: " + e.Err.Error()
}

// Unwrap returns the underlying virtual table error.
func (e *VirtualTableError) Unwrap() error {
	return e.Err
}

// ShellError is returned when the interactive SQLite shell encounters a
// fatal error. Err is the underlying failure.
type ShellError struct {
	Err error
}

// Error returns a human-readable description of the shell error.
func (e *ShellError) Error() string {
	return "qrdb-sqlite: shell error: " + e.Err.Error()
}

// Unwrap returns the underlying shell error.
func (e *ShellError) Unwrap() error {
	return e.Err
}

// TranslationError is returned when a SQL query cannot be translated from
// standard SQL to the QRDB dialect. Query is the offending SQL statement;
// Err is the underlying translation failure.
type TranslationError struct {
	Query string
	Err   error
}

// Error returns a human-readable description of the translation error.
func (e *TranslationError) Error() string {
	return "qrdb-sqlite: SQL translation failed for query [" + e.Query + "]: " + e.Err.Error()
}

// Unwrap returns the underlying translation error.
func (e *TranslationError) Unwrap() error {
	return e.Err
}

// MetadataError is returned when table metadata cannot be discovered from
// the QRDB server. Err is the underlying failure.
type MetadataError struct {
	Err error
}

// Error returns a human-readable description of the metadata error.
func (e *MetadataError) Error() string {
	return "qrdb-sqlite: table metadata discovery failed: " + e.Err.Error()
}

// Unwrap returns the underlying metadata error.
func (e *MetadataError) Unwrap() error {
	return e.Err
}

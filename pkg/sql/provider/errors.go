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

// Package provider implements the go-mysql-server DatabaseProvider interface
// backed by QRDB's KV service layer. It maps SQL operations to KV Put, Get,
// Delete, and Scan calls, enabling SQL access to the distributed key-value
// store.
package provider

// DatabaseNotFoundError is returned when a requested database does not
// exist in the provider.
type DatabaseNotFoundError struct {
	Name string
}

// Error returns a human-readable description.
func (e *DatabaseNotFoundError) Error() string {
	return "provider: database not found: " + e.Name
}

// TableNotFoundError is returned when a requested table does not exist
// within the specified database.
type TableNotFoundError struct {
	Database string
	Table    string
}

// Error returns a human-readable description.
func (e *TableNotFoundError) Error() string {
	return "provider: table not found: " + e.Database + "." + e.Table
}

// DuplicateKeyError is returned when an insert would violate a primary
// key uniqueness constraint.
type DuplicateKeyError struct {
	Key string
}

// Error returns a human-readable description.
func (e *DuplicateKeyError) Error() string {
	return "provider: duplicate key: " + e.Key
}

// InvalidKeyError is returned when a key value cannot be used as a KV
// store key (e.g., empty string or wrong type).
type InvalidKeyError struct {
	Key string
}

// Error returns a human-readable description.
func (e *InvalidKeyError) Error() string {
	return "provider: invalid key: " + e.Key
}

// InvalidValueError is returned when a value cannot be stored (e.g.,
// nil or unsupported type).
type InvalidValueError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *InvalidValueError) Error() string {
	return "provider: invalid value: " + e.Reason
}

// CreateDatabaseError is returned when a CREATE DATABASE operation fails.
type CreateDatabaseError struct {
	Name string
	Err  error
}

// Error returns a human-readable description.
func (e *CreateDatabaseError) Error() string {
	return "provider: create database failed: " + e.Name + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CreateDatabaseError) Unwrap() error {
	return e.Err
}

// DropDatabaseError is returned when a DROP DATABASE operation fails.
type DropDatabaseError struct {
	Name string
	Err  error
}

// Error returns a human-readable description.
func (e *DropDatabaseError) Error() string {
	return "provider: drop database failed: " + e.Name + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DropDatabaseError) Unwrap() error {
	return e.Err
}

// MutableOperationNotSupportedError is returned when a mutable operation
// (CREATE/DROP DATABASE) is attempted on a legacy single-database provider
// that has no database manager configured.
type MutableOperationNotSupportedError struct {
	Operation string
}

// Error returns a human-readable description.
func (e *MutableOperationNotSupportedError) Error() string {
	return "provider: " + e.Operation + " not supported without database manager"
}

// IndexLookupError is returned when an index lookup operation fails
// during SQL query execution.
type IndexLookupError struct {
	IndexName string
	Err       error
}

// Error returns a human-readable description including the index name
// and the underlying cause.
func (e *IndexLookupError) Error() string {
	return "provider: index lookup failed: " + e.IndexName + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for errors.Is and errors.As
// unwrapping.
func (e *IndexLookupError) Unwrap() error {
	return e.Err
}

// RangeTranslationError is returned when a SQL range filter produced by
// the query planner cannot be translated to a QRDB index operation.
type RangeTranslationError struct {
	Reason string
}

// Error returns a human-readable description of the translation failure.
func (e *RangeTranslationError) Error() string {
	return "provider: range translation failed: " + e.Reason
}

// UnsupportedRangeTypeError is returned when a MySQLRangeColumnExpr has
// a RangeType that cannot be mapped to a QRDB index operation.
type UnsupportedRangeTypeError struct {
	RangeType string
}

// Error returns a human-readable description including the unsupported
// range type.
func (e *UnsupportedRangeTypeError) Error() string {
	return "provider: unsupported range type: " + e.RangeType
}

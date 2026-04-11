//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package scalar

import "strconv"

// ScalarIndexNotFoundError indicates that a scalar index for the specified field
// does not exist.
type ScalarIndexNotFoundError struct {
	Field string
}

// Error implements the error interface.
func (e *ScalarIndexNotFoundError) Error() string {
	return "scalar index not found for field: " + e.Field
}

// UnsupportedScalarIndexTypeError indicates that the specified scalar index type
// is not recognized or supported.
type UnsupportedScalarIndexTypeError struct {
	Type ScalarIndexType
}

// Error implements the error interface.
func (e *UnsupportedScalarIndexTypeError) Error() string {
	return "unsupported scalar index type: " + e.Type.String()
}

// ScalarQueryError indicates a failure during a scalar index query operation.
type ScalarQueryError struct {
	Op    QueryOp
	Value interface{}
	Err   error
}

// Error implements the error interface.
func (e *ScalarQueryError) Error() string {
	return "scalar query failed for op " + e.Op.String() + ": " + e.Err.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ScalarQueryError) Unwrap() error {
	return e.Err
}

// UnsupportedQueryOpError indicates that the specified query operator is not
// supported by this index type.
type UnsupportedQueryOpError struct {
	Op        QueryOp
	IndexType ScalarIndexType
}

// Error implements the error interface.
func (e *UnsupportedQueryOpError) Error() string {
	return "operator " + e.Op.String() + " not supported by " + e.IndexType.String() + " index"
}

// NilValueError indicates that a nil value was provided where a non-nil value
// is required.
type NilValueError struct {
	Op string
}

// Error implements the error interface.
func (e *NilValueError) Error() string {
	return "nil value not allowed for operation: " + e.Op
}

// InvalidRangeError indicates that a range query received invalid bounds.
type InvalidRangeError struct {
	Min string
	Max string
}

// Error implements the error interface.
func (e *InvalidRangeError) Error() string {
	return "invalid range: min=" + e.Min + " max=" + e.Max
}

// InvalidValueTypeError indicates that the provided value cannot be converted
// to the required type.
type InvalidValueTypeError struct {
	Value    interface{}
	Expected string
}

// Error implements the error interface.
func (e *InvalidValueTypeError) Error() string {
	if e.Value == nil {
		return "invalid value type: nil, expected " + e.Expected
	}
	return "invalid value type: expected " + e.Expected
}

// IndexClosedError indicates that an operation was attempted on a closed index.
type IndexClosedError struct {
	IndexType ScalarIndexType
}

// Error implements the error interface.
func (e *IndexClosedError) Error() string {
	return e.IndexType.String() + " index is closed"
}

// DuplicateValueError indicates that a value was provided for a vector ID that
// already exists. This is informational; the Add method updates the value.
type DuplicateValueError struct {
	VectorID uint64
}

// Error implements the error interface.
func (e *DuplicateValueError) Error() string {
	return "duplicate entry for vector ID " + strconv.FormatUint(e.VectorID, 10)
}

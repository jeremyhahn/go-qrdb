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

// UnsupportedScalarIndexTypeError indicates that the specified scalar index type
// is not recognized or supported.
type UnsupportedScalarIndexTypeError struct {
	Type ScalarIndexType
}

// Error implements the error interface.
func (e *UnsupportedScalarIndexTypeError) Error() string {
	return "unsupported scalar index type: " + e.Type.String()
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

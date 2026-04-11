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

package flat

import (
	"errors"
	"strconv"
)

var (
	// ErrEmptyIndex is returned when searching an empty index.
	ErrEmptyIndex = errors.New("flat index is empty")

	// ErrInvalidVector is returned when a vector has invalid dimensions.
	ErrInvalidVector = errors.New("invalid vector dimensions")

	// ErrNotFound is returned when a vector ID is not found.
	ErrNotFound = errors.New("vector not found")

	// ErrDuplicateID is returned when attempting to insert a duplicate ID.
	ErrDuplicateID = errors.New("duplicate vector ID")

	// ErrInvalidK is returned when k is less than or equal to zero.
	ErrInvalidK = errors.New("k must be greater than zero")
)

// DimensionMismatchError indicates a vector has the wrong number of dimensions.
type DimensionMismatchError struct {
	// Got is the actual number of dimensions.
	Got int

	// Expected is the expected number of dimensions.
	Expected int
}

// Error implements the error interface.
func (e *DimensionMismatchError) Error() string {
	return "dimension mismatch: got " + strconv.Itoa(e.Got) + ", expected " + strconv.Itoa(e.Expected)
}

// Unwrap returns the sentinel ErrInvalidVector error.
func (e *DimensionMismatchError) Unwrap() error {
	return ErrInvalidVector
}

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

// Package quantization provides vector quantization algorithms for memory-efficient
// storage and search. Supports SQ8, Product Quantization, Float16, and BFloat16.
package quantization

import (
	"errors"
	"strconv"
)

var (
	// ErrNotCalibrated indicates the quantizer has not been calibrated.
	ErrNotCalibrated = errors.New("quantizer not calibrated")

	// ErrNotTrained indicates the quantizer has not been trained.
	ErrNotTrained = errors.New("quantizer not trained")

	// ErrDimensionMismatch indicates a vector dimension mismatch.
	ErrDimensionMismatch = errors.New("vector dimension mismatch")
)

// QuantizationError represents a general quantization operation error.
type QuantizationError struct {
	Op      string
	Message string
	Err     error
}

// Error implements the error interface.
func (e *QuantizationError) Error() string {
	s := e.Op + ": " + e.Message
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *QuantizationError) Unwrap() error {
	return e.Err
}

// DimensionError indicates a vector has the wrong number of dimensions.
type DimensionError struct {
	Got      int
	Expected int
}

// Error implements the error interface.
func (e *DimensionError) Error() string {
	return "vector has " + strconv.Itoa(e.Got) + " dimensions, expected " + strconv.Itoa(e.Expected)
}

// Is supports error comparison via errors.Is with ErrDimensionMismatch.
func (e *DimensionError) Is(target error) bool {
	return target == ErrDimensionMismatch
}

// EmptyVectorSetError indicates an operation was attempted on an empty vector set.
type EmptyVectorSetError struct {
	Op string
}

// Error implements the error interface.
func (e *EmptyVectorSetError) Error() string {
	return e.Op + ": empty vector set"
}

// InvalidConfigError indicates an invalid quantization configuration.
type InvalidConfigError struct {
	Message string
}

// Error implements the error interface.
func (e *InvalidConfigError) Error() string {
	return "invalid quantization config: " + e.Message
}

// InsufficientVectorsError indicates too few vectors for the requested operation.
type InsufficientVectorsError struct {
	Required int
	Got      int
}

// Error implements the error interface.
func (e *InsufficientVectorsError) Error() string {
	return "insufficient vectors: need at least " + strconv.Itoa(e.Required) + ", got " + strconv.Itoa(e.Got)
}

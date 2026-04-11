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

package api

import (
	"fmt"
	"strconv"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// =============================================================================
// IVF Index Error Types
// =============================================================================

// IVFConfigValidationError indicates an IVF configuration validation failure.
type IVFConfigValidationError struct {
	Message string
}

// Error implements the error interface.
func (e *IVFConfigValidationError) Error() string {
	return e.Message
}

// NewIVFConfigValidationError creates a QRDBError for an IVF config validation failure.
func NewIVFConfigValidationError(op, message string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &IVFConfigValidationError{Message: message},
	}
}

// IVFInvalidConfigError indicates an invalid IVF configuration.
type IVFInvalidConfigError struct {
	Cause error
}

// Error implements the error interface.
func (e *IVFInvalidConfigError) Error() string {
	return "invalid config: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *IVFInvalidConfigError) Unwrap() error {
	return e.Cause
}

// NewIVFInvalidConfigError creates a QRDBError for an invalid IVF configuration.
func NewIVFInvalidConfigError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &IVFInvalidConfigError{Cause: cause},
	}
}

// IVFEmptyTrainingSetError indicates an attempt to train on an empty vector set.
type IVFEmptyTrainingSetError struct{}

// Error implements the error interface.
func (e *IVFEmptyTrainingSetError) Error() string {
	return "cannot train on empty vector set"
}

// NewIVFEmptyTrainingSetError creates a QRDBError for training on an empty set.
func NewIVFEmptyTrainingSetError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &IVFEmptyTrainingSetError{},
	}
}

// IVFDimensionMismatchError indicates a vector has the wrong number of dimensions.
type IVFDimensionMismatchError struct {
	Index    int
	Got      int
	Expected int
}

// Error implements the error interface.
func (e *IVFDimensionMismatchError) Error() string {
	if e.Index >= 0 {
		return fmt.Sprintf("vector %d has %d dimensions, expected %d", e.Index, e.Got, e.Expected)
	}
	return fmt.Sprintf("vector has %d dimensions, expected %d", e.Got, e.Expected)
}

// NewIVFDimensionMismatchError creates a QRDBError for a vector dimension mismatch.
func NewIVFDimensionMismatchError(op string, index, got, expected int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &IVFDimensionMismatchError{Index: index, Got: got, Expected: expected},
	}
}

// IVFClusteringFailedError indicates k-means clustering failed.
type IVFClusteringFailedError struct {
	Cause error
}

// Error implements the error interface.
func (e *IVFClusteringFailedError) Error() string {
	return "k-means clustering failed: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *IVFClusteringFailedError) Unwrap() error {
	return e.Cause
}

// NewIVFClusteringFailedError creates a QRDBError for a clustering failure.
func NewIVFClusteringFailedError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &IVFClusteringFailedError{Cause: cause},
	}
}

// IVFNotTrainedError indicates an operation was attempted on an untrained index.
type IVFNotTrainedError struct {
	Operation string
}

// Error implements the error interface.
func (e *IVFNotTrainedError) Error() string {
	return "index must be trained" + e.suffix()
}

func (e *IVFNotTrainedError) suffix() string {
	if e.Operation != "" {
		return " before " + e.Operation
	}
	return ""
}

// NewIVFNotTrainedError creates a QRDBError for an untrained index operation.
func NewIVFNotTrainedError(op, operation string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrPreconditionFailed,
		Op:   op,
		Err:  &IVFNotTrainedError{Operation: operation},
	}
}

// IVFDuplicateIDError indicates a vector with the given ID already exists.
type IVFDuplicateIDError struct {
	ID string
}

// Error implements the error interface.
func (e *IVFDuplicateIDError) Error() string {
	return "vector with ID " + e.ID + " already exists"
}

// NewIVFDuplicateIDError creates a QRDBError for a duplicate vector ID.
func NewIVFDuplicateIDError(op, id string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrAlreadyExists,
		Op:   op,
		Err:  &IVFDuplicateIDError{ID: id},
	}
}

// IVFVectorNotFoundError indicates a vector with the given ID was not found.
type IVFVectorNotFoundError struct {
	ID string
}

// Error implements the error interface.
func (e *IVFVectorNotFoundError) Error() string {
	return "vector with ID " + e.ID + " not found"
}

// NewIVFVectorNotFoundError creates a QRDBError for a missing vector.
func NewIVFVectorNotFoundError(op, id string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &IVFVectorNotFoundError{ID: id},
	}
}

// IVFSaveError indicates a failure saving or loading an IVF index.
type IVFSaveError struct {
	Action string
	Cause  error
}

// Error implements the error interface.
func (e *IVFSaveError) Error() string {
	return "failed to " + e.Action + ": " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *IVFSaveError) Unwrap() error {
	return e.Cause
}

// NewIVFSaveError creates a QRDBError for an IVF index save/load failure.
func NewIVFSaveError(op, action string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrStorage,
		Op:   op,
		Err:  &IVFSaveError{Action: action, Cause: cause},
	}
}

// IVFUntrainedSaveError indicates an attempt to save an untrained index.
type IVFUntrainedSaveError struct{}

// Error implements the error interface.
func (e *IVFUntrainedSaveError) Error() string {
	return "cannot save untrained index"
}

// NewIVFUntrainedSaveError creates a QRDBError for saving an untrained index.
func NewIVFUntrainedSaveError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrPreconditionFailed,
		Op:   op,
		Err:  &IVFUntrainedSaveError{},
	}
}

// IVFQueryDimensionError indicates a query vector has the wrong dimensions.
type IVFQueryDimensionError struct {
	Got      int
	Expected int
}

// Error implements the error interface.
func (e *IVFQueryDimensionError) Error() string {
	return fmt.Sprintf("query has %d dimensions, expected %d", e.Got, e.Expected)
}

// NewIVFQueryDimensionError creates a QRDBError for a query dimension mismatch.
func NewIVFQueryDimensionError(op string, got, expected int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &IVFQueryDimensionError{Got: got, Expected: expected},
	}
}

// =============================================================================
// K-Means Error Types
// =============================================================================

// KMeansEmptyVectorSetError indicates an attempt to cluster an empty vector set.
type KMeansEmptyVectorSetError struct{}

// Error implements the error interface.
func (e *KMeansEmptyVectorSetError) Error() string {
	return "cannot cluster empty vector set"
}

// NewKMeansEmptyVectorSetError creates a QRDBError for clustering an empty set.
func NewKMeansEmptyVectorSetError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &KMeansEmptyVectorSetError{},
	}
}

// KMeansInsufficientVectorsError indicates too few vectors for the requested clusters.
type KMeansInsufficientVectorsError struct {
	Clusters int
	Vectors  int
}

// Error implements the error interface.
func (e *KMeansInsufficientVectorsError) Error() string {
	return fmt.Sprintf("cannot create %d clusters from %d vectors", e.Clusters, e.Vectors)
}

// NewKMeansInsufficientVectorsError creates a QRDBError for insufficient vectors.
func NewKMeansInsufficientVectorsError(op string, clusters, vectors int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &KMeansInsufficientVectorsError{Clusters: clusters, Vectors: vectors},
	}
}

// KMeansInitError indicates failure to initialize centroids.
type KMeansInitError struct {
	Cause error
}

// Error implements the error interface.
func (e *KMeansInitError) Error() string {
	return "failed to initialize centroids: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *KMeansInitError) Unwrap() error {
	return e.Cause
}

// NewKMeansInitError creates a QRDBError for a centroid initialization failure.
func NewKMeansInitError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &KMeansInitError{Cause: cause},
	}
}

// KMeansRecomputeError indicates failure to recompute centroids during iteration.
type KMeansRecomputeError struct {
	Iteration int
	Cause     error
}

// Error implements the error interface.
func (e *KMeansRecomputeError) Error() string {
	return "failed to recompute centroids at iteration " + strconv.Itoa(e.Iteration) + ": " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *KMeansRecomputeError) Unwrap() error {
	return e.Cause
}

// NewKMeansRecomputeError creates a QRDBError for a centroid recompute failure.
func NewKMeansRecomputeError(op string, iteration int, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &KMeansRecomputeError{Iteration: iteration, Cause: cause},
	}
}

// =============================================================================
// HNSW Persistence Error Types
// =============================================================================

// HNSWPersistenceError indicates a failure during HNSW index persistence.
type HNSWPersistenceError struct {
	Action string
	Cause  error
}

// Error implements the error interface.
func (e *HNSWPersistenceError) Error() string {
	return "failed to " + e.Action + ": " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *HNSWPersistenceError) Unwrap() error {
	return e.Cause
}

// NewHNSWPersistenceError creates a QRDBError for an HNSW persistence failure.
func NewHNSWPersistenceError(op, action string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrStorage,
		Op:   op,
		Err:  &HNSWPersistenceError{Action: action, Cause: cause},
	}
}

// =============================================================================
// RAG Adapter Error Types
// =============================================================================

// RAGSearchError indicates a failure during RAG vector search.
type RAGSearchError struct {
	Action string
	Cause  error
}

// Error implements the error interface.
func (e *RAGSearchError) Error() string {
	return e.Action + " failed: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *RAGSearchError) Unwrap() error {
	return e.Cause
}

// NewRAGSearchError creates a QRDBError for a RAG search failure.
func NewRAGSearchError(op, action string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &RAGSearchError{Action: action, Cause: cause},
	}
}

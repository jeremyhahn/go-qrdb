// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package vector

import "errors"

var (
	// ErrInvalidConfig indicates an invalid configuration.
	ErrInvalidConfig = errors.New("invalid vector storage configuration")

	// ErrStorageClosed indicates the storage is closed.
	ErrStorageClosed = errors.New("vector storage is closed")

	// ErrNamespaceNotFound indicates the namespace does not exist.
	ErrNamespaceNotFound = errors.New("namespace not found")

	// ErrNamespaceExists indicates the namespace already exists.
	ErrNamespaceExists = errors.New("namespace already exists")

	// ErrVectorNotFound indicates the vector does not exist.
	ErrVectorNotFound = errors.New("vector not found")

	// ErrInvalidNamespace indicates an invalid namespace name.
	ErrInvalidNamespace = errors.New("invalid namespace name")

	// ErrInvalidID indicates an invalid vector ID.
	ErrInvalidID = errors.New("invalid vector ID")

	// ErrInvalidVector indicates an invalid vector.
	ErrInvalidVector = errors.New("invalid vector")

	// ErrEmptyBatch indicates an empty batch operation.
	ErrEmptyBatch = errors.New("empty batch operation")

	// ErrBatchTooLarge indicates a batch operation is too large.
	ErrBatchTooLarge = errors.New("batch operation too large")

	// ErrContextCanceled indicates the context was canceled.
	ErrContextCanceled = errors.New("context canceled")
)

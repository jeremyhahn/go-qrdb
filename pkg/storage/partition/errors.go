// Copyright (c) 2026 Jeremy Hahn
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

// Package partition provides xxhash64-based storage partitioning that
// implements the storage.Storage interface transparently.
package partition

import "strconv"

// PartitionNotFoundError is returned when a partition ID does not exist.
type PartitionNotFoundError struct {
	PartitionID uint32
}

// Error implements the error interface.
func (e *PartitionNotFoundError) Error() string {
	return "partition not found: " + strconv.FormatUint(uint64(e.PartitionID), 10)
}

// PartitionCreateError is returned when a partition storage fails to create.
type PartitionCreateError struct {
	PartitionID uint32
	Err         error
}

// Error implements the error interface.
func (e *PartitionCreateError) Error() string {
	return "failed to create partition " + strconv.FormatUint(uint64(e.PartitionID), 10) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *PartitionCreateError) Unwrap() error {
	return e.Err
}

// PartitionCloseError is returned when one or more partitions fail to close.
type PartitionCloseError struct {
	PartitionID uint32
	Err         error
}

// Error implements the error interface.
func (e *PartitionCloseError) Error() string {
	return "failed to close partition " + strconv.FormatUint(uint64(e.PartitionID), 10) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *PartitionCloseError) Unwrap() error {
	return e.Err
}

// MergeIteratorError is returned when the merge iterator encounters an error.
type MergeIteratorError struct {
	Message string
	Err     error
}

// Error implements the error interface.
func (e *MergeIteratorError) Error() string {
	if e.Err != nil {
		return "merge iterator: " + e.Message + ": " + e.Err.Error()
	}
	return "merge iterator: " + e.Message
}

// Unwrap returns the underlying error.
func (e *MergeIteratorError) Unwrap() error {
	return e.Err
}

// ConfigError is returned when the partition configuration is invalid.
type ConfigError struct {
	Field   string
	Message string
}

// Error implements the error interface.
func (e *ConfigError) Error() string {
	return "partition config: " + e.Field + ": " + e.Message
}

// BatchCommitError is returned when one or more partition batches fail to commit.
type BatchCommitError struct {
	Errors []error
}

// Error implements the error interface.
func (e *BatchCommitError) Error() string {
	return "partition batch: " + strconv.Itoa(len(e.Errors)) + " partition(s) failed to commit"
}

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

package chunker

import "strconv"

// EmptyTextError is returned when an empty string is passed to Chunk.
type EmptyTextError struct{}

// Error returns a human-readable description.
func (e *EmptyTextError) Error() string {
	return "chunker: text must not be empty"
}

// InvalidConfigError is returned when a Config field has an invalid value.
type InvalidConfigError struct {
	Reason string
}

// Error returns a human-readable description of the invalid configuration.
func (e *InvalidConfigError) Error() string {
	return "chunker: invalid config: " + e.Reason
}

// ChunkTooSmallError is returned when a resulting chunk is smaller than MinSize
// and cannot be merged because there is no previous chunk.
type ChunkTooSmallError struct {
	Size    int
	MinSize int
}

// Error returns a human-readable description of the size violation.
func (e *ChunkTooSmallError) Error() string {
	return "chunker: chunk size " + strconv.Itoa(e.Size) +
		" is below minimum " + strconv.Itoa(e.MinSize)
}

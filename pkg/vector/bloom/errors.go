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

// Package bloom provides probabilistic data structures for efficient vector ID
// existence checks, enabling segment pruning during vector search operations.
package bloom

// IncompatibleFilterError indicates that two bloom filters cannot be merged
// because they have different sizes or hash configurations.
type IncompatibleFilterError struct {
	Reason string
}

// Error implements the error interface.
func (e *IncompatibleFilterError) Error() string {
	return "incompatible bloom filter: " + e.Reason
}

// InvalidParameterError indicates invalid parameters were provided to create a bloom filter.
type InvalidParameterError struct {
	Parameter string
	Reason    string
}

// Error implements the error interface.
func (e *InvalidParameterError) Error() string {
	return "invalid bloom filter parameter " + e.Parameter + ": " + e.Reason
}

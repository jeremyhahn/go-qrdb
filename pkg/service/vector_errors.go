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

package service

import "strconv"

// VectorNotFoundError is returned when a vector ID does not exist in
// the specified namespace.
type VectorNotFoundError struct {
	Namespace string
	ID        string
}

// Error returns a human-readable description of the missing vector.
func (e *VectorNotFoundError) Error() string {
	return "service: vector not found: " + e.Namespace + "/" + e.ID
}

// NamespaceNotFoundError is returned when an operation references a
// vector namespace that does not exist.
type NamespaceNotFoundError struct {
	Namespace string
}

// Error returns a human-readable description of the missing namespace.
func (e *NamespaceNotFoundError) Error() string {
	return "service: namespace not found: " + e.Namespace
}

// NamespaceExistsError is returned when attempting to create a namespace
// with a name that is already in use.
type NamespaceExistsError struct {
	Namespace string
}

// Error returns a human-readable description of the duplicate namespace.
func (e *NamespaceExistsError) Error() string {
	return "service: namespace already exists: " + e.Namespace
}

// DimensionMismatchError is returned when a vector's dimensionality does
// not match the namespace's configured dimension.
type DimensionMismatchError struct {
	Expected int
	Actual   int
}

// Error returns a human-readable description of the dimension mismatch.
func (e *DimensionMismatchError) Error() string {
	return "service: dimension mismatch: expected " + strconv.Itoa(e.Expected) +
		", got " + strconv.Itoa(e.Actual)
}

// InvalidVectorError is returned when vector data is malformed or
// contains invalid values.
type InvalidVectorError struct {
	Reason string
}

// Error returns a human-readable description of the invalid vector.
func (e *InvalidVectorError) Error() string {
	return "service: invalid vector: " + e.Reason
}

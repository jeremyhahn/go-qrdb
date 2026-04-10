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

package messaging

// DeltaNilInputError represents an error when nil input is provided.
type DeltaNilInputError struct {
	Parameter string
}

// Error implements the error interface.
func (e *DeltaNilInputError) Error() string {
	return "delta computation requires non-nil " + e.Parameter
}

// DeltaTypeMismatchError represents an error when types do not match.
type DeltaTypeMismatchError struct {
	OldType string
	NewType string
	Path    string
}

// Error implements the error interface.
func (e *DeltaTypeMismatchError) Error() string {
	if e.Path != "" {
		return "type mismatch at " + e.Path + ": " + e.OldType + " vs " + e.NewType
	}
	return "type mismatch: " + e.OldType + " vs " + e.NewType
}

// DeltaMaxDepthExceededError represents an error when recursion depth is exceeded.
type DeltaMaxDepthExceededError struct {
	MaxDepth int
	Path     string
}

// Error implements the error interface.
func (e *DeltaMaxDepthExceededError) Error() string {
	return "max recursion depth exceeded at " + e.Path
}

// DeltaDeserializationError represents a deserialization failure.
type DeltaDeserializationError struct {
	Cause string
}

// Error implements the error interface.
func (e *DeltaDeserializationError) Error() string {
	return "failed to deserialize delta input: " + e.Cause
}

// DeltaDiffError represents a failure in the Diffable interface.
type DeltaDiffError struct {
	Cause string
}

// Error implements the error interface.
func (e *DeltaDiffError) Error() string {
	return "diffable interface failed: " + e.Cause
}

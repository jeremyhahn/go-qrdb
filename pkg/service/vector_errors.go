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

// InvalidVectorError is returned when a vector-service request is malformed:
// a required field is missing, a value is out of range, or the vector data
// itself is invalid. Reason names what is wrong.
type InvalidVectorError struct {
	Reason string
}

// Error returns a human-readable description of the invalid vector.
func (e *InvalidVectorError) Error() string {
	return "service: invalid vector: " + e.Reason
}

// VectorServiceClosedError is returned when an operation is attempted on a
// vector service that has already been closed, or whose storage has.
type VectorServiceClosedError struct{}

// Error returns a human-readable description of the closed-service condition.
func (e *VectorServiceClosedError) Error() string {
	return "service: vector service is closed"
}

// VectorJobNotFoundError is returned when a rebuild or compaction job the
// caller names — by job id, or by the namespace it ran against — does not
// exist.
type VectorJobNotFoundError struct {
	// Job is the kind of job: "rebuild" or "compaction".
	Job string
	// ID is the job id, or the namespace when the lookup was by namespace.
	ID string
}

// Error returns a human-readable description of the missing job.
func (e *VectorJobNotFoundError) Error() string {
	return "service: vector " + e.Job + " job not found: " + e.ID
}

// VectorOperationError is returned when a vector-storage or index operation
// fails for a reason that is not the caller's: a storage fault, an index that
// could not be built or searched. Op names the operation; Err, when set, is
// the cause.
type VectorOperationError struct {
	Op  string
	Err error
}

// Error returns a human-readable description of the failed operation.
func (e *VectorOperationError) Error() string {
	if e.Err == nil {
		return "service: vector " + e.Op + " failed"
	}
	return "service: vector " + e.Op + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *VectorOperationError) Unwrap() error {
	return e.Err
}

// TenantRequiredError is returned when strict tenant isolation is enabled and
// an operation is attempted without a tenant ID in the request context.
type TenantRequiredError struct {
	// Op is the service operation that was rejected.
	Op string
}

// Error returns a human-readable description of the missing tenant.
func (e *TenantRequiredError) Error() string {
	return "service: tenant required for " + e.Op + ": strict tenant isolation is enabled"
}

// InvalidNamespaceNameError is returned when a namespace name is empty, exceeds
// the maximum length, or contains a control character or the \x1f separator byte
// used to partition scoped storage keys.
type InvalidNamespaceNameError struct {
	// Name is the invalid namespace name that was rejected.
	Name   string
	Reason string
}

// Error returns a human-readable description of the invalid namespace name.
func (e *InvalidNamespaceNameError) Error() string {
	return "service: invalid namespace name " + strconv.Quote(e.Name) + ": " + e.Reason
}

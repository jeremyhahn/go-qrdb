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

import (
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/feature"
)

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

// UnknownDistanceMetricError is returned when a namespace is created with a
// distance metric the vector index layer does not implement. Refusing the
// name is the point: reading it as L2 would build a namespace that answers
// every search under a metric its caller never asked for.
type UnknownDistanceMetricError struct {
	Metric string
}

// Error returns a human-readable description naming the metrics accepted.
func (e *UnknownDistanceMetricError) Error() string {
	return "service: unknown distance metric " + strconv.Quote(e.Metric) +
		" (want L2, EUCLIDEAN, COSINE, DOT_PRODUCT or MANHATTAN)"
}

// UnknownIndexTypeError is returned when a namespace is created with an index
// type the service does not build. Reading it as HNSW would hide that the
// namespace answers from a different index than the one it records.
type UnknownIndexTypeError struct {
	IndexType string
}

// Error returns a human-readable description naming the index types accepted.
func (e *UnknownIndexTypeError) Error() string {
	return "service: unknown vector index type " + strconv.Quote(e.IndexType) +
		" (want HNSW, FLAT, IVF or IVF_FLAT)"
}

// VectorMetricNotActiveError is returned when a FLAT or IVF namespace is
// created with a distance metric whose record value needs a cluster feature
// not yet active for the group: a replica running a binary from before the
// value would refuse the record its peers accept. Nothing is created. It is a
// failed precondition, not a transient fault: the create succeeds only after
// every replica of the group is upgraded.
type VectorMetricNotActiveError struct {
	IndexType string
	Feature   feature.Feature
	ShardID   uint64
}

// Error names the index type, the feature and the group.
func (e *VectorMetricNotActiveError) Error() string {
	return "service: the distance metric of a " + e.IndexType + " namespace needs cluster feature " +
		strconv.Quote(string(e.Feature)) + ", not yet active for shard " + strconv.FormatUint(e.ShardID, 10)
}

// VectorIndexUntrainedError is returned by a similarity search on an IVF
// namespace that holds fewer vectors than its num_centroids, the floor below
// which its index cannot train. Unlike VectorIndexWarmingError it does not
// clear by waiting: the namespace answers once it holds enough vectors.
type VectorIndexUntrainedError struct {
	Namespace string
}

// Error returns a human-readable description.
func (e *VectorIndexUntrainedError) Error() string {
	return "service: vector namespace " + strconv.Quote(e.Namespace) +
		" holds fewer vectors than its IVF index needs to train (num_centroids)"
}

// UnsupportedNamespaceGeometryError is returned by every operation that
// touches a stored namespace whose distance metric or index type this server
// does not implement: a record an earlier release accepted without checking.
// The server never reads such a namespace with a default geometry, and never
// answers a search on it with an empty result; the namespace serves nothing
// until it is dropped and recreated with a supported geometry.
type UnsupportedNamespaceGeometryError struct {
	// Namespace is the logical (unscoped) namespace name.
	Namespace string
	// Field is "distance metric" or "index type".
	Field string
	// Value is the stored value this server refuses.
	Value string
}

// Error names the namespace, the field and the stored value.
func (e *UnsupportedNamespaceGeometryError) Error() string {
	return "service: vector namespace " + strconv.Quote(e.Namespace) + " stores unsupported " +
		e.Field + " " + strconv.Quote(e.Value) + "; drop and recreate it with a supported geometry"
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

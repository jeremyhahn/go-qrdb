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

package grpc

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mapServiceError translates service-layer errors into gRPC status errors
// with appropriate codes. Unknown errors are mapped to codes.Internal.
func mapServiceError(err error) error {
	if err == nil {
		return nil
	}

	var knf *service.KeyNotFoundError
	if errors.As(err, &knf) {
		return status.Error(codes.NotFound, err.Error())
	}

	var nle *service.NotLeaderError
	if errors.As(err, &nle) {
		return status.Error(codes.Unavailable, err.Error())
	}

	var pe *service.ProposeError
	if errors.As(err, &pe) {
		return status.Error(codes.Internal, err.Error())
	}

	var re *service.ReadError
	if errors.As(err, &re) {
		return status.Error(codes.Internal, err.Error())
	}

	var me *service.MembershipError
	if errors.As(err, &me) {
		return status.Error(codes.Internal, err.Error())
	}

	var se *service.SnapshotRequestError
	if errors.As(err, &se) {
		return status.Error(codes.Internal, err.Error())
	}

	var de *service.DrainError
	if errors.As(err, &de) {
		return status.Error(codes.Unavailable, err.Error())
	}

	var rce *service.RevisionCompactedError
	if errors.As(err, &rce) {
		return status.Error(codes.OutOfRange, err.Error())
	}

	var rnf *service.RevisionNotFoundError
	if errors.As(err, &rnf) {
		return status.Error(codes.NotFound, err.Error())
	}

	var tce *service.TransactionConflictError
	if errors.As(err, &tce) {
		return status.Error(codes.Aborted, err.Error())
	}

	var wce *service.WatchCanceledError
	if errors.As(err, &wce) {
		return status.Error(codes.Canceled, err.Error())
	}

	var lnf *service.LeaseNotFoundError
	if errors.As(err, &lnf) {
		return status.Error(codes.NotFound, err.Error())
	}

	var lee *service.LeaseExpiredError
	if errors.As(err, &lee) {
		return status.Error(codes.NotFound, err.Error())
	}

	var lexe *service.LeaseExistsError
	if errors.As(err, &lexe) {
		return status.Error(codes.AlreadyExists, err.Error())
	}

	var be *service.BatchError
	if errors.As(err, &be) {
		return status.Error(codes.Internal, err.Error())
	}

	var inf *service.IndexNotFoundError
	if errors.As(err, &inf) {
		return status.Error(codes.NotFound, err.Error())
	}

	var iee *service.IndexExistsError
	if errors.As(err, &iee) {
		return status.Error(codes.AlreadyExists, err.Error())
	}

	var tse *service.TimeSeriesError
	if errors.As(err, &tse) {
		return status.Error(codes.Internal, err.Error())
	}

	var cse *service.ConsistencyError
	if errors.As(err, &cse) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}

	var vnf *service.VectorNotFoundError
	if errors.As(err, &vnf) {
		return status.Error(codes.NotFound, err.Error())
	}

	var nnf *service.NamespaceNotFoundError
	if errors.As(err, &nnf) {
		return status.Error(codes.NotFound, err.Error())
	}

	var nee *service.NamespaceExistsError
	if errors.As(err, &nee) {
		return status.Error(codes.AlreadyExists, err.Error())
	}

	var dme *service.DimensionMismatchError
	if errors.As(err, &dme) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var ive *service.InvalidVectorError
	if errors.As(err, &ive) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var cae *service.CollectionAlreadyExistsError
	if errors.As(err, &cae) {
		return status.Error(codes.AlreadyExists, err.Error())
	}

	var cnf *service.CollectionNotFoundError
	if errors.As(err, &cnf) {
		return status.Error(codes.NotFound, err.Error())
	}

	var cen *service.CollectionEmptyNameError
	if errors.As(err, &cen) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var ced *service.CollectionEmptyDatabaseError
	if errors.As(err, &ced) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var cis *service.CollectionInvalidIDStrategyError
	if errors.As(err, &cis) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var cmf *service.CollectionMissingFieldHashError
	if errors.As(err, &cmf) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var cdnf *service.CollectionDocumentNotFoundError
	if errors.As(err, &cdnf) {
		return status.Error(codes.NotFound, err.Error())
	}

	var cedc *service.CollectionEmptyDocumentError
	if errors.As(err, &cedc) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var cidi *service.CollectionInvalidDocumentIDError
	if errors.As(err, &cidi) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var cme *service.CollectionMetadataError
	if errors.As(err, &cme) {
		return status.Error(codes.Internal, err.Error())
	}

	var lhe *lock.LockHeldError
	if errors.As(err, &lhe) {
		return status.Error(codes.Aborted, err.Error())
	}

	var nlhe *lock.NotLockHolderError
	if errors.As(err, &nlhe) {
		return status.Error(codes.PermissionDenied, err.Error())
	}

	var lnfe *lock.LockNotFoundError
	if errors.As(err, &lnfe) {
		return status.Error(codes.NotFound, err.Error())
	}

	return status.Error(codes.Internal, err.Error())
}

// StartError is returned when the gRPC server fails to start listening
// on the configured address.
type StartError struct {
	Addr string
	Err  error
}

// Error returns a human-readable description.
func (e *StartError) Error() string {
	return "grpc: server failed to start on " + e.Addr + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *StartError) Unwrap() error {
	return e.Err
}

// ServiceNotConfiguredError is returned when a gRPC handler is invoked
// for a service that was not provided during server construction.
type ServiceNotConfiguredError struct {
	Service string
}

// Error returns a human-readable description.
func (e *ServiceNotConfiguredError) Error() string {
	return "grpc: " + e.Service + " service is not configured"
}

// MetadataMarshalError is returned when metadata cannot be serialized to JSON
// for a gRPC response.
type MetadataMarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *MetadataMarshalError) Error() string {
	return "grpc: failed to marshal metadata: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MetadataMarshalError) Unwrap() error {
	return e.Err
}

// MetadataUnmarshalError is returned when metadata JSON from a gRPC request
// cannot be deserialized.
type MetadataUnmarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *MetadataUnmarshalError) Error() string {
	return "grpc: failed to unmarshal metadata: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MetadataUnmarshalError) Unwrap() error {
	return e.Err
}

// VectorIDParseError is returned when a vector ID string cannot be parsed
// as a uint64 identifier.
type VectorIDParseError struct {
	ID  string
	Err error
}

// Error returns a human-readable description.
func (e *VectorIDParseError) Error() string {
	return "grpc: failed to parse vector ID " + e.ID + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VectorIDParseError) Unwrap() error {
	return e.Err
}

// TTLParseError is returned when a TTL duration string cannot be parsed.
type TTLParseError struct {
	Value string
	Err   error
}

// Error returns a human-readable description.
func (e *TTLParseError) Error() string {
	return "grpc: invalid TTL duration " + e.Value + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TTLParseError) Unwrap() error {
	return e.Err
}

// ClientCertRequiredError is returned when a share-claim handler requires
// a verified mTLS client certificate but the connection carries none.
type ClientCertRequiredError struct{}

// Error returns a human-readable description.
func (e *ClientCertRequiredError) Error() string {
	return "grpc: client certificate required for share claim"
}

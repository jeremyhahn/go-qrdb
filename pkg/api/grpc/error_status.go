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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
)

// grpcCodeByClass is how this transport says each class of failure. The unix
// socket serves these same services, and MCP renders these codes through its
// own correspondence table, so this one table is the answer on three
// transports.
//
// It owns NO clauses about qrdb's conditions — that is pkg/api/errclass's job
// (go-qrdb#557), and it replaced nine per-family ladders here that a gRPC
// service had to pick between. It is total over errclass.All;
// TestGRPCCodeByClass_IsTotal fails the build for a class without a row,
// which would otherwise render as codes.OK — a failure reported as success.
var grpcCodeByClass = map[errclass.Class]codes.Code{
	errclass.Unclassified:       codes.Internal,
	errclass.InvalidArgument:    codes.InvalidArgument,
	errclass.NotFound:           codes.NotFound,
	errclass.AlreadyExists:      codes.AlreadyExists,
	errclass.Aborted:            codes.Aborted,
	errclass.FailedPrecondition: codes.FailedPrecondition,
	errclass.Gone:               codes.FailedPrecondition,
	errclass.PreconditionFailed: codes.FailedPrecondition,
	errclass.Unprocessable:      codes.FailedPrecondition,
	errclass.PermissionDenied:   codes.PermissionDenied,
	errclass.Unauthenticated:    codes.Unauthenticated,
	errclass.ResourceExhausted:  codes.ResourceExhausted,
	errclass.TooLarge:           codes.ResourceExhausted,
	errclass.OutOfRange:         codes.OutOfRange,
	errclass.Unimplemented:      codes.Unimplemented,
	errclass.Unavailable:        codes.Unavailable,
	errclass.Canceled:           codes.Canceled,
	errclass.DataLoss:           codes.DataLoss,
	errclass.Internal:           codes.Internal,
	errclass.DeadlineExceeded:   codes.DeadlineExceeded,
}

// This transport's own refusals declare their class here, beside the table
// that renders it, rather than in pkg/api/errclass, which cannot import this
// package.
var (
	_ errclass.Classed = (*ServiceNotConfiguredError)(nil)
	_ errclass.Classed = (*CeremonyCANotConfiguredError)(nil)
	_ errclass.Classed = (*ClientCertRequiredError)(nil)
	_ errclass.Classed = (*MetadataUnmarshalError)(nil)
	_ errclass.Classed = (*MetadataMarshalError)(nil)
	_ errclass.Classed = (*VectorIDParseError)(nil)
	_ errclass.Classed = (*TTLParseError)(nil)
	_ errclass.Classed = (*DocumentUnmarshalError)(nil)
	_ errclass.Classed = (*DocumentMarshalError)(nil)
	_ errclass.Classed = (*SignatureDecodeError)(nil)
	_ errclass.Classed = (*TimestampParseError)(nil)
)

// ErrorClass reports that this server was built without the service.
func (e *ServiceNotConfiguredError) ErrorClass() errclass.Class { return errclass.Unimplemented }

// ErrorClass reports that no ceremony CA was configured.
func (e *CeremonyCANotConfiguredError) ErrorClass() errclass.Class { return errclass.Unimplemented }

// ErrorClass reports a share claim refused for want of a verified client
// certificate. The caller never established who it is, so it is an
// authentication failure — the same class on every transport's twin of this
// type (REST, QUIC, MCP). It used to answer Internal from the gRPC mapper and
// InvalidArgument from the handlers that built the status inline.
func (e *ClientCertRequiredError) ErrorClass() errclass.Class { return errclass.Unauthenticated }

// ErrorClass reports request metadata JSON that does not decode: the caller's.
func (e *MetadataUnmarshalError) ErrorClass() errclass.Class { return errclass.InvalidArgument }

// ErrorClass reports response metadata this server could not encode: a fault.
func (e *MetadataMarshalError) ErrorClass() errclass.Class { return errclass.Internal }

// ErrorClass reports a vector ID that is not a uint64: the caller's.
func (e *VectorIDParseError) ErrorClass() errclass.Class { return errclass.InvalidArgument }

// ErrorClass reports a TTL duration that does not parse: the caller's.
func (e *TTLParseError) ErrorClass() errclass.Class { return errclass.InvalidArgument }

// ErrorClass reports a document body that does not decode: the caller's.
func (e *DocumentUnmarshalError) ErrorClass() errclass.Class { return errclass.InvalidArgument }

// ErrorClass reports a stored document this server could not encode: a fault.
func (e *DocumentMarshalError) ErrorClass() errclass.Class { return errclass.Internal }

// ErrorClass reports a ceremony signature that is not valid base64: the
// caller's.
func (e *SignatureDecodeError) ErrorClass() errclass.Class { return errclass.InvalidArgument }

// ErrorClass reports a request timestamp that does not parse: the caller's.
func (e *TimestampParseError) ErrorClass() errclass.Class { return errclass.InvalidArgument }

// ServiceErrorCode reports the gRPC status code this transport assigns to err,
// without building a status error around it.
//
// MCP renders its tool errors from this function, so an MCP caller and a gRPC
// caller are told the same thing about one event; the cross-transport parity
// test in pkg/api/errclass drives it as this transport's real answer.
//
// A nil error classifies as codes.OK.
func ServiceErrorCode(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	return grpcCodeByClass[errclass.Classify(err)]
}

// mapServiceError converts err into a gRPC status error carrying its class's
// code and err's message. A nil error stays nil.
func mapServiceError(err error) error {
	if err == nil {
		return nil
	}
	return status.Error(ServiceErrorCode(err), err.Error())
}

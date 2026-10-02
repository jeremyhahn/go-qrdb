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

package quic

import (
	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
)

// wireStatusByClass is how this transport says each class of failure.
//
// It owns NO clauses about qrdb's conditions. Before go-qrdb#557 this package
// carried its own ~80-clause ladder that mirrored gRPC's by hand, and every
// family that ladder never listed — identity, tenants, credentials, ceremonies,
// backup, embedding, auth factors — reached a QUIC caller as StatusError while
// gRPC and REST answered it precisely. What kind of failure an error is, is
// now pkg/api/errclass's answer for every transport; this table only chooses
// the status that carries it.
//
// Each row is the status wire.StatusForGRPCCode assigns to gRPC's code for the
// same class. The parity test in pkg/api/errclass asserts it through both
// transports' real mappings, so a QUIC caller and a gRPC caller cannot be told
// different things about one event. It is total over errclass.All.
var wireStatusByClass = map[errclass.Class]uint16{
	errclass.Unclassified:       wire.StatusError,
	errclass.InvalidArgument:    wire.StatusInvalidArgument,
	errclass.NotFound:           wire.StatusNotFound,
	errclass.AlreadyExists:      wire.StatusAlreadyExists,
	errclass.Aborted:            wire.StatusAborted,
	errclass.FailedPrecondition: wire.StatusFailedPrecondition,
	errclass.Gone:               wire.StatusFailedPrecondition,
	errclass.PreconditionFailed: wire.StatusFailedPrecondition,
	errclass.Unprocessable:      wire.StatusFailedPrecondition,
	errclass.PermissionDenied:   wire.StatusPermissionDenied,
	errclass.Unauthenticated:    wire.StatusUnauthenticated,
	errclass.ResourceExhausted:  wire.StatusResourceExhausted,
	errclass.TooLarge:           wire.StatusResourceExhausted,
	errclass.OutOfRange:         wire.StatusOutOfRange,
	errclass.Unimplemented:      wire.StatusUnimplemented,
	errclass.Unavailable:        wire.StatusUnavailable,
	errclass.Canceled:           wire.StatusCanceled,
	errclass.DataLoss:           wire.StatusDataLoss,
	errclass.Internal:           wire.StatusError,
	errclass.DeadlineExceeded:   wire.StatusDeadlineExceeded,
}

// This transport's own refusals declare their class here, beside the table
// that renders it, rather than in pkg/api/errclass, which cannot import this
// package.
var (
	_ errclass.Classed = (*ServiceNotConfiguredError)(nil)
	_ errclass.Classed = (*CeremonyCANotConfiguredError)(nil)
	_ errclass.Classed = (*InvalidArgumentError)(nil)
	_ errclass.Classed = (*UnmarshalError)(nil)
)

// ErrorClass reports that this server was built without the service.
func (e *ServiceNotConfiguredError) ErrorClass() errclass.Class { return errclass.Unimplemented }

// ErrorClass reports that no ceremony CA was configured.
func (e *CeremonyCANotConfiguredError) ErrorClass() errclass.Class { return errclass.Unimplemented }

// ErrorClass reports a malformed or incomplete frame payload: the caller's
// request, not a server fault.
func (e *InvalidArgumentError) ErrorClass() errclass.Class { return errclass.InvalidArgument }

// ErrorClass reports a request payload that does not decode. Its sibling
// MarshalError stays unclassified: a response this server could not encode IS
// a server fault.
func (e *UnmarshalError) ErrorClass() errclass.Class { return errclass.InvalidArgument }

// ServiceErrorStatus reports the wire status this transport answers for err.
//
// An error the classification table does not recognise gets one more chance:
// a PROJECTED capability module answers in gRPC's vocabulary rather than in
// qrdb's typed errors, so its status is translated through
// wire.StatusForGRPCCode. Consulting it only for an unclassified error means it
// can never shadow qrdb's own classification. A nil error is StatusOK.
//
// It is exported so the cross-transport parity test (pkg/api/errclass) can
// drive this transport's real answer.
func ServiceErrorStatus(err error) uint16 {
	if err == nil {
		return wire.StatusOK
	}
	class := errclass.Classify(err)
	if class == errclass.Unclassified {
		if st, ok := moduleStatus(err); ok {
			return st
		}
	}
	return wireStatusByClass[class]
}

// mapServiceError translates err into a wire status and the error frame body
// that carries it (errorFrame): err's message, and the delay it names, if any.
// Returns StatusOK with a nil payload when err is nil.
func mapServiceError(err error) (uint16, []byte) {
	if err == nil {
		return wire.StatusOK, nil
	}
	return errorFrame(ServiceErrorStatus(err), err)
}

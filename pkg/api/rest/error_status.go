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

package rest

import (
	"net/http"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
)

// httpStatusByClass is how this transport says each class of failure.
//
// It owns NO clauses about qrdb's conditions: what kind of failure an error
// is, is pkg/api/errclass's answer, and it is the same answer gRPC, the unix
// socket, MCP and QUIC render (go-qrdb#557). This table only decides which
// HTTP status carries each class. It is total over errclass.All —
// TestHTTPStatusByClass_IsTotal fails the build for a class without a row,
// which would otherwise render as status 0.
//
// Canceled and DataLoss have no HTTP status of their own and answer 500; the
// class, not the status, is what the other transports compare against.
var httpStatusByClass = map[errclass.Class]int{
	errclass.Unclassified:       http.StatusInternalServerError,
	errclass.InvalidArgument:    http.StatusBadRequest,
	errclass.NotFound:           http.StatusNotFound,
	errclass.AlreadyExists:      http.StatusConflict,
	errclass.Aborted:            http.StatusConflict,
	errclass.FailedPrecondition: http.StatusConflict,
	errclass.Gone:               http.StatusGone,
	errclass.PreconditionFailed: http.StatusPreconditionFailed,
	errclass.Unprocessable:      http.StatusUnprocessableEntity,
	errclass.PermissionDenied:   http.StatusForbidden,
	errclass.Unauthenticated:    http.StatusUnauthorized,
	errclass.ResourceExhausted:  http.StatusTooManyRequests,
	errclass.TooLarge:           http.StatusRequestEntityTooLarge,
	errclass.OutOfRange:         http.StatusRequestedRangeNotSatisfiable,
	errclass.Unimplemented:      http.StatusNotImplemented,
	errclass.Unavailable:        http.StatusServiceUnavailable,
	errclass.Canceled:           http.StatusInternalServerError,
	errclass.DataLoss:           http.StatusInternalServerError,
	errclass.Internal:           http.StatusInternalServerError,
	errclass.DeadlineExceeded:   http.StatusGatewayTimeout,
}

// This transport's own refusals declare their class here, beside the table
// that renders it, rather than in pkg/api/errclass, which cannot import this
// package. The assertions make a dropped method a compile error instead of a
// silent fall to 500.
var (
	_ errclass.Classed = (*ServiceNotImplementedError)(nil)
	_ errclass.Classed = (*RequestTooLargeError)(nil)
	_ errclass.Classed = (*PayloadTooLargeError)(nil)
	_ errclass.Classed = (*KeyTooLargeError)(nil)
	_ errclass.Classed = (*InvalidParameterError)(nil)
)

// ErrorClass reports that the service behind the route is not wired.
func (e *ServiceNotImplementedError) ErrorClass() errclass.Class { return errclass.Unimplemented }

// ErrorClass reports a request body over the endpoint's byte bound: the
// caller can act on it by sending less, so it is not a 400.
func (e *RequestTooLargeError) ErrorClass() errclass.Class { return errclass.TooLarge }

// ErrorClass reports a payload over its byte bound.
func (e *PayloadTooLargeError) ErrorClass() errclass.Class { return errclass.TooLarge }

// ErrorClass reports a key over its byte bound.
func (e *KeyTooLargeError) ErrorClass() errclass.Class { return errclass.TooLarge }

// ErrorClass reports a request parameter the handler could not parse: the
// caller's input is wrong, so it is a 400 wherever it is rendered.
func (e *InvalidParameterError) ErrorClass() errclass.Class { return errclass.InvalidArgument }

// ServiceErrorStatus reports the HTTP status this transport answers for err.
//
// It is the one mapping every REST handler uses, and it is exported so the
// cross-transport parity test (pkg/api/errclass) can drive this transport's
// real answer rather than a recording of it. A nil error is 200.
func ServiceErrorStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	return httpStatusByClass[errclass.Classify(err)]
}

// mapHTTPError maps an error to the HTTP status for its class; it is the name
// the handlers call. An error the classification table does not recognise
// answers 500.
func mapHTTPError(err error) int {
	return ServiceErrorStatus(err)
}

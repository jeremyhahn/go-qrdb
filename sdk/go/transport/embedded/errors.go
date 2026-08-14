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

package embedded

import (
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// NilServiceError is returned when a nil service is passed to New or
// when an operation requires a service that was not injected.
type NilServiceError struct {
	Service string
}

// Error returns a human-readable description.
func (e *NilServiceError) Error() string {
	return "embedded: " + e.Service + " must not be nil"
}

// UnknownMethodError is returned when Request is called with an
// unrecognized method name.
type UnknownMethodError struct {
	Method string
}

// Error returns a human-readable description.
func (e *UnknownMethodError) Error() string {
	return "embedded: unknown method: " + e.Method
}

// InvalidRequestError is returned when the request type does not match
// the expected type for a given method.
type InvalidRequestError struct {
	Method string
}

// Error returns a human-readable description.
func (e *InvalidRequestError) Error() string {
	return "embedded: invalid request type for method: " + e.Method
}

// NilRequestError is returned when a typed request pointer required by an
// embedded transport method is nil.
type NilRequestError struct {
	Method string
}

// Error returns a human-readable description.
func (e *NilRequestError) Error() string {
	return "embedded: " + e.Method + " request must not be nil"
}

// translateError converts service-layer errors to transport-layer errors
// so callers can use errors.As with transport.KeyNotFoundError,
// transport.NotLeaderError, etc.
func translateError(err error) error {
	if err == nil {
		return nil
	}

	var knf *service.KeyNotFoundError
	if errors.As(err, &knf) {
		return &transport.KeyNotFoundError{Key: knf.Key}
	}

	var nle *service.NotLeaderError
	if errors.As(err, &nle) {
		return &transport.NotLeaderError{
			ShardID:       nle.ShardID,
			LeaderID:      nle.LeaderID,
			LeaderAddress: nle.LeaderAddress,
		}
	}

	var rce *service.RevisionCompactedError
	if errors.As(err, &rce) {
		return &transport.RevisionCompactedError{Revision: rce.Requested}
	}

	var rnf *service.RevisionNotFoundError
	if errors.As(err, &rnf) {
		return &transport.RevisionNotFoundError{Revision: rnf.Revision}
	}

	var lnf *service.LeaseNotFoundError
	if errors.As(err, &lnf) {
		return &transport.LeaseNotFoundError{LeaseID: lnf.LeaseID}
	}

	var le *service.LeaseExpiredError
	if errors.As(err, &le) {
		return &transport.LeaseExpiredError{LeaseID: le.LeaseID}
	}

	var wce *service.WatchCanceledError
	if errors.As(err, &wce) {
		return &transport.WatchCanceledError{Reason: wce.Reason}
	}

	var ine *service.IndexNotFoundError
	if errors.As(err, &ine) {
		return &transport.IndexNotFoundError{Name: ine.Name}
	}

	var iee *service.IndexExistsError
	if errors.As(err, &iee) {
		return &transport.IndexExistsError{Name: iee.Name}
	}

	var vnf *service.VectorNotFoundError
	if errors.As(err, &vnf) {
		return &transport.VectorNotFoundError{Namespace: vnf.Namespace, ID: vnf.ID}
	}

	var nsnf *service.NamespaceNotFoundError
	if errors.As(err, &nsnf) {
		return &transport.NamespaceNotFoundError{Namespace: nsnf.Namespace}
	}

	var nse *service.NamespaceExistsError
	if errors.As(err, &nse) {
		return &transport.NamespaceExistsError{Namespace: nse.Namespace}
	}

	var dme *service.DimensionMismatchError
	if errors.As(err, &dme) {
		return &transport.DimensionMismatchError{Expected: dme.Expected, Got: dme.Actual}
	}

	return &transport.RequestError{Method: "embedded", Err: err}
}

// ClientCertRequiredError is returned when an operation that binds to the
// caller's mTLS identity — the Security Officer share claim — is invoked
// without a verified peer certificate on the context. The share claim fails
// closed rather than releasing quorum material to an unidentified caller.
type ClientCertRequiredError struct{}

// Error returns a human-readable description.
func (e *ClientCertRequiredError) Error() string {
	return "embedded: client certificate required for share claim"
}

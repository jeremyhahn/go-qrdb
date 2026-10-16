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

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// BarrierSealedError indicates that the encrypted-at-rest data plane is
// unavailable because its seal barrier is sealed: the transport's seal gate
// refuses a data-plane call with it, and a composer that cannot open its
// storage before the barrier is unsealed returns it. It always wraps
// seal.ErrSealed so callers can match via errors.Is.
type BarrierSealedError struct {
	Err error
}

// Error returns a human-readable description.
func (e *BarrierSealedError) Error() string {
	if e.Err == nil {
		return "embedded: data-plane barrier is sealed"
	}
	return "embedded: data-plane barrier is sealed: " + e.Err.Error()
}

// Unwrap returns the underlying seal error so errors.Is(err, seal.ErrSealed)
// matches.
func (e *BarrierSealedError) Unwrap() error {
	if e.Err == nil {
		return seal.ErrSealed
	}
	return e.Err
}

// As lets errors.As match a *transport.BarrierSealedError, so a caller tells
// a sealed node apart with transport.IsBarrierSealed on every transport,
// embedded included.
func (e *BarrierSealedError) As(target any) bool {
	sealed, ok := target.(**transport.BarrierSealedError)
	if !ok {
		return false
	}
	*sealed = &transport.BarrierSealedError{Message: e.Error()}
	return true
}

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

	// A capability refusal keeps its identity across the seam. Without this
	// case it falls through to a generic RequestError and the caller is left
	// matching message text (go-qrdb#141).
	var cue *service.CapabilityUnsupportedError
	if errors.As(err, &cue) {
		return &transport.CapabilityUnsupportedError{Message: cue.Message}
	}

	// An MVCC-only operation on a store that keeps no revision history is
	// the same "this node cannot do that" refusal; every network transport
	// classifies it as unimplemented, and so does this one.
	var mbr *service.MVCCBackendRequiredError
	if errors.As(err, &mbr) {
		return &transport.CapabilityUnsupportedError{Message: mbr.Error()}
	}

	// A node that cannot report its replica states is the same refusal: the
	// network transports carry all three as unimplemented.
	var rsu *service.ReplicaStateUnsupportedError
	var rru *service.ReplicaStateReadUnsupportedError
	var shu *service.StateMachineHashUnsupportedError
	if errors.As(err, &rsu) || errors.As(err, &rru) || errors.As(err, &shu) {
		return &transport.CapabilityUnsupportedError{Message: err.Error()}
	}
	// audit.VerificationUnsupportedError is the same kind of capability
	// refusal, for the AuditLogger backend rather than the KV backend
	// (go-qrdb#453): the configured logger holds no hash chain to verify.
	// Mapped to NotImplementedError so callers get the SAME typed error as
	// the network transports (grpc/unix map codes.Unimplemented, REST maps
	// 501) rather than having to type-assert on an internal pkg/audit type.
	var vue *audit.VerificationUnsupportedError
	if errors.As(err, &vue) {
		return &transport.NotImplementedError{Method: "VerifyChain"}
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

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
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// StartError is returned when the QUIC server fails to start listening.
type StartError struct {
	Addr string
	Err  error
}

// Error returns a human-readable description.
func (e *StartError) Error() string {
	return "quic: server failed to start on " + e.Addr + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *StartError) Unwrap() error {
	return e.Err
}

// AcceptError is returned when accepting a QUIC connection fails.
type AcceptError struct {
	Err error
}

// Error returns a human-readable description.
func (e *AcceptError) Error() string {
	return "quic: failed to accept connection: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *AcceptError) Unwrap() error {
	return e.Err
}

// StreamError is returned when accepting or handling a QUIC stream fails.
type StreamError struct {
	Err error
}

// Error returns a human-readable description.
func (e *StreamError) Error() string {
	return "quic: stream error: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *StreamError) Unwrap() error {
	return e.Err
}

// DispatchError is returned when a method cannot be dispatched.
type DispatchError struct {
	Method string
}

// Error returns a human-readable description.
func (e *DispatchError) Error() string {
	return "quic: unknown method: " + e.Method
}

// HandlerError is returned when a handler fails during execution.
type HandlerError struct {
	Method string
	Err    error
}

// Error returns a human-readable description.
func (e *HandlerError) Error() string {
	return "quic: handler error for " + e.Method + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *HandlerError) Unwrap() error {
	return e.Err
}

// ServiceNotConfiguredError is returned when a handler is invoked for a
// service that was not provided during server construction. Hint, when
// set, names the remedy (e.g. "requires mode: raft").
type ServiceNotConfiguredError struct {
	Service string
	Hint    string
}

// Error returns a human-readable description.
func (e *ServiceNotConfiguredError) Error() string {
	msg := "quic: " + e.Service + " service is not configured"
	if e.Hint != "" {
		msg += ": " + e.Hint
	}
	return msg
}

// CeremonyCANotConfiguredError is returned when CeremonyInitialize is
// invoked but the server was not configured with a CeremonyCA via
// WithCeremonyCA. The ceremony service dereferences the CA to issue
// certificates, so failing closed here avoids a nil-pointer panic.
type CeremonyCANotConfiguredError struct{}

// Error returns a human-readable description.
func (e *CeremonyCANotConfiguredError) Error() string {
	return "quic: ceremony CA is not configured"
}

// InvalidArgumentError is returned when a handler receives an invalid or
// incomplete request payload.
type InvalidArgumentError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *InvalidArgumentError) Error() string {
	return "quic: invalid argument: " + e.Reason
}

// ClientCertRequiredError is returned when a share-claim handler requires a
// verified mTLS client certificate but the connection carries none.
type ClientCertRequiredError struct{}

// Error returns a human-readable description.
func (e *ClientCertRequiredError) Error() string {
	return "quic: client certificate required for share claim"
}

// MarshalError is returned when protobuf marshaling fails.
type MarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *MarshalError) Error() string {
	return "quic: failed to marshal response: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MarshalError) Unwrap() error {
	return e.Err
}

// UnmarshalError is returned when protobuf unmarshaling fails.
type UnmarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *UnmarshalError) Error() string {
	return "quic: failed to unmarshal request: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *UnmarshalError) Unwrap() error {
	return e.Err
}

// ModuleMethodRequestTypeError is returned when a projected capability
// module's gRPC method Handler asks to decode into a value that is not a
// proto.Message. This is a construction-time bug in the module's generated
// gRPC code, not a caller error: every properly generated unary Handler
// always decodes into a *pb.XRequest, which is always a proto.Message.
type ModuleMethodRequestTypeError struct {
	Method string
}

// Error returns a human-readable description.
func (e *ModuleMethodRequestTypeError) Error() string {
	return "quic: projected method " + e.Method + ": decode target is not a proto.Message"
}

// ModuleMethodResponseTypeError is returned when a projected capability
// module's gRPC method returns a response that is not a proto.Message. Like
// ModuleMethodRequestTypeError, this reflects a bug in the module's
// generated gRPC code, not a caller error.
type ModuleMethodResponseTypeError struct {
	Method string
}

// Error returns a human-readable description.
func (e *ModuleMethodResponseTypeError) Error() string {
	return "quic: projected method " + e.Method + ": response is not a proto.Message"
}

// mapServiceError translates a service-layer error into a wire status code
// and error message payload. Returns StatusOK with nil payload when err is nil.
func mapServiceError(err error) (uint16, []byte) {
	if err == nil {
		return wire.StatusOK, nil
	}

	// Unimplemented, not Error. The node is healthy; it simply was not built
	// with this verb on the configured state machine (an absent opcode, or a
	// subsystem it was constructed without). Mapping it to the generic error
	// status made a healthy node declining a verb indistinguishable from a
	// server fault, so a QUIC client could not degrade gracefully the way its
	// gRPC twin could against the identical server-side condition — the
	// go-qrdb#141 split had reached gRPC and REST but never QUIC.
	//
	// Only this type, not service.SMNotConfiguredError: that sibling also
	// covers refusals decided on the request's merits (an invalid lease TTL, a
	// unique-index violation), which are client errors and must not be
	// reported as "this node cannot do that". The clause sits first to mirror
	// pkg/api/grpc.mapServiceError, whose first clause it is.
	var cue *service.CapabilityUnsupportedError
	if errors.As(err, &cue) {
		return wire.StatusUnimplemented, []byte(err.Error())
	}

	var knf *service.KeyNotFoundError
	if errors.As(err, &knf) {
		return wire.StatusError, []byte(err.Error())
	}

	var nle *service.NotLeaderError
	if errors.As(err, &nle) {
		return wire.StatusError, []byte(err.Error())
	}

	var snc *ServiceNotConfiguredError
	if errors.As(err, &snc) {
		return wire.StatusUnimplemented, []byte(err.Error())
	}

	var ccnc *CeremonyCANotConfiguredError
	if errors.As(err, &ccnc) {
		return wire.StatusUnimplemented, []byte(err.Error())
	}

	var tqee *tenant.QuotaExceededError
	if errors.As(err, &tqee) {
		return wire.StatusError, []byte(err.Error())
	}

	var infe *placement.InsufficientNodesError
	if errors.As(err, &infe) {
		// Cluster has no free node for the request — distinct from a quota
		// rejection, retryable once capacity changes.
		return wire.StatusUnavailable, []byte(err.Error())
	}

	var bse *service.BarrierSealedError
	if errors.As(err, &bse) {
		// A data-plane operation targeted a sealed barrier. Surface
		// Unavailable so the caller retries after the barrier is unsealed,
		// matching the gRPC and REST BarrierSealedError mapping.
		return wire.StatusUnavailable, []byte(err.Error())
	}

	if errors.Is(err, tenant.ErrTenantSealed) {
		// A barrier-isolated tenant's IAM (user/role) store resolved to a
		// SEALED barrier. The StoreResolver fails closed rather than leaking
		// the operation through the shared system store — surface Unavailable
		// so the caller retries after the tenant's barrier is unsealed,
		// matching the BarrierSealedError data-plane mapping.
		return wire.StatusUnavailable, []byte(err.Error())
	}

	var xt *tenant.CrossTenantDeniedError
	if errors.As(err, &xt) {
		// A normal tenant caller supplied a target tenant other than its own.
		// The IAM routing layer refuses to act on a client-supplied tenant —
		// surface PermissionDenied, matching the REST 403 / gRPC PermissionDenied
		// mapping across transports.
		return wire.StatusPermissionDenied, []byte(err.Error())
	}

	var cso *service.CompactSystemOnlyError
	if errors.As(err, &cso) {
		// A non-system tenant attempted a store-global MVCC compaction — the
		// service layer fails closed. PermissionDenied, matching the REST 403 /
		// gRPC PermissionDenied mapping and the ScopeCross authz gate.
		return wire.StatusPermissionDenied, []byte(err.Error())
	}

	var tade *database.TenantAccessDeniedError
	if errors.As(err, &tade) {
		// A caller targeted a per-database control operation (seal/unseal/
		// barrier-status, drop) on a database owned by a different tenant. The
		// service layer fails closed independently of transport RBAC.
		return wire.StatusPermissionDenied, []byte(err.Error())
	}

	if errors.Is(err, watch.ErrCompacted) {
		// A watch requested a start revision at or below the compacted floor
		// — the intervening history is gone.
		return wire.StatusError, []byte(err.Error())
	}

	if errors.Is(err, watch.ErrStartRevisionUnsupported) {
		// A watch requested a start revision against a store with no change
		// history attached (a non-MVCC store) — replay is impossible.
		return wire.StatusUnimplemented, []byte(err.Error())
	}

	var mbr *service.MVCCBackendRequiredError
	if errors.As(err, &mbr) {
		// The shard's state machine keeps no MVCC revision history (the
		// default raft.sm_type is "pebble", not "mvcc") — Unimplemented so
		// the caller learns the operation is unavailable on this deployment,
		// not merely that this one call failed. Matches the gRPC/REST mapping.
		return wire.StatusUnimplemented, []byte(err.Error())
	}

	var rciu *service.RestoreChangeIndexUnavailableError
	if errors.As(err, &rciu) {
		// A prefix-scoped restore needs the durable revision index and this
		// backend maintains none — same family as MVCCBackendRequiredError.
		return wire.StatusUnimplemented, []byte(err.Error())
	}

	var rike *service.RestoreInternalKeyError
	if errors.As(err, &rike) {
		// A restore scope reached the internal key namespace — refused as an
		// authority the caller does not have, not a malformed request.
		return wire.StatusPermissionDenied, []byte(err.Error())
	}

	return wire.StatusError, []byte(err.Error())
}

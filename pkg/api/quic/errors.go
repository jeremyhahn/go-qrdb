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
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
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
//
// The clause order and the status chosen for each condition MIRROR
// pkg/api/grpc.mapServiceError row for row, through the correspondence in
// status.go. Both files answer the same question about the same service-layer
// errors, and TestQUICAndGRPCAgreeOnEveryCondition asserts they give the same
// answer for every condition either one classifies, so the two cannot drift
// into telling a QUIC caller and a gRPC caller different things about one
// event.
//
// Before the vocabulary landed this function could only reach six statuses, so
// roughly sixty of the conditions below collapsed into the generic StatusError
// and a QUIC caller could not tell a missing user from a broken node. See the
// wire package's Status documentation for why the set is gRPC's.
func mapServiceError(err error) (uint16, []byte) {
	if err == nil {
		return wire.StatusOK, nil
	}

	// AUTHORIZATION FIRST, and via the shared classifier rather than a clause
	// per error type.
	//
	// The eleven tenant-barrier handlers gate on the auth middleware and
	// `return nil, err`, so their refusals arrive HERE, not at the method-level
	// gate in handleStream. Nothing below classified them: *AuditError,
	// *StoreUnavailableError and *RateLimitedError all fell through to
	// StatusError, and so did every RBAC denial from the tenant-scoped gate.
	// The docs claimed QUIC answered StatusUnavailable for a failed audit write
	// and it did not -- on precisely the handlers that claim was written about.
	//
	// Routing through authzStatus means REST, gRPC, MCP and QUIC all answer
	// from one middleware.ClassifyAuthError. Fixing the mapper in one place and
	// leaving eleven callers on the old path is the shape this defect already
	// took once (go-qrdb#335 review round 2, the AuthorizationError unwrap), so
	// it is fixed for the CALLERS here rather than for the one entry point that
	// happened to be under the microscope.
	//
	// StatusError stays the fallback for an unclassified error, so nothing this
	// package did not already recognise changes answer.
	if class := authmw.ClassifyAuthError(err); class != authmw.AuthFailureUnknown {
		return authzStatus(err), []byte(err.Error())
	}

	msg := []byte(err.Error())

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
		return wire.StatusUnimplemented, msg
	}

	// NotFound, not Error. A key that is absent is not a server fault, and
	// until the vocabulary existed the SDK client had to recover the
	// distinction by substring-matching "key not found" out of the message —
	// the fail-open classification go-qrdb#289 exists to delete.
	var knf *service.KeyNotFoundError
	if errors.As(err, &knf) {
		return wire.StatusNotFound, msg
	}

	// Must precede ReadError: LeaseService.TimeToLive's translateReadError no
	// longer wraps this cause (it is returned unwrapped, the same way
	// KeyNotFoundError above is), but this arm stays ahead of ReadError
	// anyway, matching gRPC, so a future caller that reintroduces the wrap
	// does not silently regress this transport back to StatusError
	// (go-qrdb#387).
	var lnf *service.LeaseNotFoundError
	if errors.As(err, &lnf) {
		return wire.StatusNotFound, msg
	}

	// The identity not-found family (go-qrdb#289). pkg/user.UserNotFoundError
	// is an ALIAS of transport.UserNotFoundError, so the type the service
	// returns and the type the SDK promises are one type; all this clause has
	// to do is stop flattening it into StatusError. The SDK's call-site
	// mappers rebuild it from the id THEY asked for, never from this message.
	var unf *transport.UserNotFoundError
	if errors.As(err, &unf) {
		return wire.StatusNotFound, msg
	}

	// CHECKED BEFORE EVERY IDENTITY ARM THAT COULD BE ITS CAUSE, as in REST
	// and gRPC. IndexCompensationError Unwraps to its cause, so a cause arm
	// listed above it would win and report "taken" or "retry" while dropping
	// the far more serious fact — that the rollback FAILED and a login index
	// is inconsistent. DataLoss is the honest status: secondary state may be
	// inconsistent and a blind client retry could compound it. The arm
	// exists here because the cert-binding conflict arm below is the first
	// cause of this wrapper this ladder classifies; before it every cause
	// fell to StatusError, which was uninformative but never a lie.
	var ice *user.IndexCompensationError
	if errors.As(err, &ice) {
		return wire.StatusDataLoss, msg
	}

	// The cert-binding pair, matching REST and gRPC (go-qrdb#518, #536). A
	// withdrawal naming a binding the account does not hold is NotFound on
	// the binding, as a missing user is NotFound on the user; a thumbprint
	// already bound to ANOTHER user is AlreadyExists, the answer every other
	// taken handle gets — the store refuses to rebind it because doing so
	// silently would let the caller take over that user's mTLS identity, and
	// a deliberate refusal is not StatusError.
	var cbnf *user.CertBindingNotFoundError
	if errors.As(err, &cbnf) {
		return wire.StatusNotFound, msg
	}
	var cbc *user.CertBindingConflictError
	if errors.As(err, &cbc) {
		return wire.StatusAlreadyExists, msg
	}

	// The taken-identity family: a username, an email login key, or a
	// store-generated user id (go-qrdb#555) already held by another record.
	// AlreadyExists, the answer REST (409) and gRPC give each of them; without
	// these clauses they fell through to the generic StatusError. All three
	// sit below the IndexCompensationError arm for the reason that arm
	// states: a failed rollback wrapping one of these collisions is DataLoss.
	var uee *user.UsernameExistsError
	if errors.As(err, &uee) {
		return wire.StatusAlreadyExists, msg
	}
	var eee *user.EmailExistsError
	if errors.As(err, &eee) {
		return wire.StatusAlreadyExists, msg
	}
	var uide *user.UserIDExistsError
	if errors.As(err, &uide) {
		return wire.StatusAlreadyExists, msg
	}

	var rnfe *role.RoleNotFoundError
	if errors.As(err, &rnfe) {
		// Deliberately the SAME status a missing user gets, matching gRPC and
		// REST. AssignRole can fail for either subject and the wire does not
		// separate them, which is why the SDK types neither — see
		// TestUserNotFound_AmbiguousMethods in sdk/go/conformance.
		return wire.StatusNotFound, msg
	}

	// Unavailable, not Error: a non-leader is a routing condition the caller
	// retries, exactly as gRPC has always reported it.
	var nle *service.NotLeaderError
	if errors.As(err, &nle) {
		return wire.StatusUnavailable, msg
	}

	// Must precede ProposeError: a request the protocol cannot encode was
	// never proposed, so it is the caller's input that is wrong and not the
	// cluster.
	var ere *service.EncodeRequestError
	if errors.As(err, &ere) {
		return wire.StatusInvalidArgument, msg
	}

	// The ceremony-time auth-factor tenant mismatch refusal (go-qrdb#464),
	// matching REST's 400 and gRPC's InvalidArgument: an EnrollFactor request
	// naming a tenant other than the caller's context is the caller's input
	// being wrong, not a server fault. Unclassified here it fell to the
	// generic wire.StatusError, indistinguishable from an internal failure —
	// exactly the ambiguity the surrounding vocabulary exists to remove.
	var tenantMismatch *service.AuthFactorTenantMismatchError
	if errors.As(err, &tenantMismatch) {
		return wire.StatusInvalidArgument, msg
	}

	// The ceremony-time auth-factor surface's own typed errors, classified
	// row for row as pkg/api/grpc.mapAuthFactorGRPCError classifies them.
	// Unclassified here, a missing or expired challenge reached a QUIC
	// caller as the generic wire.StatusError while every other transport
	// reported it as not-found or failed-precondition.
	if st, ok := authFactorStatus(err); ok {
		return st, msg
	}

	// Must precede ProposeError and ReadError: a call that met a closed store
	// arrives wrapped in one of them, and StatusError would tell the caller
	// the server broke when in fact the node is going away underneath it
	// (go-qrdb#371). This is the quic half of PR #435, which typed the
	// condition on embedded and classified it on gRPC, unix and REST and had
	// no status to classify it with here.
	if errors.Is(err, storage.ErrStorageClosed) {
		return wire.StatusUnavailable, msg
	}

	var pe *service.ProposeError
	if errors.As(err, &pe) {
		return wire.StatusError, msg
	}

	var re *service.ReadError
	if errors.As(err, &re) {
		return wire.StatusError, msg
	}

	var me *service.MembershipError
	if errors.As(err, &me) {
		return wire.StatusError, msg
	}

	var se *service.SnapshotRequestError
	if errors.As(err, &se) {
		return wire.StatusError, msg
	}

	var de *service.DrainError
	if errors.As(err, &de) {
		return wire.StatusUnavailable, msg
	}

	var rce *service.RevisionCompactedError
	if errors.As(err, &rce) {
		return wire.StatusOutOfRange, msg
	}

	var rnf *service.RevisionNotFoundError
	if errors.As(err, &rnf) {
		return wire.StatusNotFound, msg
	}

	var tce *service.TransactionConflictError
	if errors.As(err, &tce) {
		return wire.StatusAborted, msg
	}

	var wce *service.WatchCanceledError
	if errors.As(err, &wce) {
		return wire.StatusCanceled, msg
	}

	// The lease family (go-qrdb#169). Both not-found and expired answer
	// NotFound, matching gRPC exactly: neither protocol has an expired-specific
	// sibling of NotFound and REST's 404 does not either. The SDK's
	// mapLeaseAbsent tells the two apart at the call site, where the subject is
	// already known to be a lease. LeaseNotFoundError itself is classified
	// above, ahead of ReadError (go-qrdb#387).
	var lee *service.LeaseExpiredError
	if errors.As(err, &lee) {
		return wire.StatusNotFound, msg
	}

	var lexe *service.LeaseExistsError
	if errors.As(err, &lexe) {
		return wire.StatusAlreadyExists, msg
	}

	var be *service.BatchError
	if errors.As(err, &be) {
		return wire.StatusError, msg
	}

	var inf *service.IndexNotFoundError
	if errors.As(err, &inf) {
		return wire.StatusNotFound, msg
	}

	var iee *service.IndexExistsError
	if errors.As(err, &iee) {
		return wire.StatusAlreadyExists, msg
	}

	var tse *service.TimeSeriesError
	if errors.As(err, &tse) {
		return wire.StatusError, msg
	}

	var cse *service.ConsistencyError
	if errors.As(err, &cse) {
		return wire.StatusFailedPrecondition, msg
	}

	var vnf *service.VectorNotFoundError
	if errors.As(err, &vnf) {
		return wire.StatusNotFound, msg
	}

	var nnf *service.NamespaceNotFoundError
	if errors.As(err, &nnf) {
		return wire.StatusNotFound, msg
	}

	var nee *service.NamespaceExistsError
	if errors.As(err, &nee) {
		return wire.StatusAlreadyExists, msg
	}

	var dme *service.DimensionMismatchError
	if errors.As(err, &dme) {
		return wire.StatusInvalidArgument, msg
	}

	var ive *service.InvalidVectorError
	if errors.As(err, &ive) {
		return wire.StatusInvalidArgument, msg
	}

	var cae *service.CollectionAlreadyExistsError
	if errors.As(err, &cae) {
		return wire.StatusAlreadyExists, msg
	}

	var cnf *service.CollectionNotFoundError
	if errors.As(err, &cnf) {
		return wire.StatusNotFound, msg
	}

	var cen *service.CollectionEmptyNameError
	if errors.As(err, &cen) {
		return wire.StatusInvalidArgument, msg
	}

	var ced *service.CollectionEmptyDatabaseError
	if errors.As(err, &ced) {
		return wire.StatusInvalidArgument, msg
	}

	var cis *service.CollectionInvalidIDStrategyError
	if errors.As(err, &cis) {
		return wire.StatusInvalidArgument, msg
	}

	var cmf *service.CollectionMissingFieldHashError
	if errors.As(err, &cmf) {
		return wire.StatusInvalidArgument, msg
	}

	var cdnf *service.CollectionDocumentNotFoundError
	if errors.As(err, &cdnf) {
		return wire.StatusNotFound, msg
	}

	var cedc *service.CollectionEmptyDocumentError
	if errors.As(err, &cedc) {
		return wire.StatusInvalidArgument, msg
	}

	var cidi *service.CollectionInvalidDocumentIDError
	if errors.As(err, &cidi) {
		return wire.StatusInvalidArgument, msg
	}

	var cme *service.CollectionMetadataError
	if errors.As(err, &cme) {
		return wire.StatusError, msg
	}

	// -------------------------------------------------------------------
	// Bulk import guard errors.
	// -------------------------------------------------------------------

	var iif *service.ImportInvalidFormatError
	if errors.As(err, &iif) {
		return wire.StatusInvalidArgument, msg
	}

	var imk *service.ImportMissingKeyFieldError
	if errors.As(err, &imk) {
		return wire.StatusInvalidArgument, msg
	}

	var ivm *service.ImportInvalidValueModeError
	if errors.As(err, &ivm) {
		return wire.StatusInvalidArgument, msg
	}

	var imv *service.ImportMissingValueFieldError
	if errors.As(err, &imv) {
		return wire.StatusInvalidArgument, msg
	}

	var ipe *service.ImportParseError
	if errors.As(err, &ipe) {
		return wire.StatusInvalidArgument, msg
	}

	var iae *service.ImportArchiveError
	if errors.As(err, &iae) {
		return wire.StatusInvalidArgument, msg
	}

	var ita *service.ImportTransactionAbortedError
	if errors.As(err, &ita) {
		return wire.StatusAborted, msg
	}

	var iite *service.ImportInputTooLargeError
	if errors.As(err, &iite) {
		return wire.StatusResourceExhausted, msg
	}

	var iaete *service.ImportArchiveExpandedTooLargeError
	if errors.As(err, &iaete) {
		return wire.StatusResourceExhausted, msg
	}

	var itme *service.ImportTooManyEntriesError
	if errors.As(err, &itme) {
		return wire.StatusInvalidArgument, msg
	}

	var itmr *service.ImportTooManyRowsError
	if errors.As(err, &itmr) {
		return wire.StatusInvalidArgument, msg
	}

	var iptl *service.ImportProposalTooLargeError
	if errors.As(err, &iptl) {
		return wire.StatusResourceExhausted, msg
	}

	var snc *ServiceNotConfiguredError
	if errors.As(err, &snc) {
		return wire.StatusUnimplemented, msg
	}

	var ccnc *CeremonyCANotConfiguredError
	if errors.As(err, &ccnc) {
		return wire.StatusUnimplemented, msg
	}

	// InvalidArgumentError is this package's own guard for a malformed or
	// incomplete frame payload. It answered StatusError for want of anywhere
	// better, which told a caller its own bad request was a server fault.
	var iae2 *InvalidArgumentError
	if errors.As(err, &iae2) {
		return wire.StatusInvalidArgument, msg
	}

	// A share-claim frame with no verified client certificate is refused as an
	// authority the caller does not have.
	var ccr *ClientCertRequiredError
	if errors.As(err, &ccr) {
		return wire.StatusPermissionDenied, msg
	}

	// A malformed request payload is the caller's, not the server's. Its
	// sibling MarshalError stays StatusError: a response this server could not
	// encode IS a server fault.
	var ue *UnmarshalError
	if errors.As(err, &ue) {
		return wire.StatusInvalidArgument, msg
	}

	var tnf *service.TenantNotFoundError
	if errors.As(err, &tnf) {
		return wire.StatusNotFound, msg
	}

	var dnf *service.DatabaseNotFoundError
	if errors.As(err, &dnf) {
		return wire.StatusNotFound, msg
	}

	var bse *service.BarrierSealedError
	if errors.As(err, &bse) {
		// A data-plane operation targeted a sealed barrier. Surface
		// Unavailable so the caller retries after the barrier is unsealed,
		// matching the gRPC and REST BarrierSealedError mapping.
		return wire.StatusUnavailable, msg
	}

	var rte *tenant.ReservedTenantIDError
	if errors.As(err, &rte) {
		// A reserved id is not the caller's to name, on create or on delete.
		return wire.StatusInvalidArgument, msg
	}

	var tde *tenant.TenantDeletedError
	if errors.As(err, &tde) {
		// A deleted tenant id is retired permanently: the id is taken, in
		// exactly the sense an already-existing tenant's id is taken. Mirrors
		// the gRPC AlreadyExists and REST 409 mappings.
		return wire.StatusAlreadyExists, msg
	}

	var thd *tenant.TenantHasDatabasesError
	if errors.As(err, &thd) {
		// The tenant still owns databases: a precondition the caller can
		// satisfy by dropping the databases the error names.
		return wire.StatusFailedPrecondition, msg
	}

	if errors.Is(err, tenant.ErrTenantSealed) || errors.Is(err, seal.ErrSealed) {
		// A barrier-isolated tenant's IAM (user/role) store resolved to a
		// SEALED barrier. The StoreResolver fails closed rather than leaking
		// the operation through the shared system store — surface Unavailable
		// so the caller retries after the tenant's barrier is unsealed,
		// matching the BarrierSealedError data-plane mapping.
		// tenant.ErrTenantSealed is what Resolve returns when the barrier is
		// ALREADY sealed; seal.ErrSealed is what the resolved store's own
		// Get/Put return when the barrier seals AFTER Resolve succeeded but
		// BEFORE the store operation runs (go-qrdb#464 round 6) — a live
		// per-call check, not a cached decision. Mirrors
		// pkg/api/grpc.mapServiceError's identical addition, per this
		// function's own row-for-row doc comment.
		return wire.StatusUnavailable, msg
	}

	var lhe *lock.LockHeldError
	if errors.As(err, &lhe) {
		return wire.StatusAborted, msg
	}

	var nlh *lock.NotLockHolderError
	if errors.As(err, &nlh) {
		return wire.StatusPermissionDenied, msg
	}

	var lnfe *lock.LockNotFoundError
	if errors.As(err, &lnfe) {
		return wire.StatusNotFound, msg
	}

	// A THROTTLED caller, not a denied one.
	//
	// This condition is why the rate-limit feature's own documentation carried
	// the line "no separate numeric status for this class of error today": the
	// authorization gate had to answer it with whatever status was nearest,
	// and every candidate in a six-status vocabulary said something false.
	// PermissionDenied is terminal in every client and would tell a caller to
	// fix grants that were never the problem; Unavailable at least carries the
	// right instruction (retry) but loses the reason and points an operator at
	// the node's health.
	//
	// MERGE NOTE for the audit/authz classifier work in flight: that change
	// routes authorization refusals through one middleware.ClassifyAuthError
	// table, and its table sends the rate-limited class to StatusUnavailable
	// with a NAMED PROTOCOL EXCLUSION saying the wire declares no rate-limit
	// status. That exclusion is no longer true. When the two meet, the class
	// table is the right home for this clause and the row it should carry is
	// wire.StatusResourceExhausted — merging the two without changing that row
	// would put a throttled caller back on Unavailable, which is the state
	// this clause exists to leave.
	var rle *authmw.RateLimitedError
	if errors.As(err, &rle) {
		return wire.StatusResourceExhausted, msg
	}

	var tqee *tenant.QuotaExceededError
	if errors.As(err, &tqee) {
		// Tenant-wide replica-usage reservation lost a concurrency race —
		// ResourceExhausted, matching gRPC's quota family. It answered the
		// generic StatusError here, which a client cannot tell from a fault.
		return wire.StatusResourceExhausted, msg
	}

	var infe *placement.InsufficientNodesError
	if errors.As(err, &infe) {
		// Cluster has no free node for the request — distinct from a quota
		// rejection, retryable once capacity changes.
		return wire.StatusUnavailable, msg
	}

	var rfe *service.ReplicaFloorError
	if errors.As(err, &rfe) {
		return wire.StatusInvalidArgument, msg
	}

	var ndre *service.NotDedicatedRaftDatabaseError
	if errors.As(err, &ndre) {
		return wire.StatusInvalidArgument, msg
	}

	var utoe *service.UnsupportedTransactionOpError
	if errors.As(err, &utoe) {
		return wire.StatusInvalidArgument, msg
	}

	var xt *tenant.CrossTenantDeniedError
	if errors.As(err, &xt) {
		// A normal tenant caller supplied a target tenant other than its own.
		// The IAM routing layer refuses to act on a client-supplied tenant —
		// surface PermissionDenied, matching the REST 403 / gRPC PermissionDenied
		// mapping across transports.
		return wire.StatusPermissionDenied, msg
	}

	var cso *service.CompactSystemOnlyError
	if errors.As(err, &cso) {
		// A non-system tenant attempted a store-global MVCC compaction — the
		// service layer fails closed. PermissionDenied, matching the REST 403 /
		// gRPC PermissionDenied mapping and the ScopeCross authz gate.
		return wire.StatusPermissionDenied, msg
	}

	var tade *database.TenantAccessDeniedError
	if errors.As(err, &tade) {
		// A caller targeted a per-database control operation (seal/unseal/
		// barrier-status, drop) on a database owned by a different tenant. The
		// service layer fails closed independently of transport RBAC.
		return wire.StatusPermissionDenied, msg
	}

	if errors.Is(err, watch.ErrCompacted) {
		// A watch requested a start revision at or below the compacted floor
		// — the intervening history is gone. OutOfRange names exactly that,
		// where StatusError named nothing.
		return wire.StatusOutOfRange, msg
	}

	if errors.Is(err, watch.ErrStartRevisionUnsupported) {
		// A watch requested a start revision against a store with no change
		// history attached (a non-MVCC store) — replay is impossible.
		return wire.StatusUnimplemented, msg
	}

	var mbr *service.MVCCBackendRequiredError
	if errors.As(err, &mbr) {
		// The shard's state machine keeps no MVCC revision history (the
		// default raft.sm_type is "pebble", not "mvcc") — Unimplemented so
		// the caller learns the operation is unavailable on this deployment,
		// not merely that this one call failed. Matches the gRPC/REST mapping.
		return wire.StatusUnimplemented, msg
	}

	var rciu *service.RestoreChangeIndexUnavailableError
	if errors.As(err, &rciu) {
		// A prefix-scoped restore needs the durable revision index and this
		// backend maintains none — same family as MVCCBackendRequiredError.
		return wire.StatusUnimplemented, msg
	}

	var rike *service.RestoreInternalKeyError
	if errors.As(err, &rike) {
		// A restore scope reached the internal key namespace — refused as an
		// authority the caller does not have, not a malformed request.
		return wire.StatusPermissionDenied, msg
	}

	if st, ok := restoreGuardStatus(err); ok {
		return st, msg
	}

	if st, ok := pinStatus(err); ok {
		return st, msg
	}

	// LAST, and only for an error that carries a gRPC status: a PROJECTED
	// capability module answers in gRPC's vocabulary, never this one. Placing
	// it after every typed clause means it can only ever classify what nothing
	// above recognised, so it cannot shadow qrdb's own errors.
	if st, ok := moduleStatus(err); ok {
		return st, msg
	}

	return wire.StatusError, msg
}

// restoreGuardStatus classifies the selective point-in-time restore guards.
//
// They are split out because they are a closed family that answers exactly two
// statuses and would otherwise add eight near-identical clauses to the middle
// of mapServiceError. The mapping matches pkg/api/grpc row for row.
func restoreGuardStatus(err error) (uint16, bool) {
	var rnre *service.RestoreNilRequestError
	if errors.As(err, &rnre) {
		return wire.StatusInvalidArgument, true
	}

	var rrie *service.RestoreRevisionInvalidError
	if errors.As(err, &rrie) {
		return wire.StatusInvalidArgument, true
	}

	var rrfe *service.RestoreRevisionFutureError
	if errors.As(err, &rrfe) {
		return wire.StatusInvalidArgument, true
	}

	var rsee *service.RestoreScopeEmptyError
	if errors.As(err, &rsee) {
		return wire.StatusInvalidArgument, true
	}

	var rsae *service.RestoreScopeAmbiguousError
	if errors.As(err, &rsae) {
		return wire.StatusInvalidArgument, true
	}

	var reke *service.RestoreEmptyKeyError
	if errors.As(err, &reke) {
		return wire.StatusInvalidArgument, true
	}

	var rstl *service.RestoreScopeTooLargeError
	if errors.As(err, &rstl) {
		return wire.StatusInvalidArgument, true
	}

	var rupe *service.RestoreUnboundedPrefixError
	if errors.As(err, &rupe) {
		return wire.StatusInvalidArgument, true
	}

	var rae *service.RestoreApplyError
	if errors.As(err, &rae) {
		return wire.StatusError, true
	}

	return 0, false
}

// pinStatusBySentinel is the PIN gate's status table, the twin of
// pkg/api/grpc.pinErrorCodes and mapped through the same correspondence.
//
// A map keyed by the sentinel, not a switch: the sentinels are values, the
// dispatch is a lookup, and a PIN error added to pkg/pin without a row here is
// a missing key rather than a case someone forgot to write.
//
// Before this existed the QUIC barrier-PIN handlers had no mapping at all, so
// a locked-out PIN, a too-short PIN and a corrupted PIN state file were the
// same frame as a disk fault — on the one surface where telling them apart
// decides whether an operator waits or restores from backup.
var pinStatusBySentinel = map[error]uint16{
	pin.ErrPINNotSet:         wire.StatusFailedPrecondition,
	pin.ErrPINLocked:         wire.StatusResourceExhausted,
	pin.ErrPINInvalid:        wire.StatusUnauthenticated,
	pin.ErrSOPINRequired:     wire.StatusUnauthenticated,
	pin.ErrPINTooShort:       wire.StatusInvalidArgument,
	pin.ErrPINAlreadySet:     wire.StatusAlreadyExists,
	pin.ErrInvalidCurrentPIN: wire.StatusUnauthenticated,
	pin.ErrStateCorrupted:    wire.StatusDataLoss,
	pin.ErrStrategyNotSet:    wire.StatusFailedPrecondition,
}

// pinStatus classifies a PIN backend error. The second return is false for
// anything pinStatusBySentinel does not name, which leaves the caller's
// fallback in charge.
//
// Iterating a map is safe here because the sentinels are mutually exclusive:
// each is a distinct errors.New value and no error in pkg/pin wraps two, so
// the nondeterministic iteration order cannot change the answer.
func pinStatus(err error) (uint16, bool) {
	for sentinel, st := range pinStatusBySentinel {
		if errors.Is(err, sentinel) {
			return st, true
		}
	}
	return 0, false
}

// authFactorStatusRules classifies pkg/authfactors's error types. It mirrors
// pkg/api/grpc.mapAuthFactorGRPCError rule for rule, in the same order, so
// one condition reaches a QUIC caller and a gRPC caller as the same class;
// TestMapServiceError_EveryCondition holds the two to that against the live
// gRPC classifier.
var authFactorStatusRules = []struct {
	matches func(error) bool
	status  uint16
}{
	{isErrorType[*authfactors.EnrollmentNotFoundError], wire.StatusNotFound},
	{isErrorType[*authfactors.FactorNotFoundError], wire.StatusNotFound},
	{isErrorType[*authfactors.EnrollmentInvalidError], wire.StatusInvalidArgument},
	{isErrorType[*authfactors.EnrollmentRevokedError], wire.StatusFailedPrecondition},
	{isErrorType[*authfactors.ChallengeExpiredError], wire.StatusFailedPrecondition},
	{isErrorType[*authfactors.VerificationFailedError], wire.StatusUnauthenticated},
	{isErrorType[*authfactors.HardwareUnavailableError], wire.StatusUnavailable},
	{func(err error) bool { return errors.Is(err, authfactors.ErrFactorDisabled) }, wire.StatusUnimplemented},
}

// authFactorStatus returns the status the first matching
// authFactorStatusRules row assigns err, and whether any row matched.
func authFactorStatus(err error) (uint16, bool) {
	for _, rule := range authFactorStatusRules {
		if rule.matches(err) {
			return rule.status, true
		}
	}
	return 0, false
}

// isErrorType reports whether err's chain holds an error of type T.
func isErrorType[T error](err error) bool {
	_, ok := errors.AsType[T](err)
	return ok
}

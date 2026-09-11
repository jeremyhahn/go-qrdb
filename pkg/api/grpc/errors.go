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
	"crypto/tls"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// serviceErrorClassifiers are this package's per-family error mappers, in the
// order ServiceErrorCode consults them.
//
// There are nine of them because a gRPC SERVICE knows its own family and calls
// exactly one. Each is total — every one ends in codes.Internal — and Internal
// is what each of them means by "I do not recognise this", not by "this is a
// server fault I have identified".
//
// The order is a tiebreak for an error two families both classify. No error in
// TestServiceErrorCode_NoFamilyDisagrees is claimed twice with different codes,
// and that test is what would catch one appearing.
var serviceErrorClassifiers = []func(error) error{
	// The data plane first: it is the classifier every transport already runs
	// on every RPC, and the one whose errors.Is clauses (storage.ErrStorageClosed,
	// watch.ErrCompacted) can match through another family's wrapper.
	mapServiceError,
	mapAuthServiceError,
	mapTenantError,
	mapCredentialError,
	mapPINError,
	mapAuthFactorGRPCError,
	mapUserAuthFactorGRPCError,
	mapBarrierCeremonyError,
	mapTenantBarrierCeremonyError,
}

// ServiceErrorCode reports the gRPC status code this package's classifiers
// assign to err, without building a status error around it.
//
// It exists so a transport whose wire is NOT gRPC can reuse this
// classification instead of growing a ladder of its own. The MCP transport
// does exactly that: pkg/api/mcp renders the code returned here into its own
// JSON-RPC vocabulary through a seventeen-row correspondence table, and owns
// no condition-to-class clauses at all (go-qrdb#438).
//
// That is deliberate, and it is the answer to go-qrdb#462. This repository
// already carries TWO ~80-clause classifiers that must agree and are not
// forced to (this package's and pkg/api/quic's). A third would have compounded
// the problem; a renderer over this one cannot drift from it, because there is
// nothing in it to drift. Whether the classification eventually moves to a
// transport-neutral home is #462's call, and this function is deliberately a
// thin, side-effect-free view of the mappers so that move stays cheap.
//
// # Why it consults every family and a gRPC service consults one
//
// A gRPC service method belongs to one family and calls that family's mapper.
// MCP classifies in a tool-handler middleware that is family-agnostic by
// design — it has to cover a module's tools too, and a module has no family
// here at all — so it asks all of them and takes the FIRST that classifies.
//
// "Classifies" means "answers something other than codes.Internal", because
// Internal is every one of these mappers' default branch: the answer they give
// to an error they do not recognise. Preferring a real classification over
// that is the correct resolution, and it is the only one available to a caller
// that cannot know which family an error came from.
//
// A nil error classifies as codes.OK.
func ServiceErrorCode(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	for _, classify := range serviceErrorClassifiers {
		if code := status.Code(classify(err)); code != codes.Internal {
			return code
		}
	}
	return codes.Internal
}

// mapServiceError translates service-layer errors into gRPC status errors
// with appropriate codes. Unknown errors are mapped to codes.Internal.
func mapServiceError(err error) error {
	if err == nil {
		return nil
	}

	// Unimplemented, not Internal. The node is healthy; it simply was not
	// built with this verb on the configured state machine. Internal means
	// "the server broke", which makes a client library alert and retry an
	// operation that can never succeed (go-qrdb#141, the go-qrdb#52 pattern).
	var cue *service.CapabilityUnsupportedError
	if errors.As(err, &cue) {
		return status.Error(codes.Unimplemented, err.Error())
	}

	var knf *service.KeyNotFoundError
	if errors.As(err, &knf) {
		return status.Error(codes.NotFound, err.Error())
	}

	var nle *service.NotLeaderError
	if errors.As(err, &nle) {
		return status.Error(codes.Unavailable, err.Error())
	}

	// Must precede ProposeError: a request the protocol cannot encode was never
	// proposed, so it is the caller's input that is wrong and not the cluster.
	// Reported as Internal it fires an operational alert and invites a retry of
	// an input that can never succeed (go-qrdb#52).
	var ere *service.EncodeRequestError
	if errors.As(err, &ere) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	// Must precede ProposeError and ReadError: a call that met a closed store
	// arrives wrapped in one of them, and Internal would tell the caller the
	// server broke when in fact the node is going away underneath it.
	// Unavailable is the retryable classification a client library already
	// knows how to act on (go-qrdb#371).
	//
	// The typed value does not cross the wire — a status code is all gRPC
	// carries — so a remote caller reads the code, while an in-process caller
	// on the embedded transport still matches storage.ErrStorageClosed.
	if errors.Is(err, storage.ErrStorageClosed) {
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

	// AlreadyExists, the mirror of the NotFound above: an insert refused
	// because the document it would have written over is already stored.
	var cde *service.CollectionDocumentExistsError
	if errors.As(err, &cde) {
		return status.Error(codes.AlreadyExists, err.Error())
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

	var iif *service.ImportInvalidFormatError
	if errors.As(err, &iif) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var imk *service.ImportMissingKeyFieldError
	if errors.As(err, &imk) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var ivm *service.ImportInvalidValueModeError
	if errors.As(err, &ivm) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var imv *service.ImportMissingValueFieldError
	if errors.As(err, &imv) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var ipe *service.ImportParseError
	if errors.As(err, &ipe) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var iae *service.ImportArchiveError
	if errors.As(err, &iae) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var ita *service.ImportTransactionAbortedError
	if errors.As(err, &ita) {
		return status.Error(codes.Aborted, err.Error())
	}

	var iite *service.ImportInputTooLargeError
	if errors.As(err, &iite) {
		return status.Error(codes.ResourceExhausted, err.Error())
	}

	var iaete *service.ImportArchiveExpandedTooLargeError
	if errors.As(err, &iaete) {
		return status.Error(codes.ResourceExhausted, err.Error())
	}

	var itme *service.ImportTooManyEntriesError
	if errors.As(err, &itme) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var itmr *service.ImportTooManyRowsError
	if errors.As(err, &itmr) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var ipte *service.ImportProposalTooLargeError
	if errors.As(err, &ipte) {
		return status.Error(codes.ResourceExhausted, err.Error())
	}

	var snc *ServiceNotConfiguredError
	if errors.As(err, &snc) {
		return status.Error(codes.Unimplemented, err.Error())
	}

	var ccnc *CeremonyCANotConfiguredError
	if errors.As(err, &ccnc) {
		return status.Error(codes.Unimplemented, err.Error())
	}

	var tnf *service.TenantNotFoundError
	if errors.As(err, &tnf) {
		return status.Error(codes.NotFound, err.Error())
	}

	var dnf *service.DatabaseNotFoundError
	if errors.As(err, &dnf) {
		return status.Error(codes.NotFound, err.Error())
	}

	var bse *service.BarrierSealedError
	if errors.As(err, &bse) {
		return status.Error(codes.Unavailable, err.Error())
	}

	if errors.Is(err, tenant.ErrTenantSealed) {
		// A barrier-isolated tenant's IAM (user/role) store resolved to a
		// SEALED barrier. The StoreResolver fails closed rather than leaking
		// the operation through the shared system store — surface Unavailable
		// so the caller retries after the tenant's barrier is unsealed,
		// matching the BarrierSealedError data-plane mapping.
		return status.Error(codes.Unavailable, err.Error())
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

	var tqee *tenant.QuotaExceededError
	if errors.As(err, &tqee) {
		// Tenant-wide replica-usage reservation lost a concurrency race —
		// same status family as quota.ExceededError.
		return status.Error(codes.ResourceExhausted, err.Error())
	}

	var infe *placement.InsufficientNodesError
	if errors.As(err, &infe) {
		// Tenant is within its own quota but the cluster has no free node —
		// distinct from quota-exceeded, retryable once capacity changes.
		return status.Error(codes.Unavailable, err.Error())
	}

	var rfe *service.ReplicaFloorError
	if errors.As(err, &rfe) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var ndre *service.NotDedicatedRaftDatabaseError
	if errors.As(err, &ndre) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var utoe *service.UnsupportedTransactionOpError
	if errors.As(err, &utoe) {
		// A transaction branch (success/failure ops) contained a read
		// (Get/Range) or other unsupported op type — malformed request.
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var cso *service.CompactSystemOnlyError
	if errors.As(err, &cso) {
		// A non-system tenant attempted a store-global MVCC compaction — the
		// service layer fails closed. PermissionDenied, matching the REST 403
		// and the ScopeCross authz gate on the mvcc:compact permission.
		return status.Error(codes.PermissionDenied, err.Error())
	}

	// -------------------------------------------------------------------
	// Selective point-in-time restore (RestoreToRevision) guard errors.
	// -------------------------------------------------------------------

	var mbr *service.MVCCBackendRequiredError
	if errors.As(err, &mbr) {
		// The shard's state machine keeps no MVCC revision history (the
		// default raft.sm_type is "pebble", not "mvcc") — Unimplemented so
		// the caller learns the operation is unavailable on this deployment,
		// not merely that this one call failed.
		return status.Error(codes.Unimplemented, err.Error())
	}

	var rike *service.RestoreInternalKeyError
	if errors.As(err, &rike) {
		// A restore scope reached the internal key namespace — refused as an
		// authority the caller does not have, not a malformed request.
		return status.Error(codes.PermissionDenied, err.Error())
	}

	var rciu *service.RestoreChangeIndexUnavailableError
	if errors.As(err, &rciu) {
		// A prefix-scoped restore needs the durable revision index and this
		// backend maintains none — same family as MVCCBackendRequiredError.
		return status.Error(codes.Unimplemented, err.Error())
	}

	var rnre *service.RestoreNilRequestError
	if errors.As(err, &rnre) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var rrie *service.RestoreRevisionInvalidError
	if errors.As(err, &rrie) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var rrfe *service.RestoreRevisionFutureError
	if errors.As(err, &rrfe) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var rsee *service.RestoreScopeEmptyError
	if errors.As(err, &rsee) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var rsae *service.RestoreScopeAmbiguousError
	if errors.As(err, &rsae) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var reke *service.RestoreEmptyKeyError
	if errors.As(err, &reke) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var rste *service.RestoreScopeTooLargeError
	if errors.As(err, &rste) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var rupe *service.RestoreUnboundedPrefixError
	if errors.As(err, &rupe) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var rae *service.RestoreApplyError
	if errors.As(err, &rae) {
		// A write in an already-validated plan failed part way through.
		// Applied reports how many changes committed; re-issuing the
		// identical request is safe (see RestoreApplyError doc comment).
		return status.Error(codes.Internal, err.Error())
	}

	var tade *database.TenantAccessDeniedError
	if errors.As(err, &tade) {
		// A caller targeted a per-database control operation (seal/unseal/
		// barrier-status, drop) on a database owned by a different tenant. The
		// service layer fails closed independently of transport RBAC.
		return status.Error(codes.PermissionDenied, err.Error())
	}

	if errors.Is(err, watch.ErrCompacted) {
		// A watch requested a start revision at or below the compacted floor
		// — the intervening history is gone. Same status family as
		// service.RevisionCompactedError.
		return status.Error(codes.OutOfRange, err.Error())
	}

	if errors.Is(err, watch.ErrStartRevisionUnsupported) {
		// A watch requested a start revision against a store with no change
		// history attached (a non-MVCC store) — replay is impossible.
		return status.Error(codes.Unimplemented, err.Error())
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
// for a service that was not provided during server construction. Hint,
// when set, names the remedy (e.g. "requires mode: raft").
type ServiceNotConfiguredError struct {
	Service string
	Hint    string
}

// Error returns a human-readable description.
func (e *ServiceNotConfiguredError) Error() string {
	msg := "grpc: " + e.Service + " service is not configured"
	if e.Hint != "" {
		msg += ": " + e.Hint
	}
	return msg
}

// CeremonyCANotConfiguredError is returned when CeremonyInitialize is
// invoked but the server was not configured with a CeremonyCA (WithCeremonyCA
// for the TCP gRPC transport, WithRegisterCeremonyCA for the Unix domain
// socket transport). The ceremony service dereferences the CA to issue
// certificates, so failing closed here avoids a nil-pointer panic.
type CeremonyCANotConfiguredError struct{}

// Error returns a human-readable description.
func (e *CeremonyCANotConfiguredError) Error() string {
	return "grpc: ceremony CA is not configured"
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

// TLSNotConfiguredError is returned by Start when no TLS configuration was
// supplied via WithTLS. The gRPC transport is TLS-only — the mTLS client
// certificate is the identity the auth middleware authenticates against — so
// Start fails closed rather than bind an unauthenticated plaintext listener.
// The orchestrator (pkg/server) auto-provisions a certificate from the node's
// CeremonyCA, so a normal deployment supplies a config and never triggers this.
type TLSNotConfiguredError struct{}

// Error returns a human-readable description.
func (e *TLSNotConfiguredError) Error() string {
	return "grpc: no TLS configured; the gRPC transport is TLS-only, call WithTLS with a TLS 1.3+ config"
}

// WeakTLSVersionError is returned by Start when WithTLS was given a
// *tls.Config whose MinVersion is explicitly set below TLS 1.3. TLS 1.3 is
// the minimum version this codebase supports for the client-certificate
// identity model the auth middleware relies on.
type WeakTLSVersionError struct {
	MinVersion uint16
}

// Error returns a human-readable description.
func (e *WeakTLSVersionError) Error() string {
	return "grpc: TLS MinVersion " + tlsVersionName(e.MinVersion) + " is below the required TLS 1.3"
}

// tlsVersionName renders a crypto/tls version constant (or 0 for "unset")
// as a human-readable string for error messages.
func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	case 0:
		return "unset"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}

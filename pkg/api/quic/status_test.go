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

// What the QUIC wire vocabulary is, and what every condition answers on it
// (go-qrdb#437).
//
// No build constraint: these name only mapServiceError, moduleStatus,
// wireStatusByGRPCCode, pkg/api/grpc's ServiceErrorCode and the service-layer
// error types, all unconstrained.

package quic

import (
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// The vocabulary
// ---------------------------------------------------------------------------

// allGRPCCodes is gRPC's closed set, written out.
//
// It is a literal and not a range over 0..16, because the property under test
// is that the correspondence covers the set gRPC ACTUALLY defines. A range
// would keep passing if gRPC grew an eighteenth code, which is the case this
// test is meant to catch.
var allGRPCCodes = []codes.Code{
	codes.OK, codes.Canceled, codes.Unknown, codes.InvalidArgument,
	codes.DeadlineExceeded, codes.NotFound, codes.AlreadyExists,
	codes.PermissionDenied, codes.ResourceExhausted, codes.FailedPrecondition,
	codes.Aborted, codes.OutOfRange, codes.Unimplemented, codes.Internal,
	codes.Unavailable, codes.DataLoss, codes.Unauthenticated,
}

// TestWireStatusByGRPCCode_IsABijection is the claim the vocabulary decision
// rests on: this wire can express every condition gRPC can, and no two of
// gRPC's conditions collapse into one status here.
//
// It matters because the QUIC server PROJECTS gRPC services onto its frames
// (modules.go). A code with no row degrades a module's answer to StatusError,
// and two codes sharing a row make two different module answers the same
// frame — both of which are the defect go-qrdb#437 exists to remove, arriving
// through the one path that cannot be fixed by adding a clause to a handler.
func TestWireStatusByGRPCCode_IsABijection(t *testing.T) {
	if len(wireStatusByGRPCCode) != len(allGRPCCodes) {
		t.Errorf("correspondence has %d rows for %d gRPC codes",
			len(wireStatusByGRPCCode), len(allGRPCCodes))
	}

	seen := make(map[uint16]codes.Code, len(wireStatusByGRPCCode))
	for _, code := range allGRPCCodes {
		st, ok := wireStatusByGRPCCode[code]
		if !ok {
			t.Errorf("codes.%s has no wire status: a module answering it would "+
				"reach a QUIC caller as the generic StatusError", code)
			continue
		}
		if !wire.StatusDeclared(st) {
			t.Errorf("codes.%s maps to status %d, which the wire package does not declare", code, st)
		}
		if prev, dup := seen[st]; dup {
			t.Errorf("codes.%s and codes.%s both map to %s: two conditions "+
				"sharing one status cannot be told apart by a caller",
				prev, code, wire.StatusName(st))
		}
		seen[st] = code
	}
}

// TestWireStatusByGRPCCode_CoversEveryDeclaredStatus is the bijection's other
// direction: a status the correspondence never produces is a status no
// projected module can ever reach, which would make it dead vocabulary.
func TestWireStatusByGRPCCode_CoversEveryDeclaredStatus(t *testing.T) {
	produced := make(map[uint16]bool, len(wireStatusByGRPCCode))
	for _, st := range wireStatusByGRPCCode {
		produced[st] = true
	}
	for st := uint16(0); wire.StatusDeclared(st); st++ {
		if !produced[st] {
			t.Errorf("%s (%d) is declared but no gRPC code maps to it", wire.StatusName(st), st)
		}
	}
}

// ---------------------------------------------------------------------------
// The module projection bridge
// ---------------------------------------------------------------------------

// TestModuleStatus_TranslatesEveryGRPCCode walks the whole vocabulary through
// the bridge a projected capability module's answer actually travels.
func TestModuleStatus_TranslatesEveryGRPCCode(t *testing.T) {
	for _, code := range allGRPCCodes {
		if code == codes.OK {
			// status.Error(codes.OK, ...) returns a nil error, so there is
			// nothing for the bridge to classify; mapServiceError's nil clause
			// owns that case and TestMapServiceError_NilIsOK pins it.
			continue
		}
		t.Run(code.String(), func(t *testing.T) {
			got, ok := moduleStatus(status.Error(code, "from a module"))
			if !ok {
				t.Fatalf("codes.%s was not recognised as a gRPC status", code)
			}
			if want := wireStatusByGRPCCode[code]; got != want {
				t.Errorf("status = %s, want %s", wire.StatusName(got), wire.StatusName(want))
			}
		})
	}
}

// TestModuleStatus_IgnoresOrdinaryErrors is the guard that keeps the bridge
// from shadowing qrdb's own classification.
//
// status.FromError reports false AND hands back a synthesised codes.Unknown
// status for any error carrying no gRPC status. Reading the code without
// checking the boolean would map every unclassified error in the process to
// StatusUnknown, and every typed service error would reach the bridge before
// mapServiceError's clauses had a chance to answer.
func TestModuleStatus_IgnoresOrdinaryErrors(t *testing.T) {
	for _, err := range []error{
		errors.New("an ordinary error"),
		&service.KeyNotFoundError{Key: "k"},
		&transport.UserNotFoundError{ID: 7},
	} {
		if st, ok := moduleStatus(err); ok {
			t.Errorf("%T was treated as a gRPC status and mapped to %s",
				err, wire.StatusName(st))
		}
	}
}

// TestModuleStatus_ExplicitUnknownIsNotStatusError: a module that says "I could
// not classify this" is making a different claim from this server declining to
// classify, and the two have separate statuses.
func TestModuleStatus_ExplicitUnknownIsNotStatusError(t *testing.T) {
	got, ok := moduleStatus(status.Error(codes.Unknown, "the module could not say"))
	if !ok {
		t.Fatal("an explicit codes.Unknown carries a gRPC status")
	}
	if got != wire.StatusUnknown {
		t.Fatalf("status = %s, want StatusUnknown", wire.StatusName(got))
	}
	if got == wire.StatusError {
		t.Fatal("StatusUnknown and StatusError are different claims and must not share a number")
	}
}

// ---------------------------------------------------------------------------
// Every condition, and the gRPC code it agrees with
// ---------------------------------------------------------------------------

// conditionRow is one service-layer condition, the wire status it must answer,
// and the gRPC code pkg/api/grpc answers for the SAME error value.
//
// grpcCode is not decoration. It is the parity claim: this transport and the
// gRPC transport must not tell two callers different things about one event,
// and writing the code beside each row makes a divergence visible in a diff
// rather than only in a consumer's bug report. The row asserts
// mapServiceError(err) == wireStatusByGRPCCode[grpcCode], so the wire status
// is never written twice and can never be "corrected" on one side alone.
//
// The gap this comment used to state is closed (go-qrdb#462). It said the
// table could not CALL pkg/api/grpc's mapServiceError because it was
// unexported and in another package, so a change to the gRPC mapper that this
// table was not updated alongside would go unnoticed. pkg/api/grpc now exports
// ServiceErrorCode for exactly this purpose, and the row asserts against it
// LIVE: grpcCode is no longer a recorded answer that can drift from the mapper
// it claims to mirror, it is an assertion about what that mapper returns
// today. pkg/api/grpc does not import pkg/api/quic, so there is no cycle, and
// pkg/api/mcp already reaches the classifier the same way.
//
// Still open, and still an architecture change rather than this table's to
// make: the two mappers remain two implementations that must agree, instead of
// one shared classifier. What is different is that disagreement is now caught
// here instead of by a consumer.
type conditionRow struct {
	name     string
	err      error
	want     uint16
	grpcCode codes.Code
}

// transportLocalConditions names the rows whose error TYPE is declared in this
// package and has no counterpart VALUE on the gRPC side: a malformed frame, a
// payload that will not decode, a service this transport was not configured
// with. gRPC answers the same CONDITION through its own types, so grpcCode
// stays the parity claim and stays worth recording — but it cannot be checked
// by running this error value through gRPC's classifiers, because that value
// never reaches them.
//
// Keyed by row name, with the reason, rather than by a fifth struct field: the
// rows are unkeyed composite literals, so a new field would have to be spelled
// out on all fifty of them to mark five.
//
// This set is deliberately small and is not a waiver. An entry says "gRPC
// cannot be asked about this VALUE", never "these two transports need not
// agree". If a type here ever gains a gRPC counterpart, delete the entry.
var transportLocalConditions = map[string]string{
	"frame payload malformed":         "InvalidArgumentError is a framing fault; gRPC's codec rejects the equivalent before a handler runs",
	"request payload will not decode": "UnmarshalError wraps this transport's own proto decode",
	"service not configured":          "ServiceNotConfiguredError is this server's wiring check",
	"ceremony CA not configured":      "CeremonyCANotConfiguredError is this server's wiring check",
	"share claim needs a client cert": "ClientCertRequiredError is raised from the QUIC peer-certificate path",
}

// grpcAnswer reports the code the gRPC stack answers for err, resolved in the
// ORDER the gRPC stack resolves it. Getting the order wrong is how this check
// manufactures failures: pkg/api/grpc's ServiceErrorCode is the classifier for
// SERVICE-layer conditions only, and its default branch is codes.Internal, so
// feeding it an error from another family reports a divergence that does not
// exist.
//
//   - A module answers in gRPC's own vocabulary — it implements its gRPC and
//     QUIC surfaces over the same code and has never seen this envelope — so a
//     carried status is taken first and verbatim. pkg/api/mcp's
//     classifyToolError says the same thing for the same reason.
//   - An auth-middleware failure is converted by authmw.GRPCError, through
//     grpcCodeByClass. Rate limiting is the live example: pkg/api/grpc
//     contains no RateLimited clause at all, and answers ResourceExhausted
//     entirely through that table.
//   - Everything else is a service-layer condition, which is what
//     ServiceErrorCode exists to classify.
func grpcAnswer(err error) codes.Code {
	if st, ok := status.FromError(err); ok {
		return st.Code()
	}
	if c := status.Code(authmw.GRPCError(err, codes.Internal)); c != codes.Internal {
		return c
	}
	return grpcapi.ServiceErrorCode(err)
}

// conditions covers every wire status at least twice, and every condition the
// four pieces of work that escalated to go-qrdb#437 were blocked on.
var conditions = []conditionRow{
	// --- go-qrdb#289: a missing user -----------------------------------
	{"user missing by id", &transport.UserNotFoundError{ID: 999999}, wire.StatusNotFound, codes.NotFound},
	{"user missing by lookup", &transport.UserNotFoundError{Lookup: "system/nope"}, wire.StatusNotFound, codes.NotFound},
	{"role missing", &role.RoleNotFoundError{ID: "no-such-role"}, wire.StatusNotFound, codes.NotFound},

	// --- go-qrdb#518 / #536: the cert-binding pair ----------------------
	{"cert binding not held", &user.CertBindingNotFoundError{UserID: 7, Thumbprint: "AAAA"}, wire.StatusNotFound, codes.NotFound},
	{"cert binding held by another user", &user.CertBindingConflictError{Thumbprint: "AAAA", BoundUserID: 7, TargetUserID: 9}, wire.StatusAlreadyExists, codes.AlreadyExists},
	// The wrapper outranks its cause: a conflict whose rollback FAILED is a
	// data-loss condition, not a conflict, and must not be downgraded by the
	// arm above matching first.
	{"cert binding conflict whose rollback failed", &user.IndexCompensationError{
		Op: "update", Cause: &user.CertBindingConflictError{Thumbprint: "AAAA", BoundUserID: 7, TargetUserID: 9},
	}, wire.StatusDataLoss, codes.DataLoss},

	// --- go-qrdb#169: the lease family ---------------------------------
	{"lease missing", &service.LeaseNotFoundError{LeaseID: 42}, wire.StatusNotFound, codes.NotFound},
	{"lease expired", &service.LeaseExpiredError{LeaseID: 42}, wire.StatusNotFound, codes.NotFound},
	{"lease exists", &service.LeaseExistsError{LeaseID: 42}, wire.StatusAlreadyExists, codes.AlreadyExists},

	// --- go-qrdb#387: the shape a pre-fix LeaseService.TimeToLive actually
	// produced -- LeaseNotFoundError wrapped in ReadError, not the bare
	// sentinel above. The ReadError arm must not win this race, matching
	// the closed-store pair's own reasoning two rows down.
	{"lease missing under a read", &service.ReadError{Err: &service.LeaseNotFoundError{LeaseID: 42}}, wire.StatusNotFound, codes.NotFound},

	// --- go-qrdb#371 / PR #435: the closed store -----------------------
	{"store closed", storage.ErrStorageClosed, wire.StatusUnavailable, codes.Unavailable},
	{"store closed under a propose", &service.ProposeError{Err: storage.ErrStorageClosed}, wire.StatusUnavailable, codes.Unavailable},
	{"store closed under a read", &service.ReadError{Err: storage.ErrStorageClosed}, wire.StatusUnavailable, codes.Unavailable},
	// The shape the backend actually produces, not a hand-held sentinel: the
	// storage layer wraps its closed error in an OpError and the service layer
	// wraps that again, so a clause matching only a bare sentinel would be
	// inert in production. This is the same construction pkg/api/grpc's own
	// closed-store test uses.
	{"store closed, as the backend wraps it", &service.ProposeError{
		Err: &storage.OpError{Op: "set", Err: &storage.ClosedError{}},
	}, wire.StatusUnavailable, codes.Unavailable},

	// --- the rate-limit family PR #443 recorded as a named exclusion ---
	{"tenant quota exceeded", &tenant.QuotaExceededError{TenantID: "t", Resource: "replicas"}, wire.StatusResourceExhausted, codes.ResourceExhausted},
	{"import input too large", &service.ImportInputTooLargeError{Max: 1}, wire.StatusResourceExhausted, codes.ResourceExhausted},
	{"PIN locked out", pin.ErrPINLocked, wire.StatusResourceExhausted, codes.ResourceExhausted},
	{"caller throttled", &authmw.RateLimitedError{
		Permission: permission.KVPut, Subject: "a", TenantID: "t", RetryAfter: time.Second,
	}, wire.StatusResourceExhausted, codes.ResourceExhausted},

	// --- absence, beyond the identity family ---------------------------
	{"key missing", &service.KeyNotFoundError{Key: "k"}, wire.StatusNotFound, codes.NotFound},
	{"revision missing", &service.RevisionNotFoundError{Revision: 3}, wire.StatusNotFound, codes.NotFound},
	{"index missing", &service.IndexNotFoundError{Name: "ix"}, wire.StatusNotFound, codes.NotFound},
	{"vector missing", &service.VectorNotFoundError{Namespace: "ns", ID: "v"}, wire.StatusNotFound, codes.NotFound},
	{"namespace missing", &service.NamespaceNotFoundError{Namespace: "ns"}, wire.StatusNotFound, codes.NotFound},
	{"collection missing", &service.CollectionNotFoundError{Database: "d", Collection: "c"}, wire.StatusNotFound, codes.NotFound},
	{"document missing", &service.CollectionDocumentNotFoundError{Database: "d", Collection: "c", ID: 1}, wire.StatusNotFound, codes.NotFound},
	{"tenant missing", &service.TenantNotFoundError{TenantID: "t"}, wire.StatusNotFound, codes.NotFound},
	{"database missing", &service.DatabaseNotFoundError{TenantID: "t", Database: "d"}, wire.StatusNotFound, codes.NotFound},
	{"lock missing", &lock.LockNotFoundError{Key: "k"}, wire.StatusNotFound, codes.NotFound},

	// --- collisions ----------------------------------------------------
	{"index exists", &service.IndexExistsError{Name: "ix"}, wire.StatusAlreadyExists, codes.AlreadyExists},
	{"namespace exists", &service.NamespaceExistsError{Namespace: "ns"}, wire.StatusAlreadyExists, codes.AlreadyExists},
	{"collection exists", &service.CollectionAlreadyExistsError{Database: "d", Collection: "c"}, wire.StatusAlreadyExists, codes.AlreadyExists},
	{"PIN already set", pin.ErrPINAlreadySet, wire.StatusAlreadyExists, codes.AlreadyExists},

	// --- the caller's own request --------------------------------------
	{"request will not encode", &service.EncodeRequestError{Err: errors.New("key too long")}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"vector dimension mismatch", &service.DimensionMismatchError{Expected: 3, Actual: 4}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"collection name empty", &service.CollectionEmptyNameError{}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"restore request nil", &service.RestoreNilRequestError{}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"frame payload malformed", &InvalidArgumentError{Reason: "missing id"}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"PIN too short", pin.ErrPINTooShort, wire.StatusInvalidArgument, codes.InvalidArgument},

	// --- state, not merits ---------------------------------------------
	{"consistency unmet", &service.ConsistencyError{Level: "linearizable", Reason: "no quorum"}, wire.StatusFailedPrecondition, codes.FailedPrecondition},
	{"PIN not set", pin.ErrPINNotSet, wire.StatusFailedPrecondition, codes.FailedPrecondition},
	{"PIN strategy unset", pin.ErrStrategyNotSet, wire.StatusFailedPrecondition, codes.FailedPrecondition},

	// --- concurrency ---------------------------------------------------
	{"transaction conflict", &service.TransactionConflictError{Key: "k"}, wire.StatusAborted, codes.Aborted},
	{"lock held elsewhere", &lock.LockHeldError{Key: "k", HolderID: "other"}, wire.StatusAborted, codes.Aborted},
	{"import transaction aborted", &service.ImportTransactionAbortedError{Row: 2, Key: "k"}, wire.StatusAborted, codes.Aborted},

	// --- history --------------------------------------------------------
	{"revision compacted away", &service.RevisionCompactedError{Requested: 1, Compacted: 9}, wire.StatusOutOfRange, codes.OutOfRange},
	{"watch start below the floor", watch.ErrCompacted, wire.StatusOutOfRange, codes.OutOfRange},

	// --- stopped work ----------------------------------------------------
	{"watch canceled", &service.WatchCanceledError{Reason: "shutdown"}, wire.StatusCanceled, codes.Canceled},

	// --- corruption -------------------------------------------------------
	{"PIN state corrupted", pin.ErrStateCorrupted, wire.StatusDataLoss, codes.DataLoss},

	// --- retryable node conditions ---------------------------------------
	{"not the leader", &service.NotLeaderError{ShardID: 1, LeaderID: 2}, wire.StatusUnavailable, codes.Unavailable},
	{"barrier sealed", &service.BarrierSealedError{TenantID: "t"}, wire.StatusUnavailable, codes.Unavailable},
	{"tenant barrier sealed", tenant.ErrTenantSealed, wire.StatusUnavailable, codes.Unavailable},
	{"cluster has no free node", &placement.InsufficientNodesError{}, wire.StatusUnavailable, codes.Unavailable},

	// --- authority --------------------------------------------------------
	{"not the lock holder", &lock.NotLockHolderError{Key: "k", ExpectedToken: 1}, wire.StatusPermissionDenied, codes.PermissionDenied},
	{"compaction is system only", &service.CompactSystemOnlyError{TenantID: "t"}, wire.StatusPermissionDenied, codes.PermissionDenied},
	{"database owned by another tenant", &database.TenantAccessDeniedError{}, wire.StatusPermissionDenied, codes.PermissionDenied},
	{"restore reached an internal key", &service.RestoreInternalKeyError{}, wire.StatusPermissionDenied, codes.PermissionDenied},

	// --- identity ----------------------------------------------------------
	{"PIN invalid", pin.ErrPINInvalid, wire.StatusUnauthenticated, codes.Unauthenticated},
	{"SO PIN required", pin.ErrSOPINRequired, wire.StatusUnauthenticated, codes.Unauthenticated},

	// --- the node does not carry the verb ----------------------------------
	{"capability unsupported", &service.CapabilityUnsupportedError{Message: "no opcode"}, wire.StatusUnimplemented, codes.Unimplemented},
	// The standalone gate's refusal (go-qrdb#473). It used to be a REST-only
	// type and fell through to StatusError here.
	{"raft-only capability under standalone", &service.CapabilityUnsupportedError{Message: "watch: not implemented on this server: requires mode: raft"}, wire.StatusUnimplemented, codes.Unimplemented},
	{"service not configured", &ServiceNotConfiguredError{Service: "lease"}, wire.StatusUnimplemented, codes.Unimplemented},
	{"MVCC backend required", &service.MVCCBackendRequiredError{Err: errors.New("pebble")}, wire.StatusUnimplemented, codes.Unimplemented},
	{"watch replay unsupported", watch.ErrStartRevisionUnsupported, wire.StatusUnimplemented, codes.Unimplemented},

	// --- the rest of the ported table -------------------------------------
	//
	// Every remaining condition pkg/api/quic classifies, so no clause in the
	// mapper is asserted only by the coverage number. They are grouped last
	// rather than interleaved because none of them is what any of the four
	// escalated pieces of work was blocked on; they are the conditions that
	// were collapsing into StatusError alongside those four.
	{"membership change failed", &service.MembershipError{Op: "add", Err: errors.New("raft")}, wire.StatusError, codes.Internal},
	{"snapshot request failed", &service.SnapshotRequestError{Err: errors.New("io")}, wire.StatusError, codes.Internal},
	{"batch failed", &service.BatchError{Index: 2, Err: errors.New("io")}, wire.StatusError, codes.Internal},
	{"time series failed", &service.TimeSeriesError{Op: "append", Err: errors.New("io")}, wire.StatusError, codes.Internal},
	{"collection metadata failed", &service.CollectionMetadataError{Op: "read", Err: errors.New("io")}, wire.StatusError, codes.Internal},
	{"node draining", &service.DrainError{Err: errors.New("draining")}, wire.StatusUnavailable, codes.Unavailable},
	{"vector invalid", &service.InvalidVectorError{Reason: "NaN"}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"collection database empty", &service.CollectionEmptyDatabaseError{}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"collection id strategy invalid", &service.CollectionInvalidIDStrategyError{Strategy: "x"}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"collection field hash missing", &service.CollectionMissingFieldHashError{}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"collection document empty", &service.CollectionEmptyDocumentError{}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"collection document id invalid", &service.CollectionInvalidDocumentIDError{}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"import format invalid", &service.ImportInvalidFormatError{Format: "xls"}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"import key field missing", &service.ImportMissingKeyFieldError{}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"import value mode invalid", &service.ImportInvalidValueModeError{Mode: "x"}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"import value field missing", &service.ImportMissingValueFieldError{}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"import parse failed", &service.ImportParseError{Err: errors.New("row 3")}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"import archive unreadable", &service.ImportArchiveError{Err: errors.New("zip")}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"import entry count over the bound", &service.ImportTooManyEntriesError{Max: 10}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"import row count over the bound", &service.ImportTooManyRowsError{Max: 10}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"import archive expands too far", &service.ImportArchiveExpandedTooLargeError{Max: 10}, wire.StatusResourceExhausted, codes.ResourceExhausted},
	{"import proposal too large", &service.ImportProposalTooLargeError{Max: 10}, wire.StatusResourceExhausted, codes.ResourceExhausted},
	{"replica count below the floor", &service.ReplicaFloorError{Database: "d", Requested: 0}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"database is not dedicated raft", &service.NotDedicatedRaftDatabaseError{Database: "d"}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"transaction op unsupported", &service.UnsupportedTransactionOpError{}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"restore revision invalid", &service.RestoreRevisionInvalidError{Revision: -1}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"restore scope ambiguous", &service.RestoreScopeAmbiguousError{Prefix: "p", KeyCount: 2}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"restore key empty", &service.RestoreEmptyKeyError{Index: 1}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"restore scope too large", &service.RestoreScopeTooLargeError{Limit: 10, Scope: "prefix"}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"restore prefix unbounded", &service.RestoreUnboundedPrefixError{Prefix: ""}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"ceremony CA not configured", &CeremonyCANotConfiguredError{}, wire.StatusUnimplemented, codes.Unimplemented},
	{"share claim needs a client cert", &ClientCertRequiredError{}, wire.StatusPermissionDenied, codes.PermissionDenied},
	{"request payload will not decode", &UnmarshalError{Err: errors.New("proto")}, wire.StatusInvalidArgument, codes.InvalidArgument},
	{"tenant supplied is not the caller's", &tenant.CrossTenantDeniedError{
		IdentityTenantID: "a", RequestedTenantID: "b", Op: "users.list",
	}, wire.StatusPermissionDenied, codes.PermissionDenied},

	// A PROJECTED capability module's answer, which arrives in gRPC's
	// vocabulary and reaches the wire through the bridge in status.go. This is
	// the row that makes the module path part of the same table as everything
	// else rather than a separate concern.
	{"a module's not-found", status.Error(codes.NotFound, "module: widget 7"), wire.StatusNotFound, codes.NotFound},
	{"a module's deadline", status.Error(codes.DeadlineExceeded, "module: timed out"), wire.StatusDeadlineExceeded, codes.DeadlineExceeded},

	// --- the server broke ---------------------------------------------------
	{"propose failed", &service.ProposeError{Err: errors.New("raft is down")}, wire.StatusError, codes.Internal},
	{"read failed", &service.ReadError{Err: errors.New("disk")}, wire.StatusError, codes.Internal},
	{"restore apply failed", &service.RestoreApplyError{Key: "k", Err: errors.New("disk")}, wire.StatusError, codes.Internal},
}

// TestMapServiceError_EveryCondition walks the table.
func TestMapServiceError_EveryCondition(t *testing.T) {
	for _, row := range conditions {
		t.Run(row.name, func(t *testing.T) {
			got, payload := mapServiceError(row.err)
			if got != row.want {
				t.Errorf("status = %s, want %s", wire.StatusName(got), wire.StatusName(row.want))
			}
			if want := wireStatusByGRPCCode[row.grpcCode]; got != want {
				t.Errorf("status %s disagrees with codes.%s, which this condition "+
					"answers on the gRPC transport (%s). One event must not reach two "+
					"callers as two different conditions",
					wire.StatusName(got), row.grpcCode, wire.StatusName(want))
			}
			// The parity claim, made against the live gRPC stack rather than
			// against the code recorded beside the row. Without this, a change
			// to pkg/api/grpc's mapper leaves this table asserting agreement
			// with an answer that transport no longer gives.
			if _, local := transportLocalConditions[row.name]; !local {
				if live := grpcAnswer(row.err); live != row.grpcCode {
					t.Errorf("the gRPC stack answers codes.%s for this condition, but the "+
						"row records codes.%s. gRPC moved and this table did not: either "+
						"the row is stale or the transports have genuinely diverged",
						live, row.grpcCode)
				}
			}
			if len(payload) == 0 {
				t.Error("the payload must carry the server's reason")
			}
		})
	}
}

// TestTransportLocalConditions_IsHonest keeps the exclusion list from becoming
// a silent waiver, which is the failure mode of every hand-maintained skip set.
//
// Two claims, and the second is the one that matters. First, every name in the
// set must match a row — a typo, or a row renamed without touching the set,
// would otherwise exclude nothing while reading as though it excluded
// something. Second, every excluded row must ACTUALLY be unclassifiable by the
// gRPC stack: grpcAnswer must fall through to codes.Internal. That is what
// makes the entry necessary rather than convenient, and it is self-repairing —
// if one of these types ever gains a gRPC counterpart, this test fails and
// tells you to delete the entry instead of leaving a row permanently unchecked.
func TestTransportLocalConditions_IsHonest(t *testing.T) {
	byName := make(map[string]conditionRow, len(conditions))
	for _, row := range conditions {
		byName[row.name] = row
	}
	for name, reason := range transportLocalConditions {
		row, ok := byName[name]
		if !ok {
			t.Errorf("%q is excluded from the live parity check but matches no row: "+
				"the set excludes nothing and reads as though it excludes something", name)
			continue
		}
		if reason == "" {
			t.Errorf("%q is excluded with no reason recorded", name)
		}
		if live := grpcAnswer(row.err); live != codes.Internal {
			t.Errorf("%q is excluded from the live parity check, but the gRPC stack "+
				"classifies it as codes.%s — it is no longer transport-local, so "+
				"delete the entry and let the row be checked", name, live)
		}
	}
}

// TestMapServiceError_TableCoversEveryStatus proves the vocabulary has no dead
// arms: every status the wire declares is produced by at least one condition
// above, or by the module bridge for the two that only a projected module can
// reach.
func TestMapServiceError_TableCoversEveryStatus(t *testing.T) {
	// Reachable only through a projected capability module, by design: qrdb's
	// own handlers produce neither. They are declared so the bridge in
	// status.go is total — see the wire package's Status documentation.
	moduleOnly := map[uint16]bool{
		wire.StatusOK:               true, // the nil clause, not a condition
		wire.StatusDeadlineExceeded: true,
		wire.StatusUnknown:          true,
	}

	produced := make(map[uint16]bool, len(conditions))
	for _, row := range conditions {
		st, _ := mapServiceError(row.err)
		produced[st] = true
	}
	for st := uint16(0); wire.StatusDeclared(st); st++ {
		if produced[st] || moduleOnly[st] {
			continue
		}
		t.Errorf("%s (%d) is declared but no condition in this table produces it: "+
			"a status nothing emits is vocabulary a reader cannot trust",
			wire.StatusName(st), st)
	}
}

// TestMapServiceError_NilIsOK and the unclassified fallback.
func TestMapServiceError_NilIsOK(t *testing.T) {
	st, payload := mapServiceError(nil)
	if st != wire.StatusOK {
		t.Errorf("status = %s, want StatusOK", wire.StatusName(st))
	}
	if payload != nil {
		t.Errorf("payload = %q, want nil", payload)
	}
}

// TestMapServiceError_UnclassifiedStaysStatusError: the vocabulary grew, the
// fallback did not move. An error this package does not recognise answers
// exactly what it answered before go-qrdb#437.
func TestMapServiceError_UnclassifiedStaysStatusError(t *testing.T) {
	for _, err := range []error{
		errors.New("some service failure"),
		&MarshalError{Err: errors.New("proto")},
		&HandlerError{Method: "kv.Put", Err: errors.New("boom")},
	} {
		st, payload := mapServiceError(err)
		if st != wire.StatusError {
			t.Errorf("%T: status = %s, want StatusError", err, wire.StatusName(st))
		}
		if len(payload) == 0 {
			t.Errorf("%T: the payload must carry the reason", err)
		}
	}
}

// TestPinStatus_CoversEverySentinel: a PIN error pkg/pin adds without a row in
// pinStatusBySentinel falls through to StatusError, which is the state QUIC's
// barrier-PIN handlers were in before go-qrdb#437 — a locked-out PIN and a
// disk fault as the same frame.
func TestPinStatus_CoversEverySentinel(t *testing.T) {
	sentinels := []error{
		pin.ErrPINNotSet, pin.ErrPINLocked, pin.ErrPINInvalid,
		pin.ErrSOPINRequired, pin.ErrPINTooShort, pin.ErrPINAlreadySet,
		pin.ErrInvalidCurrentPIN, pin.ErrStateCorrupted, pin.ErrStrategyNotSet,
	}
	if len(pinStatusBySentinel) != len(sentinels) {
		t.Errorf("pinStatusBySentinel has %d rows for %d sentinels",
			len(pinStatusBySentinel), len(sentinels))
	}
	for _, sentinel := range sentinels {
		st, ok := pinStatus(sentinel)
		if !ok {
			t.Errorf("%v has no status", sentinel)
			continue
		}
		if st == wire.StatusError {
			t.Errorf("%v classified as the generic StatusError", sentinel)
		}
	}
}

// TestPinStatus_ClassifiesThroughAWrapper: the handlers return the backend's
// error wrapped, so a mapping that only matched a bare sentinel would be inert
// in production.
func TestPinStatus_ClassifiesThroughAWrapper(t *testing.T) {
	wrapped := &HandlerError{Method: "barrier.Unseal", Err: pin.ErrPINLocked}
	st, payload := mapServiceError(wrapped)
	if st != wire.StatusResourceExhausted {
		t.Fatalf("status = %s, want StatusResourceExhausted", wire.StatusName(st))
	}
	if len(payload) == 0 {
		t.Error("the payload must carry the reason")
	}
}

// TestPinStatus_IgnoresUnrelatedErrors.
func TestPinStatus_IgnoresUnrelatedErrors(t *testing.T) {
	if st, ok := pinStatus(errors.New("not a PIN error")); ok {
		t.Fatalf("classified as %s", wire.StatusName(st))
	}
	if st, ok := pinStatus(nil); ok {
		t.Fatalf("nil classified as %s", wire.StatusName(st))
	}
}

// TestRestoreGuardStatus_IgnoresUnrelatedErrors keeps the split-out family
// from claiming errors it does not own.
func TestRestoreGuardStatus_IgnoresUnrelatedErrors(t *testing.T) {
	if st, ok := restoreGuardStatus(errors.New("unrelated")); ok {
		t.Fatalf("classified as %s", wire.StatusName(st))
	}
	if st, ok := restoreGuardStatus(&service.KeyNotFoundError{Key: "k"}); ok {
		t.Fatalf("a key-not-found was claimed by the restore guards as %s", wire.StatusName(st))
	}
}

// TestRestoreGuardStatus_ClassifiesItsFamily.
func TestRestoreGuardStatus_ClassifiesItsFamily(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want uint16
	}{
		{&service.RestoreNilRequestError{}, wire.StatusInvalidArgument},
		{&service.RestoreScopeEmptyError{}, wire.StatusInvalidArgument},
		{&service.RestoreApplyError{Key: "k", Err: errors.New("disk")}, wire.StatusError},
	} {
		st, ok := restoreGuardStatus(tc.err)
		if !ok {
			t.Errorf("%T was not classified", tc.err)
			continue
		}
		if st != tc.want {
			t.Errorf("%T: status = %s, want %s", tc.err, wire.StatusName(st), wire.StatusName(tc.want))
		}
	}
}

// TestModuleMethodTypeErrors_Render covers the two construction-time guards on
// the projection path. Both report a bug in a module's GENERATED gRPC code —
// a unary Handler always decodes into a proto.Message and always returns one —
// so neither is reachable from a well-formed module, and the message is the
// only thing that would tell whoever hit it what went wrong.
func TestModuleMethodTypeErrors_Render(t *testing.T) {
	req := (&ModuleMethodRequestTypeError{Method: "/m.v1.S/Do"}).Error()
	if !strings.Contains(req, "/m.v1.S/Do") || !strings.Contains(req, "decode target") {
		t.Errorf("got %q", req)
	}

	resp := (&ModuleMethodResponseTypeError{Method: "/m.v1.S/Do"}).Error()
	if !strings.Contains(resp, "/m.v1.S/Do") || !strings.Contains(resp, "response") {
		t.Errorf("got %q", resp)
	}
	if req == resp {
		t.Error("the request and response guards must be distinguishable in a log")
	}
}

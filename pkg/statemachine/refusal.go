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

package statemachine

import (
	"errors"
	"strconv"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// refusalRule recognises one error a subsystem returns during apply and
// names the kind of refusal it is.
type refusalRule struct {
	match func(error) bool
	kind  protocol.AppErrorKind
}

// isType reports whether err's chain holds a *T.
func isType[T any, E interface {
	*T
	error
}](err error) bool {
	var target E
	return errors.As(err, &target)
}

// refusalRules is the ordered table RefusalKind consults, first match wins.
// A slice, not a map keyed by type: errors.As needs a typed target.
//
// Before go-qrdb#557 every one of these travelled as the same untyped
// application error, so an invalid TTL, a missing lessor and a time-series IO
// failure all reached a caller as one "state machine refused" type the
// transports could only classify as a single guess.
var refusalRules = []refusalRule{
	// An absent capability declares itself (protocol.CapabilityError), as
	// timeseries.NotCompiledError does.
	{func(err error) bool { _, ok := protocol.AsCapabilityError(err); return ok }, protocol.AppErrorCapability},
	{isType[ReservedKeyError], protocol.AppErrorInvalid},
	{isType[lease.InvalidTTLError], protocol.AppErrorInvalid},
	{isType[lease.ExistsError], protocol.AppErrorConflict},
	{isType[lease.LessorClosedError], protocol.AppErrorUnavailable},
	{isType[timeseries.InvalidMetricError], protocol.AppErrorInvalid},
	{isType[timeseries.InvalidTimestampError], protocol.AppErrorInvalid},
	{isType[timeseries.InvalidTimeRangeError], protocol.AppErrorInvalid},
	{isType[timeseries.ValidationError], protocol.AppErrorInvalid},
	{isType[timeseries.InvalidAggFuncError], protocol.AppErrorInvalid},
	{isType[timeseries.InvalidBatchPointError], protocol.AppErrorInvalid},
	{isType[timeseries.ClosedError], protocol.AppErrorUnavailable},
	// A deterministic consequence of the committed OpAuditAppend entry's own
	// bytes (a zero Timestamp, an event that will not re-marshal, a
	// conflicting or invalid tenant id, or a well-formed body that fails to
	// unmarshal as an AuditEvent) -- every replica reaches it identically,
	// so it classifies as invalid input exactly like lease.InvalidTTLError,
	// never as a fault. See pkg/store/pebble.go's pebbleHandleAuditAppend
	// and pkg/engine/mvcc_engine.go's handleAuditAppend for why only
	// *vlog.TableWriteError (a local storage fault) is excluded from this
	// classification and returned as a Go error instead.
	{isType[audit.MissingTimestampError], protocol.AppErrorInvalid},
	{isType[audit.EncodeEventError], protocol.AppErrorInvalid},
	{isType[audit.TenantMismatchError], protocol.AppErrorInvalid},
	{isType[vlog.InvalidTenantIDError], protocol.AppErrorInvalid},
	{isType[protocol.InvalidCommandError], protocol.AppErrorInvalid},
	// A named log's refusals (OpVerifiableLog), each a deterministic
	// consequence of the committed bytes; see ApplyVerifiableLog for the
	// table faults that halt instead.
	{isType[vlog.InvalidLogNameError], protocol.AppErrorInvalid},
	{isType[vlog.InvalidDescriptorError], protocol.AppErrorInvalid},
	{isType[vlog.LogNotFoundError], protocol.AppErrorInvalid},
	{isType[vlog.AppendBatchError], protocol.AppErrorInvalid},
	{isType[vlog.SizeOutOfRangeError], protocol.AppErrorInvalid},
	{isType[vlog.IndexOutOfRangeError], protocol.AppErrorInvalid},
	{isType[vlog.CheckpointRootMismatchError], protocol.AppErrorInvalid},
	{isType[vlog.EmptyCheckpointError], protocol.AppErrorInvalid},
	{isType[vlog.ExpireNotAllowedError], protocol.AppErrorInvalid},
	{isType[vlog.ReservedLogError], protocol.AppErrorInvalid},
	{isType[vlog.NotAuditLogError], protocol.AppErrorInvalid},
	{isType[vlog.AuditNameTakenError], protocol.AppErrorConflict},
	{isType[vlog.UnsupportedKeyTypeError], protocol.AppErrorInvalid},
	{isType[vlog.InvalidSigningKeyError], protocol.AppErrorInvalid},
	{isType[vlog.LogExistsError], protocol.AppErrorConflict},
	{isType[vlog.CheckpointExistsError], protocol.AppErrorConflict},
	{isType[vlog.KeyVersionConflictError], protocol.AppErrorConflict},
}

// RefusalKind reports what kind of refusal err is, for a subsystem error a
// handler encodes into its result instead of failing the apply. An error no
// rule recognises is AppErrorFault: the subsystem failed, and nothing about
// the request says the caller did anything wrong.
// Vector-engine refusals (docs/vectors/engineering.md "Target: the vector
// engine") do not need a row in refusalRules above: every typed vector
// validation error in pkg/vector implements protocol.RefusalError directly,
// so pkg/engine's vector handlers return it as a plain Go error and
// ResolveApplyError classifies it without this package's help -- the
// "declaration beside the refusal" protocol.CapabilityError's own doc
// comment argues for, extended to refusals. That path encodes every
// refusal as AppErrorInvalid; it is this package's refusalRules/RefusalKind
// table that gives a vector.NamespaceExistsError the same AppErrorConflict
// an index-ownership refusal gets, which the vector engine does not need
// for Phase 1 and can add here later without touching pkg/engine.
func RefusalKind(err error) protocol.AppErrorKind {
	for _, r := range refusalRules {
		if r.match(err) {
			return r.kind
		}
	}
	return protocol.AppErrorFault
}

// RefusalResult encodes err as a non-fatal application-error result of the
// kind RefusalKind assigns it.
func RefusalResult(err error) sm.Result {
	return sm.Result{Data: protocol.EncodeAppError(RefusalKind(err), err.Error())}
}

// CapabilityResult encodes an absent-capability refusal: a subsystem this
// state machine was built without (a nil lessor, no time-series store).
func CapabilityResult(msg string) sm.Result {
	return sm.Result{Data: protocol.EncodeCapabilityError(msg)}
}

// LeaseRefusal answers whether a write naming leaseIDs must be refused
// because a lease it names cannot be bound, and if so returns the refusal.
// lease.NoLease names nothing and always passes.
//
// It is asked BEFORE the write stages anything, for every write that carries
// a lease. The service layer checks the lease too, but that check runs on the
// proposer before the write is committed; a revoke or an expiry committed in
// between leaves the write naming a lease that no longer exists. Writing the
// key anyway would produce a key bound to no lease -- one that never expires,
// with nothing to tell its owner -- so the write is refused here, in the
// apply path, where every replica decides it against the same state. The
// refusal carries the lease ID (protocol.EncodeLeaseNotFoundError), and the
// service layer answers it with service.LeaseNotFoundError: the same error
// the pre-proposal check returns, and etcd's answer to a put naming an
// unknown lease.
//
// A state machine built without a lessor cannot bind any lease, so a write
// naming one is a capability refusal rather than a silently lease-less key.
func LeaseRefusal(lr *lease.Lessor, leaseIDs ...int64) (sm.Result, bool) {
	for _, id := range leaseIDs {
		if id == lease.NoLease {
			continue
		}
		if lr == nil {
			return RefusalResult(&ServiceNotConfiguredError{Service: "lease"}), true
		}
		if !lr.Has(id) {
			return sm.Result{Data: protocol.EncodeLeaseNotFoundError(id)}, true
		}
	}
	return sm.Result{}, false
}

// OpsLeaseRefusal is LeaseRefusal for every put in a batch or transaction
// operation set, asked before any of the set is staged so a refusal leaves
// the whole set unapplied.
func OpsLeaseRefusal(lr *lease.Lessor, ops []protocol.DecodedOp) (sm.Result, bool) {
	for _, op := range ops {
		if op.Type != protocol.OpMVCCPut {
			continue
		}
		if res, refused := LeaseRefusal(lr, op.LeaseID); refused {
			return res, true
		}
	}
	return sm.Result{}, false
}

// ReservedKeyError refuses a write naming a key that begins with 0x00, the
// byte a state machine that stores user keys raw reserves for its own records
// (the last-applied index, the lease table). Such a write could overwrite or
// delete one of them -- forge a lease, roll back the applied index.
type ReservedKeyError struct {
	Key []byte
}

func (e *ReservedKeyError) Error() string {
	return "statemachine: key " + strconv.Quote(string(e.Key)) + " is in the reserved 0x00 namespace"
}

// IsReservedKey reports whether key begins with the 0x00 byte a state
// machine that stores user keys raw reserves for its own records: the
// last-applied index, the lease table, the replicated audit table, and any
// future one. It is the ONE predicate every reserved-keyspace check in this
// codebase shares -- write-side (ReservedKeyRefusal) and read-side alike
// (a point Get, a transaction compare, an OpGuarded compare) -- so a table
// added to the namespace is covered everywhere at once rather than by a
// list of specific prefixes repeated at each call site.
func IsReservedKey(key []byte) bool {
	return len(key) > 0 && key[0] == 0x00
}

// ReservedKeyRefusal answers whether a write, or a read a write's outcome
// depends on (a transaction or OpGuarded compare), naming keys must be
// refused because one of them is in the reserved 0x00 namespace, and if so
// returns the refusal. It is asked before anything -- a write, or a
// compare's read -- runs, which matters as much for a compare as for a
// write: a compare's result (CompareResultLess/Greater, or simply whether it
// holds at all) is observable to the proposer, so evaluating it against a
// reserved key would make an ordinary transaction or guarded write an
// extraction oracle over state no caller may read directly.
func ReservedKeyRefusal(keys ...[]byte) (sm.Result, bool) {
	for _, k := range keys {
		if IsReservedKey(k) {
			return RefusalResult(&ReservedKeyError{Key: k}), true
		}
	}
	return sm.Result{}, false
}

// OpKeys returns the keys an operation set names, for ReservedKeyRefusal.
func OpKeys(ops []protocol.DecodedOp) [][]byte {
	keys := make([][]byte, 0, len(ops))
	for _, op := range ops {
		keys = append(keys, op.Key)
	}
	return keys
}

// CompareKeys returns the keys a set of compares names, for
// ReservedKeyRefusal. A transaction's Compares and an OpGuarded command's
// compares are both []protocol.DecodedCompare; this is the one place that
// extracts a key from one, so a transaction compare and a guard compare can
// never drift onto two different notions of "the key a compare names."
//
// A transaction or guarded write must refuse every one of them against a
// reserved keyspace BEFORE any compare is evaluated: evaluating one first
// and only then refusing would have already let its Result
// (CompareResultLess/Greater, or whether a guard held) answer a
// binary-search probe against the reserved key's stored bytes -- an
// extraction oracle, not merely an unauthorized write (go-qrdb
// vector-replicated review, B3-residual). Callers pass this alongside
// OpKeys to vector.RefuseForgedKeys so a transaction's compares and its
// selected operation set are both checked the same way.
func CompareKeys(compares []protocol.DecodedCompare) [][]byte {
	keys := make([][]byte, 0, len(compares))
	for _, c := range compares {
		keys = append(keys, c.Key)
	}
	return keys
}

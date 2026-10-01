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

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
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
}

// RefusalKind reports what kind of refusal err is, for a subsystem error a
// handler encodes into its result instead of failing the apply. An error no
// rule recognises is AppErrorFault: the subsystem failed, and nothing about
// the request says the caller did anything wrong.
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

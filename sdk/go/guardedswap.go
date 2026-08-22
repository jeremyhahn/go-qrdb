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

package sdk

import (
	"bytes"
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// SwapPlane is what GuardedSwap needs from a client: a linearizable read that
// carries MVCC revision metadata, and a transaction to write under a guard.
//
// It is TWO capabilities rather than one because the transaction plane cannot
// read. Transaction branches carry write operations only — pkg/service's
// encoder maps OpPut and OpDelete and fails closed on anything else, so a
// transaction that tried to read would be refused before reaching Raft. The
// read therefore goes through GetRevision, which is the supported linearizable
// read, and ModRevision comes back with it at no extra cost.
//
// transport.Client satisfies this, as does any narrower client offering both
// methods.
type SwapPlane interface {
	transport.TransactionClient

	// GetRevision reads the record with its MVCC revision metadata, and
	// reports a missing key with *transport.KeyNotFoundError.
	GetRevision(ctx context.Context, key string, opts ...transport.ReadOption) (*transport.MVCCKeyValue, error)
}

// SwapDecision is what a Decide callback tells GuardedSwap to do with the
// record it was shown.
type SwapDecision uint8

const (
	// SwapWrite proposes the returned bytes under the guard. If the guard
	// holds, the write lands and GuardedSwap returns. If it does not, the
	// record has moved and Decide is called again with what it moved to.
	SwapWrite SwapDecision = iota

	// SwapDone stops without writing and reports success. Use it when the
	// record already says what this caller wanted it to say — an idempotent
	// re-run, or a transition another writer has already made.
	SwapDone

	// SwapLose stops without writing and reports ErrSwapLost. Use it when
	// another writer has won something this caller cannot take back: a claim
	// on a monotonic mark, a lease held by a higher generation.
	SwapLose
)

// Decide inspects the record as it currently stands and says what to do.
//
// exists distinguishes a key that is absent from one holding an empty value;
// current is nil when exists is false. A returned error stops the loop and is
// passed through to the caller unwrapped, so a Decide that cannot decode the
// record reports its own error rather than being reported as contention.
//
// It must be a pure function of what it is shown. GuardedSwap may call it
// several times, and calling it is not a commitment to write.
type Decide func(current []byte, exists bool) (write []byte, decision SwapDecision, err error)

// GuardedSwapRequest configures one guarded swap.
type GuardedSwapRequest struct {
	// Key is the record to swap. Required.
	Key []byte

	// Decide chooses what to write, given the record as read. Required.
	Decide Decide

	// MaxAttempts bounds how many times the guard may be refused before
	// GuardedSwap gives up with ErrSwapContended. Zero means
	// DefaultSwapAttempts; a NEGATIVE value is a SwapConfigError rather than a
	// silent default, because it means the caller computed the bound wrongly
	// and hiding that would leave an invalid configuration looking valid.
	//
	// It is a bound, not a target: a caller that legitimately needs more
	// attempts than this is contending on a record it should be partitioning
	// instead.
	MaxAttempts int

	// GuardOnValue guards the swap on the record's VALUE bytes rather than on
	// its ModRevision.
	//
	// The default (false) is ModRevision, and that default is deliberate. A
	// value guard admits the ABA case: a record that goes A → B → A between
	// the read and the swap satisfies a compare against A, so the swap lands
	// on top of a state the caller never saw. That is safe only for records
	// whose value moves monotonically, which is a property of the CALLER's
	// data that this package cannot check. A ModRevision guard has no such
	// caveat — the revision never repeats — and costs nothing extra, since
	// the read already returns it.
	//
	// Set this only for a store whose transaction plane does not evaluate
	// CompareModRevision, and only for records you know to be monotonic.
	GuardOnValue bool
}

// DefaultSwapAttempts bounds a guarded swap that does not set MaxAttempts.
//
// Bounded rather than unbounded because the failure it guards against is a
// livelock, and a livelocked caller that never returns is harder to diagnose
// than one that returns a typed error naming the record it could not win.
const DefaultSwapAttempts = 8

// GuardedSwapResult reports how a swap ended.
type GuardedSwapResult struct {
	// Written is true when a write landed. False means Decide returned
	// SwapDone without proposing one.
	Written bool

	// Revision is the MVCC revision the write landed at, or 0 when nothing
	// was written.
	Revision int64

	// Attempts is how many times the guard was proposed, including the one
	// that won. It is 0 when Decide returned SwapDone on the first read.
	Attempts int
}

// GuardedSwap performs the read / decide / guarded-write loop over the
// transaction plane: the optimistic-CAS dance that every transaction-plane
// consumer otherwise hand-rolls (go-qrdb#44).
//
// An attempt that WINS is one round trip: the guard and the write travel in a
// single transaction, so the common case costs a read and a write rather than
// a read, a write and a confirming re-read. An attempt whose guard is REFUSED
// costs a second round trip, because the record that beat it has to be read
// back before the next decision. The transaction plane cannot fold that read
// into the failure branch — see SwapPlane for why.
//
// A record that does not yet exist is guarded on CompareCreateRevision == 0, so
// exactly one of several concurrent creators wins and the rest see the winner's
// value on their next call to Decide. An existing record is guarded on its
// ModRevision (see GuardOnValue).
//
// It fails CLOSED in both directions. Reaching MaxAttempts returns
// ErrSwapContended, and a guard that is refused without the record moving
// returns ErrSwapStalled — neither reports the write as done, and neither spins
// forever. See ErrSwapStalled for why "refused but unchanged" is treated as a
// fault rather than retried.
func GuardedSwap(
	ctx context.Context,
	client SwapPlane,
	req GuardedSwapRequest,
	opts ...transport.WriteOption,
) (GuardedSwapResult, error) {
	if client == nil {
		return GuardedSwapResult{}, &SwapConfigError{Reason: "nil transaction client"}
	}
	if len(req.Key) == 0 {
		return GuardedSwapResult{}, &SwapConfigError{Reason: "empty key"}
	}
	if req.Decide == nil {
		return GuardedSwapResult{}, &SwapConfigError{Reason: "nil Decide"}
	}
	// Zero means "use the default"; NEGATIVE means the caller computed a bound
	// wrongly, and silently substituting the default would hide that. The
	// contract says zero, so only zero.
	if req.MaxAttempts < 0 {
		return GuardedSwapResult{}, &SwapConfigError{Reason: "negative MaxAttempts"}
	}
	maxAttempts := req.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = DefaultSwapAttempts
	}

	current, exists, modRev, err := swapRead(ctx, client, req.Key)
	if err != nil {
		return GuardedSwapResult{}, err
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		write, decision, derr := req.Decide(current, exists)
		if derr != nil {
			return GuardedSwapResult{Attempts: attempt}, derr
		}
		switch decision {
		case SwapDone:
			return GuardedSwapResult{Attempts: attempt}, nil
		case SwapLose:
			return GuardedSwapResult{Attempts: attempt}, &SwapLostError{Key: append([]byte(nil), req.Key...)}
		case SwapWrite:
		default:
			return GuardedSwapResult{Attempts: attempt}, &SwapConfigError{Reason: "unknown SwapDecision"}
		}

		resp, xerr := client.ExecuteTransaction(ctx, &transport.TransactionRequest{
			Compares:   []transport.Compare{swapGuard(req, exists, current, modRev)},
			SuccessOps: []transport.Op{{Type: transport.OpPut, Key: req.Key, Value: write}},
			// No FailureOps. A read here would be the obvious way to learn
			// what beat the guard in the same round trip, and it is exactly
			// what the transaction plane refuses: branch ops are writes only.
			// The read-back below uses the supported read instead.
		}, opts...)
		if xerr != nil {
			return GuardedSwapResult{Attempts: attempt + 1}, xerr
		}
		if resp.Succeeded {
			return GuardedSwapResult{Written: true, Revision: resp.Revision, Attempts: attempt + 1}, nil
		}

		next, nextExists, nextModRev, rerr := swapRead(ctx, client, req.Key)
		if rerr != nil {
			return GuardedSwapResult{Attempts: attempt + 1}, rerr
		}

		// Progress precondition. The guard was refused, so the record must
		// have moved; if it has not, retrying cannot produce a different
		// outcome and the loop would spin until MaxAttempts reporting
		// contention for what is actually a fault.
		if nextExists == exists && nextModRev == modRev && bytes.Equal(next, current) {
			return GuardedSwapResult{Attempts: attempt + 1}, &SwapStalledError{
				Key:      append([]byte(nil), req.Key...),
				Attempts: attempt + 1,
			}
		}
		current, exists, modRev = next, nextExists, nextModRev
	}

	return GuardedSwapResult{Attempts: maxAttempts}, &SwapContendedError{
		Key:      append([]byte(nil), req.Key...),
		Attempts: maxAttempts,
	}
}

// swapGuard builds the precondition for one attempt.
//
// Absent records are guarded on CompareCreateRevision == 0 rather than on a
// nil value: "this key has never been created" is a statement the state machine
// can evaluate exactly, whereas comparing against an empty value cannot
// distinguish an absent key from one holding no bytes.
func swapGuard(req GuardedSwapRequest, exists bool, current []byte, modRev int64) transport.Compare {
	if !exists {
		return transport.Compare{
			Key:            req.Key,
			Target:         transport.CompareCreateRevision,
			Result:         transport.CompareEqual,
			CreateRevision: 0,
		}
	}
	if req.GuardOnValue {
		return transport.Compare{
			Key:    req.Key,
			Target: transport.CompareValue,
			Result: transport.CompareEqual,
			Value:  current,
		}
	}
	return transport.Compare{
		Key:         req.Key,
		Target:      transport.CompareModRevision,
		Result:      transport.CompareEqual,
		ModRevision: modRev,
	}
}

// swapRead reads the record and its ModRevision through the supported
// linearizable read.
//
// It is used for both the initial read and the read-back after a refused
// guard, so the two see the record through exactly the same path — a
// read-back that resolved absence differently from the initial read would
// make the progress precondition below compare two different things.
//
// A missing key is absence, not failure: the guard for an absent record is
// CompareCreateRevision == 0, which is a swap the caller can legitimately
// attempt. Both signals are accepted, since a transport may report a missing
// key as *transport.KeyNotFoundError or as a nil record.
func swapRead(
	ctx context.Context,
	client SwapPlane,
	key []byte,
) (value []byte, exists bool, modRev int64, err error) {
	kv, err := client.GetRevision(ctx, string(key))
	if err != nil {
		var notFound *transport.KeyNotFoundError
		if errors.As(err, &notFound) {
			return nil, false, 0, nil
		}
		return nil, false, 0, err
	}
	if kv == nil {
		return nil, false, 0, nil
	}
	return kv.Value, true, kv.ModRevision, nil
}

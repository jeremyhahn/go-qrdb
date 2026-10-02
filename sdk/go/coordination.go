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
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Coordination surface (go-qrdb#166)
// ---------------------------------------------------------------------------
//
// This file surfaces qrdb's existing coordination primitives — leases,
// compare-and-swap, and change watches — through one small, consumer-facing
// interface. It is a thin wiring layer over the transport sub-clients the
// Client already exposes (KV, Lease, MVCC, Transaction, Watch); it introduces
// no new consensus and no server-side behavior. The intended consumer is a
// control plane (go-trusted-platform) whose own coordination interface, defined
// in go-blockstore, wraps a value of this shape.
//
// Mode mapping: every method behaves IDENTICALLY in standalone (single-node,
// non-raft) and clustered (raft) modes.
//
//   - Get, Put and CompareAndSwap: a standalone node is engine-direct and
//     single-process, so its compare-and-swap is already linearizable —
//     consensus is what makes a swap linearizable across REPLICAS, and a
//     standalone node has none (go-qrdb#62).
//
//   - AcquireLease and Watch: a standalone node composes the lease, MVCC and
//     watch families over the same engine, lessor and watch hub as raft
//     mode's MVCC state machine, and runs the lease-expiry sweeper itself
//     (go-qrdb#582). sdk/go/conformance's coordination parity suite runs the
//     same lease, watch and lock assertions against both modes on every
//     transport. The one boundary is the storage engine, not the mode: a
//     standalone node on the file or objstore backend keeps no leases or
//     revisions, so AcquireLease is refused there with
//     *transport.CapabilityUnsupportedError; check Capabilities and the
//     backend rather than match the error.

// EventType identifies the kind of change carried by a coordination Event. It
// aliases the transport event type so the two never drift.
type EventType = transport.EventType

const (
	// EventPut indicates a key was created or updated.
	EventPut = transport.EventPut

	// EventDelete indicates a key was deleted.
	EventDelete = transport.EventDelete
)

// Event is a single change delivered by Watch. Value carries the key's new
// value on a put and is nil on a delete; PrevValue carries the value the key
// held before the change when the server includes it. Revision is the MVCC
// revision at which the change was committed.
type Event struct {
	Type      EventType
	Key       string
	Value     []byte
	PrevValue []byte
	Revision  int64
}

// Lease is an acquired TTL lease bound to a key. The key exists for as long as
// the lease is kept alive and is deleted when the lease is released or expires.
// It is the qrdb-side value a go-blockstore lease wraps.
type Lease interface {
	// Key returns the key the lease is bound to.
	Key() string

	// Renew resets the lease TTL, keeping the bound key alive. A consumer
	// calls it on an interval shorter than the TTL to retain ownership.
	//
	// A keepalive the server accepts only AFTER Done has closed returns
	// LeaseLostError rather than nil. Ownership ends at the last acknowledged
	// confirmation's deadline, and a renewal that lands past it could not have
	// told the holder in time — by then it has already been told it lost the
	// key and must have fenced. The remedy is to renew early enough that the
	// acknowledgement arrives before the deadline, which is what a cadence
	// comfortably shorter than the TTL buys; it is not to trust a late ack.
	Renew(ctx context.Context) error

	// Release revokes the lease, deleting the bound key, and closes Done. It
	// is idempotent once it has succeeded: releasing an already-released lease
	// is a no-op. A revoke the server did not acknowledge is NOT a release —
	// the error is returned, Done stays open, and the caller can retry.
	Release(ctx context.Context) error

	// Done is closed when the lease is lost — either released by this holder or
	// expired because it was not renewed in time. A consumer selects on it to
	// learn it no longer holds the key.
	Done() <-chan struct{}
}

// CoordinationClient is the coordination surface a control-plane consumer
// wraps. Coordinator is its only implementation; the interface exists so
// consumers can substitute a fake in their own tests.
type CoordinationClient interface {
	AcquireLease(ctx context.Context, key string, ttl time.Duration) (Lease, error)
	CompareAndSwap(ctx context.Context, key string, expect, set []byte) (bool, error)
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, val []byte) error
	Watch(ctx context.Context, prefix string) (WatchStream, error)
}

// CoordinationUnsupportedError is returned by Coordination and NewCoordinator
// when the underlying transport does not implement a sub-client the
// coordination surface requires. It names the missing capability. It is
// distinct from the "not configured" error a call returns when the transport
// implements the interface but the node's run-mode does not serve the family:
// this error means the verb is absent from the wire contract entirely.
type CoordinationUnsupportedError struct {
	Capability string
}

// Error returns a human-readable description.
func (e *CoordinationUnsupportedError) Error() string {
	return "sdk: transport does not support coordination capability: " + e.Capability
}

// Coordinator implements CoordinationClient by delegating to the transport
// sub-clients. Construct one with Client.Coordination, or NewCoordinator when
// composing the sub-clients directly.
type Coordinator struct {
	kv    transport.KVClient
	lease transport.LeaseClient
	txn   transport.TransactionClient
	watch transport.WatchClient
}

// Compile-time interface assertion.
var _ CoordinationClient = (*Coordinator)(nil)

// Coordination returns a Coordinator backed by this client's transport. It
// returns ClientClosedError on a closed client and CoordinationUnsupportedError
// when the transport lacks a required sub-client. A backend that keeps no
// leases is NOT reported here — every transport implements the interfaces; the
// refusal surfaces from the individual call (see the mode mapping at the top
// of this file).
func (c *Client) Coordination() (*Coordinator, error) {
	if c.closed.Load() {
		return nil, &ClientClosedError{}
	}
	kv, ok := c.transport.(transport.KVClient)
	if !ok {
		return nil, &CoordinationUnsupportedError{Capability: "kv"}
	}
	lease, ok := c.transport.(transport.LeaseClient)
	if !ok {
		return nil, &CoordinationUnsupportedError{Capability: "lease"}
	}
	txn, ok := c.transport.(transport.TransactionClient)
	if !ok {
		return nil, &CoordinationUnsupportedError{Capability: "transaction"}
	}
	watch, ok := c.transport.(transport.WatchClient)
	if !ok {
		return nil, &CoordinationUnsupportedError{Capability: "watch"}
	}
	return &Coordinator{kv: kv, lease: lease, txn: txn, watch: watch}, nil
}

// NewCoordinator builds a Coordinator from explicitly supplied sub-clients,
// serving callers that compose the clients themselves rather than take them all
// from one connected Client (compare NewKVStoreAdapter). Every argument is
// required; a nil one yields CoordinationUnsupportedError naming it.
func NewCoordinator(
	kv transport.KVClient,
	lease transport.LeaseClient,
	txn transport.TransactionClient,
	watch transport.WatchClient,
) (*Coordinator, error) {
	switch {
	case kv == nil:
		return nil, &CoordinationUnsupportedError{Capability: "kv"}
	case lease == nil:
		return nil, &CoordinationUnsupportedError{Capability: "lease"}
	case txn == nil:
		return nil, &CoordinationUnsupportedError{Capability: "transaction"}
	case watch == nil:
		return nil, &CoordinationUnsupportedError{Capability: "watch"}
	}
	return &Coordinator{kv: kv, lease: lease, txn: txn, watch: watch}, nil
}

// ---------------------------------------------------------------------------
// Get / Put
// ---------------------------------------------------------------------------

// Get returns the value stored under key, or (nil, nil) when the key does not
// exist. The absence-as-(nil, nil) convention matches the SDK's kvstore adapter
// (go-qrdb#58): a missing key is not an error. A present key with an empty
// value returns a non-nil empty slice, so a caller can tell absent from empty.
func (c *Coordinator) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := c.kv.Get(ctx, key)
	if err != nil {
		var notFound *transport.KeyNotFoundError
		if errors.As(err, &notFound) {
			return nil, nil
		}
		return nil, err
	}
	return val, nil
}

// Put stores val under key. It is a straight delegation to the KV data plane
// and behaves identically in standalone and clustered modes.
func (c *Coordinator) Put(ctx context.Context, key string, val []byte) error {
	return c.kv.Put(ctx, key, val)
}

// ---------------------------------------------------------------------------
// CompareAndSwap
// ---------------------------------------------------------------------------

// CompareAndSwap atomically stores set under key when the key's current state
// matches expect, returning whether the swap happened. It is expressed as a
// single transaction whose precondition the state machine evaluates, so two
// racing callers cannot both win and no read-then-write window exists.
//
// expect selects the precondition:
//
//   - expect == nil means "create only if absent": the precondition is that the
//     key's create-revision is 0 (it does not exist). This is the initialization
//     case — the first writer wins and later writers see ok == false.
//
//   - expect != nil (an empty slice included) means "swap only if the key
//     EXISTS and its current value equals expect": the precondition pairs a
//     value compare with an existence compare. The existence compare is what
//     keeps an empty expect honest — a value compare alone would match an absent
//     key, because the compare evaluator reads a missing key's value as nil and
//     nil equals an empty slice, so CompareAndSwap(key, []byte{}, set) would
//     otherwise create a key it was asked only to swap.
//
// A precondition miss returns (false, nil); it is a normal outcome, not an
// error. A transport or server failure returns (false, err).
//
// This behaves identically in standalone and clustered modes (go-qrdb#62).
func (c *Coordinator) CompareAndSwap(ctx context.Context, key string, expect, set []byte) (bool, error) {
	var compares []transport.Compare
	if expect == nil {
		compares = []transport.Compare{{
			Key:            []byte(key),
			Target:         transport.CompareCreateRevision,
			Result:         transport.CompareEqual,
			CreateRevision: 0,
		}}
	} else {
		compares = []transport.Compare{
			{
				// Key exists: create-revision > 0.
				Key:            []byte(key),
				Target:         transport.CompareCreateRevision,
				Result:         transport.CompareGreater,
				CreateRevision: 0,
			},
			{
				Key:    []byte(key),
				Target: transport.CompareValue,
				Result: transport.CompareEqual,
				Value:  expect,
			},
		}
	}
	resp, err := c.txn.ExecuteTransaction(ctx, &transport.TransactionRequest{
		Compares: compares,
		SuccessOps: []transport.Op{{
			Type:  transport.OpPut,
			Key:   []byte(key),
			Value: set,
		}},
	})
	if err != nil {
		return false, err
	}
	return resp.Succeeded, nil
}

// ---------------------------------------------------------------------------
// Watch
// ---------------------------------------------------------------------------

// WatchStream is a live watch: a channel of changes plus the reason the watch
// ended. It follows the same shape as Lease — an interface over an unexported
// implementation — so a consumer can substitute a fake in its own tests.
//
// Why the terminal condition is not simply "the channel closed": a watch can
// end three ways that a consumer must tell apart, because they call for
// different responses. The caller cancelling its own context is an orderly
// shutdown. The server closing the stream cleanly is nothing to act on either.
// But a watch dropped because the requested revision was COMPACTED, or because
// the store cannot replay revisions, means changes happened that this consumer
// never saw — and resuming from where it left off would silently skip them.
// That third case has to be recoverable by reconciling against current state,
// which a consumer can only do if it can recognize it happened.
type WatchStream interface {
	// Events returns the channel of changes, one Event per key change. It is
	// closed when the watch ends, for any of the three reasons above.
	Events() <-chan Event

	// Err returns what ended the watch, and is meaningful only once Events has
	// closed. It is nil when the server closed the stream cleanly; ctx.Err()
	// when the caller cancelled; and the transport's typed watch error —
	// WatchCompactedError, WatchStartRevisionUnsupportedError, or a generic
	// WatchCanceledError — when the server terminated the watch itself.
	Err() error
}

// watchStream is the concrete WatchStream.
type watchStream struct {
	events chan Event

	mu  sync.Mutex
	err error
}

// Compile-time interface assertion.
var _ WatchStream = (*watchStream)(nil)

// Events returns the channel of changes.
func (w *watchStream) Events() <-chan Event { return w.events }

// Err returns the terminal condition, valid once Events has closed.
func (w *watchStream) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// finish records why the watch ended and then closes the event channel. The
// order matters: the error is in place BEFORE the close a consumer observes, so
// there is no window where the stream looks finished but has no reason yet.
func (w *watchStream) finish(err error) {
	w.mu.Lock()
	w.err = err
	w.mu.Unlock()
	close(w.events)
}

// Watch streams changes to every key beginning with prefix. Events delivers one
// Event per key change and closes when the watch ends; Err then reports what
// ended it. The caller stops the watch by cancelling ctx; a spawned goroutine
// drains the transport stream until then.
//
// Watch is served in both run modes (see the mode mapping at the top of this
// file).
func (c *Coordinator) Watch(ctx context.Context, prefix string) (WatchStream, error) {
	raw, err := c.watch.WatchPrefix(ctx, prefix, transport.WatchOptions{})
	if err != nil {
		return nil, err
	}
	w := &watchStream{events: make(chan Event)}
	go func() {
		for {
			select {
			case <-ctx.Done():
				w.finish(ctx.Err())
				return
			case resp, ok := <-raw:
				if !ok {
					// The transport ended the stream without a terminal
					// cancellation. That is usually a clean close, but not
					// always: a transport commonly closes the raw stream AS A
					// CONSEQUENCE of the caller cancelling, and select picks
					// among ready cases at random, so arriving here is no proof
					// the close came first. ctx.Err() settles it — nil when the
					// caller is still watching, its cancellation otherwise.
					w.finish(ctx.Err())
					return
				}
				if resp.Canceled {
					// A terminal cancellation carries no events, only a reason.
					// The transport classifies it — compaction, an unsupported
					// replay revision, or a generic cancellation — and that
					// classification is the whole point of surfacing it: it is
					// what tells the consumer whether it missed changes. Err
					// never returns nil for a cancelled response, so this stays
					// distinguishable from the clean close above.
					w.finish(resp.Err())
					return
				}
				for _, ev := range resp.Events {
					if ev == nil {
						continue
					}
					select {
					case w.events <- toEvent(ev):
					case <-ctx.Done():
						w.finish(ctx.Err())
						return
					}
				}
			}
		}
	}()
	return w, nil
}

// toEvent converts a transport watch event into the coordination Event. A put
// carries the new value from Kv; a delete carries a nil Value. PrevValue is
// populated from PrevKv when the server included it (WatchOptions.PrevKV).
func toEvent(ev *transport.WatchEvent) Event {
	e := Event{Type: ev.Type}
	if ev.Kv != nil {
		e.Key = string(ev.Kv.Key)
		e.Revision = ev.Kv.ModRevision
		if ev.Type == transport.EventPut {
			e.Value = ev.Kv.Value
		}
	}
	if ev.PrevKv != nil {
		e.PrevValue = ev.PrevKv.Value
		if e.Key == "" {
			e.Key = string(ev.PrevKv.Key)
		}
	}
	return e
}

// ---------------------------------------------------------------------------
// AcquireLease
// ---------------------------------------------------------------------------

// leaseMarker is the value written under a leased key. The key's existence and
// its lease attachment are the ownership signal; the value itself is
// deliberately a non-empty constant so Get distinguishes a held lease (non-nil)
// from an absent key (nil).
var leaseMarker = []byte{1}

// leaseCleanupTimeout bounds the detached revoke that cleans up a grant whose
// exclusive bind did not land. It is a cleanup budget, not a request budget: it
// only has to be long enough for a healthy service to answer, because a revoke
// that never lands costs one grant that expires on its own TTL, while a revoke
// that never returns costs the caller its AcquireLease call. It is a var only
// so tests can prove the bound holds without waiting out the production one.
var leaseCleanupTimeout = 5 * time.Second

// LeaseHeldError is returned by AcquireLease when the key is already held by
// another lease. Acquisition is exclusive: the key is bound to the new lease
// only if it does not already exist, so a contender is refused rather than
// silently stealing the binding from the current holder.
type LeaseHeldError struct {
	Key string
}

// Error returns a human-readable description.
func (e *LeaseHeldError) Error() string {
	return "sdk: lease key already held: " + e.Key
}

// LeaseLostError is returned by Renew when the keepalive succeeded but the
// lease had already ended: its confirmed lifetime ran out and Done closed
// before the acknowledgement arrived. The server may well still hold the lease
// — that is not the point. The holder was told it had lost the key and has
// acted on that, so the ack is stale, and reporting it as success would put a
// holder that already fenced back to work on a key it no longer safely owns.
type LeaseLostError struct {
	Key string
}

// Error returns a human-readable description.
func (e *LeaseLostError) Error() string {
	return "sdk: lease lost before renewal was acknowledged: " + e.Key
}

// AcquireLease acquires an EXCLUSIVE TTL lease on key: it grants a lease and, in
// a single transaction, binds key to it only if key does not already exist. If
// another holder already owns key the transaction's precondition fails, the
// just-granted lease is revoked, and LeaseHeldError is returned — a second
// contender never displaces the current holder. The bound key is deleted
// automatically when the lease expires or is released, so the lease is a
// TTL-scoped distributed lock and the returned Lease renews, releases, and
// reports loss via Done.
//
// The exclusive bind is one transaction (create-if-absent with the lease
// attached to the put), so two racing callers cannot both win: the state
// machine evaluates the absent-key precondition, exactly as CompareAndSwap's
// create path does.
//
// ttl is rounded UP to whole seconds (the lease grant's granularity), with a
// floor of one second. A caller must Renew on an interval shorter than ttl to
// retain the key.
//
// AcquireLease is served in both run modes; a backend that keeps no leases
// (standalone file or objstore) refuses the grant with
// *transport.CapabilityUnsupportedError (see the mode mapping at the top of
// this file).
func (c *Coordinator) AcquireLease(ctx context.Context, key string, ttl time.Duration) (Lease, error) {
	secs := ttl / time.Second
	if ttl%time.Second != 0 {
		secs++
	}
	if secs < 1 {
		secs = 1
	}

	// The grant's clock starts server-side, some time after this request goes
	// out. Timing the lease from the send is the conservative side of that gap:
	// it under-states the life remaining rather than over-stating it.
	granted := time.Now()
	info, err := c.lease.LeaseGrant(ctx, int64(secs))
	if err != nil {
		return nil, err
	}

	resp, err := c.txn.ExecuteTransaction(ctx, &transport.TransactionRequest{
		Compares: []transport.Compare{{
			Key:            []byte(key),
			Target:         transport.CompareCreateRevision,
			Result:         transport.CompareEqual,
			CreateRevision: 0,
		}},
		SuccessOps: []transport.Op{{
			Type:    transport.OpPut,
			Key:     []byte(key),
			Value:   leaseMarker,
			LeaseID: info.ID,
		}},
	})
	if err != nil || !resp.Succeeded {
		// The grant succeeded but the exclusive bind did not (transport error,
		// or the key is already held). Revoke so the grant does not leak.
		//
		// The cleanup is detached from ctx because it must still run when ctx
		// is already cancelled — but detached is not the same as unbounded.
		// context.WithoutCancel drops the parent's DEADLINE along with its
		// cancellation, so an unresponsive lease service would hold this revoke,
		// and with it AcquireLease, open forever — long after the caller's own
		// context had expired. Give the cleanup a deadline of its own.
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), leaseCleanupTimeout)
		// A cleanup that fails leaks a grant that will expire on its own TTL;
		// what must not be lost is why the acquisition failed, so that error is
		// what the caller gets either way.
		_ = c.lease.LeaseRevoke(cleanupCtx, info.ID)
		cancelCleanup()
		if err != nil {
			return nil, err
		}
		return nil, &LeaseHeldError{Key: key}
	}

	return newLease(c.lease, key, info.ID, int64(secs), granted), nil
}

// lease is the concrete Lease. It observes lease liveness with a background
// poller so Done fires on external expiry as well as on Release.
type lease struct {
	client transport.LeaseClient
	key    string
	id     int64
	ttl    time.Duration // the granted TTL, which is what a keepalive restores

	// heldUntil is the instant up to which the lease is KNOWN to still be held:
	// the floor implied by the last thing that confirmed it. A grant or a
	// keepalive confirms a whole TTL; a liveness lookup confirms only the whole
	// seconds it reported, which is a floor of its own because the server
	// truncates. Nothing else moves it. Past this instant the holder has to be
	// told it may no longer own the key — a fencing signal that arrives late is
	// the dangerous one, because late is what puts two holders on one key.
	heldMu    sync.Mutex
	heldUntil time.Time

	// lost records that the deadline passed and Done was closed on it. It is
	// what makes a confirmation and a loss decide in one order rather than two:
	// both take heldMu, so a keepalive either extends the deadline before the
	// lease ends or finds it already ended and reports itself stale.
	lost bool

	done      chan struct{}
	closeOnce sync.Once
	stop      chan struct{} // closed by a successful Release to end the poller
	stopOnce  sync.Once

	// releaseMu serializes Release and is held ACROSS the revoke, so a second
	// caller waits for the in-flight revoke's outcome instead of reporting a
	// success the server never gave. released records that outcome and is set
	// only once a revoke is acknowledged — that is what keeps a failed revoke
	// retryable instead of consuming the one shot at cleanup.
	releaseMu sync.Mutex
	released  bool
}

// Compile-time interface assertion.
var _ Lease = (*lease)(nil)

// newLease starts the liveness poller and returns the lease. The poll interval
// is a third of the TTL, floored at one second, so an unrenewed lease's expiry
// is observed within roughly one interval plus the confirming truncation window
// the monitor waits out before declaring loss.
func newLease(client transport.LeaseClient, key string, id, ttlSeconds int64, granted time.Time) *lease {
	ttl := time.Duration(ttlSeconds) * time.Second
	l := &lease{
		client:    client,
		key:       key,
		id:        id,
		ttl:       ttl,
		heldUntil: granted.Add(ttl),
		done:      make(chan struct{}),
		stop:      make(chan struct{}),
	}
	interval := time.Duration(ttlSeconds/3) * time.Second
	if interval < time.Second {
		interval = time.Second
	}
	go l.monitor(interval)
	return l
}

// confirmHeld extends the known-held floor to d past the instant the confirming
// request was SENT — the reply came back later, and dating the confirmation from
// the send is what keeps the floor on the conservative side of the round trip.
// It never moves the floor backwards.
// It refuses, reporting false, once the lease has already ended: the deadline
// passed, Done is closed, and a confirmation arriving now is stale news about a
// lease its holder has already been told it lost.
func (l *lease) confirmHeld(sent time.Time, d time.Duration) bool {
	l.heldMu.Lock()
	defer l.heldMu.Unlock()
	// The deadline is the fact; lost is only the monitor's notice of it. Test
	// both, so a confirmation landing in the moment between the two cannot
	// resurrect a lease whose lifetime had already run out.
	if l.lost || !time.Now().Before(l.heldUntil) {
		return false
	}
	if until := sent.Add(d); until.After(l.heldUntil) {
		l.heldUntil = until
	}
	return true
}

// endIfDeadlinePassed closes Done when the confirmed lifetime has run out,
// reporting whether the lease is now over.
//
// heldUntil is a HARD deadline, and only a confirmation the server actually
// acknowledged ever moves it. A renewal still in flight does not, however
// likely it looks: a keepalive that stalls in transport and never reaches the
// server produces no extension there, so crediting it here would hold the
// ownership signal open past a lease the server had already expired and handed
// to somebody else. That is the one failure this signal exists to prevent, and
// it is worth more than the spurious fencing the strictness occasionally costs.
func (l *lease) endIfDeadlinePassed() bool {
	l.heldMu.Lock()
	if time.Now().Before(l.heldUntil) {
		l.heldMu.Unlock()
		return false
	}
	l.lost = true
	l.heldMu.Unlock()
	l.closeDone()
	return true
}

// end marks the lease over and closes Done, for a loss the server proved
// outright rather than one the deadline settled.
func (l *lease) end() {
	l.heldMu.Lock()
	l.lost = true
	l.heldMu.Unlock()
	l.closeDone()
}

// checkBudget is how long the monitor may let pass before it must weigh the
// deadline again: its usual interval, or whatever is left of the confirmed
// lifetime when that is shorter.
//
// It governs both the wait between polls and each lookup's own timeout, because
// either can carry ownership past the deadline on its own. A fixed poll cadence
// only lines up with the deadline by coincidence — a 7s lease polls every 2s, so
// the polls fall at 2s, 4s, 6s and 8s while ownership ends at 7s — and a lookup
// that hangs runs its whole budget regardless. Capping both at the deadline is
// what makes Done close when ownership actually ends rather than at whichever
// poll happens to notice.
func (l *lease) checkBudget(interval time.Duration) time.Duration {
	l.heldMu.Lock()
	remaining := time.Until(l.heldUntil)
	l.heldMu.Unlock()
	switch {
	case remaining < 0:
		return 0
	case remaining < interval:
		return remaining
	default:
		return interval
	}
}

// waitFor sleeps for d, reporting false if Release stopped the monitor first.
func (l *lease) waitFor(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-l.stop:
		return false
	case <-timer.C:
		return true
	}
}

// probe performs one liveness lookup, extending the known-held floor when the
// server reports time still to run. It reports true only when the server itself
// says the lease is absent — the one answer that proves loss outright.
func (l *lease) probe(timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	sent := time.Now()
	info, err := l.client.LeaseTimeToLive(ctx, l.id)
	cancel()
	if err != nil {
		// Only the server reporting the lease absent proves loss. Any other
		// error — a timeout, a connection reset — establishes nothing either
		// way, and is left to the known-held floor to adjudicate.
		//
		// Every transport now carries an absent lease across the seam as a
		// typed transport.LeaseNotFoundError/LeaseExpiredError (go-qrdb#169
		// finished #141 for the lease family: grpc and unix classify their
		// shared codes.NotFound by message text at the lease call sites, quic
		// does the same against its wire.StatusError payload, and mcp against
		// its tool-result text — mirroring the classification embedded already
		// carried, and REST carries for LeaseTimeToLive only: its Revoke and
		// KeepAlive still flatten a 404 into a generic RequestError, and it
		// emits no LeaseExpiredError at all because both conditions are the
		// same HTTP status). Typing only makes loss detection PROMPTER; it
		// is not what makes it correct. leaseAbsent still degrades safely
		// against an untyped error — errors.As simply does not match — so a
		// transport that somehow returned one would still be bounded by the
		// confirmed lifetime rather than mis-declared lost.
		return leaseAbsent(err)
	}
	if info != nil && info.RemainingTTL > 0 {
		// RemainingTTL is truncated to whole seconds server-side, so it is a
		// floor on the life left, not the figure itself. Trust exactly what it
		// says and no more: a lookup reporting 1s does not license believing
		// the lease held for a further whole TTL.
		_ = l.confirmHeld(sent, time.Duration(info.RemainingTTL)*time.Second)
	}
	return false
}

// monitor polls the lease and closes done when the lease is gone. It is the only
// place, besides Release, that closes done.
//
// A lookup is not the arbiter here; the known-held floor is. That is what keeps
// two opposite failures out of the signal at once. The server truncates
// RemainingTTL to whole seconds and floors a negative at zero
// (pkg/lease.remainingTTL), so a reading of 0 means anything from "expired" to
// "0.999s still to run" — a tick landing a millisecond late on a 2s lease reads
// 0 with almost a second left, and treating that as expiry told a holder
// renewing on a perfectly good cadence that it had lost a lease the server still
// held. Equally, a lookup that merely failed proves nothing, so a transport blip
// must not end a lease either. But neither can uncertainty postpone the signal
// forever: a holder that cannot reach the server cannot renew either, so its
// lease really is running out, and a fencing signal that never arrives is what
// puts a second holder on the key. Loss is therefore declared when the server
// proves it, or when the lease's own confirmed lifetime has run out — whichever
// comes first, and no sooner. Confirmed means ACKNOWLEDGED: work still in
// flight, however promising, never postpones the deadline, because a request
// that dies in transport extends nothing on the server either.
func (l *lease) monitor(interval time.Duration) {
	for {
		// Sleep until the next poll or the deadline, whichever comes first, so
		// that ownership ending is never something the monitor finds out about
		// one poll late.
		if !l.waitFor(l.checkBudget(interval)) {
			return // released
		}
		// The deadline is weighed BEFORE the lookup, never only after it: a
		// lookup takes time, and starting one on a lease whose ownership has
		// already ended would delay the signal by however long it ran.
		if l.endIfDeadlinePassed() {
			return
		}
		if l.probe(l.checkBudget(interval)) {
			l.end()
			return
		}
		// And again on the way out, because the lookup's budget is capped at
		// the deadline too: when it is that cap the lookup hit, this is the
		// check that ends the lease at the deadline itself.
		if l.endIfDeadlinePassed() {
			return
		}
	}
}

// Key returns the bound key.
func (l *lease) Key() string { return l.key }

// Renew resets the lease TTL via keepalive. A keepalive the server accepted is
// the strongest confirmation there is — it restored the full TTL — so it
// extends the known-held floor, which is what lets a holder renewing on a
// correct cadence ride out liveness lookups that time out or read as truncated
// zeroes. A failed keepalive confirms nothing and moves nothing.
func (l *lease) Renew(ctx context.Context) error {
	sent := time.Now()
	if _, err := l.client.LeaseKeepAlive(ctx, l.id); err != nil {
		return err
	}
	if !l.confirmHeld(sent, l.ttl) {
		// Ownership had already ended. The monitor may not have noticed yet, so
		// end it here too: a caller told its lease is lost and a sibling
		// goroutine fencing on Done must never be looking at different answers.
		// end is idempotent, so saying so twice costs nothing.
		l.end()
		return &LeaseLostError{Key: l.key}
	}
	return nil
}

// Release revokes the lease (deleting the bound key) and, once the server has
// acknowledged the revoke, stops the poller and closes Done. A revoke that
// fails — a timeout, say — leaves the lease exactly as it was: the monitor keeps
// running, Done stays open, the error is returned, and a later Release retries.
// Returning nil there would tell the caller the key was freed while the server
// still held it and contenders still saw LeaseHeldError until expiry.
//
// Release is idempotent AFTER it succeeds: a second call returns nil without
// touching the server. Concurrent calls serialize — the ones that arrive while a
// revoke is in flight wait for its outcome, and none returns nil unless a revoke
// actually succeeded or the server confirmed the lease is already gone.
func (l *lease) Release(ctx context.Context) error {
	l.releaseMu.Lock()
	defer l.releaseMu.Unlock()
	if l.released {
		return nil
	}
	if err := l.client.LeaseRevoke(ctx, l.id); err != nil && !leaseAbsent(err) {
		return err
	}
	l.released = true
	l.stopMonitor()
	// A released lease is over on the same terms as an expired one, so a
	// keepalive that lands afterwards is stale in the same way: Done is closed,
	// and Renew must not tell the caller it still holds a key it just gave up.
	l.end()
	return nil
}

// leaseAbsent reports whether a revoke error is the server saying it no longer
// has the lease. Not-found and expired both mean the binding — and with it the
// bound key — is already gone, which is the outcome Release asks for, so they
// count as a release rather than a failure to retry. Any other error leaves the
// lease possibly still held and is returned to the caller.
func leaseAbsent(err error) bool {
	var notFound *transport.LeaseNotFoundError
	if errors.As(err, &notFound) {
		return true
	}
	var expired *transport.LeaseExpiredError
	return errors.As(err, &expired)
}

// Done returns the channel closed when the lease is lost.
func (l *lease) Done() <-chan struct{} { return l.done }

func (l *lease) closeDone() {
	l.closeOnce.Do(func() { close(l.done) })
}

func (l *lease) stopMonitor() {
	l.stopOnce.Do(func() { close(l.stop) })
}

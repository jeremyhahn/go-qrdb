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

package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Writer is the minimal write capability Chain needs to stage a mutation into
// a state machine's apply batch: mirrors lease.Writer exactly, and for the
// same reason -- Chain's authoritative state is kept in memory (restored once
// from storage.Reader) and the apply path never reads it back, so Writer never
// needs a Get.
type Writer interface {
	// Set stages key = value.
	Set(key, value []byte) error

	// Delete stages the removal of key.
	Delete(key []byte) error
}

// headState is the in-memory half of one tenant's head record: seq is the
// newest assigned sequence, lo is the lowest SURVIVING sequence (events below
// lo have been pruned), and digest is the chain's running hash as of seq.
type headState struct {
	seq    uint64
	lo     uint64
	digest [32]byte
}

// Chain is the in-memory authority for every tenant's replicated audit hash
// chain, backed by the raw keyspace table this file's sibling (table.go)
// defines. It is the audit equivalent of lease.Lessor: restored once from
// storage at state-machine open (and after snapshot recovery), consulted and
// updated only during Apply, and never read back from the batch it writes
// into.
//
// Determinism is the entire point, for BOTH of Chain's mutations. Append
// assigns the next sequence number and digest from the Chain's own current
// head -- never from a value a client proposed. Prune computes the range it
// deletes from the Chain's own current head alone -- never from a storage
// read -- which is what makes it immune to how many committed entries one
// Raft Update call happens to pack together: a read-the-committed-range
// Prune (this package's first version) answered a DIFFERENT result for
// `[append, append, append, prune(3)]` applied as one batch versus four,
// because the batch that performed the prior appends was not yet visible to
// a read issued from the SAME Update call on some code paths and was on
// others. Reading only c.heads, which every Append in the SAME Update call
// has already updated in memory before Prune (if any) runs, removes the
// dependence on batching entirely: Prune and Append agree with each other and
// with every replica regardless of packing. See
// docs/audit/architecture.md#the-hash-chain-stays-linear-under-raft-because-it-is-assigned-at-apply-time.
//
// Attach exactly one Chain per state machine instance, unconditionally --
// SetAuditChain(NewChain()) belongs beside SetLessor(lease.NewLessor()) in
// every composition, whether or not replicated audit is enabled for any
// tenant on this node. Opt-in governs whether anything ever PROPOSES
// OpAuditAppend (pkg/service's entitlement gate, phase 2); it must never
// govern whether a replica CAN APPLY one it receives, because that is a
// property of the binary, not of a per-node config choice, and two replicas
// of the same group running the same binary must apply the identical
// opcode identically or diverge.
//
// Chain is safe for concurrent use. The lock exists for Restore running
// concurrently with a resync after an apply fault; ordinary Apply is already
// serialized per shard by Raft and never calls Append or Prune concurrently
// with themselves or each other.
type Chain struct {
	mu    sync.Mutex
	heads map[string]headState
}

// NewChain returns a Chain with no tenant heads. Call Restore before Apply
// reaches it, exactly as a fresh Lessor must be restored before Grant.
func NewChain() *Chain {
	return &Chain{heads: make(map[string]headState)}
}

// Restore rebuilds the in-memory head table from the durable table in r. Call
// at state-machine open and after snapshot recovery -- the log a recovering
// replica never sees is exactly why the head cannot be carried over from
// memory across either event, the same reason lease.Lessor.Restore exists.
func (c *Chain) Restore(r storage.Reader) error {
	heads := make(map[string]headState, len(c.heads))
	it := r.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte(headPrefix),
		UpperBound: prefixEnd([]byte(headPrefix)),
	})
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		// key is captured before any possible it.Close() below: a
		// storage.Iterator may invalidate the slice Key() returns once
		// closed (the in-memory backend does), so reading it again inside
		// the error path below would silently report an empty Key.
		key := it.Key()
		tenantID := string(key[len(headPrefix):])
		seq, lo, digest, decOK := decodeHead(it.Value())
		if !decOK {
			_ = it.Close()
			return &CorruptTableError{Key: string(key), Reason: "malformed head record"}
		}
		heads[tenantID] = headState{seq: seq, lo: lo, digest: digest}
	}
	if err := it.Error(); err != nil {
		_ = it.Close()
		return &TableReadError{Err: err}
	}
	if err := it.Close(); err != nil {
		return &TableReadError{Err: err}
	}
	c.mu.Lock()
	c.heads = heads
	c.mu.Unlock()
	return nil
}

// validateTenantID refuses a tenant id containing '/', which would let a
// crafted id carve a second segment inside another tenant's range in the raw
// table's concatenated keys (table.go's eventKey/headKey).
func validateTenantID(tenantID string) error {
	if strings.Contains(tenantID, "/") {
		return &InvalidTenantIDError{TenantID: tenantID}
	}
	return nil
}

// Append assigns tenantID's next sequence number and digest from the Chain's
// current in-memory head, folds the event's canonical encoding into the
// chain, and stages the event record and the updated head record into w. It
// returns the assigned sequence.
//
// ev.PrevHash is set by Append; any value the caller supplied is overwritten
// -- the same guarantee a client gets no say over a lease ID. The digest
// folds over the MARSHALED event, PrevHash included, so the link itself is
// covered by the next entry's digest and not merely the payload beside it.
//
// ev.Timestamp is NOT defaulted here, deliberately. A caller might expect
// "zero means now" -- audit.AuditEvent's own doc comment says exactly that,
// for the node-local file sink, where "now" is read once by the one process
// writing the line. Apply runs on every replica of the group, and
// time.Now() is not something two processes ever agree on: filling a zero
// Timestamp at apply time would fold a different value into each replica's
// digest for the SAME committed entry, which is precisely the divergence
// this whole design exists to prevent. "Now" is decided exactly once, by
// whichever caller proposes the command -- the same moment the proposer
// already reads a wall clock to set ev.Timestamp before encoding, mirroring
// how a retention cutoff is embedded in the command rather than read
// independently by every replica that later applies it
// (docs/audit/architecture.md#retention-and-pruning-replicated-deterministic-and-it-does-not-break-the-chain).
// A zero Timestamp here is refused rather than silently given a value only
// this replica would agree with. protocol.EncodeAuditAppend already refuses
// one before a command is even proposed; this is the apply-time backstop for
// a command built some other way.
//
// ev.TenantID is checked against tenantID: empty is left alone (the
// platform-event case Query's Untenanted matching depends on), a value
// that AGREES is left alone, and a value that DISAGREES is refused
// (*TenantMismatchError) rather than overwritten -- see that error's doc
// comment for why silently "stamping" tenantID over a conflicting value
// would be worse than refusing.
func (c *Chain) Append(w Writer, tenantID string, ev AuditEvent) (seq uint64, err error) {
	if err := validateTenantID(tenantID); err != nil {
		return 0, err
	}
	if ev.Timestamp.IsZero() {
		return 0, &MissingTimestampError{}
	}
	if ev.TenantID != "" && ev.TenantID != tenantID {
		return 0, &TenantMismatchError{EventTenantID: ev.TenantID, RoutingTenantID: tenantID}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	head := c.heads[tenantID]
	seq = head.seq + 1
	lo := head.lo
	if head.seq == 0 {
		ev.PrevHash = ""
		lo = 1 // the first live sequence is 1; nothing below it ever existed.
	} else {
		ev.PrevHash = "sha256:" + hex.EncodeToString(head.digest[:])
	}

	body, encErr := json.Marshal(ev)
	if encErr != nil {
		return 0, &EncodeEventError{Err: encErr}
	}
	folded := make([]byte, 0, len(head.digest)+len(body))
	folded = append(folded, head.digest[:]...)
	folded = append(folded, body...)
	digest := sha256.Sum256(folded)

	if setErr := w.Set(eventKey(tenantID, seq), body); setErr != nil {
		return 0, &TableWriteError{Err: setErr}
	}
	if setErr := w.Set(headKey(tenantID), encodeHead(seq, lo, digest)); setErr != nil {
		return 0, &TableWriteError{Err: setErr}
	}

	c.heads[tenantID] = headState{seq: seq, lo: lo, digest: digest}
	return seq, nil
}

// Prune deletes tenantID's events in [lo, upper), where lo is the Chain's own
// current low-watermark and upper is cutoff clamped to the Chain's own
// current seq (never deleting the newest event, so a surviving chain always
// keeps at least one record to verify against) and to lo+MaxPrunePerCommand
// (bounding how many deletes one command stages). Both bounds come from
// c.heads alone -- Prune never reads storage -- which is what makes its
// result independent of how many committed entries the apply batch
// containing it happens to hold; see Chain's own doc comment. It returns the
// number of events deleted, which may be less than requested if the
// per-command cap applied; the caller issues Prune again, with the same
// cutoff, to continue.
//
// A tenant with no chain yet (no Append has ever run) or already pruned up
// to its own head is a no-op, not an error.
func (c *Chain) Prune(w Writer, tenantID string, cutoff uint64) (deleted int, err error) {
	if err := validateTenantID(tenantID); err != nil {
		return 0, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	head, ok := c.heads[tenantID]
	if !ok || head.seq == 0 {
		return 0, nil
	}
	upper := cutoff
	if upper > head.seq {
		upper = head.seq
	}
	if upper > head.lo+MaxPrunePerCommand {
		upper = head.lo + MaxPrunePerCommand
	}
	if upper <= head.lo {
		return 0, nil
	}

	for seq := head.lo; seq < upper; seq++ {
		if delErr := w.Delete(eventKey(tenantID, seq)); delErr != nil {
			return int(seq - head.lo), &TableWriteError{Err: delErr}
		}
	}
	deleted = int(upper - head.lo)

	newHead := headState{seq: head.seq, lo: upper, digest: head.digest}
	if setErr := w.Set(headKey(tenantID), encodeHead(newHead.seq, newHead.lo, newHead.digest)); setErr != nil {
		return deleted, &TableWriteError{Err: setErr}
	}
	c.heads[tenantID] = newHead
	return deleted, nil
}

// Query answers f from r, a point-in-time range read over tenantID's event
// range. The caller is responsible for confinement
// (tenant.ConfineAuditFilter) before this is reached; Query trusts tenantID
// completely and never reads another tenant's range -- there is physically no
// way to, since the range bounds passed to the iterator never leave
// tenantID's own key prefix.
func (c *Chain) Query(r storage.Reader, tenantID string, f Filter) (*Page, error) {
	if err := validateTenantID(tenantID); err != nil {
		return nil, err
	}

	limit := normalizeLimit(f.Limit)
	startSeq := uint64(0)
	if f.Cursor != "" {
		parsed, parseErr := strconv.ParseUint(f.Cursor, 10, 64)
		if parseErr != nil {
			return nil, &InvalidCursorError{Cursor: f.Cursor}
		}
		startSeq = parsed
	}

	it := r.NewIterator(&storage.IteratorOptions{
		LowerBound: eventRangeStart(tenantID, startSeq),
		UpperBound: eventRangeEnd(tenantID),
	})
	defer func() { _ = it.Close() }()

	events := make([]AuditEvent, 0, limit)
	var next string
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		var ev AuditEvent
		if err := json.Unmarshal(it.Value(), &ev); err != nil {
			return nil, &CorruptTableError{Key: string(it.Key()), Reason: "malformed event record"}
		}
		// The existing file-sink matchesFilter(ev, f) already implements the
		// exact TenantID/Untenanted confinement semantics this needs (see
		// chain_file_logger.go); scoping f.TenantID to the range this Query
		// is already reading reuses it rather than re-deriving a second copy
		// of the same rule.
		scoped := f
		scoped.TenantID = tenantID
		if !matchesFilter(ev, scoped) {
			continue
		}
		if len(events) == limit {
			seq, seqOK := eventSeq(it.Key(), tenantID)
			if !seqOK {
				return nil, &CorruptTableError{Key: string(it.Key()), Reason: "malformed event key"}
			}
			next = strconv.FormatUint(seq, 10)
			break
		}
		events = append(events, ev)
	}
	if err := it.Error(); err != nil {
		return nil, &TableReadError{Err: err}
	}
	return &Page{Events: events, Next: next, Total: -1}, nil
}

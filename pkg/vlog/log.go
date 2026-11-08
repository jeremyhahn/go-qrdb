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

// Package vlog is the replicated, append-only log a state machine keeps in
// its raw keyspace: the apply-time head, the table it is stored in and the
// restore-then-apply contract. pkg/audit's replicated chain is its first
// instance; docs/databases/architecture.md#verifiable-log is the design.
package vlog

import (
	"strings"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Writer is the minimal write capability Chain needs to stage a mutation into
// a state machine's apply batch: mirrors lease.Writer exactly, and for the
// same reason -- Chain's authoritative state is kept in memory (restored once
// from storage.Reader) and the apply path never reads it back, so Writer never
// needs a Get. The one apply-time read, a named log's Checkpoint computing
// the root at an earlier size, takes a storage.Reader of its own and reads
// through the nodes the current Update call has staged (named.go).
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
// SetLogs(NewChain()) belongs beside SetLessor(lease.NewLessor()) in
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
//
// Chain is also the state machine's registry of named logs (named.go), keyed
// by (tenant, database, log): the same restore-then-apply contract, one
// in-memory head per log, and the tree nodes staged by the current Update
// call so that apply reads through them rather than depending on whether
// the batch they were staged into is visible yet.
type Chain struct {
	mu    sync.Mutex
	heads map[string]headState
	logs  map[LogID]logState

	// staged holds every tree node and checkpoint record staged since
	// BeginUpdate, by key. Apply reads it before storage.
	staged map[string][]byte
}

// NewChain returns a Chain with no tenant heads and no named logs. Call
// Restore before Apply reaches it, exactly as a fresh Lessor must be
// restored before Grant.
func NewChain() *Chain {
	return &Chain{
		heads:  make(map[string]headState),
		logs:   make(map[LogID]logState),
		staged: make(map[string][]byte),
	}
}

// Restore rebuilds the in-memory head table and every named log's head from
// the durable tables in r, and drops the staged nodes of any Update call
// that did not complete. Call
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
	logs, err := restoreLogs(r)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.heads = heads
	c.logs = logs
	c.staged = make(map[string][]byte)
	c.mu.Unlock()
	return nil
}

// ValidateTenantID refuses a tenant id containing '/', which would let a
// crafted id carve a second segment inside another tenant's range in the raw
// table's concatenated keys (table.go's EventKey/headKey).
func ValidateTenantID(tenantID string) error {
	if strings.Contains(tenantID, "/") {
		return &InvalidTenantIDError{TenantID: tenantID}
	}
	return nil
}

// Encoder builds the record Append stores for the next sequence from the
// head it extends: prev is that head's digest, zero before the first record,
// and genesis is true for the first record. It returns the record body and
// the head digest after it. An error aborts the Append before anything is
// staged, and Append returns it unchanged.
type Encoder func(prev [32]byte, genesis bool) (body []byte, digest [32]byte, err error)

// Append assigns tenantID's next sequence number from the Chain's current
// in-memory head, lets encode build the record and the new digest from that
// head, and stages the record and the updated head record into w. It returns
// the assigned sequence. The head is read and advanced under one lock, so
// the sequence encode sees is the one the record is stored at.
func (c *Chain) Append(w Writer, tenantID string, encode Encoder) (seq uint64, err error) {
	if err := ValidateTenantID(tenantID); err != nil {
		return 0, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Once the tenant's audit instance exists, an event this opcode carries
	// is its next leaf too; before, Append writes no tree node.
	return c.appendEvent(w, tenantID, encode, false)
}

// appendLinear stages the next record of tenantID's linear chain and its new
// head, and returns that head and the record's bytes without installing the
// head: the caller does, once everything else the command writes is staged.
// c.mu must be held.
func (c *Chain) appendLinear(w Writer, tenantID string, encode Encoder) (headState, []byte, error) {
	head := c.heads[tenantID]
	seq := head.seq + 1
	lo := head.lo
	if head.seq == 0 {
		lo = 1 // the first live sequence is 1; nothing below it ever existed.
	}

	body, digest, encErr := encode(head.digest, head.seq == 0)
	if encErr != nil {
		return headState{}, nil, encErr
	}

	if setErr := w.Set(EventKey(tenantID, seq), body); setErr != nil {
		return headState{}, nil, &TableWriteError{Err: setErr}
	}
	if setErr := w.Set(headKey(tenantID), encodeHead(seq, lo, digest)); setErr != nil {
		return headState{}, nil, &TableWriteError{Err: setErr}
	}
	return headState{seq: seq, lo: lo, digest: digest}, body, nil
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
	if err := ValidateTenantID(tenantID); err != nil {
		return 0, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.pruneLocked(w, tenantID, cutoff)
}

// pruneLocked is Prune with c.mu held. Once tenantID's audit instance exists,
// it is that instance's ExpireBodies (docs/databases/architecture.md §7): the
// bodies go, the tree stays, and the instance's low-watermark advances to the
// first leaf whose body survives.
func (c *Chain) pruneLocked(w Writer, tenantID string, cutoff uint64) (deleted int, err error) {
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
		if delErr := w.Delete(EventKey(tenantID, seq)); delErr != nil {
			return int(seq - head.lo), &TableWriteError{Err: delErr}
		}
	}
	deleted = int(upper - head.lo)

	newHead := headState{seq: head.seq, lo: upper, digest: head.digest}
	if setErr := w.Set(headKey(tenantID), encodeHead(newHead.seq, newHead.lo, newHead.digest)); setErr != nil {
		return deleted, &TableWriteError{Err: setErr}
	}

	id := AuditLogID(tenantID)
	if st, exists := c.logs[id]; exists && upper > st.treeStart {
		lo := min(upper-st.treeStart, st.size)
		if lo > st.lo {
			if setErr := w.Set(logHeadKey(id), encodeLogHead(st.size, lo, st.hashes)); setErr != nil {
				return deleted, &TableWriteError{Err: setErr}
			}
			st.lo = lo
			c.logs[id] = st
		}
	}
	c.heads[tenantID] = newHead
	return deleted, nil
}

// Head returns tenantID's current in-memory head: seq is the newest assigned
// sequence (0 for a tenant with no chain), lo the lowest surviving sequence
// and digest the chain's running hash as of seq.
func (c *Chain) Head(tenantID string) (seq, lo uint64, digest [32]byte) {
	c.mu.Lock()
	head := c.heads[tenantID]
	c.mu.Unlock()
	return head.seq, head.lo, head.digest
}

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

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// Chain is the replicated audit hash chain: the audit instance of
// vlog.Chain. vlog.Chain owns the apply-time head, the raw keyspace table and
// the restore-then-apply contract; Chain adds what makes a record an audit
// event -- event validation, its JSON encoding, the PrevHash link and the
// linear SHA-256 fold -- and answers Query and Verify over the same table.
// See vlog.Chain for the determinism and attachment rules, which hold for
// Chain unchanged: attach exactly one per state machine instance,
// unconditionally.
type Chain struct {
	log *vlog.Chain
}

// NewChain returns a Chain with no tenant heads. Call Restore before Apply
// reaches it, exactly as a fresh Lessor must be restored before Grant.
func NewChain() *Chain {
	return ChainOn(vlog.NewChain())
}

// ChainOn returns the audit Chain over log, the state machine's log registry,
// so the audit instance and its named logs share one restore and one
// per-Update staging. It returns nil for a nil log: a state machine with no
// registry has no audit chain either.
func ChainOn(log *vlog.Chain) *Chain {
	if log == nil {
		return nil
	}
	return &Chain{log: log}
}

// Restore rebuilds the in-memory head table from the durable table in r; see
// vlog.Chain.Restore.
func (c *Chain) Restore(r storage.Reader) error {
	return c.log.Restore(r)
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
func (c *Chain) Append(w vlog.Writer, tenantID string, ev AuditEvent) (seq uint64, err error) {
	if err := validateEvent(tenantID, ev); err != nil {
		return 0, err
	}
	return c.log.Append(w, tenantID, linkEvent(ev))
}

// AppendToInstance applies one protocol.OpVerifiableLog append to tenantID's
// audit instance (vlog.AuditLogID): each entry is a marshaled AuditEvent,
// validated and linked exactly as Append does, and lands both on the linear
// chain and as the instance's next leaf, the first creating the instance at
// its sequence S₀ (docs/databases/architecture.md §7). It returns the last
// event's sequence. A malformed entry refuses the whole command before
// anything is written.
func (c *Chain) AppendToInstance(w vlog.Writer, tenantID string, entries [][]byte) (seq uint64, err error) {
	if err := vlog.ValidateTenantID(tenantID); err != nil {
		return 0, err
	}
	if len(entries) == 0 || len(entries) > vlog.MaxAppendPerCommand {
		return 0, &vlog.AppendBatchError{Count: len(entries)}
	}
	events := make([]AuditEvent, len(entries))
	for i, e := range entries {
		if err := json.Unmarshal(e, &events[i]); err != nil {
			return 0, &MalformedEventError{Err: err}
		}
		if err := validateEvent(tenantID, events[i]); err != nil {
			return 0, err
		}
	}
	for _, ev := range events {
		if seq, err = c.log.AppendAudit(w, tenantID, linkEvent(ev)); err != nil {
			return 0, err
		}
	}
	return seq, nil
}

// validateEvent checks ev can be appended to tenantID's chain.
func validateEvent(tenantID string, ev AuditEvent) error {
	if err := vlog.ValidateTenantID(tenantID); err != nil {
		return err
	}
	if ev.Timestamp.IsZero() {
		return &MissingTimestampError{}
	}
	if ev.TenantID != "" && ev.TenantID != tenantID {
		return &TenantMismatchError{EventTenantID: ev.TenantID, RoutingTenantID: tenantID}
	}
	return nil
}

// linkEvent returns the encoder that sets ev's PrevHash to the chain's
// current digest and folds its marshaled bytes into the next.
func linkEvent(ev AuditEvent) vlog.Encoder {
	return func(prev [32]byte, genesis bool) ([]byte, [32]byte, error) {
		if genesis {
			ev.PrevHash = ""
		} else {
			ev.PrevHash = "sha256:" + hex.EncodeToString(prev[:])
		}

		body, encErr := json.Marshal(ev)
		if encErr != nil {
			return nil, [32]byte{}, &EncodeEventError{Err: encErr}
		}
		folded := make([]byte, 0, len(prev)+len(body))
		folded = append(folded, prev[:]...)
		folded = append(folded, body...)
		return body, sha256.Sum256(folded), nil
	}
}

// Prune deletes tenantID's events below cutoff, from the Chain's own head
// alone; see vlog.Chain.Prune.
func (c *Chain) Prune(w vlog.Writer, tenantID string, cutoff uint64) (deleted int, err error) {
	return c.log.Prune(w, tenantID, cutoff)
}

// Query answers f from r, a point-in-time range read over tenantID's event
// range. The caller is responsible for confinement
// (tenant.ConfineAuditFilter) before this is reached; Query trusts tenantID
// completely and never reads another tenant's range -- there is physically no
// way to, since the range bounds passed to the iterator never leave
// tenantID's own key prefix.
func (c *Chain) Query(r storage.Reader, tenantID string, f Filter) (*Page, error) {
	if err := vlog.ValidateTenantID(tenantID); err != nil {
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
		LowerBound: vlog.EventRangeStart(tenantID, startSeq),
		UpperBound: vlog.EventRangeEnd(tenantID),
	})
	defer func() { _ = it.Close() }()

	events := make([]AuditEvent, 0, limit)
	var next string
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		var ev AuditEvent
		if err := json.Unmarshal(it.Value(), &ev); err != nil {
			return nil, &vlog.CorruptTableError{Key: string(it.Key()), Reason: "malformed event record"}
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
			seq, seqOK := vlog.EventSeq(it.Key(), tenantID)
			if !seqOK {
				return nil, &vlog.CorruptTableError{Key: string(it.Key()), Reason: "malformed event key"}
			}
			next = strconv.FormatUint(seq, 10)
			break
		}
		events = append(events, ev)
	}
	if err := it.Error(); err != nil {
		return nil, &vlog.TableReadError{Err: err}
	}
	return &Page{Events: events, Next: next, Total: -1}, nil
}

// ChainContinuityReport is the outcome of verifying one tenant's replicated
// audit chain's hash continuity (Chain.Verify). Verified is the only field
// a caller MUST check, mirroring pkg/audit.VerificationReport for the
// node-local file chain -- the two are deliberately distinct types, because
// this one answers for a single tenant's REPLICATED chain and carries no
// file/rotation concepts at all.
type ChainContinuityReport struct {
	// Verified is true only if every surviving event folds, in sequence,
	// into the chain's own authoritative head digest.
	Verified bool
	// Reason is a human-readable description of the break; empty when
	// Verified.
	Reason string
	// Records is the number of surviving events folded (head.seq - lo + 1),
	// zero for a tenant with no chain at all.
	Records int64
	// Head is the highest surviving sequence number.
	Head uint64
	// Low is the lowest surviving sequence number.
	Low uint64
	// Genesis is true when Low == 1: the walk reached the chain's true
	// origin rather than a retention-pruned tail. Exactly as for the
	// node-local file chain, a pruned prefix is indistinguishable from a
	// deleted one by the hash alone, so a pruned tenant chain always
	// verifies with Genesis: false rather than being refused.
	Genesis bool
}

// Verify recomputes tenantID's replicated hash chain over every surviving
// event in r -- from the chain's own low-watermark through its head -- and
// reports whether the fold reaches the AUTHORITATIVE head digest this Chain
// already holds in memory (restored from the head record every replica
// applies identically). Unlike the node-local file chain, there is no
// externally-supplied seed to trust: the first surviving event's PrevHash is
// taken as the chain's own claim for what preceded it (exactly as a pruned
// node-local trail reports Genesis: false rather than refusing), but the
// TERMINAL digest is checked against replicated state no caller could have
// forged, because every replica derived it identically at apply time.
//
// A tenant with no chain at all (head.seq == 0) verifies trivially: Verified
// true, Records 0, Genesis true.
func (c *Chain) Verify(r storage.Reader, tenantID string) (*ChainContinuityReport, error) {
	if err := vlog.ValidateTenantID(tenantID); err != nil {
		return nil, err
	}

	var head struct {
		seq, lo uint64
		digest  [32]byte
	}
	head.seq, head.lo, head.digest = c.log.Head(tenantID)

	if head.seq == 0 {
		return &ChainContinuityReport{Verified: true, Genesis: true}, nil
	}

	it := r.NewIterator(&storage.IteratorOptions{
		LowerBound: vlog.EventRangeStart(tenantID, head.lo),
		UpperBound: vlog.EventRangeEnd(tenantID),
	})
	defer func() { _ = it.Close() }()

	var digest [32]byte
	seen := uint64(0)
	expectedSeq := head.lo
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		seq, seqOK := vlog.EventSeq(it.Key(), tenantID)
		if !seqOK {
			return nil, &vlog.CorruptTableError{Key: string(it.Key()), Reason: "malformed event key"}
		}
		if seq != expectedSeq {
			return &ChainContinuityReport{
				Verified: false,
				Reason:   "missing sequence " + strconv.FormatUint(expectedSeq, 10),
				Records:  int64(seen),
				Head:     head.seq,
				Low:      head.lo,
				Genesis:  head.lo == 1,
			}, nil
		}

		body := it.Value()
		var ev AuditEvent
		if err := json.Unmarshal(body, &ev); err != nil {
			return nil, &vlog.CorruptTableError{Key: string(it.Key()), Reason: "malformed event record"}
		}

		if seq == head.lo {
			// The chain's own claim for what preceded the surviving range.
			// At genesis (lo == 1) this must be empty, matching Append's own
			// zero-value seed; a pruned tenant (lo > 1) names a digest whose
			// preimage was deleted by retention and so cannot be
			// independently checked, exactly as a retention-pruned
			// node-local trail reports Genesis: false rather than refusing.
			seed, ok := decodeReplicatedDigest(ev.PrevHash)
			if !ok {
				return &ChainContinuityReport{
					Verified: false,
					Reason:   "malformed seed at sequence " + strconv.FormatUint(seq, 10),
					Records:  int64(seen),
					Head:     head.seq,
					Low:      head.lo,
					Genesis:  head.lo == 1,
				}, nil
			}
			if head.lo == 1 && ev.PrevHash != "" {
				return &ChainContinuityReport{
					Verified: false,
					Reason:   "genesis event carries a non-empty PrevHash",
					Records:  int64(seen),
					Head:     head.seq,
					Low:      head.lo,
					Genesis:  true,
				}, nil
			}
			digest = seed
		} else {
			want := "sha256:" + hex.EncodeToString(digest[:])
			if ev.PrevHash != want {
				return &ChainContinuityReport{
					Verified: false,
					Reason:   "broken link at sequence " + strconv.FormatUint(seq, 10),
					Records:  int64(seen),
					Head:     head.seq,
					Low:      head.lo,
					Genesis:  head.lo == 1,
				}, nil
			}
		}

		folded := make([]byte, 0, len(digest)+len(body))
		folded = append(folded, digest[:]...)
		folded = append(folded, body...)
		digest = sha256.Sum256(folded)

		seen++
		expectedSeq++
	}
	if err := it.Error(); err != nil {
		return nil, &vlog.TableReadError{Err: err}
	}
	if seen != head.seq-head.lo+1 {
		return &ChainContinuityReport{
			Verified: false,
			Reason:   "missing sequence " + strconv.FormatUint(expectedSeq, 10),
			Records:  int64(seen),
			Head:     head.seq,
			Low:      head.lo,
			Genesis:  head.lo == 1,
		}, nil
	}

	want := "sha256:" + hex.EncodeToString(head.digest[:])
	got := "sha256:" + hex.EncodeToString(digest[:])
	if got != want {
		return &ChainContinuityReport{
			Verified: false,
			Reason:   "head digest mismatch",
			Records:  int64(seen),
			Head:     head.seq,
			Low:      head.lo,
			Genesis:  head.lo == 1,
		}, nil
	}

	return &ChainContinuityReport{
		Verified: true,
		Records:  int64(seen),
		Head:     head.seq,
		Low:      head.lo,
		Genesis:  head.lo == 1,
	}, nil
}

// decodeReplicatedDigest decodes a replicated-chain PrevHash value --
// "" (genesis) or "sha256:<hex>" -- into its raw digest. Unlike the
// node-local file chain's PrevHash (bare hex, no prefix), Chain.Append
// always writes the "sha256:" prefix, so Verify must strip exactly that one.
func decodeReplicatedDigest(prevHash string) ([sha256.Size]byte, bool) {
	var out [sha256.Size]byte
	if prevHash == "" {
		return out, true
	}
	const prefix = "sha256:"
	if !strings.HasPrefix(prevHash, prefix) {
		return out, false
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(prevHash, prefix))
	if err != nil || len(raw) != sha256.Size {
		return out, false
	}
	copy(out[:], raw)
	return out, true
}

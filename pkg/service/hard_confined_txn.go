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

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// opsOrComparesTouchHardConfined reports whether any Compare or Op in a
// proposed transaction or batch names a key, or a range, reaching into the
// hard-confined namespace (see hard_confined_keys.go) -- the "txn compare"
// surface: a caller could otherwise use a transaction's compare
// (binary-searching the comparison result over repeated attempts) or a
// write disguised as a transaction op to read or corrupt the cluster CA's
// key material without ever calling Get.
func opsOrComparesTouchHardConfined(compares []Compare, opSets ...[]Op) bool {
	for _, c := range compares {
		if isHardConfinedKey(string(c.Key)) {
			return true
		}
	}
	for _, ops := range opSets {
		for _, op := range ops {
			if len(op.RangeEnd) == 0 {
				if isHardConfinedKey(string(op.Key)) {
					return true
				}
				continue
			}
			if hardConfinedRangeOverlaps(string(op.Key), string(op.RangeEnd)) {
				return true
			}
		}
	}
	return false
}

// hardConfinedTxn wraps a TransactionServicer, refusing any Execute /
// ExecuteWithMode whose compares or operations touch the hard-confined
// namespace before the transaction is ever proposed. Unlike a plain
// Range, a transaction's compare has no result to filter -- the compare
// ITSELF is the leak (a caller can binary-search a value by repeating
// compares with different operands) -- so this is refused outright,
// never filtered.
//
// ExecuteEntity is NOT promoted through embedding: TransactionService's
// ExecuteEntity proposes its encoded ops directly against the host, never
// calling this interface's Execute/ExecuteWithMode, so embedding would
// bypass the refusal above entirely. Its compares and ops are checked the
// same way, after composing each entity key from EntityType and ID.
type hardConfinedTxn struct {
	TransactionServicer
}

// entityTxnTouchesHardConfined reports whether any entity-typed compare or
// operation in req composes a hard-confined key, mirroring
// opsOrComparesTouchHardConfined for TransactionEntityRequest's shape.
func entityTxnTouchesHardConfined(req *TransactionEntityRequest) bool {
	if req == nil {
		return false
	}
	for _, c := range req.Compares {
		if isHardConfinedKey(entityKey(c.EntityType, c.ID)) {
			return true
		}
	}
	for _, op := range req.Success {
		if isHardConfinedKey(entityKey(op.EntityType, op.ID)) {
			return true
		}
	}
	for _, op := range req.Failure {
		if isHardConfinedKey(entityKey(op.EntityType, op.ID)) {
			return true
		}
	}
	return false
}

// NewHardConfinedTransaction wraps svc, or returns nil unchanged so a
// resolver that found no service configured for a scope keeps reporting
// that rather than a wrapped nil satisfying the interface.
func NewHardConfinedTransaction(svc TransactionServicer) TransactionServicer {
	if svc == nil {
		return nil
	}
	return &hardConfinedTxn{TransactionServicer: svc}
}

func (h *hardConfinedTxn) Execute(ctx context.Context, compares []Compare, successOps, failureOps []Op) (*TransactionResponse, error) {
	if opsOrComparesTouchHardConfined(compares, successOps, failureOps) {
		return nil, &HardConfinedKeyError{Key: hardConfinedPrefixesLabel}
	}
	return h.TransactionServicer.Execute(ctx, compares, successOps, failureOps)
}

func (h *hardConfinedTxn) ExecuteWithMode(ctx context.Context, compares []Compare, successOps, failureOps []Op, mode writemode.Mode) (*TransactionResponse, error) {
	if opsOrComparesTouchHardConfined(compares, successOps, failureOps) {
		return nil, &HardConfinedKeyError{Key: hardConfinedPrefixesLabel}
	}
	return h.TransactionServicer.ExecuteWithMode(ctx, compares, successOps, failureOps, mode)
}

func (h *hardConfinedTxn) ExecuteEntity(ctx context.Context, req *TransactionEntityRequest) (*TransactionEntityResponse, error) {
	if entityTxnTouchesHardConfined(req) {
		return nil, &HardConfinedKeyError{Key: hardConfinedPrefixesLabel}
	}
	return h.TransactionServicer.ExecuteEntity(ctx, req)
}

// hardConfinedBatch wraps a BatchServicer, refusing any Execute /
// ExecuteWithMode whose operations touch the hard-confined namespace
// before the batch is ever proposed.
//
// ExecuteEntity is NOT promoted through embedding, for the same reason
// TransactionServicer.ExecuteEntity is overridden on hardConfinedTxn: it
// proposes directly against the host rather than calling this interface's
// Execute/ExecuteWithMode.
//
// Import is the one surface this wrapper cannot confine from the outside:
// it parses a caller-supplied file into rows and proposes the resulting
// encoded ops directly, the same way ExecuteEntity does, so there is no
// "ops" value for Execute's own check to inspect before Import has
// already built and proposed them. NewHardConfinedBatch instead installs a
// row-key guard on the wrapped value through ImportKeyGuardSetter (when it
// satisfies that interface), which importRecords consults per row before a
// write for a hard-confined key is ever proposed. Import itself still gets
// an explicit override below -- a thin passthrough when the guard
// installed, an outright refusal when it could not -- so it is not
// silently promoted through embedding the way it would be if this type
// relied on the embedded BatchServicer alone; the confinement happens one
// layer down, at the proposal site, which is where Import's own
// vulnerability lives.
type hardConfinedBatch struct {
	BatchServicer

	// importGuardInstalled records whether NewHardConfinedBatch was able to
	// install the row-key guard on the wrapped value. false for any
	// BatchServicer that does not satisfy ImportKeyGuardSetter -- a test
	// double with no confinement needs of its own, or a future alternate
	// implementation that forgot to -- in which case Import has no way to
	// confine itself and must refuse rather than import unguarded.
	importGuardInstalled bool
}

// ImportKeyGuardSetter is satisfied by a BatchServicer that can accept an
// externally-supplied row-key guard for Import to consult per row before
// proposing a write. *BatchService is the one production implementation;
// NewHardConfinedBatch installs the hard-confinement guard through this
// interface when the wrapped value satisfies it, so a future alternate
// BatchServicer implementation opts in the same way, rather than this
// package needing to know its concrete type. A BatchServicer that does not
// satisfy it gets Import refused outright (see
// HardConfinedImportGuardUnavailableError): there is no other way to
// confine its writes.
type ImportKeyGuardSetter interface {
	// SetImportKeyGuard installs (or clears, with nil) the guard Import
	// consults for each row's storage key before proposing its write.
	SetImportKeyGuard(guard func(key string) error)
}

// NewHardConfinedBatch wraps svc, or returns nil unchanged so a resolver
// that found no service configured for a scope keeps reporting that
// rather than a wrapped nil satisfying the interface. When svc satisfies
// ImportKeyGuardSetter, it also installs a row-key guard so Import --
// which proposes rows directly and never calls Execute -- refuses a
// hard-confined row instead of writing it. When svc does not satisfy it,
// no guard can be installed, and Import refuses outright
// (HardConfinedImportGuardUnavailableError) rather than importing with no
// confinement at all.
func NewHardConfinedBatch(svc BatchServicer) BatchServicer {
	if svc == nil {
		return nil
	}
	setter, ok := svc.(ImportKeyGuardSetter)
	if ok {
		setter.SetImportKeyGuard(hardConfinedImportKeyGuard)
	}
	return &hardConfinedBatch{BatchServicer: svc, importGuardInstalled: ok}
}

// hardConfinedImportKeyGuard is the *BatchService row-key guard
// NewHardConfinedBatch installs: it refuses the exact same keys Put/
// PutIfAbsent would refuse through hardConfinedKV.
func hardConfinedImportKeyGuard(key string) error {
	if isHardConfinedKey(key) {
		return &HardConfinedKeyError{Key: key}
	}
	return nil
}

// HardConfinedImportGuardUnavailableError is returned by Import when the
// wrapped BatchServicer is not a *BatchService, so NewHardConfinedBatch
// could not install the row-key guard Import relies on for confinement.
// Refusing is the fail-closed choice: importing unguarded would let a row
// naming the cluster CA's key material through with no check at all.
type HardConfinedImportGuardUnavailableError struct{}

// Error returns a human-readable description.
func (e *HardConfinedImportGuardUnavailableError) Error() string {
	return "service: Import has no hard-confinement guard to enforce on this BatchServicer and refuses rather than import unguarded"
}

func (h *hardConfinedBatch) Execute(ctx context.Context, ops []Op) (*BatchResponse, error) {
	if opsOrComparesTouchHardConfined(nil, ops) {
		return nil, &HardConfinedKeyError{Key: hardConfinedPrefixesLabel}
	}
	return h.BatchServicer.Execute(ctx, ops)
}

func (h *hardConfinedBatch) ExecuteWithMode(ctx context.Context, ops []Op, mode writemode.Mode) (*BatchResponse, error) {
	if opsOrComparesTouchHardConfined(nil, ops) {
		return nil, &HardConfinedKeyError{Key: hardConfinedPrefixesLabel}
	}
	return h.BatchServicer.ExecuteWithMode(ctx, ops, mode)
}

func (h *hardConfinedBatch) ExecuteEntity(ctx context.Context, req *BatchEntityRequest) (*BatchEntityResponse, error) {
	if req != nil {
		for _, op := range req.Ops {
			if key := entityKey(op.EntityType, op.ID); isHardConfinedKey(key) {
				return nil, &HardConfinedKeyError{Key: key}
			}
		}
	}
	return h.BatchServicer.ExecuteEntity(ctx, req)
}

// ImportBegin, ImportAppend and ImportAbort are declared explicitly rather
// than relying on promotion through embedding -- but unlike ImportCommit
// below, none of the three needs importGuardInstalled: they only ever
// write to the hidden \x00qrdb.import/ session namespace
// (batch_import_session.go), never to a caller-chosen key, so there is
// nothing for the row-key guard to confine here. They are still declared
// directly so a reviewer -- and TestHardConfinedDecorators_OverrideEveryInterfaceMethod
// -- sees that absence stated, not merely inferred from its being missing.
func (h *hardConfinedBatch) ImportBegin(ctx context.Context, opts ImportOptions) (string, error) {
	return h.BatchServicer.ImportBegin(ctx, opts)
}

func (h *hardConfinedBatch) ImportAppend(ctx context.Context, sessionID string, seq uint64, chunk []byte) error {
	return h.BatchServicer.ImportAppend(ctx, sessionID, seq, chunk)
}

func (h *hardConfinedBatch) ImportAbort(ctx context.Context, sessionID string) error {
	return h.BatchServicer.ImportAbort(ctx, sessionID)
}

// ImportCommit is declared explicitly rather than relying on promotion
// through embedding: the actual confinement happens one layer down, via
// the row-key guard NewHardConfinedBatch installs on a wrapped
// *BatchService (see above), so this delegates straight through ONLY when
// that guard was actually installed. When it was not -- the wrapped value
// is some other BatchServicer, a test double or a future alternate
// implementation -- there is no confinement for ImportCommit to rely on at
// all, and committing anyway would silently let a row naming the cluster
// CA's key material through, so this refuses instead. ImportCommit ends by
// calling the same importRecords path a single-message import once did
// (the session's staged chunks, concatenated, stand in for that one file),
// proposing rows directly rather than through Execute, which is why it
// needs this guard and the three verbs above do not.
func (h *hardConfinedBatch) ImportCommit(ctx context.Context, sessionID string) (*ImportResult, error) {
	if !h.importGuardInstalled {
		return nil, &HardConfinedImportGuardUnavailableError{}
	}
	return h.BatchServicer.ImportCommit(ctx, sessionID)
}

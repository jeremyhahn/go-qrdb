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
	"io"

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
		return nil, &HardConfinedKeyError{Key: hardConfinedPrefix}
	}
	return h.TransactionServicer.Execute(ctx, compares, successOps, failureOps)
}

func (h *hardConfinedTxn) ExecuteWithMode(ctx context.Context, compares []Compare, successOps, failureOps []Op, mode writemode.Mode) (*TransactionResponse, error) {
	if opsOrComparesTouchHardConfined(compares, successOps, failureOps) {
		return nil, &HardConfinedKeyError{Key: hardConfinedPrefix}
	}
	return h.TransactionServicer.ExecuteWithMode(ctx, compares, successOps, failureOps, mode)
}

func (h *hardConfinedTxn) ExecuteEntity(ctx context.Context, req *TransactionEntityRequest) (*TransactionEntityResponse, error) {
	if entityTxnTouchesHardConfined(req) {
		return nil, &HardConfinedKeyError{Key: hardConfinedPrefix}
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
// already built and proposed them. NewHardConfinedBatch instead injects a
// row-key guard into the *BatchService it wraps (when the wrapped value is
// one), which importRecords consults per row before a write for a
// hard-confined key is ever proposed. Import itself still gets an explicit
// override below -- a thin passthrough -- so it is not silently promoted
// through embedding the way it would be if this type relied on the
// embedded BatchServicer alone; the confinement happens one layer down, at
// the proposal site, which is where Import's own vulnerability lives.
type hardConfinedBatch struct {
	BatchServicer
}

// NewHardConfinedBatch wraps svc, or returns nil unchanged so a resolver
// that found no service configured for a scope keeps reporting that
// rather than a wrapped nil satisfying the interface. When svc is a
// *BatchService, it also installs a row-key guard so Import -- which
// proposes rows directly and never calls Execute -- refuses a
// hard-confined row instead of writing it.
func NewHardConfinedBatch(svc BatchServicer) BatchServicer {
	if svc == nil {
		return nil
	}
	if bs, ok := svc.(*BatchService); ok {
		bs.setImportKeyGuard(hardConfinedImportKeyGuard)
	}
	return &hardConfinedBatch{BatchServicer: svc}
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

func (h *hardConfinedBatch) Execute(ctx context.Context, ops []Op) ([]OpResponse, error) {
	if opsOrComparesTouchHardConfined(nil, ops) {
		return nil, &HardConfinedKeyError{Key: hardConfinedPrefix}
	}
	return h.BatchServicer.Execute(ctx, ops)
}

func (h *hardConfinedBatch) ExecuteWithMode(ctx context.Context, ops []Op, mode writemode.Mode) ([]OpResponse, error) {
	if opsOrComparesTouchHardConfined(nil, ops) {
		return nil, &HardConfinedKeyError{Key: hardConfinedPrefix}
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

// Import is declared explicitly rather than relying on promotion through
// embedding, even though its body delegates straight through: the actual
// confinement for Import happens one layer down, via the row-key guard
// NewHardConfinedBatch installs on a wrapped *BatchService (see above).
// Declaring it here keeps every BatchServicer method accounted for on
// this type, rather than leaving one to arrive by promotion where a
// reviewer -- or the reflection test asserting this -- would have to know
// to look for it.
func (h *hardConfinedBatch) Import(ctx context.Context, file io.Reader, opts ImportOptions) (*ImportResult, error) {
	return h.BatchServicer.Import(ctx, file, opts)
}

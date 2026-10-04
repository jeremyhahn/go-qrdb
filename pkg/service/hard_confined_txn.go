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
// ExecuteEntity is promoted unchanged through embedding: an entity key is
// composed from EntityType and ID by the underlying store, not supplied as
// a literal string, so a caller cannot address "__sys:cluster_ca/..."
// through it.
type hardConfinedTxn struct {
	TransactionServicer
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

// hardConfinedBatch wraps a BatchServicer, refusing any Execute /
// ExecuteWithMode whose operations touch the hard-confined namespace
// before the batch is ever proposed.
//
// ExecuteEntity is promoted unchanged through embedding, for the same
// reason TransactionServicer.ExecuteEntity is on hardConfinedTxn. Import
// is promoted unchanged too: it parses caller-supplied key-value pairs
// from a file and writes them through the same Put path every other
// write goes through, so it carries no key of its own for this wrapper to
// inspect -- a row naming "__sys:cluster_ca/..." inside an imported file
// is caught at the same Put/PutIfAbsent call Import makes internally,
// which is wrapped wherever Import's own configured KVServicer is (see
// hardConfinedKV).
type hardConfinedBatch struct {
	BatchServicer
}

// NewHardConfinedBatch wraps svc, or returns nil unchanged so a resolver
// that found no service configured for a scope keeps reporting that
// rather than a wrapped nil satisfying the interface.
func NewHardConfinedBatch(svc BatchServicer) BatchServicer {
	if svc == nil {
		return nil
	}
	return &hardConfinedBatch{BatchServicer: svc}
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

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
	"bytes"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// This file holds every implementation of protocol.OpDeleteRange in this
// package, together, for the reason guarded_write.go gives: an opcode
// registered in some handler tables and not others is a defect nothing
// catches at build time. The range-delete feature gate is a property of the
// binary, not of the sm_type, so a node whose state machine did not dispatch
// the opcode would answer a reclaim's range delete with a capability refusal
// while the gate reported it active. The conformance test beside this file
// drives the same range delete through all four.
//
// A range delete is the batch of single deletes over the client keys in
// [start, end) at the moment the entry applies, applied the way each state
// machine applies OpBatch: one entry, one revision, one watch DELETE per key
// in key order. A start in the reserved 0x00 namespace refuses; any other
// start sorts above that whole namespace, so none of the state machine's own
// records is in its range.

// InvalidRangeError refuses a range delete whose bounds do not describe a
// range: an empty start, or an end at or below start. Err is the
// storage.ValidateRange sentinel naming which. It is a refusal on the
// request's merits -- every replica refuses the same entry identically -- so
// the apply classifier answers it in the result rather than halting apply.
type InvalidRangeError struct {
	Err error
}

func (e *InvalidRangeError) Error() string {
	return "statemachine: invalid range: " + e.Err.Error()
}

// Unwrap returns the storage.ValidateRange sentinel.
func (e *InvalidRangeError) Unwrap() error {
	return e.Err
}

// Refusal names the refused part of the request. See protocol.RefusalError.
func (e *InvalidRangeError) Refusal() string {
	return "range bounds"
}

var _ protocol.RefusalError = (*InvalidRangeError)(nil)

// DecodeDeleteRange decodes an OpDeleteRange payload (the command with its
// opcode byte stripped) into the range it names. A nil end is unbounded
// above: an encoded empty end means "through the end of the keyspace". Bounds
// that do not describe a range are an *InvalidRangeError. It is the one
// decoder every OpDeleteRange handler in the tree shares.
func DecodeDeleteRange(payload []byte) (start, end []byte, err error) {
	op, _, err := protocol.DecodeOp(append([]byte{protocol.OpDeleteRange}, payload...))
	if err != nil {
		return nil, nil, err
	}
	if verr := storage.ValidateRange(op.Key, op.RangeEnd); verr != nil {
		return nil, nil, &InvalidRangeError{Err: verr}
	}
	if len(op.RangeEnd) == 0 {
		return op.Key, nil, nil
	}
	return op.Key, op.RangeEnd, nil
}

// deleteRangeOps decodes an OpDeleteRange payload and returns the deletes it
// stands for: one kv.OpDelete per key list returns that lies in the range, in
// key order. list is the state machine's own sorted prefix listing; it is
// asked for the longest prefix every key in the range shares, so a bounded
// range never lists the whole keyspace. A reserved start is answered with
// refusal and refused set.
func deleteRangeOps(payload []byte, list func(prefix string) ([]string, error)) (ops []protocol.DecodedOp, refusal sm.Result, refused bool, err error) {
	start, end, err := DecodeDeleteRange(payload)
	if err != nil {
		return nil, sm.Result{}, false, err
	}
	if res, isReserved := ReservedKeyRefusal(start); isReserved {
		return nil, res, true, nil
	}
	keys, err := list(rangeListPrefix(start, end))
	if err != nil {
		return nil, sm.Result{}, false, err
	}
	for _, k := range keys {
		key := []byte(k)
		if bytes.Compare(key, start) < 0 || (end != nil && bytes.Compare(key, end) >= 0) || isStateMachineKey(key) {
			continue
		}
		ops = append(ops, protocol.DecodedOp{Type: kv.OpDelete, Key: key})
	}
	return ops, sm.Result{}, false, nil
}

// rangeListPrefix is the longest prefix shared by every key in [start, end):
// the common prefix of the two bounds, since a key between them agrees with
// both wherever they agree. An unbounded end shares nothing.
func rangeListPrefix(start, end []byte) string {
	n := 0
	for n < len(start) && n < len(end) && start[n] == end[n] {
		n++
	}
	return string(start[:n])
}

// handleDeleteRange applies an OpDeleteRange entry through the native batch
// plane handleBatch commits through: all or nothing, leases released with
// their keys, watchers notified after the commit.
func (k *KVStateMachine) handleDeleteRange(payload []byte, revision uint64) (sm.Result, error) {
	ops, refusal, refused, err := deleteRangeOps(payload, k.listKeys)
	if refused || err != nil {
		return refusal, err
	}
	muts, err := applyNativeOps(leasedKeyspace{k}, ops)
	if err != nil {
		return sm.Result{}, err
	}
	notifyMutations(k.watchManager, muts, revision)
	return sm.Result{Value: uint64(len(muts))}, nil
}

// handleDeleteRange applies an OpDeleteRange entry to the map under one
// acquisition of r.mu, as handleBatch does.
func (r *RegularStateMachine) handleDeleteRange(payload []byte, revision uint64) (sm.Result, error) {
	ops, refusal, refused, err := deleteRangeOps(payload, r.listKeys)
	if refused || err != nil {
		return refusal, err
	}
	muts, err := applyNativeOps(regularKeyspace{r}, ops)
	if err != nil {
		return sm.Result{}, err
	}
	notifyMutations(r.watchManager, muts, revision)
	return sm.Result{Value: uint64(len(muts))}, nil
}

// handleDeleteRange applies an OpDeleteRange entry to the shard cache, as
// handleBatch does; listKeys merges storage with the cache, so a key written
// earlier in the same apply is in the range.
func (f *FastStateMachine) handleDeleteRange(payload []byte, revision uint64) (sm.Result, error) {
	ops, refusal, refused, err := deleteRangeOps(payload, f.listKeys)
	if refused || err != nil {
		return refusal, err
	}
	if err := f.applyBatchOps(ops, revision); err != nil {
		return sm.Result{}, err
	}
	return sm.Result{Value: uint64(len(ops))}, nil
}

// handleDeleteRange applies an OpDeleteRange entry to the shard cache, as
// handleBatch does.
func (c *ConcurrentStateMachine) handleDeleteRange(payload []byte, revision uint64) (sm.Result, error) {
	ops, refusal, refused, err := deleteRangeOps(payload, c.listConcurrentKeys)
	if refused || err != nil {
		return refusal, err
	}
	if err := c.applyBatchOps(ops, revision); err != nil {
		return sm.Result{}, err
	}
	return sm.Result{Value: uint64(len(ops))}, nil
}

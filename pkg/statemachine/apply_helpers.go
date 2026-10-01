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
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// batchOpSupported reports whether the batch handlers in this package can
// execute op. It is the single definition of that set, so the pre-flight check
// below and each handleBatch dispatch cannot drift apart — a divergence would
// silently reintroduce the partial-write hole validateBatchOps exists to close.
// TestBatchValidationMatchesDispatch pins the two together over every opcode.
func batchOpSupported(op byte) bool {
	switch op {
	// The KV opcodes, and the MVCC opcodes pkg/service's batch and transaction
	// encoders emit (encodeServiceOp). Both spellings mean a plain put or
	// delete of the state machine's own keyspace here; refusing the MVCC pair
	// refused every service batch on every sm_type but the default
	// (go-qrdb#136).
	case kv.OpPut, kv.OpDelete, protocol.OpMVCCPut, protocol.OpMVCCDelete:
		return true
	default:
		return false
	}
}

// validateBatchOps rejects a batch naming an operation these state machines
// cannot execute, BEFORE any of it is applied.
//
// The batch handlers walk their operations applying each in turn, so without
// this they refuse only on REACHING the unknown one, having already written
// every operation before it and delivered its watch notifications. While such
// a refusal reached Raft as an Apply error that was the lesser of two problems
// — the shard was poisoned anyway. Now that an unrecognised opcode is an
// application-level refusal the shard survives (go-qrdb#129), a partial write
// would become a lie the caller acts on: told the batch was refused, with half
// of it committed.
//
// A batch is atomic or it is nothing, so an opcode this state machine does not
// implement refuses the whole batch and mutates nothing.
func validateBatchOps(ops []protocol.DecodedOp) error {
	for _, op := range ops {
		if !batchOpSupported(op.Type) {
			return &InvalidOpcodeError{Op: op.Type}
		}
	}
	return nil
}

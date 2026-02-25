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
	"errors"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// TransactionServicer provides atomic compare-and-swap transactions over
// the MVCC key-value store. A transaction evaluates a set of preconditions
// (compares) and atomically executes the success operations when all pass,
// or the failure operations when any fail.
type TransactionServicer interface {
	// Execute atomically evaluates compares and executes the
	// corresponding operation set. When all compares succeed, successOps
	// are applied; otherwise failureOps are applied. Both sets are
	// executed atomically within a single MVCC revision.
	Execute(ctx context.Context, compares []Compare, successOps, failureOps []Op) (*TransactionResponse, error)
}

// TransactionService implements TransactionServicer backed by a QuicRaft host
// running an MVCCStateMachine.
type TransactionService struct {
	host    Host
	shardID uint64
}

// NewTransactionService creates a new TransactionServicer that operates on the
// specified shard. The shard must be running an MVCCStateMachine for
// transaction operations to succeed.
func NewTransactionService(host Host, shardID uint64) TransactionServicer {
	return &TransactionService{
		host:    host,
		shardID: shardID,
	}
}

// Execute atomically evaluates compares and executes the corresponding
// operation set through Raft consensus. When all compares succeed, successOps
// are applied; otherwise failureOps are applied.
func (s *TransactionService) Execute(ctx context.Context, compares []Compare, successOps, failureOps []Op) (*TransactionResponse, error) {
	// Encode compares.
	encodedCompares := make([][]byte, len(compares))
	for i, c := range compares {
		encodedCompares[i] = protocol.EncodeCompare(
			c.Key,
			byte(c.Target),
			byte(c.Result),
			c.Value,
			c.Version,
			c.CreateRevision,
			c.ModRevision,
		)
	}

	// Encode success operations.
	encodedSuccess := make([][]byte, len(successOps))
	for i, op := range successOps {
		encodedSuccess[i] = encodeServiceOp(op)
	}

	// Encode failure operations.
	encodedFailure := make([][]byte, len(failureOps))
	for i, op := range failureOps {
		encodedFailure[i] = encodeServiceOp(op)
	}

	cmd := protocol.EncodeTransaction(encodedCompares, encodedSuccess, encodedFailure)
	result, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return nil, s.translateProposeError(err)
	}

	// Parse result: Value = revision, Data[0] = succeeded flag.
	succeeded := len(result.Data) > 0 && result.Data[0] == 1
	revision := int64(result.Value)

	// Build per-operation responses for the executed branch.
	var executedOps []Op
	if succeeded {
		executedOps = successOps
	} else {
		executedOps = failureOps
	}

	responses := make([]OpResponse, len(executedOps))
	for i := range executedOps {
		responses[i] = OpResponse{Succeeded: true}
	}

	return &TransactionResponse{
		Succeeded: succeeded,
		Revision:  revision,
		Responses: responses,
	}, nil
}

// translateProposeError converts QuicRaft proposal errors to service-level
// typed errors.
func (s *TransactionService) translateProposeError(err error) error {
	var nle *quicraft.NotLeaderError
	if errors.As(err, &nle) {
		return &NotLeaderError{
			ShardID:       nle.ShardID,
			LeaderID:      nle.LeaderID,
			LeaderAddress: nle.LeaderAddress,
		}
	}
	return &ProposeError{Err: err}
}

// ---------------------------------------------------------------------------
// Op encoding helpers
// ---------------------------------------------------------------------------

// opTypeMap maps service OpType values to protocol op codes.
var opTypeMap = map[OpType]byte{
	OpPut:    protocol.OpMVCCPut,
	OpDelete: protocol.OpMVCCDelete,
}

// encodeServiceOp converts a service.Op to its binary protocol representation.
func encodeServiceOp(op Op) []byte {
	protocolOp, ok := opTypeMap[op.Type]
	if !ok {
		// Default to put for unknown types; the state machine will reject
		// invalid op codes at execution time.
		protocolOp = protocol.OpMVCCPut
	}
	return protocol.EncodeOp(protocolOp, op.Key, op.Value, op.RangeEnd, op.LeaseID)
}

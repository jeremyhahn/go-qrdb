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

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
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

	// ExecuteWithMode atomically evaluates compares and executes the
	// corresponding operation set with the specified write durability.
	// When all compares succeed, successOps are applied; otherwise
	// failureOps are applied.
	ExecuteWithMode(ctx context.Context, compares []Compare, successOps, failureOps []Op, mode writemode.Mode) (*TransactionResponse, error)

	// ExecuteEntity atomically evaluates entity-typed compares and
	// executes the corresponding entity-typed operation set. Entity
	// keys are constructed from the EntityType and ID fields. When all
	// compares succeed, the success operations are applied; otherwise
	// the failure operations are applied.
	ExecuteEntity(ctx context.Context, req *TransactionEntityRequest) (*TransactionEntityResponse, error)
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
	cmd, err := s.encodeTransactionCommand(compares, successOps, failureOps)
	if err != nil {
		return nil, &EncodeRequestError{Err: err}
	}
	result, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return nil, translateProposeError(err)
	}
	// A state-machine-encoded application error (e.g. a unique-index violation
	// that rejected the transaction without stalling consensus) must be
	// surfaced before the result.Data success flag is interpreted.
	if resultErr := checkResultError(result); resultErr != nil {
		return nil, resultErr
	}

	return s.buildTransactionResponse(result, successOps, failureOps), nil
}

// ExecuteWithMode atomically evaluates compares and executes the
// corresponding operation set with the specified write durability through
// Raft consensus. When all compares succeed, successOps are applied;
// otherwise failureOps are applied.
func (s *TransactionService) ExecuteWithMode(ctx context.Context, compares []Compare, successOps, failureOps []Op, mode writemode.Mode) (*TransactionResponse, error) {
	cmd, err := s.encodeTransactionCommand(compares, successOps, failureOps)
	if err != nil {
		return nil, &EncodeRequestError{Err: err}
	}
	result, err := s.host.ProposeWithMode(ctx, s.shardID, cmd, mode)
	if err != nil {
		return nil, translateProposeError(err)
	}
	if resultErr := checkResultError(result); resultErr != nil {
		return nil, resultErr
	}

	return s.buildTransactionResponse(result, successOps, failureOps), nil
}

// encodeTransactionCommand encodes compares, success ops, and failure ops
// into a single transaction command for Raft proposal. Returns an error,
// without proposing anything, if any success or failure operation is not a
// supported transaction op type (see UnsupportedTransactionOpError).
func (s *TransactionService) encodeTransactionCommand(compares []Compare, successOps, failureOps []Op) ([]byte, error) {
	// Encode compares.
	encodedCompares := make([][]byte, len(compares))
	for i, c := range compares {
		encoded, err := protocol.EncodeCompare(
			c.Key,
			byte(c.Target),
			byte(c.Result),
			c.Value,
			c.Version,
			c.CreateRevision,
			c.ModRevision,
		)
		if err != nil {
			return nil, err
		}
		encodedCompares[i] = encoded
	}

	// Encode success operations.
	encodedSuccess := make([][]byte, len(successOps))
	for i, op := range successOps {
		encoded, err := encodeServiceOp(op)
		if err != nil {
			return nil, err
		}
		encodedSuccess[i] = encoded
	}

	// Encode failure operations.
	encodedFailure := make([][]byte, len(failureOps))
	for i, op := range failureOps {
		encoded, err := encodeServiceOp(op)
		if err != nil {
			return nil, err
		}
		encodedFailure[i] = encoded
	}

	return protocol.EncodeTransaction(encodedCompares, encodedSuccess, encodedFailure)
}

// buildTransactionResponse constructs a TransactionResponse from the Raft
// proposal result and the original operation sets.
func (s *TransactionService) buildTransactionResponse(result sm.Result, successOps, failureOps []Op) *TransactionResponse {
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
	}
}

// ExecuteEntity atomically evaluates entity-typed compares and executes
// the corresponding entity-typed operations through Raft consensus.
// Entity keys are constructed as "{entityType}/{id}".
func (s *TransactionService) ExecuteEntity(ctx context.Context, req *TransactionEntityRequest) (*TransactionEntityResponse, error) {
	if req == nil {
		return nil, &EntityEmptyTypeError{}
	}

	// Encode entity compares to raw compares.
	encodedCompares := make([][]byte, len(req.Compares))
	for i, c := range req.Compares {
		if c.EntityType == "" {
			return nil, &EntityEmptyTypeError{}
		}
		if c.ID == "" {
			return nil, &EntityEmptyIDError{EntityType: c.EntityType}
		}
		key := []byte(entityKey(c.EntityType, c.ID))
		encodedCompare, err := protocol.EncodeCompare(
			key,
			byte(c.Target),
			byte(c.Result),
			c.Value,
			0, // version
			0, // createRevision
			0, // modRevision
		)
		if err != nil {
			return nil, &EncodeRequestError{Err: err}
		}
		encodedCompares[i] = encodedCompare
	}

	// Encode entity success operations.
	encodedSuccess, err := encodeEntityOps(req.Success)
	if err != nil {
		return nil, &EncodeRequestError{Err: err}
	}

	// Encode entity failure operations.
	encodedFailure, err := encodeEntityOps(req.Failure)
	if err != nil {
		return nil, &EncodeRequestError{Err: err}
	}

	cmd, err := protocol.EncodeTransaction(encodedCompares, encodedSuccess, encodedFailure)
	if err != nil {
		return nil, &EncodeRequestError{Err: err}
	}

	shardID := s.shardID
	if req.ShardID != 0 {
		shardID = req.ShardID
	}

	result, proposeErr := s.host.SyncPropose(ctx, shardID, cmd)
	if proposeErr != nil {
		return nil, translateProposeError(proposeErr)
	}
	// A state-machine-encoded application error (e.g. a unique-index violation
	// that rejected the transaction without stalling consensus) must be
	// surfaced before the result.Data success flag is interpreted.
	if resultErr := checkResultError(result); resultErr != nil {
		return nil, resultErr
	}

	// Parse result: Value = revision, Data[0] = succeeded flag.
	succeeded := len(result.Data) > 0 && result.Data[0] == 1

	// Build per-operation response data for the executed branch.
	var executedCount int
	if succeeded {
		executedCount = len(req.Success)
	} else {
		executedCount = len(req.Failure)
	}

	responses := make([][]byte, executedCount)

	return &TransactionEntityResponse{
		Succeeded: succeeded,
		Responses: responses,
	}, nil
}

// encodeEntityOps validates and encodes a slice of TransactionEntityOp
// into binary protocol representations.
func encodeEntityOps(ops []TransactionEntityOp) ([][]byte, error) {
	encoded := make([][]byte, len(ops))
	for i, op := range ops {
		if op.EntityType == "" {
			return nil, &EntityEmptyTypeError{}
		}
		if op.ID == "" {
			return nil, &EntityEmptyIDError{EntityType: op.EntityType}
		}

		protocolOp, ok := entityOpTypeHandlers[op.Type]
		if !ok {
			return nil, &InvalidEntityOpTypeError{OpType: op.Type}
		}

		key := []byte(entityKey(op.EntityType, op.ID))
		encodedOp, err := protocol.EncodeOp(protocolOp, key, op.Data, nil, 0)
		if err != nil {
			return nil, err
		}
		encoded[i] = encodedOp
	}
	return encoded, nil
}

// ---------------------------------------------------------------------------
// Op encoding helpers
// ---------------------------------------------------------------------------

// opTypeMap maps service OpType values to protocol op codes.
var opTypeMap = map[OpType]byte{
	OpPut:    protocol.OpMVCCPut,
	OpDelete: protocol.OpMVCCDelete,
}

// encodeServiceOp converts a service.Op to its binary protocol
// representation. Only OpPut and OpDelete are supported: transaction and
// batch write-op encoding (the only callers of this function) never
// operate on reads (OpGet, OpRange), which are executed as linearizable
// reads through separate code paths and must never reach this encoder.
// An unmapped OpType — including OpGet, OpRange, or any future/invalid
// value — fails closed with UnsupportedTransactionOpError rather than
// silently defaulting to a write, which would otherwise let a caller's
// read request corrupt the target key with an empty-value put.
func encodeServiceOp(op Op) ([]byte, error) {
	protocolOp, ok := opTypeMap[op.Type]
	if !ok {
		return nil, &UnsupportedTransactionOpError{OpType: op.Type}
	}
	return protocol.EncodeOp(protocolOp, op.Key, op.Value, op.RangeEnd, op.LeaseID)
}

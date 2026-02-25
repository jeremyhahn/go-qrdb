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

// BatchServicer provides atomic multi-operation execution at the KV layer.
// All operations in a batch are applied within a single MVCC revision,
// guaranteeing atomicity without the conditional logic of transactions.
type BatchServicer interface {
	// Execute applies the given operations atomically. All operations
	// succeed or fail together. Returns the per-operation results in
	// the same order as the input.
	Execute(ctx context.Context, ops []Op) ([]OpResponse, error)
}

// BatchService implements BatchServicer backed by a QuicRaft host running
// an MVCCStateMachine.
type BatchService struct {
	host    Host
	shardID uint64
}

// NewBatchService creates a new BatchServicer that operates on the specified
// shard. The shard must be running an MVCCStateMachine for batch operations
// to succeed.
func NewBatchService(host Host, shardID uint64) BatchServicer {
	return &BatchService{
		host:    host,
		shardID: shardID,
	}
}

// Execute applies the given operations atomically through Raft consensus.
// All operations are executed within a single MVCC revision. Returns the
// per-operation results in the same order as the input.
func (s *BatchService) Execute(ctx context.Context, ops []Op) ([]OpResponse, error) {
	// Encode operations.
	encodedOps := make([][]byte, len(ops))
	for i, op := range ops {
		encodedOps[i] = encodeServiceOp(op)
	}

	cmd := protocol.EncodeBatch(encodedOps)
	_, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return nil, s.translateProposeError(err)
	}

	// Build per-operation responses. All ops succeed atomically.
	responses := make([]OpResponse, len(ops))
	for i := range ops {
		responses[i] = OpResponse{Succeeded: true}
	}

	return responses, nil
}

// translateProposeError converts QuicRaft proposal errors to service-level
// typed errors.
func (s *BatchService) translateProposeError(err error) error {
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

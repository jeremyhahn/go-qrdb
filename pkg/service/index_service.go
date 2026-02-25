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

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// IndexServicer is an alias for the canonical index.IndexServicer interface.
type IndexServicer = index.IndexServicer

// IndexService implements IndexServicer backed by a QuicRaft host
// running an MVCCStateMachine.
type IndexService struct {
	host    Host
	shardID uint64
}

// NewIndexService creates a new IndexServicer that operates on the
// specified shard. The shard must be running an MVCCStateMachine
// for index operations to succeed.
func NewIndexService(host Host, shardID uint64) IndexServicer {
	return &IndexService{
		host:    host,
		shardID: shardID,
	}
}

// CreateIndex creates a new secondary index through Raft consensus.
func (s *IndexService) CreateIndex(ctx context.Context, name, fieldName string, config IndexConfig) error {
	cmd := protocol.EncodeIndexCreate(name, fieldName, config.Unique, config.Sparse)
	_, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return s.translateProposeError(err)
	}
	return nil
}

// DropIndex removes a secondary index through Raft consensus.
func (s *IndexService) DropIndex(ctx context.Context, name string) error {
	cmd := protocol.EncodeIndexDrop(name)
	_, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return s.translateProposeError(err)
	}
	return nil
}

// Query returns entity IDs matching an exact value in the specified index.
func (s *IndexService) Query(ctx context.Context, indexName string, value []byte) ([]uint64, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, IndexQueryRequest{
		IndexName: indexName,
		Value:     value,
	})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	ids, ok := result.([]uint64)
	if !ok {
		return nil, &unexpectedResultTypeError{}
	}
	return ids, nil
}

// Scan returns entries in the specified index within the value range [start, end).
func (s *IndexService) Scan(ctx context.Context, indexName string, start, end []byte) (map[string][]uint64, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, IndexScanRequest{
		IndexName: indexName,
		Start:     start,
		End:       end,
	})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	entries, ok := result.(map[string][]uint64)
	if !ok {
		return nil, &unexpectedResultTypeError{}
	}
	return entries, nil
}

// translateProposeError converts QuicRaft proposal errors to service-level
// typed errors.
func (s *IndexService) translateProposeError(err error) error {
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

// translateReadError converts QuicRaft read errors to service-level
// typed errors.
func (s *IndexService) translateReadError(err error) error {
	var nle *quicraft.NotLeaderError
	if errors.As(err, &nle) {
		return &NotLeaderError{
			ShardID:       nle.ShardID,
			LeaderID:      nle.LeaderID,
			LeaderAddress: nle.LeaderAddress,
		}
	}
	return &ReadError{Err: err}
}

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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dragonErrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
)

// =============================================================================
// vectorService.proposeToRaft Tests
// =============================================================================

func TestVectorService_ProposeToRaft_NilRaftNode(t *testing.T) {
	vs := &vectorService{
		raftNode: nil,
		shardID:  VectorShardID,
	}

	cmd := &statemachine.VectorCommand{
		Op:        statemachine.OpUpsertVector,
		Namespace: "test-ns",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	}

	err := vs.proposeToRaft(context.Background(), cmd)
	// Nil raft node means local-only mode, should return nil
	require.NoError(t, err)
}

func TestVectorService_ProposeToRaft_Success(t *testing.T) {
	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}
	vs := &vectorService{
		raftNode: mockRaft,
		shardID:  VectorShardID,
	}

	cmd := &statemachine.VectorCommand{
		Op:        statemachine.OpUpsertVector,
		Namespace: "test-ns",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	}

	err := vs.proposeToRaft(context.Background(), cmd)
	require.NoError(t, err)
}

func TestVectorService_ProposeToRaft_RaftError(t *testing.T) {
	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return &vectorTestError{msg: "raft proposal failed"}
		},
	}
	vs := &vectorService{
		raftNode: mockRaft,
		shardID:  VectorShardID,
	}

	cmd := &statemachine.VectorCommand{
		Op:        statemachine.OpUpsertVector,
		Namespace: "test-ns",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	}

	err := vs.proposeToRaft(context.Background(), cmd)
	require.Error(t, err)

	var dErr *dragonErrors.DragonError
	require.ErrorAs(t, err, &dErr)
	assert.Equal(t, dragonErrors.ErrRaft, dErr.Code)
}

// vectorTestError is a test error type for vector tests.
type vectorTestError struct {
	msg string
}

func (e *vectorTestError) Error() string {
	return e.msg
}

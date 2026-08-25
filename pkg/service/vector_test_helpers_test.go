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
)

// =============================================================================
// Mock Raft Node for Vector Tests
// =============================================================================

// mockRaftNode implements interfaces.RaftNode for testing.
//
// It returns a real sm.Result rather than the nil it used to return. A real
// state machine always answers with one, and the caller now READS that answer
// to find an encoded refusal (go-qrdb#129), so a double that returns nil no
// longer stands in for the thing it doubles: it exercises only the
// unreadable-result path and never the success path it was written for.
type mockRaftNode struct {
	syncProposeFn func(ctx context.Context, shardID uint64, data []byte) error

	// result overrides what SyncPropose returns on success. Zero value means
	// an ordinary empty sm.Result, i.e. applied with nothing to report.
	result interface{}
	// resultSet distinguishes "no override" from an override to a nil or
	// otherwise zero-valued result, which is exactly what the fail-closed
	// path has to be tested with.
	resultSet bool
}

func (m *mockRaftNode) SyncPropose(ctx context.Context, shardID uint64, data []byte) (interface{}, error) {
	if m.syncProposeFn != nil {
		if err := m.syncProposeFn(ctx, shardID, data); err != nil {
			return nil, err
		}
	}
	if m.resultSet {
		return m.result, nil
	}
	return sm.Result{}, nil
}

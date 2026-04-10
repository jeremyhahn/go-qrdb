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

//go:build qrdb_vector

package service

import (
	"testing"
)

// TestWithRaftNode tests the WithRaftNode option.
func TestWithRaftNode(t *testing.T) {
	vs := &vectorService{}

	// Verify initial state
	if vs.raftNode != nil {
		t.Error("expected raftNode to be nil initially")
	}

	// Apply option with nil (should not panic)
	opt := WithRaftNode(nil)
	opt(vs)

	// raftNode should still be nil
	if vs.raftNode != nil {
		t.Error("expected raftNode to be nil after applying nil")
	}
}

// TestWithVectorShardID tests the WithVectorShardID option.
func TestWithVectorShardID(t *testing.T) {
	tests := []struct {
		name    string
		shardID uint64
	}{
		{
			name:    "default shard ID",
			shardID: 2,
		},
		{
			name:    "custom shard ID",
			shardID: 5,
		},
		{
			name:    "zero shard ID",
			shardID: 0,
		},
		{
			name:    "large shard ID",
			shardID: 999999,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vs := &vectorService{}
			opt := WithVectorShardID(tt.shardID)
			opt(vs)

			if vs.shardID != tt.shardID {
				t.Errorf("expected shardID %d, got %d", tt.shardID, vs.shardID)
			}
		})
	}
}

// TestWithNodeID tests the WithNodeID option.
func TestWithNodeID(t *testing.T) {
	tests := []struct {
		name   string
		nodeID string
	}{
		{
			name:   "empty node ID",
			nodeID: "",
		},
		{
			name:   "simple node ID",
			nodeID: "node-1",
		},
		{
			name:   "UUID node ID",
			nodeID: "550e8400-e29b-41d4-a716-446655440000",
		},
		{
			name:   "complex node ID",
			nodeID: "cluster-prod-region-us-east-1-node-42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vs := &vectorService{}
			opt := WithNodeID(tt.nodeID)
			opt(vs)

			if vs.nodeID != tt.nodeID {
				t.Errorf("expected nodeID '%s', got '%s'", tt.nodeID, vs.nodeID)
			}
		})
	}
}

// TestVectorServiceOptionChaining tests applying multiple options.
func TestVectorServiceOptionChaining(t *testing.T) {
	vs := &vectorService{}

	opts := []VectorServiceOption{
		WithVectorShardID(10),
		WithNodeID("test-node"),
	}

	for _, opt := range opts {
		opt(vs)
	}

	if vs.shardID != 10 {
		t.Errorf("expected shardID 10, got %d", vs.shardID)
	}
	if vs.nodeID != "test-node" {
		t.Errorf("expected nodeID 'test-node', got '%s'", vs.nodeID)
	}
}

// TestVectorServiceOptionOverwriting tests that later options overwrite earlier ones.
func TestVectorServiceOptionOverwriting(t *testing.T) {
	vs := &vectorService{}

	// Apply first shardID
	WithVectorShardID(5)(vs)
	if vs.shardID != 5 {
		t.Errorf("expected shardID 5, got %d", vs.shardID)
	}

	// Apply second shardID (should overwrite)
	WithVectorShardID(10)(vs)
	if vs.shardID != 10 {
		t.Errorf("expected shardID 10 after overwrite, got %d", vs.shardID)
	}
}

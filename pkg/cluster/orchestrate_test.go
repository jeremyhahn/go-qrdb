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

package cluster

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/config"
)

func TestBootstrapShard_StaticSuccess(t *testing.T) {
	node := &mockRaftNode{}
	opts := Options{
		Method:      "static",
		NodeID:      1,
		RaftAddress: "10.0.0.1:5000",
		MinPeers:    3,
		Peers: []string{
			"1=10.0.0.1:5000",
			"2=10.0.0.2:5000",
			"3=10.0.0.3:5000",
		},
	}

	if err := BootstrapShard(context.Background(), node, opts, 100, nil, config.Config{}); err != nil {
		t.Fatalf("BootstrapShard() error = %v, want nil", err)
	}
	if !node.startCalled {
		t.Fatal("StartShard was not called")
	}
	if len(node.members) != 3 {
		t.Fatalf("len(members) = %d, want 3", len(node.members))
	}
}

func TestBootstrapShard_UnknownMethod(t *testing.T) {
	node := &mockRaftNode{}
	err := BootstrapShard(context.Background(), node, Options{Method: "consul"}, 100, nil, config.Config{})
	var ude *UnknownDiscoveryError
	if !errors.As(err, &ude) {
		t.Fatalf("BootstrapShard() error = %T (%v), want *UnknownDiscoveryError", err, err)
	}
	if node.startCalled {
		t.Fatal("StartShard should not be called when discovery cannot be built")
	}
}

func TestBootstrapShard_InsufficientQuorum(t *testing.T) {
	node := &mockRaftNode{}
	opts := Options{
		Method:      "static",
		NodeID:      1,
		RaftAddress: "10.0.0.1:5000",
		MinPeers:    5, // quorum 3, only 1 peer (self) -> fails
		Peers:       []string{"1=10.0.0.1:5000"},
	}
	err := BootstrapShard(context.Background(), node, opts, 100, nil, config.Config{})
	var qe *QuorumError
	if !errors.As(err, &qe) {
		t.Fatalf("BootstrapShard() error = %T (%v), want *QuorumError", err, err)
	}
	if node.startCalled {
		t.Fatal("StartShard should not be called without quorum")
	}
}

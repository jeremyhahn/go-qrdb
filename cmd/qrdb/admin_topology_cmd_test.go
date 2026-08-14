//go:build !integration

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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// topoAdminService — a configurable service.AdminServicer mock dedicated to
// the "admin topology" and "admin capabilities" subcommands. Distinct from
// fnAdminService (admin_test.go) because that mock's GetTopology is not
// configurable per-test.
// ---------------------------------------------------------------------------

type topoAdminService struct {
	topologyFn     func(ctx context.Context) (*service.TopologyResponse, error)
	capabilitiesFn func(ctx context.Context) (*capabilities.Capabilities, error)
}

func (m *topoAdminService) Health(_ context.Context) (*service.HealthStatus, error) {
	return &service.HealthStatus{Status: "ok"}, nil
}

func (m *topoAdminService) Status(_ context.Context) (*service.StatusInfo, error) {
	return &service.StatusInfo{}, nil
}

func (m *topoAdminService) Peers(_ context.Context) (*service.PeersInfo, error) {
	return &service.PeersInfo{}, nil
}

func (m *topoAdminService) AddMember(_ context.Context, _, _ uint64, _ string) error {
	return nil
}

func (m *topoAdminService) RemoveMember(_ context.Context, _, _ uint64) error {
	return nil
}

func (m *topoAdminService) RequestSnapshot(_ context.Context, _ uint64) error {
	return nil
}

func (m *topoAdminService) Drain(_ context.Context) error {
	return nil
}

func (m *topoAdminService) Capabilities(ctx context.Context) (*capabilities.Capabilities, error) {
	if m.capabilitiesFn != nil {
		return m.capabilitiesFn(ctx)
	}
	return &capabilities.Capabilities{KV: true, MVCC: true}, nil
}

func (m *topoAdminService) StartShard(_ context.Context, _ *service.StartShardRequest) error {
	return nil
}

func (m *topoAdminService) StopShard(_ context.Context, _ uint64) error {
	return nil
}

func (m *topoAdminService) GetShardState(_ context.Context, _ uint64) (*service.ShardStateResponse, error) {
	return &service.ShardStateResponse{}, nil
}

func (m *topoAdminService) GetTopology(ctx context.Context) (*service.TopologyResponse, error) {
	if m.topologyFn != nil {
		return m.topologyFn(ctx)
	}
	return &service.TopologyResponse{}, nil
}

var _ service.AdminServicer = (*topoAdminService)(nil)

// ---------------------------------------------------------------------------
// admin topology tests
// ---------------------------------------------------------------------------

func TestAdminTopology_Table(t *testing.T) {
	adminSvc := &topoAdminService{
		topologyFn: func(_ context.Context) (*service.TopologyResponse, error) {
			return &service.TopologyResponse{
				NodeID:      "node-1",
				RaftAddress: "10.0.0.1:63001",
				ShardCount:  1,
				Shards: []service.TopologyShardInfo{
					{
						ShardID:   1,
						ReplicaID: 10,
						IsLeader:  true,
						LeaderID:  10,
						Nodes: map[uint64]string{
							10: "10.0.0.1:63001",
							20: "10.0.0.2:63001",
						},
					},
				},
			}, nil
		},
	}

	output, err := executeAdminCmd(t, adminSvc, "topology")
	if err != nil {
		t.Fatalf("admin topology error: %v", err)
	}

	for _, want := range []string{
		"NodeID:",
		"node-1",
		"RaftAddress:",
		"10.0.0.1:63001",
		"ShardCount:",
		"SHARD_ID",
		"REPLICA_ID",
		"IS_LEADER",
		"LEADER_ID",
		"NODES",
		"10=10.0.0.1:63001",
		"20=10.0.0.2:63001",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestAdminTopology_EmptyShards(t *testing.T) {
	adminSvc := &topoAdminService{
		topologyFn: func(_ context.Context) (*service.TopologyResponse, error) {
			return &service.TopologyResponse{
				NodeID:      "node-1",
				RaftAddress: "10.0.0.1:63001",
				ShardCount:  0,
			}, nil
		},
	}

	output, err := executeAdminCmd(t, adminSvc, "topology")
	if err != nil {
		t.Fatalf("admin topology error: %v", err)
	}
	if !strings.Contains(output, "node-1") {
		t.Errorf("output missing NodeID, got: %q", output)
	}
}

func TestAdminTopology_JSON(t *testing.T) {
	adminSvc := &topoAdminService{
		topologyFn: func(_ context.Context) (*service.TopologyResponse, error) {
			return &service.TopologyResponse{
				NodeID:      "node-1",
				RaftAddress: "10.0.0.1:63001",
				ShardCount:  1,
				Shards: []service.TopologyShardInfo{
					{ShardID: 1, ReplicaID: 10, IsLeader: true, LeaderID: 10, Nodes: map[uint64]string{10: "10.0.0.1:63001"}},
				},
			}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	output, err := executeAdminCmd(t, adminSvc, "topology")
	if err != nil {
		t.Fatalf("admin topology json error: %v", err)
	}

	var resp transport.TopologyResponse
	if jsonErr := json.Unmarshal([]byte(output), &resp); jsonErr != nil {
		t.Fatalf("JSON unmarshal error: %v, output: %q", jsonErr, output)
	}
	if resp.NodeID != "node-1" {
		t.Errorf("NodeID = %q, want %q", resp.NodeID, "node-1")
	}
	if len(resp.Shards) != 1 {
		t.Fatalf("Shards count = %d, want 1", len(resp.Shards))
	}
}

func TestAdminTopology_ServiceError(t *testing.T) {
	adminSvc := &topoAdminService{
		topologyFn: func(_ context.Context) (*service.TopologyResponse, error) {
			return nil, &service.ReadError{Err: errors.New("topology unavailable")}
		},
	}

	_, err := executeAdminCmd(t, adminSvc, "topology")
	if err == nil {
		t.Fatal("expected error from topology command")
	}
}

// ---------------------------------------------------------------------------
// admin capabilities tests
// ---------------------------------------------------------------------------

func TestAdminCapabilities_Table(t *testing.T) {
	adminSvc := &topoAdminService{
		capabilitiesFn: func(_ context.Context) (*capabilities.Capabilities, error) {
			return &capabilities.Capabilities{
				KV:          true,
				MVCC:        true,
				Transaction: true,
				Batch:       true,
				Watch:       true,
				Lease:       true,
				Index:       true,
				TimeSeries:  false,
				Vector:      false,
			}, nil
		},
	}

	output, err := executeAdminCmd(t, adminSvc, "capabilities")
	if err != nil {
		t.Fatalf("admin capabilities error: %v", err)
	}

	for _, want := range []string{"FEATURE", "ENABLED", "kv", "true", "timeSeries", "false"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestAdminCapabilities_JSON(t *testing.T) {
	adminSvc := &topoAdminService{
		capabilitiesFn: func(_ context.Context) (*capabilities.Capabilities, error) {
			return &capabilities.Capabilities{KV: true, Vector: true}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	output, err := executeAdminCmd(t, adminSvc, "capabilities")
	if err != nil {
		t.Fatalf("admin capabilities json error: %v", err)
	}

	var resp transport.CapabilitiesResponse
	if jsonErr := json.Unmarshal([]byte(output), &resp); jsonErr != nil {
		t.Fatalf("JSON unmarshal error: %v, output: %q", jsonErr, output)
	}
	if !resp.KV {
		t.Errorf("KV = false, want true")
	}
	if !resp.Vector {
		t.Errorf("Vector = false, want true")
	}
}

func TestAdminCapabilities_ServiceError(t *testing.T) {
	adminSvc := &topoAdminService{
		capabilitiesFn: func(_ context.Context) (*capabilities.Capabilities, error) {
			return nil, &service.ReadError{Err: errors.New("capabilities unavailable")}
		},
	}

	_, err := executeAdminCmd(t, adminSvc, "capabilities")
	if err == nil {
		t.Fatal("expected error from capabilities command")
	}
}

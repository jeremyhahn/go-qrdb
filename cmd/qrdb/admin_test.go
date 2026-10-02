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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Function-based admin mock (distinct from cmd_test.go mockAdminService)
// ---------------------------------------------------------------------------

// fnAdminService implements service.AdminServicer with configurable
// function fields, allowing per-test behavior injection.
type fnAdminService struct {
	// addIdentity records the member identity the last AddMember received.
	addIdentity       string
	healthFn          func(ctx context.Context) (*service.HealthStatus, error)
	statusFn          func(ctx context.Context) (*service.StatusInfo, error)
	peersFn           func(ctx context.Context) (*service.PeersInfo, error)
	addMemberFn       func(ctx context.Context, shardID, replicaID uint64, addr string) error
	removeMemberFn    func(ctx context.Context, shardID, replicaID uint64) error
	requestSnapshotFn func(ctx context.Context, shardID uint64) error
	drainFn           func(ctx context.Context) error
	capabilitiesFn    func(ctx context.Context) (*capabilities.Capabilities, error)
}

func (m *fnAdminService) Health(ctx context.Context) (*service.HealthStatus, error) {
	return m.healthFn(ctx)
}

func (m *fnAdminService) Status(ctx context.Context) (*service.StatusInfo, error) {
	return m.statusFn(ctx)
}

func (m *fnAdminService) Peers(ctx context.Context) (*service.PeersInfo, error) {
	return m.peersFn(ctx)
}

func (m *fnAdminService) AddMember(ctx context.Context, shardID, replicaID uint64, addr, identity string) error {
	m.addIdentity = identity
	return m.addMemberFn(ctx, shardID, replicaID, addr)
}

func (m *fnAdminService) RemoveMember(ctx context.Context, shardID, replicaID uint64) error {
	return m.removeMemberFn(ctx, shardID, replicaID)
}

func (m *fnAdminService) RequestSnapshot(ctx context.Context, shardID uint64) error {
	return m.requestSnapshotFn(ctx, shardID)
}

func (m *fnAdminService) Drain(ctx context.Context) error {
	return m.drainFn(ctx)
}

func (m *fnAdminService) Capabilities(ctx context.Context) (*capabilities.Capabilities, error) {
	if m.capabilitiesFn != nil {
		return m.capabilitiesFn(ctx)
	}
	return &capabilities.Capabilities{
		KV:          true,
		MVCC:        true,
		Transaction: true,
		Batch:       true,
		Watch:       true,
		Lease:       true,
		Index:       true,
	}, nil
}

func (m *fnAdminService) StartShard(_ context.Context, _ *service.StartShardRequest) error {
	return nil
}

func (m *fnAdminService) StopShard(_ context.Context, _ uint64) error {
	return nil
}

func (m *fnAdminService) GetShardState(_ context.Context, _ uint64) (*service.ShardStateResponse, error) {
	return &service.ShardStateResponse{}, nil
}

func (m *fnAdminService) GetTopology(_ context.Context) (*service.TopologyResponse, error) {
	return &service.TopologyResponse{}, nil
}

func (m *fnAdminService) NodeCompaction(_ context.Context) (*compaction.Policy, error) {
	return nil, nil
}

func (m *fnAdminService) SetNodeCompaction(_ context.Context, _ *compaction.Policy) error {
	return nil
}

// ---------------------------------------------------------------------------
// Minimal KV service for admin tests (distinct from cmd_test.go)
// ---------------------------------------------------------------------------

// noopKVService implements service.KVServicer as a no-op, satisfying
// the embedded.New constructor requirement for admin-focused tests.
type noopKVService struct{}

func (m *noopKVService) Put(_ context.Context, _ string, _ []byte) error { return nil }
func (m *noopKVService) PutIfAbsent(_ context.Context, _ string, _ []byte) (bool, error) {
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *noopKVService) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}
func (m *noopKVService) Get(_ context.Context, _ string) ([]byte, error)             { return nil, nil }
func (m *noopKVService) Delete(_ context.Context, _ string) error                    { return nil }
func (m *noopKVService) List(_ context.Context, _ string) ([]string, error)          { return nil, nil }
func (m *noopKVService) Scan(_ context.Context, _ string) (map[string][]byte, error) { return nil, nil }

func (m *noopKVService) GetStale(_ context.Context, _ string) ([]byte, error)    { return nil, nil }
func (m *noopKVService) ListStale(_ context.Context, _ string) ([]string, error) { return nil, nil }
func (m *noopKVService) ScanStale(_ context.Context, _ string) (map[string][]byte, error) {
	return nil, nil
}

func (m *noopKVService) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return nil, nil
}

func (m *noopKVService) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return nil, nil
}

func (m *noopKVService) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return nil, nil
}

func (m *noopKVService) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return nil, nil
}

func (m *noopKVService) PutWithMode(_ context.Context, _ string, _ []byte, _ writemode.Mode) error {
	return nil
}

func (m *noopKVService) DeleteWithMode(_ context.Context, _ string, _ writemode.Mode) error {
	return nil
}

func (m *noopKVService) GetWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]byte, error) {
	return nil, nil
}

func (m *noopKVService) ListWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]string, error) {
	return nil, nil
}

func (m *noopKVService) ScanWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return nil, nil
}

func (m *noopKVService) ExistsWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (bool, error) {
	return false, nil
}

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

// executeAdminCmd creates an embedded transport with the given admin service,
// sets the package-level client, builds the admin command tree, and executes
// it with the given args. It captures all output written to outputWriter.
func executeAdminCmd(t *testing.T, adminSvc service.AdminServicer, args ...string) (string, error) {
	t.Helper()

	kvSvc := &noopKVService{}
	tr, err := embedded.New(kvSvc, adminSvc)
	if err != nil {
		t.Fatalf("embedded.New() error: %v", err)
	}

	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient() error: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	// Save and restore package-level state.
	oldClient := client
	oldWriter := outputWriter
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
	})

	// Set the package-level client and capture output.
	client = sdkClient
	var buf bytes.Buffer
	outputWriter = &buf

	// Build the admin command tree.
	cmd := newAdminCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(context.Background())

	return buf.String(), execErr
}

// ---------------------------------------------------------------------------
// Health tests
// ---------------------------------------------------------------------------

func TestAdminHealth_Table(t *testing.T) {
	adminSvc := &fnAdminService{
		healthFn: func(_ context.Context) (*service.HealthStatus, error) {
			return &service.HealthStatus{
				Status: "ok",
				Leader: true,
			}, nil
		},
	}

	output, err := executeAdminCmd(t, adminSvc, "health")
	if err != nil {
		t.Fatalf("admin health error: %v", err)
	}

	if !strings.Contains(output, "Status: ok") {
		t.Errorf("output missing 'Status: ok', got: %q", output)
	}
	if !strings.Contains(output, "Leader: true") {
		t.Errorf("output missing 'Leader: true', got: %q", output)
	}
}

func TestAdminHealth_JSON(t *testing.T) {
	adminSvc := &fnAdminService{
		healthFn: func(_ context.Context) (*service.HealthStatus, error) {
			return &service.HealthStatus{
				Status: "ok",
				Leader: true,
			}, nil
		},
	}

	// Set output format to JSON.
	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	output, err := executeAdminCmd(t, adminSvc, "health")
	if err != nil {
		t.Fatalf("admin health json error: %v", err)
	}

	var resp transport.HealthResponse
	if jsonErr := json.Unmarshal([]byte(output), &resp); jsonErr != nil {
		t.Fatalf("JSON unmarshal error: %v, output: %q", jsonErr, output)
	}
	if resp.Status != "ok" {
		t.Errorf("Status = %q, want %q", resp.Status, "ok")
	}
	if !resp.IsLeader {
		t.Errorf("IsLeader = false, want true")
	}
}

func TestAdminHealth_ServiceError(t *testing.T) {
	adminSvc := &fnAdminService{
		healthFn: func(_ context.Context) (*service.HealthStatus, error) {
			return nil, &service.DrainError{Err: errors.New("node unhealthy")}
		},
	}

	_, err := executeAdminCmd(t, adminSvc, "health")
	if err == nil {
		t.Fatal("expected error from health command")
	}
}

// ---------------------------------------------------------------------------
// Status tests
// ---------------------------------------------------------------------------

func TestAdminStatus_Table(t *testing.T) {
	adminSvc := &fnAdminService{
		statusFn: func(_ context.Context) (*service.StatusInfo, error) {
			return &service.StatusInfo{
				NodeID:      "node-1",
				RaftAddress: "10.0.0.1:63001",
				IsLeader:    true,
				LeaderID:    1,
				ShardCount:  3,
				IsDraining:  false,
			}, nil
		},
	}

	output, err := executeAdminCmd(t, adminSvc, "status")
	if err != nil {
		t.Fatalf("admin status error: %v", err)
	}

	for _, want := range []string{
		"NodeID:",
		"node-1",
		"RaftAddress:",
		"10.0.0.1:63001",
		"IsLeader:",
		"true",
		"LeaderID:",
		"ShardCount:",
		"3",
		"IsDraining:",
		"false",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestAdminStatus_JSON(t *testing.T) {
	adminSvc := &fnAdminService{
		statusFn: func(_ context.Context) (*service.StatusInfo, error) {
			return &service.StatusInfo{
				NodeID:      "node-1",
				RaftAddress: "10.0.0.1:63001",
				IsLeader:    true,
				LeaderID:    1,
				ShardCount:  3,
				IsDraining:  false,
			}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	output, err := executeAdminCmd(t, adminSvc, "status")
	if err != nil {
		t.Fatalf("admin status json error: %v", err)
	}

	var resp transport.StatusResponse
	if jsonErr := json.Unmarshal([]byte(output), &resp); jsonErr != nil {
		t.Fatalf("JSON unmarshal error: %v, output: %q", jsonErr, output)
	}
	if resp.NodeID != "node-1" {
		t.Errorf("NodeID = %q, want %q", resp.NodeID, "node-1")
	}
	if resp.ShardCount != 3 {
		t.Errorf("ShardCount = %d, want 3", resp.ShardCount)
	}
}

func TestAdminStatus_ServiceError(t *testing.T) {
	adminSvc := &fnAdminService{
		statusFn: func(_ context.Context) (*service.StatusInfo, error) {
			return nil, &service.ReadError{Err: errors.New("raft timeout")}
		},
	}

	_, err := executeAdminCmd(t, adminSvc, "status")
	if err == nil {
		t.Fatal("expected error from status command")
	}
}

// ---------------------------------------------------------------------------
// Peers tests
// ---------------------------------------------------------------------------

func TestAdminPeers_Table(t *testing.T) {
	adminSvc := &fnAdminService{
		peersFn: func(_ context.Context) (*service.PeersInfo, error) {
			return &service.PeersInfo{
				Shards: []service.ShardPeers{
					{
						ShardID:  1,
						LeaderID: 10,
						Peers: []service.PeerInfo{
							{ReplicaID: 10, Address: "10.0.0.1:63001", IsLeader: true},
							{ReplicaID: 20, Address: "10.0.0.2:63001", IsLeader: false},
						},
					},
				},
			}, nil
		},
	}

	output, err := executeAdminCmd(t, adminSvc, "peers")
	if err != nil {
		t.Fatalf("admin peers error: %v", err)
	}

	for _, want := range []string{
		"SHARD_ID",
		"REPLICA_ID",
		"ADDRESS",
		"IS_LEADER",
		"10.0.0.1:63001",
		"10.0.0.2:63001",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestAdminPeers_JSON(t *testing.T) {
	adminSvc := &fnAdminService{
		peersFn: func(_ context.Context) (*service.PeersInfo, error) {
			return &service.PeersInfo{
				Shards: []service.ShardPeers{
					{
						ShardID:  1,
						LeaderID: 10,
						Peers: []service.PeerInfo{
							{ReplicaID: 10, Address: "10.0.0.1:63001", IsLeader: true},
						},
					},
				},
			}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	output, err := executeAdminCmd(t, adminSvc, "peers")
	if err != nil {
		t.Fatalf("admin peers json error: %v", err)
	}

	var resp transport.PeersResponse
	if jsonErr := json.Unmarshal([]byte(output), &resp); jsonErr != nil {
		t.Fatalf("JSON unmarshal error: %v, output: %q", jsonErr, output)
	}
	if len(resp.Shards) != 1 {
		t.Fatalf("Shards count = %d, want 1", len(resp.Shards))
	}
	if resp.Shards[0].Peers[0].Address != "10.0.0.1:63001" {
		t.Errorf("Peer address = %q, want %q", resp.Shards[0].Peers[0].Address, "10.0.0.1:63001")
	}
}

func TestAdminPeers_ServiceError(t *testing.T) {
	adminSvc := &fnAdminService{
		peersFn: func(_ context.Context) (*service.PeersInfo, error) {
			return nil, &service.ReadError{Err: errors.New("cluster unreachable")}
		},
	}

	_, err := executeAdminCmd(t, adminSvc, "peers")
	if err == nil {
		t.Fatal("expected error from peers command")
	}
}

// ---------------------------------------------------------------------------
// Member add tests
// ---------------------------------------------------------------------------

func TestAdminMemberAdd_Success(t *testing.T) {
	var gotShardID, gotReplicaID uint64
	var gotAddress string

	adminSvc := &fnAdminService{
		addMemberFn: func(_ context.Context, shardID, replicaID uint64, addr string) error {
			gotShardID = shardID
			gotReplicaID = replicaID
			gotAddress = addr
			return nil
		},
	}

	output, err := executeAdminCmd(t, adminSvc,
		"member", "add", "10.0.0.3:63001",
		"--shard-id", "1",
		"--replica-id", "30",
	)
	if err != nil {
		t.Fatalf("admin member add error: %v", err)
	}

	if !strings.Contains(output, "OK") {
		t.Errorf("output missing 'OK', got: %q", output)
	}
	if gotShardID != 1 {
		t.Errorf("shardID = %d, want 1", gotShardID)
	}
	if gotReplicaID != 30 {
		t.Errorf("replicaID = %d, want 30", gotReplicaID)
	}
	if gotAddress != "10.0.0.3:63001" {
		t.Errorf("address = %q, want %q", gotAddress, "10.0.0.3:63001")
	}
}

// TestAdminMemberAdd_Identity pins that --identity reaches the admin service,
// and that omitting it sends none (the committed identity is used).
func TestAdminMemberAdd_Identity(t *testing.T) {
	identity := "spki:" + strings.Repeat("ab", 32)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "with --identity", args: []string{"--identity", identity}, want: identity},
		{name: "without --identity", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adminSvc := &fnAdminService{
				addMemberFn: func(context.Context, uint64, uint64, string) error { return nil },
			}
			args := append([]string{"member", "add", "10.0.0.4:63001", "--shard-id", "1", "--replica-id", "4"}, tc.args...)
			if _, err := executeAdminCmd(t, adminSvc, args...); err != nil {
				t.Fatalf("admin member add: %v", err)
			}
			if adminSvc.addIdentity != tc.want {
				t.Fatalf("identity = %q, want %q", adminSvc.addIdentity, tc.want)
			}
		})
	}
}

func TestAdminMemberAdd_MissingShardID(t *testing.T) {
	adminSvc := &fnAdminService{
		addMemberFn: func(_ context.Context, _, _ uint64, _ string) error {
			return nil
		},
	}

	_, err := executeAdminCmd(t, adminSvc,
		"member", "add", "10.0.0.3:63001",
		"--replica-id", "30",
	)
	if err == nil {
		t.Fatal("expected error for missing --shard-id")
	}
	if !strings.Contains(err.Error(), "shard-id") {
		t.Errorf("error should mention shard-id, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Member remove tests
// ---------------------------------------------------------------------------

func TestAdminMemberRemove_Success(t *testing.T) {
	var gotShardID, gotReplicaID uint64

	adminSvc := &fnAdminService{
		removeMemberFn: func(_ context.Context, shardID, replicaID uint64) error {
			gotShardID = shardID
			gotReplicaID = replicaID
			return nil
		},
	}

	output, err := executeAdminCmd(t, adminSvc,
		"member", "remove", "20",
		"--shard-id", "1",
	)
	if err != nil {
		t.Fatalf("admin member remove error: %v", err)
	}

	if !strings.Contains(output, "OK") {
		t.Errorf("output missing 'OK', got: %q", output)
	}
	if gotShardID != 1 {
		t.Errorf("shardID = %d, want 1", gotShardID)
	}
	if gotReplicaID != 20 {
		t.Errorf("replicaID = %d, want 20", gotReplicaID)
	}
}

// ---------------------------------------------------------------------------
// Snapshot tests
// ---------------------------------------------------------------------------

func TestAdminSnapshot_Success(t *testing.T) {
	var gotShardID uint64

	adminSvc := &fnAdminService{
		requestSnapshotFn: func(_ context.Context, shardID uint64) error {
			gotShardID = shardID
			return nil
		},
	}

	output, err := executeAdminCmd(t, adminSvc,
		"snapshot",
		"--shard-id", "2",
	)
	if err != nil {
		t.Fatalf("admin snapshot error: %v", err)
	}

	if !strings.Contains(output, "OK") {
		t.Errorf("output missing 'OK', got: %q", output)
	}
	if gotShardID != 2 {
		t.Errorf("shardID = %d, want 2", gotShardID)
	}
}

// ---------------------------------------------------------------------------
// Drain tests
// ---------------------------------------------------------------------------

func TestAdminDrain_Success(t *testing.T) {
	called := false
	adminSvc := &fnAdminService{
		drainFn: func(_ context.Context) error {
			called = true
			return nil
		},
	}

	output, err := executeAdminCmd(t, adminSvc, "drain")
	if err != nil {
		t.Fatalf("admin drain error: %v", err)
	}

	if !called {
		t.Error("drain was not called")
	}
	if !strings.Contains(output, "OK") {
		t.Errorf("output missing 'OK', got: %q", output)
	}
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*noopKVService) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

// ListAll mirrors List on this double: it applies no internal-key guard, so
// the filtered and unfiltered enumerations are the same call.
func (m *noopKVService) ListAll(ctx context.Context, prefix string) ([]string, error) {
	return m.List(ctx, prefix)
}

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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// handleStartShard
// ---------------------------------------------------------------------------

func TestHandleStartShard_Success(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleStartShard(adminSvc)

	req := buildCallToolRequest(t, "qrdb_start_shard", map[string]any{
		"shard_id":   float64(1),
		"replica_id": float64(1),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleStartShard: %v", err)
	}

	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleStartShard_WithMembers(t *testing.T) {
	adminSvc := newMockAdminService()
	var capturedReq *service.StartShardRequest
	adminSvc.startShardFn = func(_ context.Context, req *service.StartShardRequest) error {
		capturedReq = req
		return nil
	}
	handler := handleStartShard(adminSvc)

	req := buildCallToolRequest(t, "qrdb_start_shard", map[string]any{
		"shard_id":     float64(2),
		"replica_id":   float64(3),
		"members_json": `{"1": "127.0.0.1:63001", "2": "127.0.0.1:63002"}`,
		"join":         false,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleStartShard: %v", err)
	}

	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}

	if capturedReq != nil {
		if capturedReq.ShardID != 2 {
			t.Errorf("shard_id: got %d, want %d", capturedReq.ShardID, 2)
		}
		if capturedReq.ReplicaID != 3 {
			t.Errorf("replica_id: got %d, want %d", capturedReq.ReplicaID, 3)
		}
	}
}

func TestHandleStartShard_MissingShardID(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleStartShard(adminSvc)

	req := buildCallToolRequest(t, "qrdb_start_shard", map[string]any{
		"replica_id": float64(1),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing shard_id")
	}
}

func TestHandleStartShard_MissingReplicaID(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleStartShard(adminSvc)

	req := buildCallToolRequest(t, "qrdb_start_shard", map[string]any{
		"shard_id": float64(1),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing replica_id")
	}
}

func TestHandleStartShard_InvalidMembersJSON(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleStartShard(adminSvc)

	req := buildCallToolRequest(t, "qrdb_start_shard", map[string]any{
		"shard_id":     float64(1),
		"replica_id":   float64(1),
		"members_json": "invalid-json",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid members_json")
	}
}

func TestHandleStartShard_InvalidMembersJSONKey(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleStartShard(adminSvc)

	req := buildCallToolRequest(t, "qrdb_start_shard", map[string]any{
		"shard_id":     float64(1),
		"replica_id":   float64(1),
		"members_json": `{"not-a-number": "127.0.0.1:63001"}`,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid replica ID in members_json key")
	}
}

func TestHandleStartShard_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.startShardFn = func(_ context.Context, _ *service.StartShardRequest) error {
		return errors.New("shard already started")
	}
	handler := handleStartShard(adminSvc)

	req := buildCallToolRequest(t, "qrdb_start_shard", map[string]any{
		"shard_id":   float64(1),
		"replica_id": float64(1),
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_start_shard" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_start_shard")
	}
}

// ---------------------------------------------------------------------------
// handleStopShard
// ---------------------------------------------------------------------------

func TestHandleStopShard_Success(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleStopShard(adminSvc)

	req := buildCallToolRequest(t, "qrdb_stop_shard", map[string]any{
		"shard_id": float64(1),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleStopShard: %v", err)
	}

	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleStopShard_MissingShardID(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleStopShard(adminSvc)

	req := buildCallToolRequest(t, "qrdb_stop_shard", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing shard_id")
	}
}

func TestHandleStopShard_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.stopShardFn = func(_ context.Context, _ uint64) error {
		return errors.New("shard not found")
	}
	handler := handleStopShard(adminSvc)

	req := buildCallToolRequest(t, "qrdb_stop_shard", map[string]any{
		"shard_id": float64(99),
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_stop_shard" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_stop_shard")
	}
}

// ---------------------------------------------------------------------------
// handleGetShardState
// ---------------------------------------------------------------------------

func TestHandleGetShardState_Success(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleGetShardState(adminSvc)

	req := buildCallToolRequest(t, "qrdb_shard_state", map[string]any{
		"shard_id": float64(1),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleGetShardState: %v", err)
	}

	text := resultText(t, result)
	var state service.ShardStateResponse
	if jsonErr := json.Unmarshal([]byte(text), &state); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
}

func TestHandleGetShardState_MissingShardID(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleGetShardState(adminSvc)

	req := buildCallToolRequest(t, "qrdb_shard_state", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing shard_id")
	}
}

func TestHandleGetShardState_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.getShardStateFn = func(_ context.Context, _ uint64) (*service.ShardStateResponse, error) {
		return nil, errors.New("shard not found")
	}
	handler := handleGetShardState(adminSvc)

	req := buildCallToolRequest(t, "qrdb_shard_state", map[string]any{
		"shard_id": float64(99),
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_shard_state" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_shard_state")
	}
}

// ---------------------------------------------------------------------------
// handleGetTopology
// ---------------------------------------------------------------------------

func TestHandleGetTopology_Success(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleGetTopology(adminSvc)

	req := buildCallToolRequest(t, "qrdb_topology", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleGetTopology: %v", err)
	}

	text := resultText(t, result)
	var topo service.TopologyResponse
	if jsonErr := json.Unmarshal([]byte(text), &topo); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
}

func TestHandleGetTopology_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.getTopologyFn = func(_ context.Context) (*service.TopologyResponse, error) {
		return nil, errors.New("topology unavailable")
	}
	handler := handleGetTopology(adminSvc)

	req := buildCallToolRequest(t, "qrdb_topology", map[string]any{})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_topology" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_topology")
	}
}

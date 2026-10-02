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

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// ---------------------------------------------------------------------------
// handleDatabaseUpdateProtocol
// ---------------------------------------------------------------------------

func TestHandleDatabaseUpdateProtocol_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	handler := handleDatabaseUpdateProtocol(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_update_protocol", map[string]any{
		"name":       "mydb",
		"mysql":      true,
		"postgresql": false,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDatabaseUpdateProtocol: %v", err)
	}

	text := resultText(t, result)
	var info database.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if info.Name != "mydb" {
		t.Errorf("name: got %q, want %q", info.Name, "mydb")
	}
}

func TestHandleDatabaseUpdateProtocol_MissingName(t *testing.T) {
	dbSvc := newMockDatabaseService()
	handler := handleDatabaseUpdateProtocol(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_update_protocol", map[string]any{
		"mysql": true,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleDatabaseUpdateProtocol_ServiceError(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.updateProtocolFn = func(_ string, _ database.ProtocolConfig) (*database.DatabaseInfo, error) {
		return nil, errors.New("update failed")
	}
	handler := handleDatabaseUpdateProtocol(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_update_protocol", map[string]any{
		"name":  "mydb",
		"mysql": true,
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_database_update_protocol" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_database_update_protocol")
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseUpdateEtcd
// ---------------------------------------------------------------------------

func TestHandleDatabaseUpdateEtcd_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.updateEtcdFn = func(_ string, cfg *database.EtcdConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:   "mydb",
			Engine: database.EngineKV,
			Status: database.StatusOpen,
		}, nil
	}
	handler := handleDatabaseUpdateEtcd(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_update_etcd", map[string]any{
		"name":        "mydb",
		"enabled":     true,
		"listen_addr": ":2379",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDatabaseUpdateEtcd: %v", err)
	}

	text := resultText(t, result)
	var info database.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if info.Name != "mydb" {
		t.Errorf("name: got %q, want %q", info.Name, "mydb")
	}
}

func TestHandleDatabaseUpdateEtcd_MissingName(t *testing.T) {
	dbSvc := newMockDatabaseService()
	handler := handleDatabaseUpdateEtcd(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_update_etcd", map[string]any{
		"enabled": true,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleDatabaseUpdateEtcd_ServiceError(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.updateEtcdFn = func(_ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
		return nil, errors.New("etcd update failed")
	}
	handler := handleDatabaseUpdateEtcd(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_update_etcd", map[string]any{
		"name":    "mydb",
		"enabled": true,
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_database_update_etcd" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_database_update_etcd")
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseUpdateRaft
// ---------------------------------------------------------------------------

func TestHandleDatabaseUpdateRaft_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.updateRaftFn = func(_ string, cfg *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:   "mydb",
			Engine: database.EngineKV,
			Status: database.StatusOpen,
		}, nil
	}
	handler := handleDatabaseUpdateRaft(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_update_raft", map[string]any{
		"name":         "mydb",
		"cluster_mode": "distributed",
		"shard_id":     float64(1),
		"replica_id":   float64(1),
		"raft_address": "127.0.0.1:63001",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDatabaseUpdateRaft: %v", err)
	}

	text := resultText(t, result)
	var info database.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if info.Name != "mydb" {
		t.Errorf("name: got %q, want %q", info.Name, "mydb")
	}
}

func TestHandleDatabaseUpdateRaft_MissingName(t *testing.T) {
	dbSvc := newMockDatabaseService()
	handler := handleDatabaseUpdateRaft(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_update_raft", map[string]any{
		"cluster_mode": "distributed",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleDatabaseUpdateRaft_ServiceError(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.updateRaftFn = func(_ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
		return nil, errors.New("raft update failed")
	}
	handler := handleDatabaseUpdateRaft(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_update_raft", map[string]any{
		"name":         "mydb",
		"cluster_mode": "distributed",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_database_update_raft" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_database_update_raft")
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseUpdateBarrier
// ---------------------------------------------------------------------------

func TestHandleDatabaseUpdateBarrier_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.updateBarrierFn = func(_ string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:   "mydb",
			Engine: database.EngineKV,
			Status: database.StatusOpen,
		}, nil
	}
	handler := handleDatabaseUpdateBarrier(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_update_barrier", map[string]any{
		"name":    "mydb",
		"enabled": true,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDatabaseUpdateBarrier: %v", err)
	}

	text := resultText(t, result)
	var info database.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if info.Name != "mydb" {
		t.Errorf("name: got %q, want %q", info.Name, "mydb")
	}
}

// TestHandleDatabaseUpdateBarrier_MetadataFields is the MEDIUM #9 proof for
// the MCP tool: mode/shares_required/shares_total tool arguments must reach
// UpdateBarrierForTenant's DatabaseBarrierConfig, not just enabled.
func TestHandleDatabaseUpdateBarrier_MetadataFields(t *testing.T) {
	dbSvc := newMockDatabaseService()
	var got *database.DatabaseBarrierConfig
	dbSvc.updateBarrierFn = func(_ string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
		got = cfg
		return &database.DatabaseInfo{Name: "mydb", Barrier: cfg}, nil
	}
	handler := handleDatabaseUpdateBarrier(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_update_barrier", map[string]any{
		"name":            "mydb",
		"enabled":         true,
		"mode":            "passphrase",
		"shares_required": 2,
		"shares_total":    3,
	})

	if _, err := handler(context.Background(), req); err != nil {
		t.Fatalf("handleDatabaseUpdateBarrier: %v", err)
	}

	if got == nil {
		t.Fatal("service was not invoked with a barrier config")
	}
	if got.Mode != "passphrase" {
		t.Errorf("Mode = %q, want %q", got.Mode, "passphrase")
	}
	if got.SharesRequired != 2 {
		t.Errorf("SharesRequired = %d, want 2", got.SharesRequired)
	}
	if got.SharesTotal != 3 {
		t.Errorf("SharesTotal = %d, want 3", got.SharesTotal)
	}
}

func TestHandleDatabaseUpdateBarrier_MissingName(t *testing.T) {
	dbSvc := newMockDatabaseService()
	handler := handleDatabaseUpdateBarrier(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_update_barrier", map[string]any{
		"enabled": true,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleDatabaseUpdateBarrier_ServiceError(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.updateBarrierFn = func(_ string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
		return nil, errors.New("barrier update failed")
	}
	handler := handleDatabaseUpdateBarrier(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_update_barrier", map[string]any{
		"name":    "mydb",
		"enabled": true,
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_database_update_barrier" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_database_update_barrier")
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseUpdateConnection
// ---------------------------------------------------------------------------

func TestHandleDatabaseUpdateConnection_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.updateConnectionFn = func(_ string, cfg *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:   "mydb",
			Engine: database.EngineKV,
			Status: database.StatusOpen,
		}, nil
	}
	handler := handleDatabaseUpdateConnection(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_update_connection", map[string]any{
		"name":           "mydb",
		"transport":      "tcp",
		"remote_address": "10.0.0.1:8080",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDatabaseUpdateConnection: %v", err)
	}

	text := resultText(t, result)
	var info database.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if info.Name != "mydb" {
		t.Errorf("name: got %q, want %q", info.Name, "mydb")
	}
}

func TestHandleDatabaseUpdateConnection_MissingName(t *testing.T) {
	dbSvc := newMockDatabaseService()
	handler := handleDatabaseUpdateConnection(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_update_connection", map[string]any{
		"transport": "tcp",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleDatabaseUpdateConnection_ServiceError(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.updateConnectionFn = func(_ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
		return nil, errors.New("connection update failed")
	}
	handler := handleDatabaseUpdateConnection(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_update_connection", map[string]any{
		"name":      "mydb",
		"transport": "tcp",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_database_update_connection" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_database_update_connection")
	}
}

// ---------------------------------------------------------------------------
// buildRaftConfigFromUpdateRequest
// ---------------------------------------------------------------------------

func TestBuildRaftConfigFromUpdateRequest_Static(t *testing.T) {
	req := buildCallToolRequest(t, "qrdb_database_update_raft", map[string]any{
		"name":                 "mydb",
		"cluster_mode":         "distributed",
		"discovery_method":     "static",
		"static_shared_secret": "mysecret",
	})

	cfg, err := buildRaftConfigFromUpdateRequest(req, "distributed")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClusterMode != database.RaftClusterMode("distributed") {
		t.Errorf("cluster_mode: got %q, want %q", cfg.ClusterMode, "distributed")
	}
	if cfg.Static == nil {
		t.Fatal("expected static discovery config to be set")
	}
	if cfg.Static.SharedSecret != "mysecret" {
		t.Errorf("shared_secret: got %q, want %q", cfg.Static.SharedSecret, "mysecret")
	}
}

func TestBuildRaftConfigFromUpdateRequest_Multicast(t *testing.T) {
	req := buildCallToolRequest(t, "qrdb_database_update_raft", map[string]any{
		"name":                    "mydb",
		"discovery_method":        "multicast",
		"multicast_shared_secret": "mc-secret",
		"multicast_group":         "239.255.77.77:63777",
		"multicast_interface":     "eth0",
		"multicast_min_peers":     float64(3),
	})

	cfg, err := buildRaftConfigFromUpdateRequest(req, "distributed")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Multicast == nil {
		t.Fatal("expected multicast discovery config to be set")
	}
	if cfg.Multicast.SharedSecret != "mc-secret" {
		t.Errorf("shared_secret: got %q, want %q", cfg.Multicast.SharedSecret, "mc-secret")
	}
	if cfg.Multicast.MulticastGroup != "239.255.77.77:63777" {
		t.Errorf("multicast_group: got %q, want %q", cfg.Multicast.MulticastGroup, "239.255.77.77:63777")
	}
}

func TestBuildRaftConfigFromUpdateRequest_DNS(t *testing.T) {
	req := buildCallToolRequest(t, "qrdb_database_update_raft", map[string]any{
		"name":             "mydb",
		"discovery_method": "dns",
		"dns_domain":       "example.com",
		"dns_service":      "qrdb",
		"dns_proto":        "tcp",
	})

	cfg, err := buildRaftConfigFromUpdateRequest(req, "distributed")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DNS == nil {
		t.Fatal("expected DNS discovery config to be set")
	}
	if cfg.DNS.Domain != "example.com" {
		t.Errorf("domain: got %q, want %q", cfg.DNS.Domain, "example.com")
	}
	if cfg.DNS.Service != "qrdb" {
		t.Errorf("service: got %q, want %q", cfg.DNS.Service, "qrdb")
	}
}

func TestBuildRaftConfigFromUpdateRequest_Token(t *testing.T) {
	req := buildCallToolRequest(t, "qrdb_database_update_raft", map[string]any{
		"name":               "mydb",
		"discovery_method":   "token",
		"token_endpoint":     "https://token.example.com",
		"token_value":        "mytoken",
		"token_cluster_size": float64(3),
	})

	cfg, err := buildRaftConfigFromUpdateRequest(req, "distributed")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token == nil {
		t.Fatal("expected token discovery config to be set")
	}
	if cfg.Token.Endpoint != "https://token.example.com" {
		t.Errorf("endpoint: got %q, want %q", cfg.Token.Endpoint, "https://token.example.com")
	}
	if cfg.Token.Token != "mytoken" {
		t.Errorf("token: got %q, want %q", cfg.Token.Token, "mytoken")
	}
	if cfg.Token.ClusterSize != 3 {
		t.Errorf("cluster_size: got %d, want %d", cfg.Token.ClusterSize, 3)
	}
}

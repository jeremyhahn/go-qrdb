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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd && !integration

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Stub backup service
// ---------------------------------------------------------------------------

type stubMCPBackupService struct {
	createFn  func(ctx context.Context, opts service.BackupOptions) (*service.BackupInfo, error)
	listFn    func(ctx context.Context) ([]service.BackupInfo, error)
	getFn     func(ctx context.Context, id string) (*service.BackupInfo, error)
	deleteFn  func(ctx context.Context, id string) error
	restoreFn func(ctx context.Context, opts service.RestoreOptions) (*service.RestoreResult, error)
	verifyFn  func(ctx context.Context, id string) (*service.VerifyResult, error)
}

var _ service.BackupServicer = (*stubMCPBackupService)(nil)

func newStubMCPBackupService() *stubMCPBackupService {
	now := time.Now()
	info := &service.BackupInfo{
		ID:          "bkp-1",
		Timestamp:   now,
		Size:        1024,
		ShardCount:  2,
		Status:      "completed",
		Checksum:    "abc123",
		Destination: "/backups",
		Duration:    500 * time.Millisecond,
	}
	return &stubMCPBackupService{
		createFn: func(_ context.Context, _ service.BackupOptions) (*service.BackupInfo, error) { return info, nil },
		listFn:   func(_ context.Context) ([]service.BackupInfo, error) { return []service.BackupInfo{*info}, nil },
		getFn:    func(_ context.Context, _ string) (*service.BackupInfo, error) { return info, nil },
		deleteFn: func(_ context.Context, _ string) error { return nil },
		restoreFn: func(_ context.Context, _ service.RestoreOptions) (*service.RestoreResult, error) {
			return &service.RestoreResult{BackupID: "bkp-1", ShardsRestored: 2, Duration: time.Second}, nil
		},
		verifyFn: func(_ context.Context, _ string) (*service.VerifyResult, error) {
			return &service.VerifyResult{Valid: true, ChecksumMatch: true, ExpectedChecksum: "abc", ActualChecksum: "abc"}, nil
		},
	}
}

func (s *stubMCPBackupService) CreateBackup(ctx context.Context, opts service.BackupOptions) (*service.BackupInfo, error) {
	return s.createFn(ctx, opts)
}
func (s *stubMCPBackupService) ListBackups(ctx context.Context) ([]service.BackupInfo, error) {
	return s.listFn(ctx)
}
func (s *stubMCPBackupService) GetBackup(ctx context.Context, id string) (*service.BackupInfo, error) {
	return s.getFn(ctx, id)
}
func (s *stubMCPBackupService) DeleteBackup(ctx context.Context, id string) error {
	return s.deleteFn(ctx, id)
}
func (s *stubMCPBackupService) RestoreBackup(ctx context.Context, opts service.RestoreOptions) (*service.RestoreResult, error) {
	return s.restoreFn(ctx, opts)
}
func (s *stubMCPBackupService) VerifyBackup(ctx context.Context, id string) (*service.VerifyResult, error) {
	return s.verifyFn(ctx, id)
}

// ---------------------------------------------------------------------------
// Stub rebalance service
// ---------------------------------------------------------------------------

type stubMCPRebalanceService struct {
	statsFn   func(ctx context.Context, shardID uint64) (*service.ShardStats, error)
	planFn    func(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error)
	executeFn func(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error)
	reportFn  func(ctx context.Context) (*service.ImbalanceReportResult, error)
}

var _ service.RebalanceServicer = (*stubMCPRebalanceService)(nil)

func newStubMCPRebalanceService() *stubMCPRebalanceService {
	return &stubMCPRebalanceService{
		statsFn: func(_ context.Context, shardID uint64) (*service.ShardStats, error) {
			return &service.ShardStats{ShardID: shardID, KeyCount: 100, ByteCount: 4096, LeaderID: 1, ReplicaCount: 3}, nil
		},
		planFn: func(_ context.Context, _ *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
			return &service.RebalancePlanResult{
				Moves: []service.RebalanceMovePlan{
					{ShardID: 1, FromNode: "n1", ToNode: "n2", EstimatedBytes: 512},
				},
				EstimatedDuration: 2 * time.Second,
				CurrentImbalance:  0.25,
			}, nil
		},
		executeFn: func(_ context.Context, _ *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
			return &service.RebalanceExecutionResult{
				MovesCompleted: 1,
				MovesFailed:    0,
				Duration:       3 * time.Second,
				FinalImbalance: 0.05,
			}, nil
		},
		reportFn: func(_ context.Context) (*service.ImbalanceReportResult, error) {
			return &service.ImbalanceReportResult{
				Nodes: []service.NodeLoadInfo{
					{NodeID: "n1", Address: "127.0.0.1:6300", ShardCount: 2, TotalKeys: 200},
				},
				ImbalanceScore: 0.1,
			}, nil
		},
	}
}

func (s *stubMCPRebalanceService) GetShardStats(ctx context.Context, shardID uint64) (*service.ShardStats, error) {
	return s.statsFn(ctx, shardID)
}
func (s *stubMCPRebalanceService) CreateRebalancePlan(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
	return s.planFn(ctx, opts)
}
func (s *stubMCPRebalanceService) ExecuteRebalance(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
	return s.executeFn(ctx, plan)
}
func (s *stubMCPRebalanceService) GetImbalanceReport(ctx context.Context) (*service.ImbalanceReportResult, error) {
	return s.reportFn(ctx)
}

// ---------------------------------------------------------------------------
// Stub database service (wraps existing mockDatabaseService for barrier tests)
// ---------------------------------------------------------------------------

type stubMCPDBWithBarrier struct {
	mockDatabaseService
	sealFn          func(ctx context.Context, name string) error
	unsealFn        func(ctx context.Context, name string, creds seal.Credentials) error
	barrierStatusFn func(name string) (*seal.BarrierStatus, error)
}

func newStubMCPDBWithBarrier() *stubMCPDBWithBarrier {
	return &stubMCPDBWithBarrier{
		sealFn:   func(_ context.Context, _ string) error { return nil },
		unsealFn: func(_ context.Context, _ string, _ seal.Credentials) error { return nil },
		barrierStatusFn: func(_ string) (*seal.BarrierStatus, error) {
			return &seal.BarrierStatus{
				Sealed:   false,
				Strategy: "software",
			}, nil
		},
	}
}

func (s *stubMCPDBWithBarrier) SealDatabase(ctx context.Context, name string) error {
	return s.sealFn(ctx, name)
}
func (s *stubMCPDBWithBarrier) UnsealDatabase(ctx context.Context, name string, creds seal.Credentials) error {
	return s.unsealFn(ctx, name, creds)
}
func (s *stubMCPDBWithBarrier) DatabaseBarrierStatus(_ context.Context, name string) (*seal.BarrierStatus, error) {
	return s.barrierStatusFn(name)
}

// ---------------------------------------------------------------------------
// Stub ceremony service with controllable custodian methods
// ---------------------------------------------------------------------------

type stubMCPCeremonySvc struct {
}

var _ service.CeremonyServicer = (*stubMCPCeremonySvc)(nil)

func newStubMCPCeremonySvc() *stubMCPCeremonySvc {
	return &stubMCPCeremonySvc{}
}

// CeremonyInitialize — not used in custodian tests.
func (s *stubMCPCeremonySvc) CeremonyInitialize(_ context.Context, _ ceremony.CeremonyCA, _ *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	return nil, nil
}
func (s *stubMCPCeremonySvc) CeremonySignCSR(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) {
	return nil, nil
}
func (s *stubMCPCeremonySvc) CeremonyStatus(_ context.Context) (*service.CeremonyStatusInfo, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// WithBackupService server option
// ---------------------------------------------------------------------------

func TestServer_WithBackupService(t *testing.T) {
	bkpSvc := newStubMCPBackupService()
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithBackupService(bkpSvc))

	if s.backupSvc != bkpSvc {
		t.Error("backupSvc not set on server")
	}

	tools := s.MCPServer().ListTools()
	for _, name := range []string{
		"qrdb_backup_create",
		"qrdb_backup_list",
		"qrdb_backup_get",
		"qrdb_backup_delete",
		"qrdb_backup_restore",
		"qrdb_backup_verify",
	} {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %s not registered", name)
		}
	}
}

// ---------------------------------------------------------------------------
// WithRebalanceService server option
// ---------------------------------------------------------------------------

func TestServer_WithRebalanceService(t *testing.T) {
	rbSvc := newStubMCPRebalanceService()
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithRebalanceService(rbSvc))

	if s.rebalanceSvc != rbSvc {
		t.Error("rebalanceSvc not set on server")
	}

	tools := s.MCPServer().ListTools()
	for _, name := range []string{
		"qrdb_rebalance_shard_stats",
		"qrdb_rebalance_plan",
		"qrdb_rebalance_execute",
		"qrdb_rebalance_report",
	} {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %s not registered", name)
		}
	}
}

// ---------------------------------------------------------------------------
// parseShardIDs
// ---------------------------------------------------------------------------

func TestParseShardIDs_Empty(t *testing.T) {
	req := buildCallToolRequest(t, "test", map[string]any{})
	ids := parseShardIDs(req, "shard_ids")
	if len(ids) != 0 {
		t.Errorf("expected empty, got %v", ids)
	}
}

func TestParseShardIDs_NonSlice(t *testing.T) {
	req := buildCallToolRequest(t, "test", map[string]any{"shard_ids": "bad"})
	ids := parseShardIDs(req, "shard_ids")
	if len(ids) != 0 {
		t.Errorf("expected empty for non-slice, got %v", ids)
	}
}

func TestParseShardIDs_ValidSlice(t *testing.T) {
	req := buildCallToolRequest(t, "test", map[string]any{
		"shard_ids": []interface{}{float64(1), float64(3)},
	})
	ids := parseShardIDs(req, "shard_ids")
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 3 {
		t.Errorf("expected [1 3], got %v", ids)
	}
}

func TestParseShardIDs_NonFloat64Ignored(t *testing.T) {
	req := buildCallToolRequest(t, "test", map[string]any{
		"shard_ids": []interface{}{float64(5), "bad", float64(7)},
	})
	ids := parseShardIDs(req, "shard_ids")
	if len(ids) != 2 || ids[0] != 5 || ids[1] != 7 {
		t.Errorf("expected [5 7], got %v", ids)
	}
}

// ---------------------------------------------------------------------------
// marshalToolResult – error path
// ---------------------------------------------------------------------------

func TestMarshalToolResult_Success(t *testing.T) {
	type payload struct {
		ID string `json:"id"`
	}
	result, err := marshalToolResult("test", payload{ID: "abc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var p payload
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.ID != "abc" {
		t.Errorf("id = %q, want abc", p.ID)
	}
}

// ---------------------------------------------------------------------------
// handleBackupCreate – missing destination / base64 key / success / error
// ---------------------------------------------------------------------------

func TestHandleBackupCreate_MissingDestination(t *testing.T) {
	svc := newStubMCPBackupService()
	handler := handleBackupCreate(svc)
	req := buildCallToolRequest(t, "qrdb_backup_create", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing destination")
	}
}

func TestHandleBackupCreate_BadBase64Key(t *testing.T) {
	svc := newStubMCPBackupService()
	handler := handleBackupCreate(svc)
	req := buildCallToolRequest(t, "qrdb_backup_create", map[string]any{
		"destination":    "/tmp",
		"encryption_key": "!!!not-base64!!!",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for bad base64 key")
	}
}

func TestHandleBackupCreate_Success(t *testing.T) {
	svc := newStubMCPBackupService()
	handler := handleBackupCreate(svc)

	encKey := base64.StdEncoding.EncodeToString([]byte("test-key"))
	req := buildCallToolRequest(t, "qrdb_backup_create", map[string]any{
		"destination":    "/backups",
		"compression":    "zstd",
		"encryption":     "aes256",
		"encryption_key": encKey,
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", resultText(t, result))
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result text")
	}
}

func TestHandleBackupCreate_ServiceError(t *testing.T) {
	svc := newStubMCPBackupService()
	svc.createFn = func(_ context.Context, _ service.BackupOptions) (*service.BackupInfo, error) {
		return nil, errors.New("storage full")
	}
	handler := handleBackupCreate(svc)
	req := buildCallToolRequest(t, "qrdb_backup_create", map[string]any{
		"destination": "/backups",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service failure")
	}
}

// ---------------------------------------------------------------------------
// handleBackupList
// ---------------------------------------------------------------------------

func TestHandleBackupList_Success(t *testing.T) {
	svc := newStubMCPBackupService()
	handler := handleBackupList(svc)
	req := buildCallToolRequest(t, "qrdb_backup_list", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", resultText(t, result))
	}
}

func TestHandleBackupList_ServiceError(t *testing.T) {
	svc := newStubMCPBackupService()
	svc.listFn = func(_ context.Context) ([]service.BackupInfo, error) {
		return nil, errors.New("list failed")
	}
	handler := handleBackupList(svc)
	req := buildCallToolRequest(t, "qrdb_backup_list", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service failure")
	}
}

// ---------------------------------------------------------------------------
// handleBackupGet
// ---------------------------------------------------------------------------

func TestHandleBackupGet_MissingID(t *testing.T) {
	svc := newStubMCPBackupService()
	handler := handleBackupGet(svc)
	req := buildCallToolRequest(t, "qrdb_backup_get", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing id")
	}
}

func TestHandleBackupGet_Success(t *testing.T) {
	svc := newStubMCPBackupService()
	handler := handleBackupGet(svc)
	req := buildCallToolRequest(t, "qrdb_backup_get", map[string]any{"id": "bkp-1"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result")
	}
}

func TestHandleBackupGet_ServiceError(t *testing.T) {
	svc := newStubMCPBackupService()
	svc.getFn = func(_ context.Context, _ string) (*service.BackupInfo, error) {
		return nil, errors.New("not found")
	}
	handler := handleBackupGet(svc)
	req := buildCallToolRequest(t, "qrdb_backup_get", map[string]any{"id": "bkp-1"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service failure")
	}
}

// ---------------------------------------------------------------------------
// handleBackupDelete
// ---------------------------------------------------------------------------

func TestHandleBackupDelete_MissingID(t *testing.T) {
	svc := newStubMCPBackupService()
	handler := handleBackupDelete(svc)
	req := buildCallToolRequest(t, "qrdb_backup_delete", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing id")
	}
}

func TestHandleBackupDelete_Success(t *testing.T) {
	svc := newStubMCPBackupService()
	handler := handleBackupDelete(svc)
	req := buildCallToolRequest(t, "qrdb_backup_delete", map[string]any{"id": "bkp-1"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result")
	}
}

func TestHandleBackupDelete_ServiceError(t *testing.T) {
	svc := newStubMCPBackupService()
	svc.deleteFn = func(_ context.Context, _ string) error { return errors.New("locked") }
	handler := handleBackupDelete(svc)
	req := buildCallToolRequest(t, "qrdb_backup_delete", map[string]any{"id": "bkp-1"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service failure")
	}
}

// ---------------------------------------------------------------------------
// handleBackupRestore
// ---------------------------------------------------------------------------

func TestHandleBackupRestore_MissingBackupID(t *testing.T) {
	svc := newStubMCPBackupService()
	handler := handleBackupRestore(svc)
	req := buildCallToolRequest(t, "qrdb_backup_restore", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing backup_id")
	}
}

func TestHandleBackupRestore_BadBase64DecryptionKey(t *testing.T) {
	svc := newStubMCPBackupService()
	handler := handleBackupRestore(svc)
	req := buildCallToolRequest(t, "qrdb_backup_restore", map[string]any{
		"backup_id":      "bkp-1",
		"decryption_key": "!!!not-base64!!!",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for bad base64 decryption key")
	}
}

func TestHandleBackupRestore_Success(t *testing.T) {
	svc := newStubMCPBackupService()
	handler := handleBackupRestore(svc)
	decKey := base64.StdEncoding.EncodeToString([]byte("dec-key"))
	req := buildCallToolRequest(t, "qrdb_backup_restore", map[string]any{
		"backup_id":      "bkp-1",
		"target_shards":  []interface{}{float64(1)},
		"validate_first": true,
		"decryption_key": decKey,
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", resultText(t, result))
	}
}

func TestHandleBackupRestore_ServiceError(t *testing.T) {
	svc := newStubMCPBackupService()
	svc.restoreFn = func(_ context.Context, _ service.RestoreOptions) (*service.RestoreResult, error) {
		return nil, errors.New("restore failed")
	}
	handler := handleBackupRestore(svc)
	req := buildCallToolRequest(t, "qrdb_backup_restore", map[string]any{"backup_id": "bkp-1"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service failure")
	}
}

// ---------------------------------------------------------------------------
// handleBackupVerify
// ---------------------------------------------------------------------------

func TestHandleBackupVerify_MissingID(t *testing.T) {
	svc := newStubMCPBackupService()
	handler := handleBackupVerify(svc)
	req := buildCallToolRequest(t, "qrdb_backup_verify", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing id")
	}
}

func TestHandleBackupVerify_Success(t *testing.T) {
	svc := newStubMCPBackupService()
	handler := handleBackupVerify(svc)
	req := buildCallToolRequest(t, "qrdb_backup_verify", map[string]any{"id": "bkp-1"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result")
	}
}

func TestHandleBackupVerify_ServiceError(t *testing.T) {
	svc := newStubMCPBackupService()
	svc.verifyFn = func(_ context.Context, _ string) (*service.VerifyResult, error) {
		return nil, errors.New("checksum unavailable")
	}
	handler := handleBackupVerify(svc)
	req := buildCallToolRequest(t, "qrdb_backup_verify", map[string]any{"id": "bkp-1"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service failure")
	}
}

// ---------------------------------------------------------------------------
// handleRebalanceShardStats
// ---------------------------------------------------------------------------

func TestHandleRebalanceShardStats_Success(t *testing.T) {
	svc := newStubMCPRebalanceService()
	handler := handleRebalanceShardStats(svc)
	req := buildCallToolRequest(t, "qrdb_rebalance_shard_stats", map[string]any{"shard_id": float64(3)})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", resultText(t, result))
	}
}

func TestHandleRebalanceShardStats_ServiceError(t *testing.T) {
	svc := newStubMCPRebalanceService()
	svc.statsFn = func(_ context.Context, _ uint64) (*service.ShardStats, error) {
		return nil, errors.New("shard not found")
	}
	handler := handleRebalanceShardStats(svc)
	req := buildCallToolRequest(t, "qrdb_rebalance_shard_stats", map[string]any{"shard_id": float64(1)})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service failure")
	}
}

// ---------------------------------------------------------------------------
// handleRebalancePlan
// ---------------------------------------------------------------------------

func TestHandleRebalancePlan_Success(t *testing.T) {
	svc := newStubMCPRebalanceService()
	handler := handleRebalancePlan(svc)
	req := buildCallToolRequest(t, "qrdb_rebalance_plan", map[string]any{
		"max_moves":      float64(5),
		"dry_run":        true,
		"target_balance": float64(80),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", resultText(t, result))
	}
}

func TestHandleRebalancePlan_ServiceError(t *testing.T) {
	svc := newStubMCPRebalanceService()
	svc.planFn = func(_ context.Context, _ *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
		return nil, errors.New("cluster unreachable")
	}
	handler := handleRebalancePlan(svc)
	req := buildCallToolRequest(t, "qrdb_rebalance_plan", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service failure")
	}
}

// ---------------------------------------------------------------------------
// handleRebalanceExecute
// ---------------------------------------------------------------------------

func TestHandleRebalanceExecute_MissingPlan(t *testing.T) {
	svc := newStubMCPRebalanceService()
	handler := handleRebalanceExecute(svc)
	req := buildCallToolRequest(t, "qrdb_rebalance_execute", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing plan")
	}
}

func TestHandleRebalanceExecute_InvalidJSON(t *testing.T) {
	svc := newStubMCPRebalanceService()
	handler := handleRebalanceExecute(svc)
	req := buildCallToolRequest(t, "qrdb_rebalance_execute", map[string]any{
		"plan": "not-json",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for invalid plan JSON")
	}
}

func TestHandleRebalanceExecute_Success(t *testing.T) {
	svc := newStubMCPRebalanceService()
	handler := handleRebalanceExecute(svc)
	planJSON, _ := json.Marshal(service.RebalancePlanResult{
		Moves: []service.RebalanceMovePlan{
			{ShardID: 1, FromNode: "n1", ToNode: "n2", EstimatedBytes: 256},
		},
	})
	req := buildCallToolRequest(t, "qrdb_rebalance_execute", map[string]any{
		"plan": string(planJSON),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", resultText(t, result))
	}
}

func TestHandleRebalanceExecute_ServiceError(t *testing.T) {
	svc := newStubMCPRebalanceService()
	svc.executeFn = func(_ context.Context, _ *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
		return nil, errors.New("execution failed")
	}
	handler := handleRebalanceExecute(svc)
	planJSON, _ := json.Marshal(service.RebalancePlanResult{})
	req := buildCallToolRequest(t, "qrdb_rebalance_execute", map[string]any{
		"plan": string(planJSON),
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service failure")
	}
}

// ---------------------------------------------------------------------------
// handleRebalanceReport
// ---------------------------------------------------------------------------

func TestHandleRebalanceReport_Success(t *testing.T) {
	svc := newStubMCPRebalanceService()
	handler := handleRebalanceReport(svc)
	req := buildCallToolRequest(t, "qrdb_rebalance_report", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", resultText(t, result))
	}
}

func TestHandleRebalanceReport_ServiceError(t *testing.T) {
	svc := newStubMCPRebalanceService()
	svc.reportFn = func(_ context.Context) (*service.ImbalanceReportResult, error) {
		return nil, errors.New("metrics unavailable")
	}
	handler := handleRebalanceReport(svc)
	req := buildCallToolRequest(t, "qrdb_rebalance_report", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service failure")
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseSeal
// ---------------------------------------------------------------------------

func TestHandleDatabaseSeal_MissingName(t *testing.T) {
	svc := newStubMCPDBWithBarrier()
	handler := handleDatabaseSeal(svc)
	req := buildCallToolRequest(t, "qrdb_database_seal", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing name")
	}
}

func TestHandleDatabaseSeal_Success(t *testing.T) {
	svc := newStubMCPDBWithBarrier()
	handler := handleDatabaseSeal(svc)
	req := buildCallToolRequest(t, "qrdb_database_seal", map[string]any{"name": "mydb"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", resultText(t, result))
	}
}

func TestHandleDatabaseSeal_ServiceError(t *testing.T) {
	svc := newStubMCPDBWithBarrier()
	svc.sealFn = func(_ context.Context, _ string) error { return errors.New("seal failed") }
	handler := handleDatabaseSeal(svc)
	req := buildCallToolRequest(t, "qrdb_database_seal", map[string]any{"name": "mydb"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service failure")
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseUnseal
// ---------------------------------------------------------------------------

func TestHandleDatabaseUnseal_MissingName(t *testing.T) {
	svc := newStubMCPDBWithBarrier()
	handler := handleDatabaseUnseal(svc)
	req := buildCallToolRequest(t, "qrdb_database_unseal", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing name")
	}
}

func TestHandleDatabaseUnseal_Success(t *testing.T) {
	svc := newStubMCPDBWithBarrier()
	handler := handleDatabaseUnseal(svc)
	req := buildCallToolRequest(t, "qrdb_database_unseal", map[string]any{
		"name":   "mydb",
		"secret": "mysecret",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", resultText(t, result))
	}
}

func TestHandleDatabaseUnseal_ServiceError(t *testing.T) {
	svc := newStubMCPDBWithBarrier()
	svc.unsealFn = func(_ context.Context, _ string, _ seal.Credentials) error {
		return errors.New("wrong secret")
	}
	handler := handleDatabaseUnseal(svc)
	req := buildCallToolRequest(t, "qrdb_database_unseal", map[string]any{
		"name":   "mydb",
		"secret": "bad",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service failure")
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseBarrierStatus
// ---------------------------------------------------------------------------

func TestHandleDatabaseBarrierStatus_MissingName(t *testing.T) {
	svc := newStubMCPDBWithBarrier()
	handler := handleDatabaseBarrierStatus(svc)
	req := buildCallToolRequest(t, "qrdb_database_barrier_status", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing name")
	}
}

func TestHandleDatabaseBarrierStatus_Success(t *testing.T) {
	svc := newStubMCPDBWithBarrier()
	handler := handleDatabaseBarrierStatus(svc)
	req := buildCallToolRequest(t, "qrdb_database_barrier_status", map[string]any{"name": "mydb"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", resultText(t, result))
	}
}

func TestHandleDatabaseBarrierStatus_ServiceError(t *testing.T) {
	svc := newStubMCPDBWithBarrier()
	svc.barrierStatusFn = func(_ string) (*seal.BarrierStatus, error) {
		return nil, errors.New("database not found")
	}
	handler := handleDatabaseBarrierStatus(svc)
	req := buildCallToolRequest(t, "qrdb_database_barrier_status", map[string]any{"name": "mydb"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service failure")
	}
}

// ---------------------------------------------------------------------------
// handleCredentialStore
// ---------------------------------------------------------------------------

func TestHandleCredentialStore_MissingName(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialStore(svc)
	req := buildCallToolRequest(t, "qrdb_credential_store", map[string]any{"value": "v"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing name")
	}
}

func TestHandleCredentialStore_MissingValue(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialStore(svc)
	req := buildCallToolRequest(t, "qrdb_credential_store", map[string]any{"name": "mykey"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing value")
	}
}

func TestHandleCredentialStore_Success(t *testing.T) {
	svc := newMockCredentialService()
	stored := ""
	svc.storeFn = func(_ context.Context, name string, _ []byte) error {
		stored = name
		return nil
	}
	handler := handleCredentialStore(svc)
	req := buildCallToolRequest(t, "qrdb_credential_store", map[string]any{
		"name":  "mykey",
		"value": base64.StdEncoding.EncodeToString([]byte("s3cr3t")),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", resultText(t, result))
	}
	if stored != "mykey" {
		t.Errorf("stored = %q, want mykey", stored)
	}
}

func TestHandleCredentialStore_ServiceError(t *testing.T) {
	svc := newMockCredentialService()
	svc.storeFn = func(_ context.Context, _ string, _ []byte) error {
		return errors.New("barrier sealed")
	}
	handler := handleCredentialStore(svc)
	req := buildCallToolRequest(t, "qrdb_credential_store", map[string]any{
		"name":  "k",
		"value": base64.StdEncoding.EncodeToString([]byte("v")),
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service failure")
	}
}

// ---------------------------------------------------------------------------
// handleCredentialRetrieve
// ---------------------------------------------------------------------------

func TestHandleCredentialRetrieve_MissingName(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialRetrieve(svc)
	req := buildCallToolRequest(t, "qrdb_credential_retrieve", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing name")
	}
}

func TestHandleCredentialRetrieve_Success(t *testing.T) {
	svc := newMockCredentialService()
	svc.retrieveFn = func(_ context.Context, _ string) ([]byte, error) {
		return []byte("retrieved-value"), nil
	}
	handler := handleCredentialRetrieve(svc)
	req := buildCallToolRequest(t, "qrdb_credential_retrieve", map[string]any{"name": "mykey"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result")
	}
	decoded, decErr := base64.StdEncoding.DecodeString(resultText(t, result))
	if decErr != nil {
		t.Fatalf("result is not valid base64: %v", decErr)
	}
	if string(decoded) != "retrieved-value" {
		t.Errorf("value = %q, want retrieved-value", string(decoded))
	}
}

func TestHandleCredentialRetrieve_ServiceError(t *testing.T) {
	svc := newMockCredentialService()
	svc.retrieveFn = func(_ context.Context, _ string) ([]byte, error) {
		return nil, errors.New("not found")
	}
	handler := handleCredentialRetrieve(svc)
	req := buildCallToolRequest(t, "qrdb_credential_retrieve", map[string]any{"name": "k"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service failure")
	}
}

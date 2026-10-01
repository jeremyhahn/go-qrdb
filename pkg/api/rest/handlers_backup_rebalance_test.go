// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !integration

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Stub backup service
// ---------------------------------------------------------------------------

type stubBackupService struct {
	createFn  func(ctx context.Context, opts service.BackupOptions) (*service.BackupInfo, error)
	listFn    func(ctx context.Context) ([]service.BackupInfo, error)
	getFn     func(ctx context.Context, id string) (*service.BackupInfo, error)
	deleteFn  func(ctx context.Context, id string) error
	restoreFn func(ctx context.Context, opts service.RestoreOptions) (*service.RestoreResult, error)
	verifyFn  func(ctx context.Context, id string) (*service.VerifyResult, error)
}

var _ service.BackupServicer = (*stubBackupService)(nil)

func newStubBackupService() *stubBackupService {
	now := time.Now()
	info := &service.BackupInfo{
		ID:          "bkp-1",
		Timestamp:   now,
		Size:        1024,
		ShardCount:  2,
		Status:      "completed",
		Checksum:    "abc123",
		Destination: "/backups",
		Compression: "zstd",
		Encryption:  "none",
		Duration:    500 * time.Millisecond,
	}
	return &stubBackupService{
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

func (s *stubBackupService) CreateBackup(ctx context.Context, opts service.BackupOptions) (*service.BackupInfo, error) {
	return s.createFn(ctx, opts)
}
func (s *stubBackupService) ListBackups(ctx context.Context) ([]service.BackupInfo, error) {
	return s.listFn(ctx)
}
func (s *stubBackupService) GetBackup(ctx context.Context, id string) (*service.BackupInfo, error) {
	return s.getFn(ctx, id)
}
func (s *stubBackupService) DeleteBackup(ctx context.Context, id string) error {
	return s.deleteFn(ctx, id)
}
func (s *stubBackupService) RestoreBackup(ctx context.Context, opts service.RestoreOptions) (*service.RestoreResult, error) {
	return s.restoreFn(ctx, opts)
}
func (s *stubBackupService) VerifyBackup(ctx context.Context, id string) (*service.VerifyResult, error) {
	return s.verifyFn(ctx, id)
}

// ---------------------------------------------------------------------------
// Stub rebalance service
// ---------------------------------------------------------------------------

type stubRebalanceService struct {
	statsFn   func(ctx context.Context, shardID uint64) (*service.ShardStats, error)
	planFn    func(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error)
	executeFn func(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error)
	reportFn  func(ctx context.Context) (*service.ImbalanceReportResult, error)
}

var _ service.RebalanceServicer = (*stubRebalanceService)(nil)

func newStubRebalanceService() *stubRebalanceService {
	return &stubRebalanceService{
		statsFn: func(_ context.Context, shardID uint64) (*service.ShardStats, error) {
			return &service.ShardStats{ShardID: shardID, KeyCount: 100, ByteCount: 4096, LeaderID: 1, ReplicaCount: 3}, nil
		},
		planFn: func(_ context.Context, _ *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
			return &service.RebalancePlanResult{
				Moves: []service.RebalanceMovePlan{
					{ShardID: 1, FromNode: "node-a", ToNode: "node-b", EstimatedBytes: 512},
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
					{NodeID: "n1", Address: "127.0.0.1:6300", ShardCount: 2, TotalKeys: 200, TotalBytes: 8192, LeaderCount: 1},
				},
				ImbalanceScore: 0.1,
				Hotspots:       nil,
			}, nil
		},
	}
}

func (s *stubRebalanceService) GetShardStats(ctx context.Context, shardID uint64) (*service.ShardStats, error) {
	return s.statsFn(ctx, shardID)
}
func (s *stubRebalanceService) CreateRebalancePlan(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
	return s.planFn(ctx, opts)
}
func (s *stubRebalanceService) ExecuteRebalance(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
	return s.executeFn(ctx, plan)
}
func (s *stubRebalanceService) GetImbalanceReport(ctx context.Context) (*service.ImbalanceReportResult, error) {
	return s.reportFn(ctx)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newBackupServer(t *testing.T, bkp service.BackupServicer) *Server {
	t.Helper()
	return NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithBackupService(bkp))
}

func newRebalanceServer(t *testing.T, rb service.RebalanceServicer) *Server {
	t.Helper()
	return NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithRebalanceService(rb))
}

// ---------------------------------------------------------------------------
// handleCreateBackup
// ---------------------------------------------------------------------------

func TestHandleCreateBackup_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/backups", strings.NewReader(`{"destination":"/tmp"}`))
	rec := httptest.NewRecorder()
	s.handleCreateBackup(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCreateBackup_BadBody(t *testing.T) {
	s := newBackupServer(t, newStubBackupService())
	req := httptest.NewRequest(http.MethodPost, "/v1/backups", strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()
	s.handleCreateBackup(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCreateBackup_Success(t *testing.T) {
	s := newBackupServer(t, newStubBackupService())
	body := `{"destination":"/backups","compression":"zstd"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/backups", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCreateBackup(rec, req)
	assertStatus(t, rec, http.StatusCreated)

	var resp backupResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != "bkp-1" {
		t.Errorf("id = %q, want bkp-1", resp.ID)
	}
}

func TestHandleCreateBackup_ServiceError(t *testing.T) {
	svc := newStubBackupService()
	svc.createFn = func(_ context.Context, _ service.BackupOptions) (*service.BackupInfo, error) {
		return nil, &service.BackupNotFoundError{ID: "x"}
	}
	s := newBackupServer(t, svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/backups", strings.NewReader(`{"destination":"/tmp"}`))
	rec := httptest.NewRecorder()
	s.handleCreateBackup(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// handleListBackups
// ---------------------------------------------------------------------------

func TestHandleListBackups_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodGet, "/v1/backups/list", nil)
	rec := httptest.NewRecorder()
	s.handleListBackups(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleListBackups_Success(t *testing.T) {
	s := newBackupServer(t, newStubBackupService())
	req := httptest.NewRequest(http.MethodGet, "/v1/backups/list", nil)
	rec := httptest.NewRecorder()
	s.handleListBackups(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp listBackupsResponse
	decodeJSON(t, rec, &resp)
	if resp.Total != 1 || len(resp.Backups) != 1 {
		t.Errorf("total = %d, want 1", resp.Total)
	}
}

func TestHandleListBackups_ServiceError(t *testing.T) {
	svc := newStubBackupService()
	svc.listFn = func(_ context.Context) ([]service.BackupInfo, error) {
		return nil, errors.New("storage unavailable")
	}
	s := newBackupServer(t, svc)
	req := httptest.NewRequest(http.MethodGet, "/v1/backups/list", nil)
	rec := httptest.NewRecorder()
	s.handleListBackups(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// handleRestoreBackup
// ---------------------------------------------------------------------------

func TestHandleRestoreBackup_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/backups/restore", strings.NewReader(`{"backup_id":"bkp-1"}`))
	rec := httptest.NewRecorder()
	s.handleRestoreBackup(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleRestoreBackup_BadBody(t *testing.T) {
	s := newBackupServer(t, newStubBackupService())
	req := httptest.NewRequest(http.MethodPost, "/v1/backups/restore", strings.NewReader(`bad`))
	rec := httptest.NewRecorder()
	s.handleRestoreBackup(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleRestoreBackup_Success(t *testing.T) {
	s := newBackupServer(t, newStubBackupService())
	req := httptest.NewRequest(http.MethodPost, "/v1/backups/restore", strings.NewReader(`{"backup_id":"bkp-1"}`))
	rec := httptest.NewRecorder()
	s.handleRestoreBackup(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp restoreResultResponse
	decodeJSON(t, rec, &resp)
	if resp.BackupID != "bkp-1" {
		t.Errorf("backup_id = %q, want bkp-1", resp.BackupID)
	}
	if resp.ShardsRestored != 2 {
		t.Errorf("shards_restored = %d, want 2", resp.ShardsRestored)
	}
}

// TestHandleRestoreBackup_OnlineGuardRefused proves a guarded online-restore
// refusal (e.g. the system shard, or a replicated shard) surfaces as a
// per-shard error in a 200 OK restoreResultResponse — matching
// BackupService.RestoreBackup's existing partial-success contract for any
// other per-shard restore failure — rather than failing the whole request.
// The GUI/CLI must inspect result.errors[] to see the guard's
// offline-restore guidance even on a 200 response.
func TestHandleRestoreBackup_OnlineGuardRefused(t *testing.T) {
	svc := newStubBackupService()
	svc.restoreFn = func(_ context.Context, opts service.RestoreOptions) (*service.RestoreResult, error) {
		return &service.RestoreResult{
			BackupID:       opts.BackupID,
			ShardsRestored: 0,
			Errors: []service.ShardError{
				{ShardID: 1, Message: (&service.OnlineRestoreSystemShardError{ShardID: 1}).Error()},
			},
		}, nil
	}
	s := newBackupServer(t, svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/backups/restore", strings.NewReader(`{"backup_id":"bkp-1"}`))
	rec := httptest.NewRecorder()
	s.handleRestoreBackup(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp restoreResultResponse
	decodeJSON(t, rec, &resp)
	if resp.ShardsRestored != 0 {
		t.Errorf("shards_restored = %d, want 0", resp.ShardsRestored)
	}
	if len(resp.Errors) != 1 {
		t.Fatalf("errors = %+v, want 1 entry", resp.Errors)
	}
	if !strings.Contains(resp.Errors[0].Message, "offline") {
		t.Errorf("error message = %q, want it to mention the offline restore flow", resp.Errors[0].Message)
	}
}

func TestHandleRestoreBackup_ServiceError(t *testing.T) {
	svc := newStubBackupService()
	svc.restoreFn = func(_ context.Context, _ service.RestoreOptions) (*service.RestoreResult, error) {
		return nil, &service.BackupNotFoundError{ID: "bkp-1"}
	}
	s := newBackupServer(t, svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/backups/restore", strings.NewReader(`{"backup_id":"bkp-1"}`))
	rec := httptest.NewRecorder()
	s.handleRestoreBackup(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// handleBackupByID — dispatch layer
// ---------------------------------------------------------------------------

func TestHandleBackupByID_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodGet, "/v1/backups/bkp-1", nil)
	rec := httptest.NewRecorder()
	s.handleBackupByID(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleBackupByID_MissingID(t *testing.T) {
	s := newBackupServer(t, newStubBackupService())
	req := httptest.NewRequest(http.MethodGet, "/v1/backups/", nil)
	rec := httptest.NewRecorder()
	s.handleBackupByID(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleBackupByID_GET_Success(t *testing.T) {
	s := newBackupServer(t, newStubBackupService())
	req := httptest.NewRequest(http.MethodGet, "/v1/backups/bkp-1", nil)
	rec := httptest.NewRecorder()
	s.handleBackupByID(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp backupResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != "bkp-1" {
		t.Errorf("id = %q, want bkp-1", resp.ID)
	}
}

func TestHandleBackupByID_DELETE_Success(t *testing.T) {
	s := newBackupServer(t, newStubBackupService())
	req := httptest.NewRequest(http.MethodDelete, "/v1/backups/bkp-1", nil)
	rec := httptest.NewRecorder()
	s.handleBackupByID(rec, req)
	assertStatus(t, rec, http.StatusNoContent)
}

func TestHandleBackupByID_POST_Verify_Success(t *testing.T) {
	s := newBackupServer(t, newStubBackupService())
	req := httptest.NewRequest(http.MethodPost, "/v1/backups/bkp-1/verify", nil)
	rec := httptest.NewRecorder()
	s.handleBackupByID(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp verifyResultResponse
	decodeJSON(t, rec, &resp)
	if !resp.Valid {
		t.Error("expected valid=true")
	}
}

func TestHandleBackupByID_POST_MethodNotAllowed(t *testing.T) {
	s := newBackupServer(t, newStubBackupService())
	// POST to /v1/backups/{id} (without /verify suffix) is not allowed.
	req := httptest.NewRequest(http.MethodPost, "/v1/backups/bkp-1", nil)
	rec := httptest.NewRecorder()
	s.handleBackupByID(rec, req)
	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestHandleBackupByID_PUT_MethodNotAllowed(t *testing.T) {
	s := newBackupServer(t, newStubBackupService())
	req := httptest.NewRequest(http.MethodPut, "/v1/backups/bkp-1", nil)
	rec := httptest.NewRecorder()
	s.handleBackupByID(rec, req)
	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

// ---------------------------------------------------------------------------
// handleGetBackup / handleDeleteBackup / handleVerifyBackup – error paths
// ---------------------------------------------------------------------------

func TestHandleGetBackup_ServiceError(t *testing.T) {
	svc := newStubBackupService()
	svc.getFn = func(_ context.Context, _ string) (*service.BackupInfo, error) {
		return nil, &service.BackupNotFoundError{ID: "bkp-1"}
	}
	s := newBackupServer(t, svc)
	req := httptest.NewRequest(http.MethodGet, "/v1/backups/bkp-1", nil)
	rec := httptest.NewRecorder()
	s.handleBackupByID(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleDeleteBackup_ServiceError(t *testing.T) {
	svc := newStubBackupService()
	svc.deleteFn = func(_ context.Context, _ string) error {
		return &service.BackupNotFoundError{ID: "bkp-1"}
	}
	s := newBackupServer(t, svc)
	req := httptest.NewRequest(http.MethodDelete, "/v1/backups/bkp-1", nil)
	rec := httptest.NewRecorder()
	s.handleBackupByID(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleVerifyBackup_ServiceError(t *testing.T) {
	svc := newStubBackupService()
	svc.verifyFn = func(_ context.Context, _ string) (*service.VerifyResult, error) {
		return nil, errors.New("checksum mismatch")
	}
	s := newBackupServer(t, svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/backups/bkp-1/verify", nil)
	rec := httptest.NewRecorder()
	s.handleBackupByID(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// backupToResponse – nil guard
// ---------------------------------------------------------------------------

func TestBackupToResponse_Nil(t *testing.T) {
	if r := backupToResponse(nil); r != nil {
		t.Errorf("expected nil, got %+v", r)
	}
}

func TestBackupToResponse_Fields(t *testing.T) {
	info := &service.BackupInfo{
		ID:          "bkp-x",
		Timestamp:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Size:        512,
		ShardCount:  1,
		Status:      "completed",
		Checksum:    "deadbeef",
		Destination: "/tmp",
		Compression: "zstd",
		Encryption:  "none",
		Duration:    250 * time.Millisecond,
	}
	r := backupToResponse(info)
	if r.ID != "bkp-x" {
		t.Errorf("id = %q", r.ID)
	}
	if r.DurationMs != 250 {
		t.Errorf("duration_ms = %d, want 250", r.DurationMs)
	}
}

// ---------------------------------------------------------------------------
// mapHTTPError
// ---------------------------------------------------------------------------

func TestMapBackupError_AllCases(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"not_found", &service.BackupNotFoundError{ID: "x"}, http.StatusNotFound},
		{"empty_dest", &service.BackupEmptyDestinationError{}, http.StatusBadRequest},
		{"empty_id", &service.BackupEmptyIDError{}, http.StatusBadRequest},
		{"enc_key_req", &service.BackupEncryptionKeyRequiredError{}, http.StatusBadRequest},
		{"dec_key_req", &service.BackupDecryptionKeyRequiredError{}, http.StatusBadRequest},
		{"inv_comp", &service.BackupInvalidCompressionError{}, http.StatusBadRequest},
		{"inv_enc", &service.BackupInvalidEncryptionError{}, http.StatusBadRequest},
		{"checksum", &service.BackupChecksumMismatchError{}, http.StatusConflict},
		{"unsup", &service.BackupUnsupportedError{}, http.StatusNotImplemented},
		{"online_restore_system_shard", &service.OnlineRestoreSystemShardError{}, http.StatusConflict},
		{"online_restore_replicated_shard", &service.OnlineRestoreReplicatedShardError{}, http.StatusConflict},
		{"online_restore_unsupported", &service.OnlineRestoreUnsupportedError{}, http.StatusNotImplemented},
		{"generic", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapHTTPError(tc.err)
			if got != tc.want {
				t.Errorf("mapHTTPError(%T) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// handleRebalanceShardStats
// ---------------------------------------------------------------------------

func TestHandleRebalanceShardStats_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodGet, "/v1/rebalance/shards/1", nil)
	rec := httptest.NewRecorder()
	s.handleRebalanceShardStats(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleRebalanceShardStats_WrongMethod(t *testing.T) {
	s := newRebalanceServer(t, newStubRebalanceService())
	req := httptest.NewRequest(http.MethodPost, "/v1/rebalance/shards/1", nil)
	rec := httptest.NewRecorder()
	s.handleRebalanceShardStats(rec, req)
	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestHandleRebalanceShardStats_InvalidID(t *testing.T) {
	s := newRebalanceServer(t, newStubRebalanceService())
	req := httptest.NewRequest(http.MethodGet, "/v1/rebalance/shards/notanint", nil)
	rec := httptest.NewRecorder()
	s.handleRebalanceShardStats(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleRebalanceShardStats_Success(t *testing.T) {
	s := newRebalanceServer(t, newStubRebalanceService())
	req := httptest.NewRequest(http.MethodGet, "/v1/rebalance/shards/3", nil)
	rec := httptest.NewRecorder()
	s.handleRebalanceShardStats(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp shardStatsResponse
	decodeJSON(t, rec, &resp)
	if resp.ShardID != 3 {
		t.Errorf("shard_id = %d, want 3", resp.ShardID)
	}
	if resp.KeyCount != 100 {
		t.Errorf("key_count = %d, want 100", resp.KeyCount)
	}
}

func TestHandleRebalanceShardStats_ServiceError(t *testing.T) {
	svc := newStubRebalanceService()
	svc.statsFn = func(_ context.Context, _ uint64) (*service.ShardStats, error) {
		return nil, errors.New("shard not found")
	}
	s := newRebalanceServer(t, svc)
	req := httptest.NewRequest(http.MethodGet, "/v1/rebalance/shards/1", nil)
	rec := httptest.NewRecorder()
	s.handleRebalanceShardStats(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// handleRebalancePlan
// ---------------------------------------------------------------------------

func TestHandleRebalancePlan_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/rebalance/plan", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleRebalancePlan(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleRebalancePlan_Success_WithBody(t *testing.T) {
	s := newRebalanceServer(t, newStubRebalanceService())
	body := `{"max_moves":5,"dry_run":true,"target_balance":80}`
	req := httptest.NewRequest(http.MethodPost, "/v1/rebalance/plan", strings.NewReader(body))
	req.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	s.handleRebalancePlan(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp rebalancePlanResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Moves) != 1 {
		t.Errorf("moves = %d, want 1", len(resp.Moves))
	}
}

func TestHandleRebalancePlan_Success_NoBody(t *testing.T) {
	s := newRebalanceServer(t, newStubRebalanceService())
	req := httptest.NewRequest(http.MethodPost, "/v1/rebalance/plan", nil)
	req.ContentLength = 0
	rec := httptest.NewRecorder()
	s.handleRebalancePlan(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

func TestHandleRebalancePlan_BadBody(t *testing.T) {
	s := newRebalanceServer(t, newStubRebalanceService())
	req := httptest.NewRequest(http.MethodPost, "/v1/rebalance/plan", strings.NewReader(`bad`))
	req.ContentLength = 3
	rec := httptest.NewRecorder()
	s.handleRebalancePlan(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleRebalancePlan_ServiceError(t *testing.T) {
	svc := newStubRebalanceService()
	svc.planFn = func(_ context.Context, _ *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
		return nil, errors.New("cluster unreachable")
	}
	s := newRebalanceServer(t, svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/rebalance/plan", nil)
	rec := httptest.NewRecorder()
	s.handleRebalancePlan(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// handleRebalanceExecute
// ---------------------------------------------------------------------------

func TestHandleRebalanceExecute_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/rebalance/execute", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleRebalanceExecute(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleRebalanceExecute_BadBody(t *testing.T) {
	s := newRebalanceServer(t, newStubRebalanceService())
	req := httptest.NewRequest(http.MethodPost, "/v1/rebalance/execute", strings.NewReader(`bad`))
	rec := httptest.NewRecorder()
	s.handleRebalanceExecute(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleRebalanceExecute_Success(t *testing.T) {
	s := newRebalanceServer(t, newStubRebalanceService())
	body, _ := json.Marshal(rebalancePlanResponse{
		Moves: []rebalanceMoveResponse{
			{ShardID: 1, FromNode: "n1", ToNode: "n2", EstimatedBytes: 256},
		},
		EstimatedDurationMs: 2000,
		CurrentImbalance:    0.3,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/rebalance/execute", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.handleRebalanceExecute(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp rebalanceExecutionResponse
	decodeJSON(t, rec, &resp)
	if resp.MovesCompleted != 1 {
		t.Errorf("moves_completed = %d, want 1", resp.MovesCompleted)
	}
}

func TestHandleRebalanceExecute_ServiceError(t *testing.T) {
	svc := newStubRebalanceService()
	svc.executeFn = func(_ context.Context, _ *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
		return nil, errors.New("execution failed")
	}
	s := newRebalanceServer(t, svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/rebalance/execute", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleRebalanceExecute(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// TestHandleRebalanceExecute_ForwardToLeader_Success verifies that when
// ExecuteRebalance returns *service.NotLeaderError with a known leader
// address (RebalanceService.ExecuteRebalance returns this, unwrapped, when
// moveTenantGroupShard determines the local node cannot execute a
// dedicated-tenant shard MOVE -- see resolveMoveLeader), handleRebalanceExecute
// forwards the original POST request (method, path, and body intact) to the
// leader's REST server, mirroring the KV PUT/DELETE and database-scale
// leader-forwarding paths (see TestHandleScaleDatabase_ForwardToLeader_Success).
func TestHandleRebalanceExecute_ForwardToLeader_Success(t *testing.T) {
	leaderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("leader received method %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/rebalance/execute" {
			t.Fatalf("leader received path %s, want /v1/rebalance/execute", r.URL.Path)
		}
		fwd := r.Header.Get(headerForwardedBy)
		if fwd == "" {
			t.Fatal("leader did not receive X-Quicraft-Forwarded-By header")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("leader read body: %v", err)
		}
		var req rebalancePlanResponse
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("leader decode body: %v", err)
		}
		if len(req.Moves) != 1 || req.Moves[0].ShardID != 1000 {
			t.Fatalf("leader received moves=%+v, want a single move for shard 1000", req.Moves)
		}
		writeJSON(w, rebalanceExecutionResponse{MovesCompleted: 1})
	}))
	defer leaderSrv.Close()

	svc := newStubRebalanceService()
	svc.executeFn = func(_ context.Context, _ *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
		return nil, &service.NotLeaderError{
			ShardID:       1000,
			LeaderID:      2,
			LeaderAddress: "10.0.0.2:63001",
		}
	}

	reg := NewPeerAdminRegistry("63002")
	reg.Register("10.0.0.2:63001", leaderSrv.URL)

	host := &mockHost{addr: "10.0.0.1:63001"}
	srv := NewServer(&mockKVService{}, newDefaultAdminSvc(), "127.0.0.1:0",
		WithRebalanceService(svc), WithLeaderForwarding(reg), WithHost(host))

	body, _ := json.Marshal(rebalancePlanResponse{
		Moves: []rebalanceMoveResponse{
			{ShardID: 1000, FromNode: "node-a:9100", ToNode: "node-c:9100"},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/rebalance/execute", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp rebalanceExecutionResponse
	decodeJSON(t, rec, &resp)
	if resp.MovesCompleted != 1 {
		t.Errorf("resp.MovesCompleted = %d, want 1", resp.MovesCompleted)
	}
}

// TestHandleRebalanceExecute_NotLeader_NoResolver verifies that when
// ExecuteRebalance returns *service.NotLeaderError but no leader-forwarding
// resolver is configured, handleRebalanceExecute falls back to
// mapHTTPError's mapped status rather than forwarding.
func TestHandleRebalanceExecute_NotLeader_NoResolver(t *testing.T) {
	svc := newStubRebalanceService()
	svc.executeFn = func(_ context.Context, _ *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
		return nil, &service.NotLeaderError{ShardID: 1000, LeaderID: 2, LeaderAddress: "10.0.0.2:63001"}
	}
	s := newRebalanceServer(t, svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/rebalance/execute", strings.NewReader(`{"moves":[]}`))
	rec := httptest.NewRecorder()
	s.handleRebalanceExecute(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 status without a resolver, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleImbalanceReport
// ---------------------------------------------------------------------------

func TestHandleImbalanceReport_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodGet, "/v1/rebalance/report", nil)
	rec := httptest.NewRecorder()
	s.handleImbalanceReport(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleImbalanceReport_Success(t *testing.T) {
	s := newRebalanceServer(t, newStubRebalanceService())
	req := httptest.NewRequest(http.MethodGet, "/v1/rebalance/report", nil)
	rec := httptest.NewRecorder()
	s.handleImbalanceReport(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp imbalanceReportResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Nodes) != 1 {
		t.Errorf("nodes = %d, want 1", len(resp.Nodes))
	}
	if resp.ImbalanceScore != 0.1 {
		t.Errorf("imbalance_score = %f, want 0.1", resp.ImbalanceScore)
	}
}

// TestHandleImbalanceReport_HotspotsAlwaysArray is a regression test for a
// real GUI crash: a balanced cluster (the common case) has zero hotspots, and
// the "hotspots" field was tagged omitempty, so it was omitted from the JSON
// entirely — the RebalancePanel then read report.hotspots.length on undefined
// and crashed the whole page. The wire value MUST always be a JSON array
// ("hotspots":[]), never omitted and never null, for both a nil and an empty
// service result.
func TestHandleImbalanceReport_HotspotsAlwaysArray(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hotspots []string
	}{
		{"nil hotspots", nil},
		{"empty hotspots", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newStubRebalanceService()
			svc.reportFn = func(_ context.Context) (*service.ImbalanceReportResult, error) {
				return &service.ImbalanceReportResult{
					Nodes:          []service.NodeLoadInfo{},
					ImbalanceScore: 0,
					Hotspots:       tc.hotspots,
				}, nil
			}
			s := newRebalanceServer(t, svc)
			req := httptest.NewRequest(http.MethodGet, "/v1/rebalance/report", nil)
			rec := httptest.NewRecorder()
			s.handleImbalanceReport(rec, req)
			assertStatus(t, rec, http.StatusOK)

			body := rec.Body.String()
			if !strings.Contains(body, `"hotspots":[]`) {
				t.Errorf("body must contain \"hotspots\":[] (present, empty array); got %s", body)
			}
			if strings.Contains(body, `"hotspots":null`) {
				t.Errorf("hotspots must never serialize as null; got %s", body)
			}
		})
	}
}

func TestHandleImbalanceReport_ServiceError(t *testing.T) {
	svc := newStubRebalanceService()
	svc.reportFn = func(_ context.Context) (*service.ImbalanceReportResult, error) {
		return nil, errors.New("metrics unavailable")
	}
	s := newRebalanceServer(t, svc)
	req := httptest.NewRequest(http.MethodGet, "/v1/rebalance/report", nil)
	rec := httptest.NewRecorder()
	s.handleImbalanceReport(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// rebalancePlanToResponse / responseToRebalancePlan – nil guards
// ---------------------------------------------------------------------------

func TestRebalancePlanToResponse_Nil(t *testing.T) {
	r := rebalancePlanToResponse(nil)
	if len(r.Moves) != 0 {
		t.Errorf("expected empty moves, got %d", len(r.Moves))
	}
}

func TestResponseToRebalancePlan_Nil(t *testing.T) {
	r := responseToRebalancePlan(nil)
	if r != nil {
		t.Errorf("expected nil, got %+v", r)
	}
}

func TestRebalancePlanRoundTrip(t *testing.T) {
	plan := &service.RebalancePlanResult{
		Moves: []service.RebalanceMovePlan{
			{ShardID: 7, FromNode: "a", ToNode: "b", EstimatedBytes: 1024},
		},
		EstimatedDuration: 5 * time.Second,
		CurrentImbalance:  0.5,
	}
	resp := rebalancePlanToResponse(plan)
	roundTripped := responseToRebalancePlan(&resp)

	if len(roundTripped.Moves) != 1 || roundTripped.Moves[0].ShardID != 7 {
		t.Errorf("round-trip moves mismatch: %+v", roundTripped.Moves)
	}
	if roundTripped.EstimatedDuration != 5*time.Second {
		t.Errorf("duration mismatch: %v", roundTripped.EstimatedDuration)
	}
}

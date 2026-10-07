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

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Backup: CreateBackup / ListBackups / GetBackup / DeleteBackup /
//         RestoreBackup / VerifyBackup / deleteNoContent / toBackupInfo
// ===========================================================================

func TestRESTCreateBackup_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	ts := time.Now().UTC().Truncate(time.Second)
	rs.handle(exactPath(http.MethodPost, "/v1/backups"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"id":        "bk-001",
			"timestamp": ts.Format(time.RFC3339),
			"size":      int64(1024),
			"status":    "completed",
		})
	})
	info, err := rt.CreateBackup(context.Background(), &transport.BackupRequest{})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if info.ID != "bk-001" {
		t.Errorf("expected ID bk-001, got %q", info.ID)
	}
	if info.Size != 1024 {
		t.Errorf("expected size 1024, got %d", info.Size)
	}
}

func TestRESTCreateBackup_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.CreateBackup(context.Background(), &transport.BackupRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCreateBackup_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/backups"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.CreateBackup(context.Background(), &transport.BackupRequest{})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

func TestRESTListBackups_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/backups/list"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"backups": []map[string]any{
				{"id": "bk-001", "status": "completed", "size": int64(512)},
				{"id": "bk-002", "status": "completed", "size": int64(256)},
			},
			"total": 2,
		})
	})
	backups, err := rt.ListBackups(context.Background())
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(backups) != 2 {
		t.Fatalf("expected 2 backups, got %d", len(backups))
	}
}

func TestRESTListBackups_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.ListBackups(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTListBackups_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/backups/list"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	_, err := rt.ListBackups(context.Background())
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

func TestRESTGetBackup_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/backups/"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"id": "bk-007", "status": "completed"})
	})
	info, err := rt.GetBackup(context.Background(), "bk-007")
	if err != nil {
		t.Fatalf("GetBackup: %v", err)
	}
	if info.ID != "bk-007" {
		t.Errorf("expected ID bk-007, got %q", info.ID)
	}
}

func TestRESTGetBackup_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.GetBackup(context.Background(), "bk-001")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTGetBackup_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/backups/"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	_, err := rt.GetBackup(context.Background(), "bk-999")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTDeleteBackup_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodDelete, "/v1/backups/"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := rt.DeleteBackup(context.Background(), "bk-001"); err != nil {
		t.Fatalf("DeleteBackup: %v", err)
	}
}

func TestRESTDeleteBackup_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.DeleteBackup(context.Background(), "bk-001")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTDeleteBackup_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodDelete, "/v1/backups/"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	err := rt.DeleteBackup(context.Background(), "bk-001")
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

func TestRESTRestoreBackup_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/backups/restore"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"backup_id":       "bk-001",
			"shards_restored": 3,
			"duration_ms":     int64(500),
		})
	})
	result, err := rt.RestoreBackup(context.Background(), &transport.RestoreRequest{BackupID: "bk-001"})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if result.BackupID != "bk-001" {
		t.Errorf("expected BackupID bk-001, got %q", result.BackupID)
	}
	if result.ShardsRestored != 3 {
		t.Errorf("expected 3 shards restored, got %d", result.ShardsRestored)
	}
}

func TestRESTRestoreBackup_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.RestoreBackup(context.Background(), &transport.RestoreRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTRestoreBackup_WithErrors(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/backups/restore"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"backup_id":       "bk-001",
			"shards_restored": 2,
			"errors": []map[string]any{
				{"shard_id": uint64(3), "message": "shard 3 offline"},
			},
		})
	})
	result, err := rt.RestoreBackup(context.Background(), &transport.RestoreRequest{BackupID: "bk-001"})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 shard error, got %d", len(result.Errors))
	}
}

func TestRESTVerifyBackup_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodPost, "/v1/backups/"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, transport.VerifyResult{Valid: true})
	})
	result, err := rt.VerifyBackup(context.Background(), "bk-001")
	if err != nil {
		t.Fatalf("VerifyBackup: %v", err)
	}
	if !result.Valid {
		t.Error("expected valid=true")
	}
}

func TestRESTVerifyBackup_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.VerifyBackup(context.Background(), "bk-001")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTVerifyBackup_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodPost, "/v1/backups/"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "failed", http.StatusInternalServerError)
	})
	_, err := rt.VerifyBackup(context.Background(), "bk-001")
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

func TestRESTBackupInfo_TimestampParsing(t *testing.T) {
	ts := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	wire := &restBackupInfo{
		ID:          "bk-ts",
		Timestamp:   ts.Format(time.RFC3339),
		DurationMs:  250,
		Compression: "gzip",
		Encryption:  "aes256",
	}
	info := wire.toBackupInfo()
	if !info.Timestamp.Equal(ts) {
		t.Errorf("expected timestamp %v, got %v", ts, info.Timestamp)
	}
	if info.Duration != 250*time.Millisecond {
		t.Errorf("expected duration 250ms, got %v", info.Duration)
	}
}

func TestRESTBackupInfo_InvalidTimestamp(t *testing.T) {
	wire := &restBackupInfo{ID: "bk-bad", Timestamp: "not-a-time"}
	info := wire.toBackupInfo()
	if info.ID != "bk-bad" {
		t.Errorf("expected ID bk-bad, got %q", info.ID)
	}
	// Invalid timestamp should leave the field zero, not fail.
	if !info.Timestamp.IsZero() {
		t.Errorf("expected zero timestamp for invalid input, got %v", info.Timestamp)
	}
}

func TestRESTBackupInfo_KeyProviderIDSurvivesWire(t *testing.T) {
	var wire restBackupInfo
	if err := json.Unmarshal([]byte(`{"id":"bk-kp","encryption":"aes256","key_provider_id":"software"}`), &wire); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := wire.toBackupInfo().KeyProviderID; got != "software" {
		t.Errorf("KeyProviderID = %q, want software", got)
	}
	if got := (&restBackupInfo{ID: "bk-none"}).toBackupInfo().KeyProviderID; got != "" {
		t.Errorf("KeyProviderID of a plaintext backup = %q, want empty", got)
	}
}

// ===========================================================================
// Rebalance: GetShardStats / CreateRebalancePlan / ExecuteRebalance /
//            GetImbalanceReport / toRebalancePlan / planToWire
// ===========================================================================

func TestRESTGetShardStats_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/rebalance/shards/"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, restShardStats{
			ShardID:      5,
			KeyCount:     1000,
			ByteCount:    204800,
			LeaderID:     1,
			ReplicaCount: 3,
		})
	})
	stats, err := rt.GetShardStats(context.Background(), 5)
	if err != nil {
		t.Fatalf("GetShardStats: %v", err)
	}
	if stats.ShardID != 5 {
		t.Errorf("expected ShardID 5, got %d", stats.ShardID)
	}
	if stats.KeyCount != 1000 {
		t.Errorf("expected KeyCount 1000, got %d", stats.KeyCount)
	}
}

func TestRESTGetShardStats_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.GetShardStats(context.Background(), 1)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTGetShardStats_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/rebalance/shards/"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	_, err := rt.GetShardStats(context.Background(), 99)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTCreateRebalancePlan_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/rebalance/plan"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, restRebalancePlan{
			Moves: []restRebalanceMove{
				{ShardID: 1, FromNode: "nodeA", ToNode: "nodeB", EstimatedBytes: 1024},
			},
			EstimatedDurationMs: 3000,
			CurrentImbalance:    0.25,
		})
	})
	plan, err := rt.CreateRebalancePlan(context.Background(), &transport.RebalancePlanRequest{MaxMoves: 5})
	if err != nil {
		t.Fatalf("CreateRebalancePlan: %v", err)
	}
	if len(plan.Moves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(plan.Moves))
	}
	if plan.Moves[0].FromNode != "nodeA" {
		t.Errorf("expected FromNode nodeA, got %q", plan.Moves[0].FromNode)
	}
	if plan.EstimatedDuration != 3000*time.Millisecond {
		t.Errorf("expected 3s duration, got %v", plan.EstimatedDuration)
	}
}

func TestRESTCreateRebalancePlan_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.CreateRebalancePlan(context.Background(), &transport.RebalancePlanRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCreateRebalancePlan_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/rebalance/plan"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.CreateRebalancePlan(context.Background(), &transport.RebalancePlanRequest{})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

func TestRESTExecuteRebalance_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/rebalance/execute"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, restRebalanceExecution{
			MovesCompleted: 3,
			MovesFailed:    0,
			DurationMs:     2500,
			FinalImbalance: 0.05,
		})
	})
	plan := &transport.RebalancePlan{
		Moves: []transport.RebalanceMove{
			{ShardID: 1, FromNode: "a", ToNode: "b", EstimatedBytes: 512},
		},
	}
	result, err := rt.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if result.MovesCompleted != 3 {
		t.Errorf("expected 3 moves completed, got %d", result.MovesCompleted)
	}
}

func TestRESTExecuteRebalance_NilPlan(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/rebalance/execute"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, restRebalanceExecution{MovesCompleted: 0})
	})
	// planToWire handles nil gracefully by returning an empty plan.
	result, err := rt.ExecuteRebalance(context.Background(), nil)
	if err != nil {
		t.Fatalf("ExecuteRebalance with nil plan: %v", err)
	}
	if result.MovesCompleted != 0 {
		t.Errorf("expected 0 moves, got %d", result.MovesCompleted)
	}
}

func TestRESTExecuteRebalance_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.ExecuteRebalance(context.Background(), &transport.RebalancePlan{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTExecuteRebalance_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/rebalance/execute"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "failed", http.StatusInternalServerError)
	})
	_, err := rt.ExecuteRebalance(context.Background(), &transport.RebalancePlan{})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

func TestRESTGetImbalanceReport_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/rebalance/report"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, restImbalanceReport{
			Nodes: []restNodeLoad{
				{NodeID: "n1", Address: "10.0.0.1:9000", ShardCount: 3, TotalKeys: 500, TotalBytes: 1024, LeaderCount: 2},
			},
			ImbalanceScore: 0.15,
			Hotspots:       []string{"n1"},
		})
	})
	report, err := rt.GetImbalanceReport(context.Background())
	if err != nil {
		t.Fatalf("GetImbalanceReport: %v", err)
	}
	if len(report.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(report.Nodes))
	}
	if report.ImbalanceScore != 0.15 {
		t.Errorf("expected imbalance 0.15, got %f", report.ImbalanceScore)
	}
}

func TestRESTGetImbalanceReport_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.GetImbalanceReport(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTGetImbalanceReport_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/rebalance/report"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	})
	_, err := rt.GetImbalanceReport(context.Background())
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

// ===========================================================================
// Database barrier: SealDatabase / UnsealDatabase / DatabaseBarrierStatus
// ===========================================================================

func TestRESTSealDatabase_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodPost, "/v1/databases/"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := rt.SealDatabase(context.Background(), "mydb"); err != nil {
		t.Fatalf("SealDatabase: %v", err)
	}
}

func TestRESTSealDatabase_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.SealDatabase(context.Background(), "mydb")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTSealDatabase_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodPost, "/v1/databases/"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "sealed already", http.StatusConflict)
	})
	err := rt.SealDatabase(context.Background(), "mydb")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTUnsealDatabase_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodPost, "/v1/databases/"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := rt.UnsealDatabase(context.Background(), "mydb", "mysecret"); err != nil {
		t.Fatalf("UnsealDatabase: %v", err)
	}
}

func TestRESTUnsealDatabase_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.UnsealDatabase(context.Background(), "mydb", "secret")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTUnsealDatabase_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodPost, "/v1/databases/"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad secret", http.StatusUnauthorized)
	})
	err := rt.UnsealDatabase(context.Background(), "mydb", "wrong")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTDatabaseBarrierStatus_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	ts := time.Now().UTC().Truncate(time.Second)
	rs.handle(pathPrefix(http.MethodGet, "/v1/databases/"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, restDatabaseBarrierStatus{
			Sealed:         false,
			Strategy:       "software",
			HardwareBacked: false,
			InitializedAt:  ts.Format(time.RFC3339),
		})
	})
	status, err := rt.DatabaseBarrierStatus(context.Background(), "mydb")
	if err != nil {
		t.Fatalf("DatabaseBarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Error("expected unsealed")
	}
	if status.Strategy != "software" {
		t.Errorf("expected strategy software, got %q", status.Strategy)
	}
	if !status.InitializedAt.Equal(ts) {
		t.Errorf("expected InitializedAt %v, got %v", ts, status.InitializedAt)
	}
}

func TestRESTDatabaseBarrierStatus_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.DatabaseBarrierStatus(context.Background(), "mydb")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTDatabaseBarrierStatus_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/databases/"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	_, err := rt.DatabaseBarrierStatus(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTDatabaseBarrierStatus_InvalidTimestamp(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/databases/"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, restDatabaseBarrierStatus{
			Sealed:        false,
			Strategy:      "software",
			InitializedAt: "not-a-time",
		})
	})
	status, err := rt.DatabaseBarrierStatus(context.Background(), "mydb")
	if err != nil {
		t.Fatalf("DatabaseBarrierStatus: %v", err)
	}
	if !status.InitializedAt.IsZero() {
		t.Errorf("expected zero time for invalid timestamp, got %v", status.InitializedAt)
	}
}

// ===========================================================================
// Embedding: Embed / BatchEmbed / ListEmbeddingModels
// ===========================================================================

func TestRESTEmbed_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/embeddings"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, transport.EmbedResponse{
			Embeddings: [][]float32{{0.1, 0.2, 0.3}},
			Model:      "text-embedding-3-small",
		})
	})
	resp, err := rt.Embed(context.Background(), &transport.EmbedRequest{Texts: []string{"hello"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Embeddings) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(resp.Embeddings))
	}
}

func TestRESTEmbed_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.Embed(context.Background(), &transport.EmbedRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTEmbed_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/embeddings"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model not loaded", http.StatusServiceUnavailable)
	})
	_, err := rt.Embed(context.Background(), &transport.EmbedRequest{Texts: []string{"hi"}})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

func TestRESTBatchEmbed_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/embeddings/batch"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, transport.BatchEmbedResponse{
			Embeddings: [][]float32{{0.1, 0.2}, {0.3, 0.4}},
		})
	})
	resp, err := rt.BatchEmbed(context.Background(), &transport.BatchEmbedRequest{
		Texts: []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("BatchEmbed: %v", err)
	}
	if len(resp.Embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(resp.Embeddings))
	}
}

func TestRESTBatchEmbed_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.BatchEmbed(context.Background(), &transport.BatchEmbedRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTBatchEmbed_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/embeddings/batch"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
	})
	_, err := rt.BatchEmbed(context.Background(), &transport.BatchEmbedRequest{Texts: []string{"x"}})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

func TestRESTListEmbeddingModels_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/embedding/models"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, transport.ListEmbeddingModelsResponse{
			Models: []transport.EmbeddingModelInfo{{Name: "model-a", Dimensions: 768}},
		})
	})
	resp, err := rt.ListEmbeddingModels(context.Background(), "openai")
	if err != nil {
		t.Fatalf("ListEmbeddingModels: %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(resp.Models))
	}
}

func TestRESTListEmbeddingModels_NoProvider(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/embedding/models"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, transport.ListEmbeddingModelsResponse{})
	})
	_, err := rt.ListEmbeddingModels(context.Background(), "")
	if err != nil {
		t.Fatalf("ListEmbeddingModels (no provider): %v", err)
	}
}

func TestRESTListEmbeddingModels_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.ListEmbeddingModels(context.Background(), "")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTListEmbeddingModels_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/embedding/models"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	_, err := rt.ListEmbeddingModels(context.Background(), "openai")
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

// ===========================================================================
// Auth: GetUserByCertThumbprint / AddCertBinding
// ===========================================================================

func TestRESTGetUserByCertThumbprint_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/users/by-cert-thumbprint/"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.User{ID: 1, Username: "alice"})
	})
	user, err := rt.GetUserByCertThumbprint(context.Background(), "AABBCC")
	if err != nil {
		t.Fatalf("GetUserByCertThumbprint: %v", err)
	}
	if user.ID != 1 {
		t.Errorf("expected ID 1, got %d", user.ID)
	}
}

func TestRESTGetUserByCertThumbprint_NotFound(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/users/by-cert-thumbprint/"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := rt.GetUserByCertThumbprint(context.Background(), "NOTEXIST")
	var nfe *transport.UserNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected UserNotFoundError, got %T", err)
	}
}

func TestRESTGetUserByCertThumbprint_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.GetUserByCertThumbprint(context.Background(), "X")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTGetUserByCertThumbprint_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/users/by-cert-thumbprint/"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.GetUserByCertThumbprint(context.Background(), "X")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTGetUserByCertThumbprint_InvalidJSON(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/users/by-cert-thumbprint/"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{bad json"))
	})
	_, err := rt.GetUserByCertThumbprint(context.Background(), "X")
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError from JSON decode error, got %T", err)
	}
}

func TestRESTAddCertBinding_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodPost, "/users/"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.User{ID: 1, Username: "alice"})
	})
	user, err := rt.AddCertBinding(context.Background(), 1, "AABBCC", 0)
	if err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	if user.ID != 1 {
		t.Errorf("expected ID 1, got %d", user.ID)
	}
}

func TestRESTAddCertBinding_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.AddCertBinding(context.Background(), 1, "X", 0)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTAddCertBinding_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodPost, "/users/"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	_, err := rt.AddCertBinding(context.Background(), 1, "CERT", 0)
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

// ===========================================================================
// Auth: RemoveCertBinding — the withdrawal verb (go-qrdb#518)
// ===========================================================================

func TestRESTRemoveCertBinding_Success(t *testing.T) {
	const thumbprint = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var gotPath string
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodDelete, "/users/"), func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		writeJSON(w, &transport.User{ID: 1, Username: "alice"})
	})
	user, err := rt.RemoveCertBinding(context.Background(), 1, thumbprint)
	if err != nil {
		t.Fatalf("RemoveCertBinding: %v", err)
	}
	if user.ID != 1 {
		t.Errorf("expected ID 1, got %d", user.ID)
	}
	// The thumbprint travels in the PATH. A segment that lost characters in
	// escaping would withdraw nothing and report a not-found.
	if want := "/users/1/cert-bindings/" + thumbprint; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
}

func TestRESTRemoveCertBinding_AfterClose(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.RemoveCertBinding(context.Background(), 1, "X")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// TestRESTRemoveCertBinding_NotFoundIsTyped keeps a 404 classifiable without
// reading the message, the same as every other user-addressed call.
func TestRESTRemoveCertBinding_NotFoundIsTyped(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodDelete, "/users/"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := rt.RemoveCertBinding(context.Background(), 42, "CERT")
	var unf *transport.UserNotFoundError
	if !errors.As(err, &unf) {
		t.Fatalf("expected UserNotFoundError, got %T: %v", err, err)
	}
	if unf.ID != 42 {
		t.Fatalf("UserNotFoundError.ID = %d, want 42", unf.ID)
	}
}

func TestRESTRemoveCertBinding_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodDelete, "/users/"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	_, err := rt.RemoveCertBinding(context.Background(), 1, "CERT")
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
}

// ===========================================================================
// getJSONDecode error path (decode error)
// ===========================================================================

func TestRESTGetJSONDecode_DecodeError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/backups/list"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{not-valid-json"))
	})
	_, err := rt.ListBackups(context.Background())
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError from decode error, got %T", err)
	}
}

// ===========================================================================
// deleteNoContent (via DeleteBackup) success + error verified above.
// Additional coverage: 4xx response triggers decodeErrorResponse.
// ===========================================================================

func TestRESTDeleteNoContent_4xxError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodDelete, "/v1/backups/"), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(transport.ErrorResponse{Error: "denied"})
	})
	err := rt.DeleteBackup(context.Background(), "bk-001")
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
	if reqErr.StatusCode != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", reqErr.StatusCode)
	}
}

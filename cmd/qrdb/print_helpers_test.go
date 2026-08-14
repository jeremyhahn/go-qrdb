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
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// saveOutput redirects outputWriter and outputFormat for the duration of fn,
// then restores them. Returns the captured output.
func saveOutput(format string, fn func()) string {
	buf := &bytes.Buffer{}
	oldWriter := outputWriter
	oldFormat := outputFormat
	defer func() {
		outputWriter = oldWriter
		outputFormat = oldFormat
	}()
	outputWriter = buf
	outputFormat = format
	fn()
	return buf.String()
}

// ---------------------------------------------------------------------------
// printBackupInfo
// ---------------------------------------------------------------------------

func TestPrintBackupInfo_Table_Basic(t *testing.T) {
	info := &transport.BackupInfo{
		ID:          "bk-1",
		Timestamp:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Status:      transport.BackupStatusComplete,
		Destination: "/backups",
		Size:        1024,
	}

	out := saveOutput("table", func() {
		if err := printBackupInfo(info); err != nil {
			t.Errorf("printBackupInfo: %v", err)
		}
	})

	if !strings.Contains(out, "bk-1") {
		t.Errorf("expected backup ID in output, got: %q", out)
	}
}

func TestPrintBackupInfo_Table_WithErrorAndMetadata(t *testing.T) {
	info := &transport.BackupInfo{
		ID:           "bk-2",
		Timestamp:    time.Now(),
		ErrorMessage: "write failed",
		Metadata:     map[string]string{"env": "prod"},
	}

	out := saveOutput("table", func() {
		if err := printBackupInfo(info); err != nil {
			t.Errorf("printBackupInfo: %v", err)
		}
	})

	if !strings.Contains(out, "write failed") {
		t.Errorf("expected error message in output, got: %q", out)
	}
	if !strings.Contains(out, "env") {
		t.Errorf("expected metadata key in output, got: %q", out)
	}
}

func TestPrintBackupInfo_JSON(t *testing.T) {
	info := &transport.BackupInfo{ID: "bk-3", Status: transport.BackupStatusComplete}

	out := saveOutput("json", func() {
		if err := printBackupInfo(info); err != nil {
			t.Errorf("printBackupInfo(json): %v", err)
		}
	})

	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode json output: %v", err)
	}
}

// ---------------------------------------------------------------------------
// printRestoreResult
// ---------------------------------------------------------------------------

func TestPrintRestoreResult_Table_NoErrors(t *testing.T) {
	result := &transport.RestoreResult{
		BackupID:       "bk-1",
		ShardsRestored: 3,
		Duration:       2 * time.Second,
	}

	out := saveOutput("table", func() {
		if err := printRestoreResult(result); err != nil {
			t.Errorf("printRestoreResult: %v", err)
		}
	})

	if !strings.Contains(out, "bk-1") {
		t.Errorf("expected backup ID in output, got: %q", out)
	}
}

func TestPrintRestoreResult_Table_WithErrors(t *testing.T) {
	result := &transport.RestoreResult{
		BackupID:       "bk-2",
		ShardsRestored: 2,
		Errors: []transport.ShardError{
			{ShardID: 1, Message: "checksum mismatch"},
		},
	}

	out := saveOutput("table", func() {
		if err := printRestoreResult(result); err != nil {
			t.Errorf("printRestoreResult: %v", err)
		}
	})

	if !strings.Contains(out, "checksum mismatch") {
		t.Errorf("expected shard error in output, got: %q", out)
	}
}

func TestPrintRestoreResult_JSON(t *testing.T) {
	result := &transport.RestoreResult{BackupID: "bk-3"}
	out := saveOutput("json", func() {
		if err := printRestoreResult(result); err != nil {
			t.Errorf("printRestoreResult(json): %v", err)
		}
	})
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode json output: %v", err)
	}
}

// ---------------------------------------------------------------------------
// printVerifyResult
// ---------------------------------------------------------------------------

func TestPrintVerifyResult_Table_Valid(t *testing.T) {
	result := &transport.VerifyResult{
		Valid:            true,
		ExpectedChecksum: "abc123",
		ActualChecksum:   "abc123",
	}

	out := saveOutput("table", func() {
		if err := printVerifyResult(result); err != nil {
			t.Errorf("printVerifyResult: %v", err)
		}
	})

	if !strings.Contains(out, "abc123") {
		t.Errorf("expected checksum in output, got: %q", out)
	}
}

func TestPrintVerifyResult_Table_WithErrors(t *testing.T) {
	result := &transport.VerifyResult{
		Valid:  false,
		Errors: []string{"shard 2: corrupt data"},
	}

	out := saveOutput("table", func() {
		if err := printVerifyResult(result); err != nil {
			t.Errorf("printVerifyResult: %v", err)
		}
	})

	if !strings.Contains(out, "shard 2") {
		t.Errorf("expected shard error in output, got: %q", out)
	}
}

func TestPrintVerifyResult_JSON(t *testing.T) {
	result := &transport.VerifyResult{Valid: true}
	out := saveOutput("json", func() {
		if err := printVerifyResult(result); err != nil {
			t.Errorf("printVerifyResult(json): %v", err)
		}
	})
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode json output: %v", err)
	}
}

// ---------------------------------------------------------------------------
// printRebalancePlan — yaml branch and with moves
// ---------------------------------------------------------------------------

func TestPrintRebalancePlan_YAML(t *testing.T) {
	plan := &transport.RebalancePlan{
		CurrentImbalance: 0.25,
	}
	out := saveOutput("yaml", func() {
		if err := printRebalancePlan(plan); err != nil {
			t.Errorf("printRebalancePlan(yaml): %v", err)
		}
	})
	if len(out) == 0 {
		t.Error("expected non-empty yaml output")
	}
}

func TestPrintRebalancePlan_Table_WithMoves(t *testing.T) {
	plan := &transport.RebalancePlan{
		Moves: []transport.RebalanceMove{
			{ShardID: 1, FromNode: "n1", ToNode: "n2", EstimatedBytes: 1024},
		},
	}
	out := saveOutput("table", func() {
		if err := printRebalancePlan(plan); err != nil {
			t.Errorf("printRebalancePlan(table): %v", err)
		}
	})
	if !strings.Contains(out, "n1") {
		t.Errorf("expected from-node in output, got: %q", out)
	}
}

func TestPrintRebalanceResult_Table_NoMoves(t *testing.T) {
	result := &transport.RebalanceResult{
		MovesCompleted: 5,
		MovesFailed:    0,
	}
	out := saveOutput("table", func() {
		if err := printRebalanceResult(result); err != nil {
			t.Errorf("printRebalanceResult: %v", err)
		}
	})
	if !strings.Contains(out, "5") {
		t.Errorf("expected move count in output, got: %q", out)
	}
}

func TestPrintRebalanceResult_JSON(t *testing.T) {
	result := &transport.RebalanceResult{MovesCompleted: 3}
	out := saveOutput("json", func() {
		if err := printRebalanceResult(result); err != nil {
			t.Errorf("printRebalanceResult(json): %v", err)
		}
	})
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode json: %v", err)
	}
}

func TestPrintRebalanceResult_YAML(t *testing.T) {
	result := &transport.RebalanceResult{MovesFailed: 1}
	out := saveOutput("yaml", func() {
		if err := printRebalanceResult(result); err != nil {
			t.Errorf("printRebalanceResult(yaml): %v", err)
		}
	})
	if len(out) == 0 {
		t.Error("expected non-empty yaml output")
	}
}

// ---------------------------------------------------------------------------
// printShardStats — yaml branch
// ---------------------------------------------------------------------------

func TestPrintShardStats_YAML(t *testing.T) {
	resp := &transport.ShardStatsResponse{
		ShardID:      1,
		KeyCount:     100,
		ByteCount:    2048,
		LeaderID:     1,
		ReplicaCount: 3,
	}
	out := saveOutput("yaml", func() {
		if err := printShardStats(resp); err != nil {
			t.Errorf("printShardStats(yaml): %v", err)
		}
	})
	if len(out) == 0 {
		t.Error("expected non-empty yaml output")
	}
}

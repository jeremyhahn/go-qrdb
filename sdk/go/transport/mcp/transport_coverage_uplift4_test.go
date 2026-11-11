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

// Fourth coverage-uplift file for the MCP SDK transport.
// Targets remaining sub-91% paths by exercising optional parameter branches
// and decode error paths.

package mcp

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// CreateBackup — optional compression/encryption/key_provider_id/key_ref/
// shard_ids (75.0%)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// RestoreBackup — optional target_shards / validate_first /
// decryption_key_ref (76.5%)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// BatchEmbed — all optional parameters path (78.6%)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// CreateRebalancePlan — all optional parameters path (78.6%)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// ExecuteTransaction — all parameter variations (77.4%)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// RotateAuthFactor — all parameter path (76.9%)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// GetUserByCertThumbprint — error path (63.6%)
// The success path returns nil (mock), but we also need the isNotFoundError path.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// AddCertBinding — additional path (77.8%)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// tagsMapToJSON helper (83.3%)
// ---------------------------------------------------------------------------

func TestMCP_TS_TagsMapToJSON_Nil(t *testing.T) {
	// Call tagsMapToJSON with nil map to exercise the nil-return path.
	result, err := tagsMapToJSON(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Fatalf("expected empty string for nil map, got %q", result)
	}
}

func TestMCP_TS_TagsMapToJSON_NonEmpty(t *testing.T) {
	result, err := tagsMapToJSON(map[string]string{"host": "server1", "env": "prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty JSON for non-nil map")
	}
}

// ---------------------------------------------------------------------------
// toBackupInfo helper (0% coverage in transport_backup.go)
// ---------------------------------------------------------------------------

func TestMCP_toBackupInfo_Success(t *testing.T) {
	bi := &mcpBackupInfo{
		ID:          "bk-1",
		Status:      "complete",
		Compression: "zstd",
		Encryption:  "aes256",
	}
	info := bi.toBackupInfo()
	if info.ID != "bk-1" {
		t.Fatalf("unexpected ID: %q", info.ID)
	}
	if info.Status != transport.BackupStatusComplete {
		t.Fatalf("unexpected Status: %v", info.Status)
	}
}

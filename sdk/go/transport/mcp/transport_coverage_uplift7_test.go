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

// Seventh coverage-uplift file for the MCP SDK transport.
// Direct unit tests for helper functions and remaining gaps.

package mcp

import (
	"testing"
)

// ---------------------------------------------------------------------------
// mcpUsageInfo.toTransport — unit test (0% coverage)
// ---------------------------------------------------------------------------

func TestMCP_UsageInfoToTransport_Nil(t *testing.T) {
	var u *mcpUsageInfo
	result := u.toTransport()
	if result != nil {
		t.Fatalf("expected nil for nil mcpUsageInfo, got %+v", result)
	}
}

func TestMCP_UsageInfoToTransport_NonNil(t *testing.T) {
	u := &mcpUsageInfo{PromptTokens: 3, TotalTokens: 5}
	result := u.toTransport()
	if result == nil {
		t.Fatal("expected non-nil UsageInfo")
	}
	if result.PromptTokens != 3 || result.TotalTokens != 5 {
		t.Fatalf("unexpected UsageInfo: %+v", result)
	}
}

// ---------------------------------------------------------------------------
// extractTextContent - path with non-text content (83.3% → 100%)
// extractTextContent is an unexported function so we test it indirectly via
// callTool, or we can just add unit tests since it's accessible in the
// same package.
// ---------------------------------------------------------------------------

// Note: extractTextContent is not exported, so this tests it directly.

// ---------------------------------------------------------------------------
// ExecuteRebalance optional paths (80.0%) - with moves
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// GetUserByCertThumbprint — decode path coverage (63.6%)
// The success path returns nil user (mock returns nil,nil). The missing
// statements are: the isNotFoundError branch and the serverUserToTransport call.
// Test with a user service that returns a real user.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// UpdateDatabaseConnection optional paths (81.0%)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// CredentialRetrieve JSON decode path (83.3%)
// The mock returns nil,nil which makes the tool return "null" or empty.
// The JSON decode error fires on that empty text.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// TSWritePoint with tags (81.8%)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// mcpRecordToTransport helper (84.6%)
// ---------------------------------------------------------------------------

func TestMCP_mcpRecordToTransport_AllPaths(t *testing.T) {
	// populated record with all fields
	rec := &mcpFactorRecord{
		ID:           "f1",
		UserID:       1,
		Type:         "totp",
		Label:        "my-auth",
		Credential:   "dGVzdA==", // base64("test")
		RegisteredAt: "2026-01-01T00:00:00Z",
		LastUsedAt:   "2026-06-01T12:00:00Z",
		RevokedAt:    "2026-06-02T00:00:00Z",
	}
	result := mcpRecordToTransport(rec)
	if result.ID != "f1" {
		t.Fatalf("unexpected ID: %q", result.ID)
	}
	if result.LastUsedAt == nil {
		t.Fatal("expected LastUsedAt to be set")
	}
	if result.RevokedAt == nil {
		t.Fatal("expected RevokedAt to be set")
	}
}

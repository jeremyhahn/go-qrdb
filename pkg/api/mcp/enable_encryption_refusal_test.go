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

package mcp

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// migrationRefusalError stands in for a migrator's own typed refusal -- the shape
// sdk/go/node's MigrationNotSupportedError has -- which is NOT the sentinel
// itself but reports as it through errors.Is. A mapping that compared with ==
// instead of errors.Is would miss it.
type migrationRefusalError struct{ reason string }

func (e *migrationRefusalError) Error() string { return "migration not supported: " + e.reason }

func (e *migrationRefusalError) Is(target error) bool {
	return target == service.ErrMigrationProviderUnavailable
}

// TestHandleBarrierEnableEncryption_MCP_MigratorRefusalIsUnimplemented pins
// that a WIRED migrator declining the migration is coded Unimplemented, as an
// unwired one is, and not Internal.
func TestHandleBarrierEnableEncryption_MCP_MigratorRefusalIsUnimplemented(t *testing.T) {
	mig := &captureMCPMigrator{ch: make(chan mcpMigrateCall, 1), err: &migrationRefusalError{reason: "no seal store"}}
	req := buildCallToolRequest(t, "qrdb_barrier_enable_encryption", map[string]any{
		"strategy": "passphrase", "passphrase": "pw",
	})

	_, err := handleBarrierEnableEncryption(mig, allowAllTenantScope)(context.Background(), req)
	if err == nil {
		t.Fatal("expected the migrator's refusal")
	}
	if got := ServiceErrorCode(err); got != wire.CodeUnimplemented {
		t.Fatalf("code = %v, want CodeUnimplemented", got)
	}
}

// TestHandleBarrierEnableEncryption_MCP_DataPlaneRecyclingIsUnavailable pins
// the retryable answer for a migration that arrives mid-recycle.
func TestHandleBarrierEnableEncryption_MCP_DataPlaneRecyclingIsUnavailable(t *testing.T) {
	mig := &captureMCPMigrator{ch: make(chan mcpMigrateCall, 1), err: &service.DataPlaneRecyclingError{Service: "tenant"}}
	req := buildCallToolRequest(t, "qrdb_barrier_enable_encryption", map[string]any{
		"tenant_id": "acme", "strategy": "passphrase", "passphrase": "pw",
	})

	_, err := handleBarrierEnableEncryption(mig, allowAllTenantScope)(context.Background(), req)
	if err == nil {
		t.Fatal("expected the migrator's error")
	}
	if got := ServiceErrorCode(err); got != wire.CodeUnavailable {
		t.Fatalf("code = %v, want CodeUnavailable", got)
	}
}

// TestHandleBarrierEnableEncryption_MCP_NodeClosedIsUnavailable pins that a
// migration refused because the node is closing is retryable.
func TestHandleBarrierEnableEncryption_MCP_NodeClosedIsUnavailable(t *testing.T) {
	mig := &captureMCPMigrator{ch: make(chan mcpMigrateCall, 1), err: service.ErrNodeClosed}
	req := buildCallToolRequest(t, "qrdb_barrier_enable_encryption", map[string]any{
		"strategy": "passphrase", "passphrase": "pw",
	})

	_, err := handleBarrierEnableEncryption(mig, allowAllTenantScope)(context.Background(), req)
	if err == nil {
		t.Fatal("expected the migrator's error")
	}
	if got := ServiceErrorCode(err); got != wire.CodeUnavailable {
		t.Fatalf("code = %v, want CodeUnavailable", got)
	}
}

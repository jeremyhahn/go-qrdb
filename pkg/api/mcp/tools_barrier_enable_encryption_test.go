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
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// captureMCPMigrator records the EnableEncryption call for assertions.
type captureMCPMigrator struct {
	ch  chan mcpMigrateCall
	err error
}

type mcpMigrateCall struct {
	scope  service.BarrierScope
	opts   seal.MigrationOptions
	secret []byte // snapshot taken DURING the call; the handler destroys the credential after
}

func (m *captureMCPMigrator) EnableEncryption(_ context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	// The credential is a borrow valid for this call only: the handler's
	// goroutine destroys it when this returns, so an early destroy would
	// surface here as a panic from Snapshot rather than pass unnoticed.
	m.ch <- mcpMigrateCall{scope: scope, opts: opts, secret: sealtest.Snapshot(opts.Credentials)}
	return m.err
}

func TestHandleBarrierEnableEncryption_MCP_MissingStrategy(t *testing.T) {
	mig := &captureMCPMigrator{ch: make(chan mcpMigrateCall, 1)}
	handler := handleBarrierEnableEncryption(mig)

	req := buildCallToolRequest(t, "qrdb_barrier_enable_encryption", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for missing strategy")
	}
}

func TestHandleBarrierEnableEncryption_MCP_InvalidOptionsRejectedSynchronously(t *testing.T) {
	mig := &captureMCPMigrator{ch: make(chan mcpMigrateCall, 1)}
	handler := handleBarrierEnableEncryption(mig)

	req := buildCallToolRequest(t, "qrdb_barrier_enable_encryption", map[string]any{"strategy": "none"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for strategy=none")
	}
	select {
	case <-mig.ch:
		t.Fatal("migrator must not be invoked for invalid options")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHandleBarrierEnableEncryption_MCP_SystemStarts(t *testing.T) {
	mig := &captureMCPMigrator{ch: make(chan mcpMigrateCall, 1)}
	handler := handleBarrierEnableEncryption(mig)

	req := buildCallToolRequest(t, "qrdb_barrier_enable_encryption", map[string]any{
		"strategy":   "passphrase",
		"mechanism":  "swap",
		"passphrase": "pw",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp map[string]string
	if jsonErr := json.Unmarshal([]byte(resultText(t, result)), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp["status"] != "migration_started" {
		t.Fatalf("status = %q, want migration_started", resp["status"])
	}
	if resp["scope"] != "system" {
		t.Fatalf("scope = %q, want system", resp["scope"])
	}

	select {
	case call := <-mig.ch:
		if call.scope.Kind != service.ScopeSystem {
			t.Fatalf("scope.Kind = %v, want ScopeSystem", call.scope.Kind)
		}
		if string(call.secret) != "pw" {
			t.Fatalf("passphrase not threaded to credentials: %q", call.secret)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("migrator was never invoked")
	}
}

// TestHandleBarrierEnableEncryption_MCP_AcceptanceFailureSurfacesSynchronously
// proves that a synchronously-knowable rejection from the migrator (e.g.
// shared-barrier-immutable) is returned to the caller as a real tool error
// instead of the caller seeing a false "migration_started" while nothing
// actually happened (Codex #6).
func TestHandleBarrierEnableEncryption_MCP_AcceptanceFailureSurfacesSynchronously(t *testing.T) {
	mig := &captureMCPMigrator{ch: make(chan mcpMigrateCall, 1), err: service.ErrSharedBarrierImmutable}
	handler := handleBarrierEnableEncryption(mig)

	req := buildCallToolRequest(t, "qrdb_barrier_enable_encryption", map[string]any{
		"tenant_id": "acme", "strategy": "passphrase", "passphrase": "pw",
	})
	result, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error for a synchronously-rejected migration")
	}
	if result != nil {
		t.Fatalf("expected nil result on synchronous rejection, got %+v", result)
	}

	select {
	case <-mig.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("migrator was never invoked")
	}
}

func TestHandleBarrierEnableEncryption_MCP_TenantScope(t *testing.T) {
	mig := &captureMCPMigrator{ch: make(chan mcpMigrateCall, 1)}
	handler := handleBarrierEnableEncryption(mig)

	req := buildCallToolRequest(t, "qrdb_barrier_enable_encryption", map[string]any{
		"tenant_id":        "tenant-a",
		"strategy":         "shamir",
		"shamir_threshold": float64(2),
		"shamir_total":     float64(3),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var resp map[string]string
	if jsonErr := json.Unmarshal([]byte(resultText(t, result)), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp["scope"] != "tenant" {
		t.Fatalf("scope = %q, want tenant", resp["scope"])
	}

	select {
	case call := <-mig.ch:
		if call.scope.Kind != service.ScopeTenant || call.scope.TenantID != "tenant-a" {
			t.Fatalf("unexpected scope: %+v", call.scope)
		}
		if call.opts.ShamirThreshold != 2 || call.opts.ShamirTotal != 3 {
			t.Fatalf("unexpected shamir params: %+v", call.opts)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("migrator was never invoked")
	}
}

// TestBarrierTools_EnableEncryption_AlwaysRegistered is the go-qrdb#558
// regression test: qrdb_barrier_enable_encryption must be registered
// regardless of encMigrator, because a nil migrator here does not mean the
// same as "not available on this transport" -- it means "not available on
// this DEPLOYMENT" (standalone mode, or seal disabled), which the handler
// itself must answer with a typed refusal, matching REST/gRPC/Unix/QUIC.
// Previously the tool was omitted entirely when encMigrator was nil, the
// same shape of bug REST had with its barrierOn-gated route.
func TestBarrierTools_EnableEncryption_AlwaysRegistered(t *testing.T) {
	// nil is safe here: registration only captures the servicer in tool
	// handler closures, it never invokes it. Avoids depending on
	// newMockBarrierService, which lives in a build-tag-gated test file.
	var svc service.BarrierServicer

	hasEnableEncryption := func(tools []serverTool) bool {
		for _, tool := range tools {
			if tool.Tool.Name == "qrdb_barrier_enable_encryption" {
				return true
			}
		}
		return false
	}

	if !hasEnableEncryption(barrierTools(svc, nil, nil)) {
		t.Error("qrdb_barrier_enable_encryption must be registered even when encMigrator is nil")
	}
	if !hasEnableEncryption(barrierTools(svc, nil, &captureMCPMigrator{ch: make(chan mcpMigrateCall, 1)})) {
		t.Error("qrdb_barrier_enable_encryption must be registered when encMigrator is set")
	}
}

// TestHandleBarrierEnableEncryption_MCP_NilMigratorRefusesTyped proves the
// handler itself (not tool registration) is the sole authority on
// availability: called with a nil migrator, it refuses with
// service.ErrMigrationProviderUnavailable -- the same sentinel
// REST/gRPC/Unix/QUIC use for "no EncryptionMigrator configured" -- instead
// of the tool being silently absent.
func TestHandleBarrierEnableEncryption_MCP_NilMigratorRefusesTyped(t *testing.T) {
	handler := handleBarrierEnableEncryption(nil)
	req := buildCallToolRequest(t, "qrdb_barrier_enable_encryption", map[string]any{"strategy": "passphrase", "passphrase": "cGFzcw=="})
	_, err := handler(context.Background(), req)
	if !errors.Is(err, service.ErrMigrationProviderUnavailable) {
		t.Fatalf("err = %v, want service.ErrMigrationProviderUnavailable", err)
	}
}

// TestServer_BarrierEnableEncryption_RegisteredWithoutBarrierService is the
// go-qrdb#558 transport-matrix test at the MCP registration layer: a server
// with an EncryptionMigrator configured but NO barrier service (mirroring
// seal.enabled: false in raft/clustered mode) still exposes
// qrdb_barrier_enable_encryption and reaches the migrator, matching
// REST (TestHandleBarrierEnableEncryption_NoBarrierMigratorConfigured) and
// gRPC/Unix (TestStart_UnixOnly_EncryptionMigratorReachable in pkg/server).
// Before this fix the whole barrier tool group -- including this tool --
// was only ever added when s.barrierSvc was non-nil.
func TestServer_BarrierEnableEncryption_RegisteredWithoutBarrierService(t *testing.T) {
	mig := &captureMCPMigrator{ch: make(chan mcpMigrateCall, 1)}
	// Deliberately NOT WithBarrierService.
	s := NewServer(newMockKVService(), newMockAdminService(), ":0", WithEncryptionMigrator(mig))

	registeredTools := s.MCPServer().ListTools()
	handler, ok := registeredTools["qrdb_barrier_enable_encryption"]
	if !ok {
		t.Fatal("qrdb_barrier_enable_encryption not registered without a barrier service")
	}

	req := buildCallToolRequest(t, "qrdb_barrier_enable_encryption", map[string]any{"strategy": "passphrase", "passphrase": "cGFzcw=="})
	if _, err := handler.Handler(context.Background(), req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	select {
	case <-mig.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("migrator was never invoked")
	}
}

// TestServer_BarrierEnableEncryption_RegisteredWithNeitherBarrierNorMigrator
// is the go-qrdb#550/#558 round-5 regression test: a server with NEITHER a
// barrier service NOR an EncryptionMigrator (standalone mode with seal
// disabled) must still register qrdb_barrier_enable_encryption and answer a
// call to it with the typed service.ErrMigrationProviderUnavailable refusal
// -- not omit the tool and leave the caller to see a bare MCP "tool not
// found" instead. Before this fix the registration was
// `if barrierSvc != nil { ... } else if encMigrator != nil { ... }`, so this
// exact state (both nil) hit neither branch.
func TestServer_BarrierEnableEncryption_RegisteredWithNeitherBarrierNorMigrator(t *testing.T) {
	// Deliberately NOT WithBarrierService and NOT WithEncryptionMigrator.
	s := NewServer(newMockKVService(), newMockAdminService(), ":0")

	registeredTools := s.MCPServer().ListTools()
	handler, ok := registeredTools["qrdb_barrier_enable_encryption"]
	if !ok {
		t.Fatal("qrdb_barrier_enable_encryption not registered with neither a barrier service nor a migrator configured")
	}

	req := buildCallToolRequest(t, "qrdb_barrier_enable_encryption", map[string]any{"strategy": "passphrase", "passphrase": "cGFzcw=="})
	_, err := handler.Handler(context.Background(), req)
	if !errors.Is(err, service.ErrMigrationProviderUnavailable) {
		t.Fatalf("err = %v, want service.ErrMigrationProviderUnavailable", err)
	}
}

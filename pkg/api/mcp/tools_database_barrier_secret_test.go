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

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mcpCredentialDatabaseMock embeds a nil service.DatabaseServicer so it
// only needs to implement CreateForTenant (the non-credentialed fallback)
// and the DatabaseCredentialCreator pair.
type mcpCredentialDatabaseMock struct {
	service.DatabaseServicer

	gotCfg database.DatabaseConfig
	// gotSecret is a snapshot of the credential's plaintext taken during the
	// call: the handler destroys the credential when the call returns.
	gotSecret []byte
	credited  bool
}

func (m *mcpCredentialDatabaseMock) CreateForTenant(_ context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	m.gotCfg = cfg
	return &database.DatabaseInfo{Name: cfg.Name}, nil
}

func (m *mcpCredentialDatabaseMock) CreateWithCredentials(_ context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error) {
	m.gotCfg, m.gotSecret, m.credited = cfg, sealtest.Snapshot(creds), true
	return &database.DatabaseInfo{Name: cfg.Name}, nil
}

func (m *mcpCredentialDatabaseMock) CreateForTenantWithCredentials(_ context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error) {
	m.gotCfg, m.gotSecret, m.credited = cfg, sealtest.Snapshot(creds), true
	return &database.DatabaseInfo{Name: cfg.Name}, nil
}

// Compile-time interface checks.
var (
	_ service.DatabaseServicer          = (*mcpCredentialDatabaseMock)(nil)
	_ service.DatabaseCredentialCreator = (*mcpCredentialDatabaseMock)(nil)
)

func TestHandleDatabaseCreate_MCP_WithBarrierSecret_RoutesToCredentialedCreate(t *testing.T) {
	mock := &mcpCredentialDatabaseMock{}
	handler := handleDatabaseCreate(mock)

	req := buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name":                    "secured",
		"engine":                  "kv",
		"backend":                 "memory",
		"barrier_enabled":         true,
		"barrier_mode":            "passphrase",
		"barrier_shares_required": float64(2),
		"barrier_shares_total":    float64(3),
		"barrier_secret":          "topsecret",
	})

	if _, err := handler(context.Background(), req); err != nil {
		t.Fatalf("handleDatabaseCreate: %v", err)
	}

	if !mock.credited {
		t.Fatal("expected the credentialed create path to be used")
	}
	if got := string(mock.gotSecret); got != "topsecret" {
		t.Fatalf("secret = %q, want %q", got, "topsecret")
	}
	if mock.gotCfg.Barrier == nil || !mock.gotCfg.Barrier.Enabled {
		t.Fatal("expected cfg.Barrier.Enabled to be true")
	}
	if mock.gotCfg.Barrier.Mode != "passphrase" {
		t.Fatalf("barrier mode = %q, want passphrase", mock.gotCfg.Barrier.Mode)
	}
	if mock.gotCfg.Barrier.SharesRequired != 2 || mock.gotCfg.Barrier.SharesTotal != 3 {
		t.Fatalf("unexpected shamir params: %+v", mock.gotCfg.Barrier)
	}
}

func TestHandleDatabaseCreate_MCP_WithoutBarrier_NoBarrierConfig(t *testing.T) {
	mock := &mcpCredentialDatabaseMock{}
	handler := handleDatabaseCreate(mock)

	req := buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name": "plain", "engine": "kv", "backend": "memory",
	})

	if _, err := handler(context.Background(), req); err != nil {
		t.Fatalf("handleDatabaseCreate: %v", err)
	}
	if mock.gotCfg.Barrier != nil {
		t.Fatalf("expected nil cfg.Barrier when barrier_enabled is not set, got %+v", mock.gotCfg.Barrier)
	}
}

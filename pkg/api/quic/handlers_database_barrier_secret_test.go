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

package quic

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// quicCredentialDatabaseMock embeds a nil service.DatabaseServicer so it
// only needs to implement CreateForTenant (the non-credentialed fallback)
// and the DatabaseCredentialCreator pair. Self-contained (not defined in a
// build-tag-gated mocks file) so these tests compile and run under both
// plain and full tag sets.
type quicCredentialDatabaseMock struct {
	service.DatabaseServicer

	gotCfg database.DatabaseConfig
	// gotSecret is a snapshot of the credential's plaintext taken during the
	// call: the handler destroys the credential when the call returns.
	gotSecret []byte
	credited  bool
}

func (m *quicCredentialDatabaseMock) CreateForTenant(_ context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	m.gotCfg = cfg
	return &database.DatabaseInfo{Name: cfg.Name}, nil
}

func (m *quicCredentialDatabaseMock) CreateWithCredentials(_ context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error) {
	m.gotCfg, m.gotSecret, m.credited = cfg, sealtest.Snapshot(creds), true
	return &database.DatabaseInfo{Name: cfg.Name}, nil
}

func (m *quicCredentialDatabaseMock) CreateForTenantWithCredentials(_ context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error) {
	m.gotCfg, m.gotSecret, m.credited = cfg, sealtest.Snapshot(creds), true
	return &database.DatabaseInfo{Name: cfg.Name}, nil
}

// Compile-time interface checks.
var (
	_ service.DatabaseServicer          = (*quicCredentialDatabaseMock)(nil)
	_ service.DatabaseCredentialCreator = (*quicCredentialDatabaseMock)(nil)
)

func TestHandleDatabaseCreate_WithBarrierSecret_RoutesToCredentialedCreate(t *testing.T) {
	mock := &quicCredentialDatabaseMock{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(mock))

	payload, err := json.Marshal(map[string]any{
		"name": "secured", "backend": "memory",
		"barrier": map[string]any{
			"enabled":        true,
			"mode":           "passphrase",
			"sharesRequired": 2,
			"sharesTotal":    3,
			"secret":         "topsecret",
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleDatabaseCreate(context.Background(), payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
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

func TestHandleDatabaseCreate_WithoutBarrier_EmptyCredentials(t *testing.T) {
	mock := &quicCredentialDatabaseMock{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(mock))

	payload, _ := json.Marshal(map[string]any{"name": "plain", "backend": "memory"})
	if _, err := srv.handleDatabaseCreate(context.Background(), payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.gotSecret) != 0 {
		t.Fatalf("expected empty credentials, got %q", mock.gotSecret)
	}
	if mock.gotCfg.Barrier != nil {
		t.Fatalf("expected nil cfg.Barrier when no barrier config supplied, got %+v", mock.gotCfg.Barrier)
	}
}

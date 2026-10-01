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

package grpc

import (
	"context"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// credentialDatabaseMock embeds a nil service.DatabaseServicer so it only
// needs to implement the methods exercised by DatabaseCreate: CreateForTenant
// (the non-credentialed fallback) and the DatabaseCredentialCreator pair.
// Any other method call panics on the nil embedded interface, which is
// intentional -- these tests only exercise the create path.
type credentialDatabaseMock struct {
	service.DatabaseServicer

	gotCfg database.DatabaseConfig
	// gotSecret is a snapshot of the credential's plaintext taken during the
	// call: the handler destroys the credential when the call returns.
	gotSecret []byte
	credited  bool
}

func (m *credentialDatabaseMock) CreateForTenant(_ context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	m.gotCfg = cfg
	return &database.DatabaseInfo{Name: cfg.Name}, nil
}

func (m *credentialDatabaseMock) CreateWithCredentials(_ context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error) {
	m.gotCfg, m.gotSecret, m.credited = cfg, sealtest.Snapshot(creds), true
	return &database.DatabaseInfo{Name: cfg.Name}, nil
}

func (m *credentialDatabaseMock) CreateForTenantWithCredentials(_ context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error) {
	m.gotCfg, m.gotSecret, m.credited = cfg, sealtest.Snapshot(creds), true
	return &database.DatabaseInfo{Name: cfg.Name}, nil
}

// Compile-time interface checks.
var (
	_ service.DatabaseServicer          = (*credentialDatabaseMock)(nil)
	_ service.DatabaseCredentialCreator = (*credentialDatabaseMock)(nil)
)

func TestDatabaseCreate_WithBarrierSecret_RoutesToCredentialedCreate(t *testing.T) {
	mock := &credentialDatabaseMock{}
	client, cleanup := startSQLTestServer(t, WithDatabaseService(mock))
	defer cleanup()

	_, err := client.DatabaseCreate(context.Background(), &pb.DatabaseCreateRequest{
		Name:    "secured",
		Engine:  "kv",
		Backend: "memory",
		Barrier: &pb.DatabaseBarrierConfig{
			Enabled:        true,
			Mode:           "passphrase",
			SharesRequired: 2,
			SharesTotal:    3,
			Secret:         "topsecret",
		},
	})
	if err != nil {
		t.Fatalf("DatabaseCreate failed: %v", err)
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

// TestDatabaseCreate_WithoutBarrier_EmptyCredentials verifies that when the
// request carries no barrier config, DatabaseCreate still routes through the
// credentialed create path when the resolved DatabaseServicer supports it
// (mirroring the REST handler's createDatabaseForTenant), but with empty
// seal.Credentials -- this only matters when the caller later enables the
// barrier and expects a non-empty secret.
func TestDatabaseCreate_WithoutBarrier_EmptyCredentials(t *testing.T) {
	mock := &credentialDatabaseMock{}
	client, cleanup := startSQLTestServer(t, WithDatabaseService(mock))
	defer cleanup()

	_, err := client.DatabaseCreate(context.Background(), &pb.DatabaseCreateRequest{
		Name:    "plain",
		Engine:  "kv",
		Backend: "memory",
	})
	if err != nil {
		t.Fatalf("DatabaseCreate failed: %v", err)
	}
	if !mock.credited {
		t.Fatal("expected the credentialed create path to still be used")
	}
	if len(mock.gotSecret) != 0 {
		t.Fatalf("expected empty credentials, got %q", mock.gotSecret)
	}
	if mock.gotCfg.Name != "plain" {
		t.Fatalf("cfg.Name = %q, want plain", mock.gotCfg.Name)
	}
	if mock.gotCfg.Barrier != nil {
		t.Fatalf("expected nil cfg.Barrier when no barrier config supplied, got %+v", mock.gotCfg.Barrier)
	}
}

// plainDatabaseServicerMock implements only service.DatabaseServicer (NOT
// DatabaseCredentialCreator), by embedding a nil interface for the unused
// methods, exercising the shared helper's fallback branch.
type plainDatabaseServicerMock struct {
	service.DatabaseServicer
	gotCfg database.DatabaseConfig
}

func (m *plainDatabaseServicerMock) CreateForTenant(_ context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	m.gotCfg = cfg
	return &database.DatabaseInfo{Name: cfg.Name}, nil
}

// TestDatabaseCreate_NonCredentialedServicer_FallsBackToPlainCreate verifies
// that a DatabaseServicer NOT implementing DatabaseCredentialCreator (e.g. a
// bare test double) falls back to the plain CreateForTenant path without
// panicking on the type assertion.
func TestDatabaseCreate_NonCredentialedServicer_FallsBackToPlainCreate(t *testing.T) {
	mock := &plainDatabaseServicerMock{}
	client, cleanup := startSQLTestServer(t, WithDatabaseService(mock))
	defer cleanup()

	_, err := client.DatabaseCreate(context.Background(), &pb.DatabaseCreateRequest{
		Name:    "plain2",
		Engine:  "kv",
		Backend: "memory",
	})
	if err != nil {
		t.Fatalf("DatabaseCreate failed: %v", err)
	}
	if mock.gotCfg.Name != "plain2" {
		t.Fatalf("cfg.Name = %q, want plain2", mock.gotCfg.Name)
	}
}

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
	"context"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
)

// credentialedDatabaseService wraps fnDatabaseService and additionally
// implements service.DatabaseCredentialCreator (CreateWithCredentials /
// CreateForTenantWithCredentials), so the embedded transport's
// service.DatabaseCredentialCreator type-assertion in CreateDatabase
// succeeds and barrier credentials from --barrier-passphrase actually reach
// the mock. Tests record the cfg and creds seen by whichever create path
// the CLI's request shape ends up invoking.
type credentialedDatabaseService struct {
	*fnDatabaseService

	gotCfg     database.DatabaseConfig
	gotSecret  []byte // snapshot taken during the call; the CLI destroys the credential after
	calledWith string // "create", "createWithCredentials", or "" if never called
	createErr  error
	createInfo *database.DatabaseInfo
}

func newCredentialedDatabaseService() *credentialedDatabaseService {
	return &credentialedDatabaseService{fnDatabaseService: &fnDatabaseService{}}
}

func (m *credentialedDatabaseService) Create(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	m.gotCfg = cfg
	m.gotSecret = nil
	m.calledWith = "create"
	if m.createErr != nil {
		return nil, m.createErr
	}
	if m.createInfo != nil {
		return m.createInfo, nil
	}
	return &database.DatabaseInfo{Name: cfg.Name, Engine: cfg.Engine, Backend: cfg.Backend, DataDir: cfg.DataDir, Status: "open", Barrier: cfg.Barrier}, nil
}

func (m *credentialedDatabaseService) CreateWithCredentials(_ context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error) {
	m.gotCfg = cfg
	m.gotSecret = sealtest.Snapshot(creds)
	m.calledWith = "createWithCredentials"
	if m.createErr != nil {
		return nil, m.createErr
	}
	if m.createInfo != nil {
		return m.createInfo, nil
	}
	return &database.DatabaseInfo{Name: cfg.Name, Engine: cfg.Engine, Backend: cfg.Backend, DataDir: cfg.DataDir, Status: "open", Barrier: cfg.Barrier}, nil
}

func (m *credentialedDatabaseService) CreateForTenantWithCredentials(ctx context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error) {
	return m.CreateWithCredentials(ctx, cfg, creds)
}

func TestDatabaseCreateCmd_BarrierFlagsRegistered(t *testing.T) {
	cmd := newDatabaseCreateCmd()
	for _, name := range []string{
		"data-dir",
		"barrier-enabled",
		"barrier-strategy",
		"barrier-passphrase",
		"barrier-shamir-threshold",
		"barrier-shamir-total",
	} {
		if f := cmd.Flags().Lookup(name); f == nil {
			t.Errorf("create command missing --%s flag", name)
		}
	}
}

// TestDatabaseCreateCmd_BarrierEnabled_PassphraseFlowsToCredentials verifies
// --barrier-enabled --barrier-passphrase routes through
// DatabaseCredentialCreator.CreateWithCredentials with the passphrase carried
// in seal.Credentials.Secret, and that the barrier config (mode, enabled) is
// threaded into the DatabaseConfig sent to the service.
func TestDatabaseCreateCmd_BarrierEnabled_PassphraseFlowsToCredentials(t *testing.T) {
	dbSvc := newCredentialedDatabaseService()

	_, err := executeDatabaseCmd(t, dbSvc, "create", "secure-db",
		"--data-dir", "/data/secure-db",
		"--barrier-enabled",
		"--barrier-strategy", "passphrase",
		"--barrier-passphrase", "correct-horse-battery-staple",
	)
	if err != nil {
		t.Fatalf("database create error: %v", err)
	}

	if dbSvc.calledWith != "createWithCredentials" {
		t.Fatalf("calledWith = %q, want %q", dbSvc.calledWith, "createWithCredentials")
	}
	if dbSvc.gotCfg.Name != "secure-db" {
		t.Errorf("Name = %q, want %q", dbSvc.gotCfg.Name, "secure-db")
	}
	if dbSvc.gotCfg.DataDir != "/data/secure-db" {
		t.Errorf("DataDir = %q, want %q", dbSvc.gotCfg.DataDir, "/data/secure-db")
	}
	if dbSvc.gotCfg.Barrier == nil || !dbSvc.gotCfg.Barrier.Enabled {
		t.Fatal("Barrier.Enabled = false, want true")
	}
	if dbSvc.gotCfg.Barrier.Mode != "passphrase" {
		t.Errorf("Barrier.Mode = %q, want %q", dbSvc.gotCfg.Barrier.Mode, "passphrase")
	}
	if string(dbSvc.gotSecret) != "correct-horse-battery-staple" {
		t.Errorf("Credentials.Secret = %q, want %q", dbSvc.gotSecret, "correct-horse-battery-staple")
	}
}

// TestDatabaseCreateCmd_BarrierDisabled_UsesPlainCreate verifies that without
// --barrier-enabled the create command does not populate a Barrier config and
// dispatches through the plain (non-credentialed) create path.
func TestDatabaseCreateCmd_BarrierDisabled_UsesPlainCreate(t *testing.T) {
	dbSvc := newCredentialedDatabaseService()

	_, err := executeDatabaseCmd(t, dbSvc, "create", "plain-db")
	if err != nil {
		t.Fatalf("database create error: %v", err)
	}

	if dbSvc.calledWith != "create" {
		t.Fatalf("calledWith = %q, want %q", dbSvc.calledWith, "create")
	}
	if dbSvc.gotCfg.Barrier != nil {
		t.Errorf("Barrier = %+v, want nil", dbSvc.gotCfg.Barrier)
	}
}

// TestDatabaseCreateCmd_BarrierShamirFlags verifies --barrier-shamir-threshold
// and --barrier-shamir-total flow through to DatabaseBarrierConfig.
func TestDatabaseCreateCmd_BarrierShamirFlags(t *testing.T) {
	dbSvc := newCredentialedDatabaseService()

	_, err := executeDatabaseCmd(t, dbSvc, "create", "shamir-db",
		"--data-dir", "/data/shamir-db",
		"--barrier-enabled",
		"--barrier-strategy", "shamir",
		"--barrier-shamir-threshold", "3",
		"--barrier-shamir-total", "5",
	)
	if err != nil {
		t.Fatalf("database create error: %v", err)
	}

	if dbSvc.gotCfg.Barrier == nil {
		t.Fatal("Barrier is nil, want non-nil")
	}
	if dbSvc.gotCfg.Barrier.Mode != "shamir" {
		t.Errorf("Barrier.Mode = %q, want %q", dbSvc.gotCfg.Barrier.Mode, "shamir")
	}
	if dbSvc.gotCfg.Barrier.SharesRequired != 3 {
		t.Errorf("Barrier.SharesRequired = %d, want 3", dbSvc.gotCfg.Barrier.SharesRequired)
	}
	if dbSvc.gotCfg.Barrier.SharesTotal != 5 {
		t.Errorf("Barrier.SharesTotal = %d, want 5", dbSvc.gotCfg.Barrier.SharesTotal)
	}
}

// TestDatabaseCreateCmd_BarrierEnabled_SharedStoreRejected verifies that a
// SharedStoreBarrierUnsupportedError returned by the service (a database
// created without a dedicated store) surfaces as a CLI error, not a silent
// success.
func TestDatabaseCreateCmd_BarrierEnabled_SharedStoreRejected(t *testing.T) {
	dbSvc := newCredentialedDatabaseService()
	dbSvc.createErr = &database.SharedStoreBarrierUnsupportedError{Name: "shared-db", Backend: database.BackendPebble}

	_, err := executeDatabaseCmd(t, dbSvc, "create", "shared-db",
		"--barrier-enabled",
		"--barrier-passphrase", "whatever",
	)
	if err == nil {
		t.Fatal("expected error for barrier on shared-store database, got nil")
	}
	if !strings.Contains(err.Error(), "dedicated store") {
		t.Errorf("error = %v, want message mentioning %q", err, "dedicated store")
	}
}

// TestDatabaseCreateCmd_DataDirFlag verifies --data-dir populates
// DatabaseConfig.DataDir even without a barrier.
func TestDatabaseCreateCmd_DataDirFlag(t *testing.T) {
	dbSvc := newCredentialedDatabaseService()

	_, err := executeDatabaseCmd(t, dbSvc, "create", "dedicated-db", "--data-dir", "/data/dedicated-db")
	if err != nil {
		t.Fatalf("database create error: %v", err)
	}
	if dbSvc.gotCfg.DataDir != "/data/dedicated-db" {
		t.Errorf("DataDir = %q, want %q", dbSvc.gotCfg.DataDir, "/data/dedicated-db")
	}
}

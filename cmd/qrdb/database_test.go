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
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Function-based database mock
// ---------------------------------------------------------------------------

// fnDatabaseService implements service.DatabaseServicer with configurable
// function fields, allowing per-test behavior injection.
type fnDatabaseService struct {
	createFn         func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error)
	getFn            func(name string) (*database.DatabaseInfo, error)
	listFn           func() ([]database.DatabaseInfo, error)
	dropFn           func(name string) error
	updateProtocolFn func(name string, proto database.ProtocolConfig) (*database.DatabaseInfo, error)
	updateEtcdFn     func(name string, cfg *database.EtcdConfig) (*database.DatabaseInfo, error)
	scaleFn          func(name string, targetReplicas int) (*database.DatabaseInfo, error)
}

func (m *fnDatabaseService) Create(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return m.createFn(cfg)
}

func (m *fnDatabaseService) Get(name string) (*database.DatabaseInfo, error) {
	return m.getFn(name)
}

func (m *fnDatabaseService) List() ([]database.DatabaseInfo, error) {
	return m.listFn()
}

func (m *fnDatabaseService) Drop(_ context.Context, name string) error {
	return m.dropFn(name)
}

func (m *fnDatabaseService) UpdateProtocol(name string, proto database.ProtocolConfig) (*database.DatabaseInfo, error) {
	if m.updateProtocolFn != nil {
		return m.updateProtocolFn(name, proto)
	}
	return nil, &database.NotFoundError{Name: name}
}

func (m *fnDatabaseService) UpdateEtcd(name string, cfg *database.EtcdConfig) (*database.DatabaseInfo, error) {
	if m.updateEtcdFn != nil {
		return m.updateEtcdFn(name, cfg)
	}
	return nil, &database.NotFoundError{Name: name}
}

func (m *fnDatabaseService) UpdateRaft(_ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fnDatabaseService) UpdateBarrier(_ string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fnDatabaseService) UpdateConnection(_ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fnDatabaseService) SealDatabase(_ context.Context, _ string) error {
	return nil
}

func (m *fnDatabaseService) UnsealDatabase(_ context.Context, _ string, _ seal.Credentials) error {
	return nil
}

func (m *fnDatabaseService) DatabaseBarrierStatus(_ context.Context, _ string) (*seal.BarrierStatus, error) {
	return &seal.BarrierStatus{Sealed: false}, nil
}

// CreateForTenant creates as Create does: these tests run with no tenant in
// the context, and DatabaseService.CreateForTenant takes the system tenant's
// create path for such a caller.
func (m *fnDatabaseService) CreateForTenant(_ context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return m.createFn(cfg)
}

func (m *fnDatabaseService) GetForTenant(_ context.Context, _ string) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fnDatabaseService) ListForTenant(_ context.Context) ([]database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fnDatabaseService) DropForTenant(_ context.Context, _ string) error {
	return nil
}

func (m *fnDatabaseService) UpdateProtocolForTenant(_ context.Context, _ string, _ database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fnDatabaseService) UpdateEtcdForTenant(_ context.Context, _ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fnDatabaseService) UpdateRaftForTenant(_ context.Context, _ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fnDatabaseService) UpdateBarrierForTenant(_ context.Context, _ string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fnDatabaseService) UpdateConnectionForTenant(_ context.Context, _ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fnDatabaseService) UpdateSQL(name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, SQL: sqlCfg}, nil
}

func (m *fnDatabaseService) UpdateSQLForTenant(_ context.Context, name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return m.UpdateSQL(name, sqlCfg)
}

func (m *fnDatabaseService) ScaleForTenant(_ context.Context, name string, targetReplicas int) (*database.DatabaseInfo, error) {
	if m.scaleFn != nil {
		return m.scaleFn(name, targetReplicas)
	}
	return &database.DatabaseInfo{Name: name, Replicas: targetReplicas}, nil
}

// ---------------------------------------------------------------------------
// Minimal admin service for database tests
// ---------------------------------------------------------------------------

// dbTestAdminService implements service.AdminServicer as a no-op for
// database-focused tests. Only Capabilities returns a valid response
// since the embedded transport requires it.
type dbTestAdminService struct{}

func (m *dbTestAdminService) Health(_ context.Context) (*service.HealthStatus, error) {
	return nil, nil
}

func (m *dbTestAdminService) Status(_ context.Context) (*service.StatusInfo, error) {
	return nil, nil
}

func (m *dbTestAdminService) Peers(_ context.Context) (*service.PeersInfo, error) { return nil, nil }

func (m *dbTestAdminService) AddMember(_ context.Context, _, _ uint64, _, _ string) error {
	return nil
}

func (m *dbTestAdminService) RemoveMember(_ context.Context, _, _ uint64) error { return nil }

func (m *dbTestAdminService) RequestSnapshot(_ context.Context, _ uint64) error { return nil }

func (m *dbTestAdminService) Drain(_ context.Context) error { return nil }

func (m *dbTestAdminService) Capabilities(_ context.Context) (*capabilities.Capabilities, error) {
	return &capabilities.Capabilities{KV: true}, nil
}

func (m *dbTestAdminService) StartShard(_ context.Context, _ *service.StartShardRequest) error {
	return nil
}

func (m *dbTestAdminService) StopShard(_ context.Context, _ uint64) error { return nil }

func (m *dbTestAdminService) GetShardState(_ context.Context, _ uint64) (*service.ShardStateResponse, error) {
	return &service.ShardStateResponse{}, nil
}

func (m *dbTestAdminService) GetTopology(_ context.Context) (*service.TopologyResponse, error) {
	return &service.TopologyResponse{}, nil
}

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

// executeDatabaseCmd creates an embedded transport with the given database
// service, sets the package-level client, builds the database command tree,
// and executes it with the given args. It captures all output written to
// outputWriter.
func executeDatabaseCmd(t *testing.T, dbSvc service.DatabaseServicer, args ...string) (string, error) {
	t.Helper()

	kvSvc := &noopKVService{}
	adminSvc := &dbTestAdminService{}

	tr, err := embedded.New(
		kvSvc,
		adminSvc,
		embedded.WithDatabaseService(dbSvc),
	)
	if err != nil {
		t.Fatalf("embedded.New() error: %v", err)
	}

	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient() error: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	// Save and restore package-level state.
	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	// Set the package-level client and capture output.
	client = sdkClient
	var buf bytes.Buffer
	outputWriter = &buf

	// Build the database command tree.
	cmd := newDatabaseCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(context.Background())

	return buf.String(), execErr
}

// ---------------------------------------------------------------------------
// Create tests
// ---------------------------------------------------------------------------

func TestDatabaseCreate_Table(t *testing.T) {
	var gotCfg database.DatabaseConfig

	dbSvc := &fnDatabaseService{
		createFn: func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
			gotCfg = cfg
			return &database.DatabaseInfo{
				Name:    cfg.Name,
				Engine:  cfg.Engine,
				Backend: cfg.Backend,
				Status:  "open",
			}, nil
		},
	}

	output, err := executeDatabaseCmd(t, dbSvc, "create", "metrics", "--engine", "timeseries", "--backend", "pebble")
	if err != nil {
		t.Fatalf("database create error: %v", err)
	}

	if gotCfg.Name != "metrics" {
		t.Errorf("Name = %q, want %q", gotCfg.Name, "metrics")
	}
	if string(gotCfg.Engine) != "timeseries" {
		t.Errorf("Engine = %q, want %q", gotCfg.Engine, "timeseries")
	}
	if string(gotCfg.Backend) != "pebble" {
		t.Errorf("Backend = %q, want %q", gotCfg.Backend, "pebble")
	}

	for _, want := range []string{"Name:", "metrics", "Engine:", "timeseries", "Backend:", "pebble", "Status:", "open"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

// TestDatabaseCreate_ReplicasFlag verifies the --replicas flag is registered and
// flows the replication factor through to the database config.
func TestDatabaseCreate_ReplicasFlag(t *testing.T) {
	// The flag must be registered on the create command.
	if f := newDatabaseCreateCmd().Flags().Lookup("replicas"); f == nil {
		t.Fatal("create command missing --replicas flag")
	}

	var gotCfg database.DatabaseConfig
	dbSvc := &fnDatabaseService{
		createFn: func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
			gotCfg = cfg
			return &database.DatabaseInfo{
				Name:     cfg.Name,
				Engine:   cfg.Engine,
				Backend:  cfg.Backend,
				Replicas: cfg.Replicas,
				Status:   "open",
			}, nil
		},
	}

	if _, err := executeDatabaseCmd(t, dbSvc, "create", "rdb",
		"--engine", "kv", "--backend", "raft", "--replicas", "3"); err != nil {
		t.Fatalf("database create error: %v", err)
	}
	if gotCfg.Replicas != 3 {
		t.Errorf("Replicas = %d, want 3", gotCfg.Replicas)
	}
}

func TestDatabaseCreate_JSON(t *testing.T) {
	dbSvc := &fnDatabaseService{
		createFn: func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{
				Name:    cfg.Name,
				Engine:  cfg.Engine,
				Backend: cfg.Backend,
				Status:  "open",
				DataDir: "/data/metrics",
			}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	output, err := executeDatabaseCmd(t, dbSvc, "create", "metrics", "--engine", "kv")
	if err != nil {
		t.Fatalf("database create json error: %v", err)
	}

	var info transport.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(output), &info); jsonErr != nil {
		t.Fatalf("JSON unmarshal error: %v, output: %q", jsonErr, output)
	}
	if info.Name != "metrics" {
		t.Errorf("Name = %q, want %q", info.Name, "metrics")
	}
	if info.Engine != "kv" {
		t.Errorf("Engine = %q, want %q", info.Engine, "kv")
	}
	if info.DataDir != "/data/metrics" {
		t.Errorf("DataDir = %q, want %q", info.DataDir, "/data/metrics")
	}
}

func TestDatabaseCreate_YAML(t *testing.T) {
	dbSvc := &fnDatabaseService{
		createFn: func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{
				Name:    cfg.Name,
				Engine:  cfg.Engine,
				Backend: cfg.Backend,
				Status:  "open",
			}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "yaml"
	t.Cleanup(func() { outputFormat = oldFormat })

	output, err := executeDatabaseCmd(t, dbSvc, "create", "mydb")
	if err != nil {
		t.Fatalf("database create yaml error: %v", err)
	}

	for _, want := range []string{"name:", "mydb", "engine:", "status:", "open"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestDatabaseCreate_DefaultFlags(t *testing.T) {
	var gotCfg database.DatabaseConfig

	dbSvc := &fnDatabaseService{
		createFn: func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
			gotCfg = cfg
			return &database.DatabaseInfo{
				Name:    cfg.Name,
				Engine:  cfg.Engine,
				Backend: cfg.Backend,
				Status:  "open",
			}, nil
		},
	}

	_, err := executeDatabaseCmd(t, dbSvc, "create", "default")
	if err != nil {
		t.Fatalf("database create error: %v", err)
	}

	if string(gotCfg.Engine) != "kv" {
		t.Errorf("default engine = %q, want %q", gotCfg.Engine, "kv")
	}
	if string(gotCfg.Backend) != "pebble" {
		t.Errorf("default backend = %q, want %q", gotCfg.Backend, "pebble")
	}
}

func TestDatabaseCreate_ServiceError(t *testing.T) {
	dbSvc := &fnDatabaseService{
		createFn: func(_ database.DatabaseConfig) (*database.DatabaseInfo, error) {
			return nil, &database.AlreadyExistsError{Name: "metrics"}
		},
	}

	_, err := executeDatabaseCmd(t, dbSvc, "create", "metrics")
	if err == nil {
		t.Fatal("expected error from create command")
	}
}

func TestDatabaseCreate_MissingArg(t *testing.T) {
	dbSvc := &fnDatabaseService{
		createFn: func(_ database.DatabaseConfig) (*database.DatabaseInfo, error) {
			return nil, nil
		},
	}

	_, err := executeDatabaseCmd(t, dbSvc, "create")
	if err == nil {
		t.Fatal("expected error for missing name argument")
	}
}

// ---------------------------------------------------------------------------
// List tests
// ---------------------------------------------------------------------------

func TestDatabaseList_Table(t *testing.T) {
	dbSvc := &fnDatabaseService{
		listFn: func() ([]database.DatabaseInfo, error) {
			return []database.DatabaseInfo{
				{Name: "default", Engine: "kv", Backend: "pebble", Status: "open"},
				{Name: "metrics", Engine: "timeseries", Backend: "pebble", Status: "open"},
			}, nil
		},
	}

	output, err := executeDatabaseCmd(t, dbSvc, "list")
	if err != nil {
		t.Fatalf("database list error: %v", err)
	}

	for _, want := range []string{"NAME", "ENGINE", "BACKEND", "STATUS", "default", "metrics", "kv", "timeseries", "pebble", "open"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestDatabaseList_JSON(t *testing.T) {
	dbSvc := &fnDatabaseService{
		listFn: func() ([]database.DatabaseInfo, error) {
			return []database.DatabaseInfo{
				{Name: "default", Engine: "kv", Backend: "pebble", Status: "open"},
			}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	output, err := executeDatabaseCmd(t, dbSvc, "list")
	if err != nil {
		t.Fatalf("database list json error: %v", err)
	}

	var dbs []transport.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(output), &dbs); jsonErr != nil {
		t.Fatalf("JSON unmarshal error: %v, output: %q", jsonErr, output)
	}
	if len(dbs) != 1 {
		t.Fatalf("len(dbs) = %d, want 1", len(dbs))
	}
	if dbs[0].Name != "default" {
		t.Errorf("Name = %q, want %q", dbs[0].Name, "default")
	}
}

func TestDatabaseList_YAML(t *testing.T) {
	dbSvc := &fnDatabaseService{
		listFn: func() ([]database.DatabaseInfo, error) {
			return []database.DatabaseInfo{
				{Name: "default", Engine: "kv", Backend: "pebble", Status: "open"},
			}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "yaml"
	t.Cleanup(func() { outputFormat = oldFormat })

	output, err := executeDatabaseCmd(t, dbSvc, "list")
	if err != nil {
		t.Fatalf("database list yaml error: %v", err)
	}

	for _, want := range []string{"name:", "default", "engine:", "kv"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestDatabaseList_Empty(t *testing.T) {
	dbSvc := &fnDatabaseService{
		listFn: func() ([]database.DatabaseInfo, error) {
			return nil, nil
		},
	}

	output, err := executeDatabaseCmd(t, dbSvc, "list")
	if err != nil {
		t.Fatalf("database list error: %v", err)
	}

	// Table mode should still print headers.
	if !strings.Contains(output, "NAME") {
		t.Errorf("output missing header 'NAME', got: %q", output)
	}
}

func TestDatabaseList_ServiceError(t *testing.T) {
	dbSvc := &fnDatabaseService{
		listFn: func() ([]database.DatabaseInfo, error) {
			return nil, &database.MetaReadError{Path: "/data/meta", Err: io.EOF}
		},
	}

	_, err := executeDatabaseCmd(t, dbSvc, "list")
	if err == nil {
		t.Fatal("expected error from list command")
	}
}

// ---------------------------------------------------------------------------
// Info tests
// ---------------------------------------------------------------------------

func TestDatabaseInfo_Table(t *testing.T) {
	dbSvc := &fnDatabaseService{
		getFn: func(name string) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{
				Name:    name,
				Engine:  "kv",
				Backend: "pebble",
				Status:  "open",
				DataDir: "/data/default",
				ShardID: 42,
			}, nil
		},
	}

	output, err := executeDatabaseCmd(t, dbSvc, "info", "default")
	if err != nil {
		t.Fatalf("database info error: %v", err)
	}

	for _, want := range []string{
		"Name:", "default",
		"Engine:", "kv",
		"Backend:", "pebble",
		"Status:", "open",
		"DataDir:", "/data/default",
		"ShardID:", "42",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestDatabaseInfo_JSON(t *testing.T) {
	dbSvc := &fnDatabaseService{
		getFn: func(name string) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{
				Name:    name,
				Engine:  "kv",
				Backend: "pebble",
				Status:  "open",
				DataDir: "/data/default",
				ShardID: 1,
			}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	output, err := executeDatabaseCmd(t, dbSvc, "info", "default")
	if err != nil {
		t.Fatalf("database info json error: %v", err)
	}

	var info transport.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(output), &info); jsonErr != nil {
		t.Fatalf("JSON unmarshal error: %v, output: %q", jsonErr, output)
	}
	if info.Name != "default" {
		t.Errorf("Name = %q, want %q", info.Name, "default")
	}
	if info.ShardID != 1 {
		t.Errorf("ShardID = %d, want 1", info.ShardID)
	}
}

func TestDatabaseInfo_NotFound(t *testing.T) {
	dbSvc := &fnDatabaseService{
		getFn: func(name string) (*database.DatabaseInfo, error) {
			return nil, &database.NotFoundError{Name: name}
		},
	}

	_, err := executeDatabaseCmd(t, dbSvc, "info", "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent database")
	}
}

func TestDatabaseInfo_MissingArg(t *testing.T) {
	dbSvc := &fnDatabaseService{
		getFn: func(_ string) (*database.DatabaseInfo, error) {
			return nil, nil
		},
	}

	_, err := executeDatabaseCmd(t, dbSvc, "info")
	if err == nil {
		t.Fatal("expected error for missing name argument")
	}
}

func TestDatabaseInfo_TableNoOptionalFields(t *testing.T) {
	dbSvc := &fnDatabaseService{
		getFn: func(name string) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{
				Name:    name,
				Engine:  "kv",
				Backend: "pebble",
				Status:  "open",
			}, nil
		},
	}

	output, err := executeDatabaseCmd(t, dbSvc, "info", "default")
	if err != nil {
		t.Fatalf("database info error: %v", err)
	}

	// DataDir and ShardID should not be printed when empty/zero.
	if strings.Contains(output, "DataDir:") {
		t.Errorf("output should not contain 'DataDir:' when empty, got: %q", output)
	}
	if strings.Contains(output, "ShardID:") {
		t.Errorf("output should not contain 'ShardID:' when zero, got: %q", output)
	}
}

// ---------------------------------------------------------------------------
// Drop tests
// ---------------------------------------------------------------------------

func TestDatabaseDrop_Success(t *testing.T) {
	var gotName string

	dbSvc := &fnDatabaseService{
		dropFn: func(name string) error {
			gotName = name
			return nil
		},
	}

	output, err := executeDatabaseCmd(t, dbSvc, "drop", "metrics")
	if err != nil {
		t.Fatalf("database drop error: %v", err)
	}

	if gotName != "metrics" {
		t.Errorf("dropped name = %q, want %q", gotName, "metrics")
	}
	if !strings.Contains(output, "OK") {
		t.Errorf("output missing 'OK', got: %q", output)
	}
}

func TestDatabaseDrop_NotFound(t *testing.T) {
	dbSvc := &fnDatabaseService{
		dropFn: func(name string) error {
			return &database.NotFoundError{Name: name}
		},
	}

	_, err := executeDatabaseCmd(t, dbSvc, "drop", "nonexistent")
	if err == nil {
		t.Fatal("expected error for dropping nonexistent database")
	}
}

func TestDatabaseDrop_MissingArg(t *testing.T) {
	dbSvc := &fnDatabaseService{
		dropFn: func(_ string) error {
			return nil
		},
	}

	_, err := executeDatabaseCmd(t, dbSvc, "drop")
	if err == nil {
		t.Fatal("expected error for missing name argument")
	}
}

// ---------------------------------------------------------------------------
// Command structure tests
// ---------------------------------------------------------------------------

func TestDatabaseCmd_HasAlias(t *testing.T) {
	cmd := newDatabaseCmd()
	found := false
	for _, alias := range cmd.Aliases {
		if alias == "db" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("database command missing 'db' alias, aliases: %v", cmd.Aliases)
	}
}

func TestDatabaseCmd_SubcommandCount(t *testing.T) {
	cmd := newDatabaseCmd()
	subs := cmd.Commands()
	if len(subs) != 14 {
		t.Errorf("expected 14 subcommands, got %d", len(subs))
	}

	wantNames := map[string]bool{
		"create":            false,
		"list":              false,
		"info":              false,
		"drop":              false,
		"update-etcd":       false,
		"update-protocol":   false,
		"update-raft":       false,
		"scale":             false,
		"update-barrier":    false,
		"update-connection": false,
		"update-sql":        false,
		"seal":              false,
		"unseal":            false,
		"barrier-status":    false,
	}
	for _, sub := range subs {
		if _, ok := wantNames[sub.Name()]; ok {
			wantNames[sub.Name()] = true
		}
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("missing subcommand: %s", name)
		}
	}
}

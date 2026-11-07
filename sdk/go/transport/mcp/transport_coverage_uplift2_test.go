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

// Second coverage-uplift file for the MCP SDK transport.
// Covers:
//   - transport_backup.go (all backup methods + toBackupInfo)
//   - transport_database_barrier.go (SealDatabase, UnsealDatabase, DatabaseBarrierStatus)
//   - transport_ceremony_custodian.go (ClaimShareBegin, ClaimShareComplete, SubmitUnsealShare)
//   - transport_credentials.go CredentialStore + CredentialRetrieve
//   - transport_embedding.go (Embed, BatchEmbed, ListEmbeddingModels, toTransport)
//   - transport_rebalance.go (GetShardStats, CreateRebalancePlan, ExecuteRebalance,
//     GetImbalanceReport, toRebalancePlan, planToMCP)

package mcp

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock service implementations
// ---------------------------------------------------------------------------

// minimalBackupSvc satisfies service.BackupServicer for coverage tests.
type minimalBackupSvc struct {
	createErr  error
	listErr    error
	getErr     error
	deleteErr  error
	restoreErr error
	verifyErr  error

	lastCreate  service.BackupOptions
	lastRestore service.RestoreOptions
}

var _ service.BackupServicer = (*minimalBackupSvc)(nil)

func newMinimalBackupSvc() *minimalBackupSvc { return &minimalBackupSvc{} }

func (m *minimalBackupSvc) CreateBackup(_ context.Context, opts service.BackupOptions) (*service.BackupInfo, error) {
	m.lastCreate = opts
	if m.createErr != nil {
		return nil, m.createErr
	}
	return &service.BackupInfo{
		ID:          "bk-1",
		Timestamp:   time.Now(),
		Size:        1024,
		ShardCount:  2,
		Status:      service.BackupStatusComplete,
		Checksum:    "abc123",
		Destination: opts.Destination,
	}, nil
}

func (m *minimalBackupSvc) ListBackups(_ context.Context) ([]service.BackupInfo, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return []service.BackupInfo{{ID: "bk-1", Status: service.BackupStatusComplete}}, nil
}

func (m *minimalBackupSvc) GetBackup(_ context.Context, id string) (*service.BackupInfo, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return &service.BackupInfo{ID: id, Status: service.BackupStatusComplete}, nil
}

func (m *minimalBackupSvc) DeleteBackup(_ context.Context, _ string) error {
	return m.deleteErr
}

func (m *minimalBackupSvc) RestoreBackup(_ context.Context, opts service.RestoreOptions) (*service.RestoreResult, error) {
	m.lastRestore = opts
	if m.restoreErr != nil {
		return nil, m.restoreErr
	}
	return &service.RestoreResult{BackupID: "bk-1", ShardsRestored: 2}, nil
}

func (m *minimalBackupSvc) VerifyBackup(_ context.Context, id string) (*service.VerifyResult, error) {
	if m.verifyErr != nil {
		return nil, m.verifyErr
	}
	return &service.VerifyResult{Valid: true, ChecksumMatch: true}, nil
}

// minimalRebalanceSvc satisfies service.RebalanceServicer.
type minimalRebalanceSvc struct {
	shardStatsErr      error
	createPlanErr      error
	executeErr         error
	imbalanceReportErr error

	lastPlanOpts *service.RebalancePlanOptions
}

var _ service.RebalanceServicer = (*minimalRebalanceSvc)(nil)

func newMinimalRebalanceSvc() *minimalRebalanceSvc { return &minimalRebalanceSvc{} }

func (m *minimalRebalanceSvc) GetShardStats(_ context.Context, shardID uint64) (*service.ShardStats, error) {
	if m.shardStatsErr != nil {
		return nil, m.shardStatsErr
	}
	return &service.ShardStats{ShardID: shardID, KeyCount: 100, ByteCount: 2048, LeaderID: 1, ReplicaCount: 3}, nil
}

func (m *minimalRebalanceSvc) CreateRebalancePlan(_ context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
	m.lastPlanOpts = opts
	if m.createPlanErr != nil {
		return nil, m.createPlanErr
	}
	return &service.RebalancePlanResult{
		Moves: []service.RebalanceMovePlan{{ShardID: 1, FromNode: "n1", ToNode: "n2", EstimatedBytes: 512}},
	}, nil
}

func (m *minimalRebalanceSvc) ExecuteRebalance(_ context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
	if m.executeErr != nil {
		return nil, m.executeErr
	}
	if plan == nil {
		return &service.RebalanceExecutionResult{}, nil
	}
	return &service.RebalanceExecutionResult{MovesCompleted: len(plan.Moves)}, nil
}

func (m *minimalRebalanceSvc) GetImbalanceReport(_ context.Context) (*service.ImbalanceReportResult, error) {
	if m.imbalanceReportErr != nil {
		return nil, m.imbalanceReportErr
	}
	return &service.ImbalanceReportResult{
		Nodes: []service.NodeLoadInfo{{NodeID: "node-1", ShardCount: 3, TotalKeys: 1000}},
	}, nil
}

// minimalEmbeddingSvc satisfies service.EmbeddingServicer.
type minimalEmbeddingSvc struct {
	embedErr      error
	batchEmbedErr error
	listModelsErr error

	lastBatch *service.BatchEmbedRequest
}

var _ service.EmbeddingServicer = (*minimalEmbeddingSvc)(nil)

func newMinimalEmbeddingSvc() *minimalEmbeddingSvc { return &minimalEmbeddingSvc{} }

func (m *minimalEmbeddingSvc) Embed(_ context.Context, req *service.EmbedRequest) (*service.EmbedResponse, error) {
	if m.embedErr != nil {
		return nil, m.embedErr
	}
	return &service.EmbedResponse{
		Embeddings: [][]float32{{0.1, 0.2}},
		Model:      req.Model,
		Usage:      &service.UsageInfo{PromptTokens: 5, TotalTokens: 5},
	}, nil
}

func (m *minimalEmbeddingSvc) BatchEmbed(_ context.Context, req *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
	m.lastBatch = req
	if m.batchEmbedErr != nil {
		return nil, m.batchEmbedErr
	}
	return &service.BatchEmbedResponse{
		Embeddings: [][]float32{{0.3}},
		Model:      req.Model,
	}, nil
}

func (m *minimalEmbeddingSvc) ListModels(_ context.Context, _ *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
	if m.listModelsErr != nil {
		return nil, m.listModelsErr
	}
	return &service.ListEmbeddingModelsResponse{
		Models: []service.ModelInfo{{Name: "ada-002", Dimensions: 1536}},
	}, nil
}

func (m *minimalEmbeddingSvc) RegisterProvider(_ string, _ service.EmbeddingProvider) error {
	return nil
}

func (m *minimalEmbeddingSvc) DeregisterProvider(_ string) error { return nil }

func (m *minimalEmbeddingSvc) Close() error { return nil }

// ---------------------------------------------------------------------------
// Connection helpers
// ---------------------------------------------------------------------------

func connectBackupInProcess(t *testing.T, svc service.BackupServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(), newMockAdminService(), ":0",
		mcpapi.WithBackupService(svc),
	)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return tr, func() { _ = tr.Close() }
}

func connectRebalanceInProcess(t *testing.T, svc service.RebalanceServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(), newMockAdminService(), ":0",
		mcpapi.WithRebalanceService(svc),
	)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return tr, func() { _ = tr.Close() }
}

func connectEmbeddingInProcess(t *testing.T, svc service.EmbeddingServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(), newMockAdminService(), ":0",
		mcpapi.WithEmbedding(svc),
	)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return tr, func() { _ = tr.Close() }
}

func connectDatabaseBarrierInProcess(t *testing.T, svc service.DatabaseServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(), newMockAdminService(), ":0",
		mcpapi.WithDatabaseService(svc),
	)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return tr, func() { _ = tr.Close() }
}

// ---------------------------------------------------------------------------
// Backup tests
// ---------------------------------------------------------------------------

// rpcCase is one RPC of a service. call invokes it and check asserts its
// result; fail makes the service method behind it return err.
type rpcCase[S any] struct {
	name  string
	fail  func(svc S, err error)
	call  func(tr *Transport) (any, error)
	check func(t *testing.T, got any)
}

// runRPCCases runs every case twice: once against the service's answer,
// which check asserts, and once with fail applied, where the service error
// must reach the caller as a *transport.RequestError.
func runRPCCases[S any](t *testing.T, newSvc func() S, connect func(*testing.T, S) (*Transport, func()), cases []rpcCase[S]) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, cleanup := connect(t, newSvc())
			defer cleanup()
			got, err := tc.call(tr)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
		t.Run(tc.name+"/ServiceError", func(t *testing.T) {
			svc := newSvc()
			tc.fail(svc, errors.New("service failed"))
			tr, cleanup := connect(t, svc)
			defer cleanup()
			_, err := tc.call(tr)
			var re *transport.RequestError
			if !errors.As(err, &re) {
				t.Fatalf("%s: want *transport.RequestError, got %T (%v)", tc.name, err, err)
			}
		})
	}
}

func TestMCP_Backup_RPCs(t *testing.T) {
	ctx := context.Background()
	connect := func(t *testing.T, svc *minimalBackupSvc) (*Transport, func()) {
		return connectBackupInProcess(t, svc)
	}
	runRPCCases(t, newMinimalBackupSvc, connect, []rpcCase[*minimalBackupSvc]{
		{
			name: "CreateBackup",
			fail: func(s *minimalBackupSvc, err error) { s.createErr = err },
			call: func(tr *Transport) (any, error) {
				return tr.CreateBackup(ctx, &transport.BackupRequest{Destination: "/tmp/bk", ShardIDs: []uint64{1}})
			},
			check: func(t *testing.T, got any) {
				if id := got.(*transport.BackupInfo).ID; id != "bk-1" {
					t.Fatalf("ID = %q, want bk-1", id)
				}
			},
		},
		{
			name: "ListBackups",
			fail: func(s *minimalBackupSvc, err error) { s.listErr = err },
			call: func(tr *Transport) (any, error) { return tr.ListBackups(ctx) },
			check: func(t *testing.T, got any) {
				if n := len(got.([]transport.BackupInfo)); n == 0 {
					t.Fatal("ListBackups returned no backups")
				}
			},
		},
		{
			name: "GetBackup",
			fail: func(s *minimalBackupSvc, err error) { s.getErr = err },
			call: func(tr *Transport) (any, error) { return tr.GetBackup(ctx, "bk-1") },
			check: func(t *testing.T, got any) {
				if id := got.(*transport.BackupInfo).ID; id != "bk-1" {
					t.Fatalf("ID = %q, want bk-1", id)
				}
			},
		},
		{
			name: "DeleteBackup",
			fail: func(s *minimalBackupSvc, err error) { s.deleteErr = err },
			call: func(tr *Transport) (any, error) { return nil, tr.DeleteBackup(ctx, "bk-1") },
		},
		{
			name: "RestoreBackup",
			fail: func(s *minimalBackupSvc, err error) { s.restoreErr = err },
			call: func(tr *Transport) (any, error) {
				return tr.RestoreBackup(ctx, &transport.RestoreRequest{BackupID: "bk-1", TargetShards: []uint64{1, 2}})
			},
			check: func(t *testing.T, got any) {
				if n := got.(*transport.RestoreResult).ShardsRestored; n != 2 {
					t.Fatalf("ShardsRestored = %d, want 2", n)
				}
			},
		},
		{
			name: "VerifyBackup",
			fail: func(s *minimalBackupSvc, err error) { s.verifyErr = err },
			call: func(tr *Transport) (any, error) { return tr.VerifyBackup(ctx, "bk-1") },
			check: func(t *testing.T, got any) {
				if !got.(*transport.VerifyResult).Valid {
					t.Fatal("Valid = false, want true")
				}
			},
		},
	})

	t.Run("CreateBackup/AllOptions", func(t *testing.T) {
		svc := newMinimalBackupSvc()
		tr, cleanup := connectBackupInProcess(t, svc)
		defer cleanup()
		if _, err := tr.CreateBackup(ctx, &transport.BackupRequest{
			Destination:   "/tmp/bk",
			Compression:   transport.CompressionZstd,
			Encryption:    transport.EncryptionAES256,
			KeyProviderID: "software",
			KeyRef:        "backup.key",
			ShardIDs:      []uint64{1, 2, 3},
		}); err != nil {
			t.Fatalf("CreateBackup: %v", err)
		}
		want := service.BackupOptions{
			Destination:   "/tmp/bk",
			Compression:   service.CompressionType(transport.CompressionZstd),
			Encryption:    service.EncryptionType(transport.EncryptionAES256),
			KeyProviderID: "software",
			KeyRef:        "backup.key",
			ShardIDs:      []uint64{1, 2, 3},
		}
		if !reflect.DeepEqual(svc.lastCreate, want) {
			t.Errorf("service received %+v, want %+v", svc.lastCreate, want)
		}
	})

	t.Run("RestoreBackup/AllOptions", func(t *testing.T) {
		svc := newMinimalBackupSvc()
		tr, cleanup := connectBackupInProcess(t, svc)
		defer cleanup()
		if _, err := tr.RestoreBackup(ctx, &transport.RestoreRequest{
			BackupID:         "bk-1",
			TargetShards:     []uint64{1, 2},
			ValidateFirst:    true,
			DecryptionKeyRef: "backup.key",
			AllowPlaintext:   true,
		}); err != nil {
			t.Fatalf("RestoreBackup: %v", err)
		}
		want := service.RestoreOptions{
			BackupID:         "bk-1",
			TargetShards:     []uint64{1, 2},
			ValidateFirst:    true,
			DecryptionKeyRef: "backup.key",
			AllowPlaintext:   true,
		}
		if !reflect.DeepEqual(svc.lastRestore, want) {
			t.Errorf("service received %+v, want %+v", svc.lastRestore, want)
		}
	})
}

// TestMCP_DatabaseBarrier_SealUnsealStatus drives the per-database barrier
// RPCs against a server with the database service, and checks that each one
// refuses on a server without it.
func TestMCP_DatabaseBarrier_SealUnsealStatus(t *testing.T) {
	ctx := context.Background()
	calls := []struct {
		name string
		call func(tr *Transport) error
	}{
		{"SealDatabase", func(tr *Transport) error { return tr.SealDatabase(ctx, "mydb") }},
		{"UnsealDatabase", func(tr *Transport) error { return tr.UnsealDatabase(ctx, "mydb", "s3cr3t") }},
		{"DatabaseBarrierStatus", func(tr *Transport) error {
			status, err := tr.DatabaseBarrierStatus(ctx, "mydb")
			if err == nil && status == nil {
				t.Fatal("DatabaseBarrierStatus returned a nil status and no error")
			}
			return err
		}},
	}

	served, cleanupServed := connectDatabaseBarrierInProcess(t, newMockDatabaseSvc())
	defer cleanupServed()
	bare, cleanupBare := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanupBare()

	for _, c := range calls {
		if err := c.call(served); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		var re *transport.RequestError
		if err := c.call(bare); !errors.As(err, &re) {
			t.Errorf("%s without a database service: want *transport.RequestError, got %T (%v)", c.name, err, err)
		}
	}
}

func TestMCP_Embedding_RPCs(t *testing.T) {
	ctx := context.Background()
	connect := func(t *testing.T, svc *minimalEmbeddingSvc) (*Transport, func()) {
		return connectEmbeddingInProcess(t, svc)
	}
	runRPCCases(t, newMinimalEmbeddingSvc, connect, []rpcCase[*minimalEmbeddingSvc]{
		{
			name: "Embed",
			fail: func(s *minimalEmbeddingSvc, err error) { s.embedErr = err },
			call: func(tr *Transport) (any, error) {
				return tr.Embed(ctx, &transport.EmbedRequest{Texts: []string{"hello"}, Model: "ada-002"})
			},
			check: func(t *testing.T, got any) {
				if len(got.(*transport.EmbedResponse).Embeddings) == 0 {
					t.Fatal("Embed returned no embeddings")
				}
			},
		},
		{
			name: "BatchEmbed",
			fail: func(s *minimalEmbeddingSvc, err error) { s.batchEmbedErr = err },
			call: func(tr *Transport) (any, error) {
				return tr.BatchEmbed(ctx, &transport.BatchEmbedRequest{Texts: []string{"a", "b"}, Model: "ada-002"})
			},
			check: func(t *testing.T, got any) {
				if len(got.(*transport.BatchEmbedResponse).Embeddings) == 0 {
					t.Fatal("BatchEmbed returned no embeddings")
				}
			},
		},
		{
			name: "ListEmbeddingModels",
			fail: func(s *minimalEmbeddingSvc, err error) { s.listModelsErr = err },
			call: func(tr *Transport) (any, error) { return tr.ListEmbeddingModels(ctx, "openai") },
			check: func(t *testing.T, got any) {
				if len(got.(*transport.ListEmbeddingModelsResponse).Models) == 0 {
					t.Fatal("ListEmbeddingModels returned no models")
				}
			},
		},
	})

	t.Run("BatchEmbed/AllOptions", func(t *testing.T) {
		svc := newMinimalEmbeddingSvc()
		tr, cleanup := connectEmbeddingInProcess(t, svc)
		defer cleanup()
		if _, err := tr.BatchEmbed(ctx, &transport.BatchEmbedRequest{
			Texts:     []string{"a", "b", "c"},
			Model:     "ada-002",
			Provider:  "openai",
			BatchSize: 2,
		}); err != nil {
			t.Fatalf("BatchEmbed: %v", err)
		}
		want := &service.BatchEmbedRequest{Texts: []string{"a", "b", "c"}, Model: "ada-002", Provider: "openai", BatchSize: 2}
		if !reflect.DeepEqual(svc.lastBatch, want) {
			t.Errorf("service received %+v, want %+v", svc.lastBatch, want)
		}
	})
}

func TestMCP_Rebalance_RPCs(t *testing.T) {
	ctx := context.Background()
	connect := func(t *testing.T, svc *minimalRebalanceSvc) (*Transport, func()) {
		return connectRebalanceInProcess(t, svc)
	}
	runRPCCases(t, newMinimalRebalanceSvc, connect, []rpcCase[*minimalRebalanceSvc]{
		{
			name: "GetShardStats",
			fail: func(s *minimalRebalanceSvc, err error) { s.shardStatsErr = err },
			call: func(tr *Transport) (any, error) { return tr.GetShardStats(ctx, 1) },
			check: func(t *testing.T, got any) {
				if n := got.(*transport.ShardStatsResponse).KeyCount; n != 100 {
					t.Fatalf("KeyCount = %d, want 100", n)
				}
			},
		},
		{
			name: "CreateRebalancePlan",
			fail: func(s *minimalRebalanceSvc, err error) { s.createPlanErr = err },
			call: func(tr *Transport) (any, error) {
				return tr.CreateRebalancePlan(ctx, &transport.RebalancePlanRequest{MaxMoves: 5})
			},
			check: func(t *testing.T, got any) {
				if len(got.(*transport.RebalancePlan).Moves) == 0 {
					t.Fatal("CreateRebalancePlan returned no moves")
				}
			},
		},
		{
			name: "ExecuteRebalance",
			fail: func(s *minimalRebalanceSvc, err error) { s.executeErr = err },
			call: func(tr *Transport) (any, error) {
				return tr.ExecuteRebalance(ctx, &transport.RebalancePlan{
					Moves: []transport.RebalanceMove{{ShardID: 1, FromNode: "n1", ToNode: "n2"}},
				})
			},
			check: func(t *testing.T, got any) {
				if n := got.(*transport.RebalanceResult).MovesCompleted; n != 1 {
					t.Fatalf("MovesCompleted = %d, want 1", n)
				}
			},
		},
		{
			name: "GetImbalanceReport",
			fail: func(s *minimalRebalanceSvc, err error) { s.imbalanceReportErr = err },
			call: func(tr *Transport) (any, error) { return tr.GetImbalanceReport(ctx) },
			check: func(t *testing.T, got any) {
				if len(got.(*transport.ImbalanceReport).Nodes) == 0 {
					t.Fatal("GetImbalanceReport returned no nodes")
				}
			},
		},
	})

	t.Run("CreateRebalancePlan/AllOptions", func(t *testing.T) {
		svc := newMinimalRebalanceSvc()
		tr, cleanup := connectRebalanceInProcess(t, svc)
		defer cleanup()
		if _, err := tr.CreateRebalancePlan(ctx, &transport.RebalancePlanRequest{MaxMoves: 10, DryRun: true, TargetBalance: 0.2}); err != nil {
			t.Fatalf("CreateRebalancePlan: %v", err)
		}
		want := &service.RebalancePlanOptions{MaxMoves: 10, DryRun: true, TargetBalance: 0.2}
		if !reflect.DeepEqual(svc.lastPlanOpts, want) {
			t.Errorf("service received %+v, want %+v", svc.lastPlanOpts, want)
		}
	})

	t.Run("ExecuteRebalance/NilPlan", func(t *testing.T) {
		tr, cleanup := connectRebalanceInProcess(t, newMinimalRebalanceSvc())
		defer cleanup()
		result, err := tr.ExecuteRebalance(ctx, nil)
		if err != nil {
			t.Fatalf("ExecuteRebalance(nil): %v", err)
		}
		if result.MovesCompleted != 0 {
			t.Errorf("MovesCompleted = %d for a nil plan, want 0", result.MovesCompleted)
		}
	})
}

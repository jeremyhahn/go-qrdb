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
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Mock rebalance service
// ---------------------------------------------------------------------------

// cliMockRebalanceService implements service.RebalanceServicer with
// configurable function fields for per-test behavior injection.
type cliMockRebalanceService struct {
	getShardStatsFn       func(ctx context.Context, shardID uint64) (*service.ShardStats, error)
	createRebalancePlanFn func(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error)
	executeRebalanceFn    func(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error)
	getImbalanceReportFn  func(ctx context.Context) (*service.ImbalanceReportResult, error)
}

func (m *cliMockRebalanceService) GetShardStats(ctx context.Context, shardID uint64) (*service.ShardStats, error) {
	if m.getShardStatsFn != nil {
		return m.getShardStatsFn(ctx, shardID)
	}
	return &service.ShardStats{
		ShardID:      shardID,
		KeyCount:     100,
		ByteCount:    2048,
		LeaderID:     1,
		ReplicaCount: 3,
	}, nil
}

func (m *cliMockRebalanceService) CreateRebalancePlan(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
	if m.createRebalancePlanFn != nil {
		return m.createRebalancePlanFn(ctx, opts)
	}
	return &service.RebalancePlanResult{
		Moves: []service.RebalanceMovePlan{
			{ShardID: 1, FromNode: "node1:9100", ToNode: "node2:9100", EstimatedBytes: 1024},
		},
		EstimatedDuration: 5 * time.Second,
		CurrentImbalance:  0.3,
	}, nil
}

func (m *cliMockRebalanceService) ExecuteRebalance(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
	if m.executeRebalanceFn != nil {
		return m.executeRebalanceFn(ctx, plan)
	}
	return &service.RebalanceExecutionResult{
		MovesCompleted: len(plan.Moves),
		MovesFailed:    0,
		Duration:       2 * time.Second,
		FinalImbalance: 0.05,
	}, nil
}

func (m *cliMockRebalanceService) GetImbalanceReport(ctx context.Context) (*service.ImbalanceReportResult, error) {
	if m.getImbalanceReportFn != nil {
		return m.getImbalanceReportFn(ctx)
	}
	return &service.ImbalanceReportResult{
		Nodes: []service.NodeLoadInfo{
			{NodeID: "node-1", Address: "10.0.0.1:9100", ShardCount: 3, TotalKeys: 500, TotalBytes: 4096, LeaderCount: 2},
			{NodeID: "node-2", Address: "10.0.0.2:9100", ShardCount: 2, TotalKeys: 300, TotalBytes: 2048, LeaderCount: 1},
		},
		ImbalanceScore: 0.3,
		Hotspots:       []string{"node-1"},
	}, nil
}

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

// executeRebalanceCmd constructs an embedded SDK client with the given
// rebalance service, installs it as the package-level client, and executes
// the rebalance command tree with the given args.
func executeRebalanceCmd(t *testing.T, rebalanceSvc service.RebalanceServicer, args ...string) (string, error) {
	t.Helper()

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(), embedded.WithRebalanceService(rebalanceSvc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}

	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	client = sdkClient
	outputFormat = "table"
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newRebalanceCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(context.Background())
	return buf.String(), execErr
}

// ---------------------------------------------------------------------------
// rebalance stats tests
// ---------------------------------------------------------------------------

func TestRebalanceStatsCmd_Success(t *testing.T) {
	svc := &cliMockRebalanceService{}
	out, err := executeRebalanceCmd(t, svc, "stats", "--shard", "1")
	if err != nil {
		t.Fatalf("rebalance stats: %v", err)
	}
	if !strings.Contains(out, "ShardID") {
		t.Fatalf("expected ShardID in output, got: %s", out)
	}
	if !strings.Contains(out, "KeyCount") {
		t.Fatalf("expected KeyCount in output, got: %s", out)
	}
}

func TestRebalanceStatsCmd_MissingShardFlag(t *testing.T) {
	svc := &cliMockRebalanceService{}
	_, err := executeRebalanceCmd(t, svc, "stats")
	if err == nil {
		t.Fatal("expected error when --shard is missing")
	}
}

func TestRebalanceStatsCmd_ServiceError(t *testing.T) {
	svc := &cliMockRebalanceService{
		getShardStatsFn: func(_ context.Context, _ uint64) (*service.ShardStats, error) {
			return nil, errors.New("shard not found")
		},
	}
	_, err := executeRebalanceCmd(t, svc, "stats", "--shard", "99")
	if err == nil {
		t.Fatal("expected error from service")
	}
}

func TestRebalanceStatsCmd_OutputJSON(t *testing.T) {
	svc := &cliMockRebalanceService{}

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(), embedded.WithRebalanceService(svc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	client = sdkClient
	outputFormat = "json"
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newRebalanceCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs([]string{"stats", "--shard", "1"})

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("rebalance stats --output json: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `"shard_id"`) {
		t.Fatalf("expected JSON with shard_id field, got: %s", out)
	}
}

// ---------------------------------------------------------------------------
// rebalance plan tests
// ---------------------------------------------------------------------------

func TestRebalancePlanCmd_Success(t *testing.T) {
	svc := &cliMockRebalanceService{}
	out, err := executeRebalanceCmd(t, svc, "plan")
	if err != nil {
		t.Fatalf("rebalance plan: %v", err)
	}
	if !strings.Contains(out, "CurrentImbalance") {
		t.Fatalf("expected CurrentImbalance in output, got: %s", out)
	}
	if !strings.Contains(out, "node1:9100") {
		t.Fatalf("expected move details in output, got: %s", out)
	}
}

func TestRebalancePlanCmd_WithMaxMoves(t *testing.T) {
	var capturedOpts *service.RebalancePlanOptions
	svc := &cliMockRebalanceService{
		createRebalancePlanFn: func(_ context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
			capturedOpts = opts
			return &service.RebalancePlanResult{
				Moves:            nil,
				CurrentImbalance: 0.1,
			}, nil
		},
	}
	_, err := executeRebalanceCmd(t, svc, "plan", "--max-moves", "5")
	if err != nil {
		t.Fatalf("rebalance plan --max-moves: %v", err)
	}
	if capturedOpts == nil || capturedOpts.MaxMoves != 5 {
		t.Fatalf("expected MaxMoves=5, got %v", capturedOpts)
	}
}

func TestRebalancePlanCmd_DryRun(t *testing.T) {
	var capturedOpts *service.RebalancePlanOptions
	svc := &cliMockRebalanceService{
		createRebalancePlanFn: func(_ context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
			capturedOpts = opts
			return &service.RebalancePlanResult{CurrentImbalance: 0.2}, nil
		},
	}
	_, err := executeRebalanceCmd(t, svc, "plan", "--dry-run")
	if err != nil {
		t.Fatalf("rebalance plan --dry-run: %v", err)
	}
	if capturedOpts == nil || !capturedOpts.DryRun {
		t.Fatal("expected DryRun=true")
	}
}

func TestRebalancePlanCmd_ServiceError(t *testing.T) {
	svc := &cliMockRebalanceService{
		createRebalancePlanFn: func(_ context.Context, _ *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
			return nil, errors.New("cluster unavailable")
		},
	}
	_, err := executeRebalanceCmd(t, svc, "plan")
	if err == nil {
		t.Fatal("expected error from service")
	}
}

func TestRebalancePlanCmd_EmptyMoves(t *testing.T) {
	svc := &cliMockRebalanceService{
		createRebalancePlanFn: func(_ context.Context, _ *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
			return &service.RebalancePlanResult{
				Moves:            nil,
				CurrentImbalance: 0.0,
			}, nil
		},
	}
	out, err := executeRebalanceCmd(t, svc, "plan")
	if err != nil {
		t.Fatalf("rebalance plan with no moves: %v", err)
	}
	if !strings.Contains(out, "Moves:") {
		t.Fatalf("expected Moves in output, got: %s", out)
	}
}

// ---------------------------------------------------------------------------
// rebalance execute tests
// ---------------------------------------------------------------------------

func TestRebalanceExecuteCmd_Success(t *testing.T) {
	svc := &cliMockRebalanceService{}

	// Build a JSON plan to feed via stdin.
	plan := transport.RebalancePlan{
		Moves: []transport.RebalanceMove{
			{ShardID: 1, FromNode: "node1:9100", ToNode: "node2:9100", EstimatedBytes: 512},
		},
		EstimatedDuration: 5 * time.Second,
		CurrentImbalance:  0.3,
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}

	// Replace os.Stdin with a pipe containing our JSON plan.
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })

	if _, err := w.Write(planJSON); err != nil {
		t.Fatalf("write plan to pipe: %v", err)
	}
	_ = w.Close()

	out, execErr := executeRebalanceCmd(t, svc, "execute")
	if execErr != nil {
		t.Fatalf("rebalance execute: %v", execErr)
	}
	if !strings.Contains(out, "MovesCompleted") {
		t.Fatalf("expected MovesCompleted in output, got: %s", out)
	}
}

func TestRebalanceExecuteCmd_PartialFailure(t *testing.T) {
	svc := &cliMockRebalanceService{
		executeRebalanceFn: func(_ context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
			return &service.RebalanceExecutionResult{
				MovesCompleted: len(plan.Moves) - 1,
				MovesFailed:    1,
				Duration:       2 * time.Second,
				FinalImbalance: 0.2,
			}, nil
		},
	}

	plan := transport.RebalancePlan{
		Moves: []transport.RebalanceMove{
			{ShardID: 1, FromNode: "node1:9100", ToNode: "node2:9100", EstimatedBytes: 512},
		},
		EstimatedDuration: 5 * time.Second,
		CurrentImbalance:  0.3,
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })

	if _, err := w.Write(planJSON); err != nil {
		t.Fatalf("write plan to pipe: %v", err)
	}
	_ = w.Close()

	out, execErr := executeRebalanceCmd(t, svc, "execute")
	if execErr == nil {
		t.Fatal("expected a non-nil error for a partial rebalance failure")
	}
	var partialErr *RebalancePartialFailureError
	if !errors.As(execErr, &partialErr) {
		t.Fatalf("expected *RebalancePartialFailureError, got %T: %v", execErr, execErr)
	}
	if partialErr.FailedCount != 1 {
		t.Errorf("expected FailedCount 1, got %d", partialErr.FailedCount)
	}
	// The result detail must still be printed even though the command now
	// returns a non-nil error.
	if !strings.Contains(out, "MovesFailed:    1") {
		t.Fatalf("expected MovesFailed in output, got: %s", out)
	}
}

func TestRebalanceExecuteCmd_InvalidJSON(t *testing.T) {
	svc := &cliMockRebalanceService{}

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })

	if _, err := w.Write([]byte("not valid json {")); err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	_ = w.Close()

	_, execErr := executeRebalanceCmd(t, svc, "execute")
	if execErr == nil {
		t.Fatal("expected error for invalid JSON")
	}
	var argErr *InvalidArgError
	if !errors.As(execErr, &argErr) {
		t.Fatalf("expected InvalidArgError, got %T: %v", execErr, execErr)
	}
}

func TestRebalanceExecuteCmd_ServiceError(t *testing.T) {
	svc := &cliMockRebalanceService{
		executeRebalanceFn: func(_ context.Context, _ *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
			return nil, errors.New("execution failed")
		},
	}

	plan := transport.RebalancePlan{
		Moves: []transport.RebalanceMove{
			{ShardID: 1, FromNode: "node1:9100", ToNode: "node2:9100"},
		},
	}
	planJSON, _ := json.Marshal(plan)

	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })

	_, _ = w.Write(planJSON)
	_ = w.Close()

	_, execErr := executeRebalanceCmd(t, svc, "execute")
	if execErr == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// rebalance report tests
// ---------------------------------------------------------------------------

func TestRebalanceReportCmd_Success(t *testing.T) {
	svc := &cliMockRebalanceService{}
	out, err := executeRebalanceCmd(t, svc, "report")
	if err != nil {
		t.Fatalf("rebalance report: %v", err)
	}
	if !strings.Contains(out, "ImbalanceScore") {
		t.Fatalf("expected ImbalanceScore in output, got: %s", out)
	}
	if !strings.Contains(out, "node-1") {
		t.Fatalf("expected node-1 in output, got: %s", out)
	}
	if !strings.Contains(out, "node-1") {
		t.Fatalf("expected hotspot node-1 in output, got: %s", out)
	}
}

func TestRebalanceReportCmd_NoHotspots(t *testing.T) {
	svc := &cliMockRebalanceService{
		getImbalanceReportFn: func(_ context.Context) (*service.ImbalanceReportResult, error) {
			return &service.ImbalanceReportResult{
				Nodes: []service.NodeLoadInfo{
					{NodeID: "node-1", Address: "10.0.0.1:9100", ShardCount: 2},
				},
				ImbalanceScore: 0.0,
				Hotspots:       nil,
			}, nil
		},
	}
	out, err := executeRebalanceCmd(t, svc, "report")
	if err != nil {
		t.Fatalf("rebalance report no hotspots: %v", err)
	}
	if !strings.Contains(out, "0.0000") {
		t.Fatalf("expected 0.0000 imbalance score, got: %s", out)
	}
}

func TestRebalanceReportCmd_ServiceError(t *testing.T) {
	svc := &cliMockRebalanceService{
		getImbalanceReportFn: func(_ context.Context) (*service.ImbalanceReportResult, error) {
			return nil, errors.New("cluster unreachable")
		},
	}
	_, err := executeRebalanceCmd(t, svc, "report")
	if err == nil {
		t.Fatal("expected error from service")
	}
}

func TestRebalanceReportCmd_OutputJSON(t *testing.T) {
	svc := &cliMockRebalanceService{}

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(), embedded.WithRebalanceService(svc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	client = sdkClient
	outputFormat = "json"
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newRebalanceCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs([]string{"report"})

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("rebalance report --output json: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `"imbalance_score"`) {
		t.Fatalf("expected JSON with imbalance_score field, got: %s", out)
	}
}

// ---------------------------------------------------------------------------
// Output helper tests
// ---------------------------------------------------------------------------

func TestPrintShardStats_Table(t *testing.T) {
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "table"

	err := printShardStats(&transport.ShardStatsResponse{
		ShardID:      42,
		KeyCount:     100,
		ByteCount:    2048,
		LeaderID:     1,
		ReplicaCount: 3,
	})
	if err != nil {
		t.Fatalf("printShardStats: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "42") {
		t.Fatalf("expected shard ID 42 in output, got: %s", out)
	}
}

func TestPrintRebalancePlan_Table(t *testing.T) {
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "table"

	err := printRebalancePlan(&transport.RebalancePlan{
		Moves: []transport.RebalanceMove{
			{ShardID: 1, FromNode: "n1", ToNode: "n2", EstimatedBytes: 512},
		},
		CurrentImbalance:  0.25,
		EstimatedDuration: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("printRebalancePlan: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "0.2500") {
		t.Fatalf("expected imbalance 0.2500 in output, got: %s", out)
	}
}

func TestPrintRebalanceResult_Table(t *testing.T) {
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "table"

	err := printRebalanceResult(&transport.RebalanceResult{
		MovesCompleted: 3,
		MovesFailed:    0,
		Duration:       2 * time.Second,
		FinalImbalance: 0.02,
	})
	if err != nil {
		t.Fatalf("printRebalanceResult: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "MovesCompleted") {
		t.Fatalf("expected MovesCompleted in output, got: %s", out)
	}
}

func TestPrintImbalanceReport_Table(t *testing.T) {
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "table"

	err := printImbalanceReport(&transport.ImbalanceReport{
		Nodes: []transport.NodeLoad{
			{NodeID: "n1", Address: "10.0.0.1:9100", ShardCount: 3, LeaderCount: 2, TotalKeys: 500, TotalBytes: 4096},
		},
		ImbalanceScore: 0.15,
		Hotspots:       []string{"n1"},
	})
	if err != nil {
		t.Fatalf("printImbalanceReport: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "n1") {
		t.Fatalf("expected node n1 in output, got: %s", out)
	}
}

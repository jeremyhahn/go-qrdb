// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

// rebalance_cli_test.go drives the entire `qrdb rebalance` command tree
// (stats, plan, execute, report) through the real client CLI binary
// (cmd/qrdb), against every transport (REST, gRPC, QUIC, MCP, Unix) using
// the shared startMultiProtocolNode harness (multi_protocol_helpers_test.go).
// Per the CLI coverage audit (Batch F), rebalance was previously tested only
// via raw REST/iraftAPI helpers against isolation-raft clusters
// (isolation_raft_rebalance_test.go), never through the CLI binary.
//
// `rebalance stats` and `rebalance report` are meaningful on a single node
// (service.RebalanceService.GetShardStats/GetImbalanceReport read real shard
// state off the host regardless of cluster size). `rebalance plan` and
// `rebalance execute` are ALSO exercised here in their single-node,
// zero-move form: service.RebalanceService.CreateRebalancePlan short-circuits
// to a 0-move plan whenever fewer than 2 distinct node addresses host the
// cluster's shards (see rebalance_service.go's `len(report.Nodes) < 2`
// guard), and ExecuteRebalance completes a 0-move plan as a true no-op
// (MovesCompleted=0, MovesFailed=0). This is a real, meaningful round trip
// of the full command tree's wire path and flag surface; it does NOT
// exercise a non-zero-move rebalance, which requires a multi-node,
// multi-shard cluster with genuine load imbalance and is out of scope for
// this CLI-parity batch (that server-side behavior already has REST/iraftAPI
// coverage in isolation_raft_rebalance_test.go).
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"
)

// runCLIProtoStdin runs the qrdb client CLI against node over the given
// protocol exactly like runCLIProto, but additionally feeds stdin to the
// child process. This is required for `rebalance execute`, which reads its
// RebalancePlan JSON payload from stdin (see cmd/qrdb/rebalance.go's
// newRebalanceExecuteCmd), and cannot be exercised through runCLIProto's
// argv-only signature.
func runCLIProtoStdin(t *testing.T, node *multiProtoNode, proto multiProtocol, stdin string, args ...string) (string, error) {
	t.Helper()
	full := append(append([]string{}, node.cliArgsFor(proto)...), args...)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, node.cli, full...)
	cmd.Stdin = bytes.NewBufferString(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// rebalanceShardStatsDisplay mirrors transport.ShardStatsResponse's wire
// JSON shape, decoded from `rebalance stats --output json`.
type rebalanceShardStatsDisplay struct {
	ShardID      uint64 `json:"shard_id"`
	KeyCount     uint64 `json:"key_count"`
	ByteCount    uint64 `json:"byte_count"`
	LeaderID     uint64 `json:"leader_id"`
	ReplicaCount int    `json:"replica_count"`
}

// rebalancePlanDisplay mirrors transport.RebalancePlan's wire JSON shape,
// decoded from `rebalance plan --output json`.
type rebalancePlanDisplay struct {
	Moves []struct {
		ShardID        uint64 `json:"shard_id"`
		FromNode       string `json:"from_node"`
		ToNode         string `json:"to_node"`
		EstimatedBytes uint64 `json:"estimated_bytes"`
	} `json:"moves"`
	EstimatedDuration int64   `json:"estimated_duration"`
	CurrentImbalance  float64 `json:"current_imbalance"`
}

// rebalanceResultDisplay mirrors transport.RebalanceResult's wire JSON
// shape, decoded from `rebalance execute --output json`.
type rebalanceResultDisplay struct {
	MovesCompleted int     `json:"moves_completed"`
	MovesFailed    int     `json:"moves_failed"`
	Duration       int64   `json:"duration"`
	FinalImbalance float64 `json:"final_imbalance"`
}

// rebalanceReportDisplay mirrors transport.ImbalanceReport's wire JSON
// shape, decoded from `rebalance report --output json`.
type rebalanceReportDisplay struct {
	Nodes []struct {
		NodeID      string `json:"node_id"`
		Address     string `json:"address"`
		ShardCount  int    `json:"shard_count"`
		TotalKeys   uint64 `json:"total_keys"`
		TotalBytes  uint64 `json:"total_bytes"`
		LeaderCount int    `json:"leader_count"`
	} `json:"nodes"`
	ImbalanceScore float64  `json:"imbalance_score"`
	Hotspots       []string `json:"hotspots"`
}

// TestRebalance_CLI_PerProtocol drives `qrdb rebalance stats/plan/execute/
// report` -- all flag permutations -- through the CLI binary against every
// transport sharing one multiProtoNode (single shard, shard-id 1).
func TestRebalance_CLI_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testRebalanceLifecycleOverProtocol(t, node, proto)
		})
	}
}

func testRebalanceLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	// Seed a little data so `rebalance stats`/`report` report non-zero key
	// counts, not just structurally valid zero values.
	key := "rebalancecli/" + proto.name + "/seed"
	out, err := runCLIProto(t, node, proto, "kv", "put", key, "seed-value")
	if err != nil {
		t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, key, err, out)
	}

	// --- rebalance stats --shard 1 ---------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "rebalance", "stats", "--shard", "1")
	if err != nil {
		t.Fatalf("[%s] rebalance stats --shard 1: %v\noutput: %s", proto.name, err, out)
	}
	var stats rebalanceShardStatsDisplay
	if jerr := json.Unmarshal([]byte(out), &stats); jerr != nil {
		t.Fatalf("[%s] decode `rebalance stats` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if stats.ShardID != 1 {
		t.Fatalf("[%s] rebalance stats: ShardID=%d, want 1", proto.name, stats.ShardID)
	}
	if stats.ReplicaCount < 1 {
		t.Fatalf("[%s] rebalance stats: ReplicaCount=%d, want >=1", proto.name, stats.ReplicaCount)
	}
	if stats.KeyCount < 1 {
		t.Fatalf("[%s] rebalance stats: KeyCount=%d, want >=1 after seeding %s", proto.name, stats.KeyCount, key)
	}

	// --- rebalance stats requires --shard ---------------------------------
	out, err = runCLIProto(t, node, proto, "rebalance", "stats")
	if err == nil {
		t.Fatalf("[%s] rebalance stats (missing --shard) unexpectedly succeeded, output: %s", proto.name, out)
	}

	// --- rebalance stats: unknown shard ------------------------------------
	out, err = runCLIProto(t, node, proto, "rebalance", "stats", "--shard", "999999")
	if err == nil {
		t.Fatalf("[%s] rebalance stats --shard 999999 unexpectedly succeeded, output: %s", proto.name, out)
	}

	// --- rebalance report ----------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "rebalance", "report")
	if err != nil {
		t.Fatalf("[%s] rebalance report: %v\noutput: %s", proto.name, err, out)
	}
	var report rebalanceReportDisplay
	if jerr := json.Unmarshal([]byte(out), &report); jerr != nil {
		t.Fatalf("[%s] decode `rebalance report` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(report.Nodes) != 1 {
		t.Fatalf("[%s] rebalance report: len(Nodes)=%d, want 1 (single-node harness), report=%+v", proto.name, len(report.Nodes), report)
	}
	if report.Nodes[0].ShardCount < 1 {
		t.Fatalf("[%s] rebalance report: node ShardCount=%d, want >=1", proto.name, report.Nodes[0].ShardCount)
	}
	if report.ImbalanceScore != 0 {
		t.Fatalf("[%s] rebalance report: ImbalanceScore=%v, want 0 for a single-node cluster", proto.name, report.ImbalanceScore)
	}

	// --- rebalance plan (default flags) ------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "rebalance", "plan")
	if err != nil {
		t.Fatalf("[%s] rebalance plan: %v\noutput: %s", proto.name, err, out)
	}
	var plan rebalancePlanDisplay
	if jerr := json.Unmarshal([]byte(out), &plan); jerr != nil {
		t.Fatalf("[%s] decode `rebalance plan` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(plan.Moves) != 0 {
		t.Fatalf("[%s] rebalance plan: len(Moves)=%d, want 0 for a single-node cluster (nothing to balance against), plan=%+v",
			proto.name, len(plan.Moves), plan)
	}
	if plan.CurrentImbalance != 0 {
		t.Fatalf("[%s] rebalance plan: CurrentImbalance=%v, want 0", proto.name, plan.CurrentImbalance)
	}

	// --- rebalance plan flag permutations (max-moves, dry-run, target-balance) --
	out, err = runCLIProto(t, node, proto, "--output", "json", "rebalance", "plan",
		"--max-moves", "5", "--dry-run", "--target-balance", "10")
	if err != nil {
		t.Fatalf("[%s] rebalance plan (max-moves/dry-run/target-balance): %v\noutput: %s", proto.name, err, out)
	}
	var plan2 rebalancePlanDisplay
	if jerr := json.Unmarshal([]byte(out), &plan2); jerr != nil {
		t.Fatalf("[%s] decode `rebalance plan` (flags) JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(plan2.Moves) != 0 {
		t.Fatalf("[%s] rebalance plan (flags): len(Moves)=%d, want 0 for a single-node cluster, plan=%+v", proto.name, len(plan2.Moves), plan2)
	}

	// --- rebalance execute (reads the 0-move plan JSON from stdin) --------
	planJSON, jerr := json.Marshal(plan)
	if jerr != nil {
		t.Fatalf("[%s] marshal plan for execute: %v", proto.name, jerr)
	}
	out, err = runCLIProtoStdin(t, node, proto, string(planJSON), "--output", "json", "rebalance", "execute")
	if err != nil {
		t.Fatalf("[%s] rebalance execute: %v\noutput: %s", proto.name, err, out)
	}
	var result rebalanceResultDisplay
	if jerr := json.Unmarshal([]byte(out), &result); jerr != nil {
		t.Fatalf("[%s] decode `rebalance execute` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if result.MovesCompleted != 0 || result.MovesFailed != 0 {
		t.Fatalf("[%s] rebalance execute: MovesCompleted=%d MovesFailed=%d, want 0/0 for a 0-move plan",
			proto.name, result.MovesCompleted, result.MovesFailed)
	}

	// --- rebalance execute: malformed stdin JSON ----------------------------
	out, err = runCLIProtoStdin(t, node, proto, "not-json", "rebalance", "execute")
	if err == nil {
		t.Fatalf("[%s] rebalance execute (malformed stdin) unexpectedly succeeded, output: %s", proto.name, out)
	}
}

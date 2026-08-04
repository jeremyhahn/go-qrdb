// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Wire-format mirrors
// ---------------------------------------------------------------------------

// rebalanceShardStats mirrors the JSON shape of transport.ShardStatsResponse
// for integration test parsing.
type rebalanceShardStats struct {
	ShardID      uint64 `json:"shard_id"`
	KeyCount     uint64 `json:"key_count"`
	ByteCount    uint64 `json:"byte_count"`
	LeaderID     uint64 `json:"leader_id"`
	ReplicaCount int    `json:"replica_count"`
}

// rebalanceMove mirrors the JSON shape of transport.RebalanceMove.
type rebalanceMove struct {
	ShardID        uint64 `json:"shard_id"`
	FromNode       string `json:"from_node"`
	ToNode         string `json:"to_node"`
	EstimatedBytes uint64 `json:"estimated_bytes"`
}

// rebalancePlan mirrors the JSON shape of transport.RebalancePlan.
type rebalancePlan struct {
	Moves             []rebalanceMove `json:"moves"`
	EstimatedDuration int64           `json:"estimated_duration"`
	CurrentImbalance  float64         `json:"current_imbalance"`
}

// rebalanceExecResult mirrors the JSON shape of transport.RebalanceResult.
type rebalanceExecResult struct {
	MovesCompleted int     `json:"moves_completed"`
	MovesFailed    int     `json:"moves_failed"`
	Duration       int64   `json:"duration"`
	FinalImbalance float64 `json:"final_imbalance"`
}

// rebalanceNodeLoad mirrors the JSON shape of transport.NodeLoad.
type rebalanceNodeLoad struct {
	NodeID      string `json:"node_id"`
	Address     string `json:"address"`
	ShardCount  int    `json:"shard_count"`
	TotalKeys   uint64 `json:"total_keys"`
	TotalBytes  uint64 `json:"total_bytes"`
	LeaderCount int    `json:"leader_count"`
}

// rebalanceImbalanceReport mirrors the JSON shape of transport.ImbalanceReport.
type rebalanceImbalanceReport struct {
	Nodes          []rebalanceNodeLoad `json:"nodes"`
	ImbalanceScore float64             `json:"imbalance_score"`
	Hotspots       []string            `json:"hotspots"`
}

// parseRebalanceShardStats unmarshals a ShardStatsResponse JSON blob from
// CLI stdout.
func parseRebalanceShardStats(t *testing.T, proto ProtocolType, stdout string) *rebalanceShardStats {
	t.Helper()
	var stats rebalanceShardStats
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &stats); err != nil {
		t.Fatalf("[%s] failed to parse ShardStatsResponse JSON: %v\noutput: %s", proto, err, stdout)
	}
	return &stats
}

// parseRebalancePlan unmarshals a RebalancePlan JSON blob from CLI stdout.
func parseRebalancePlan(t *testing.T, proto ProtocolType, stdout string) *rebalancePlan {
	t.Helper()
	var plan rebalancePlan
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &plan); err != nil {
		t.Fatalf("[%s] failed to parse RebalancePlan JSON: %v\noutput: %s", proto, err, stdout)
	}
	return &plan
}

// parseRebalanceExecResult unmarshals a RebalanceResult JSON blob from CLI stdout.
func parseRebalanceExecResult(t *testing.T, proto ProtocolType, stdout string) *rebalanceExecResult {
	t.Helper()
	var result rebalanceExecResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
		t.Fatalf("[%s] failed to parse RebalanceResult JSON: %v\noutput: %s", proto, err, stdout)
	}
	return &result
}

// parseRebalanceImbalanceReport unmarshals an ImbalanceReport JSON blob from
// CLI stdout.
func parseRebalanceImbalanceReport(t *testing.T, proto ProtocolType, stdout string) *rebalanceImbalanceReport {
	t.Helper()
	var report rebalanceImbalanceReport
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &report); err != nil {
		t.Fatalf("[%s] failed to parse ImbalanceReport JSON: %v\noutput: %s", proto, err, stdout)
	}
	return &report
}

// ---------------------------------------------------------------------------
// rebalance stats
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolRebalanceStatsKnownShard tests "rebalance stats
// --shard 1" (the sole shard in config.test.yaml's single-node topology)
// across all protocols, in both table and JSON output.
func TestCLIMultiProtocolRebalanceStatsKnownShard(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			// Table output.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "rebalance", "stats", "--shard", "1")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] rebalance stats failed: %v", proto, err)
			}
			assertContains(t, stdout, "ShardID:", fmt.Sprintf("[%s] rebalance stats should show ShardID:", proto))
			assertContains(t, stdout, "ReplicaCount:", fmt.Sprintf("[%s] rebalance stats should show ReplicaCount:", proto))

			// JSON output.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "--output", "json", "rebalance", "stats", "--shard", "1")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] rebalance stats --output json failed: %v", proto, err)
			}
			stats := parseRebalanceShardStats(t, proto, stdout)
			assertEqual(t, uint64(1), stats.ShardID, fmt.Sprintf("[%s] shard_id mismatch", proto))
			assertTrue(t, stats.ReplicaCount >= 1, fmt.Sprintf("[%s] replica_count should be >= 1", proto))

			t.Logf("[%s] rebalance stats for shard 1: %+v", proto, stats)
		})
	}
}

// TestCLIMultiProtocolRebalanceStatsUnknownShard tests "rebalance stats"
// against a shard ID that was never loaded on the host. Per
// go-quicraft's Host.GetLeaderID (see TestGetLeaderID_ShardNotFound in
// go-quicraft/pkg/host_test.go), an unknown shard ID returns
// ErrShardNotFound, which RebalanceService.GetShardStats wraps in a typed
// ShardStatsError. This is the well-defined, code-verified error path for
// an unknown shard, not a guess.
func TestCLIMultiProtocolRebalanceStatsUnknownShard(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			_, _, err := execCLIWithProtocol(t, cfg, proto, "rebalance", "stats", "--shard", "999999999")
			assertError(t, err, fmt.Sprintf("[%s] rebalance stats for an unknown shard should error", proto))
		})
	}
}

// ---------------------------------------------------------------------------
// rebalance plan
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolRebalancePlanPermutations tests "rebalance plan"
// across default, --dry-run, --max-moves, and --target-balance flag
// permutations, in both table and JSON output.
//
// This integration deployment is single-node: every shard's replica list
// resolves to exactly one distinct Raft address, so
// RebalanceService.CreateRebalancePlan's "len(report.Nodes) < 2" early
// return (pkg/service/rebalance_service.go) always fires before any of the
// requested flags are consulted, producing a plan with zero moves and
// CurrentImbalance 0 regardless of flags. This is verified from the service
// implementation, not assumed.
func TestCLIMultiProtocolRebalancePlanPermutations(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	planArgs := []struct {
		name string
		args []string
	}{
		{"default", nil},
		{"dry-run", []string{"--dry-run"}},
		{"max-moves", []string{"--max-moves", "5"}},
		{"target-balance", []string{"--target-balance", "10.5"}},
	}

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			for _, pa := range planArgs {
				t.Run(pa.name, func(t *testing.T) {
					args := append([]string{"--output", "json", "rebalance", "plan"}, pa.args...)
					stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, args...)
					if err != nil {
						t.Logf("stdout: %s", stdout)
						t.Logf("stderr: %s", stderr)
						t.Fatalf("[%s/%s] rebalance plan failed: %v", proto, pa.name, err)
					}
					plan := parseRebalancePlan(t, proto, stdout)
					assertEqual(t, 0, len(plan.Moves),
						fmt.Sprintf("[%s/%s] single-node plan should have zero moves", proto, pa.name))
					assertEqual(t, float64(0), plan.CurrentImbalance,
						fmt.Sprintf("[%s/%s] single-node imbalance should be 0", proto, pa.name))
				})
			}

			// Table (default) output.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "rebalance", "plan")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] rebalance plan (table) failed: %v", proto, err)
			}
			assertContains(t, stdout, "CurrentImbalance:",
				fmt.Sprintf("[%s] rebalance plan table output should show CurrentImbalance:", proto))
			assertContains(t, stdout, "Moves:",
				fmt.Sprintf("[%s] rebalance plan table output should show Moves:", proto))
		})
	}
}

// ---------------------------------------------------------------------------
// rebalance execute
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolRebalanceExecuteFromPlan pipes the output of
// "rebalance plan --output json" directly into "rebalance execute" via
// stdin, mirroring the documented pipeline workflow (see
// cmd/qrdb/rebalance.go newRebalanceExecuteCmd doc comment). On this
// single-node deployment the plan always has zero moves (see
// TestCLIMultiProtocolRebalancePlanPermutations), so execution trivially
// completes with zero completed and zero failed moves.
func TestCLIMultiProtocolRebalanceExecuteFromPlan(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			planJSON, stderr, err := execCLIWithProtocol(t, cfg, proto, "--output", "json", "rebalance", "plan")
			if err != nil {
				t.Logf("stdout: %s", planJSON)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] rebalance plan failed: %v", proto, err)
			}

			stdout, stderr, err := execCLIWithStdin(t, cfg, proto, planJSON, "--output", "json", "rebalance", "execute")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] rebalance execute failed: %v", proto, err)
			}
			result := parseRebalanceExecResult(t, proto, stdout)
			assertEqual(t, 0, result.MovesCompleted,
				fmt.Sprintf("[%s] empty single-node plan should complete zero moves", proto))
			assertEqual(t, 0, result.MovesFailed,
				fmt.Sprintf("[%s] empty single-node plan should fail zero moves", proto))
		})
	}
}

// ---------------------------------------------------------------------------
// rebalance report
// ---------------------------------------------------------------------------

// TestCLIMultiProtocolRebalanceReport tests "rebalance report" across all
// protocols, in both table and JSON output. On this single-node deployment
// RebalanceService.calculateImbalanceScore short-circuits to 0.0 whenever
// len(nodes) <= 1 (pkg/service/rebalance_service.go), which always holds
// here since every shard resolves to the same single Raft address.
func TestCLIMultiProtocolRebalanceReport(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			// Table output.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "rebalance", "report")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] rebalance report failed: %v", proto, err)
			}
			assertContains(t, stdout, "ImbalanceScore:",
				fmt.Sprintf("[%s] rebalance report should show ImbalanceScore:", proto))

			// JSON output.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "--output", "json", "rebalance", "report")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] rebalance report --output json failed: %v", proto, err)
			}
			report := parseRebalanceImbalanceReport(t, proto, stdout)
			assertTrue(t, len(report.Nodes) >= 1, fmt.Sprintf("[%s] rebalance report should list at least one node", proto))
			assertEqual(t, float64(0), report.ImbalanceScore,
				fmt.Sprintf("[%s] single-node imbalance score should be 0", proto))

			t.Logf("[%s] rebalance report: %+v", proto, report)
		})
	}
}

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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// RebalanceTransportUnsupportedError is returned when the active transport
// does not implement transport.RebalanceClient.
type RebalanceTransportUnsupportedError struct{}

// Error returns a human-readable description.
func (e *RebalanceTransportUnsupportedError) Error() string {
	return "rebalance: active transport does not implement the rebalance service; use a network transport (rest, grpc, quic, mcp, unix)"
}

// rebalanceClient resolves a RebalanceClient from c. Returns
// RebalanceTransportUnsupportedError if c is nil or its active transport
// does not satisfy transport.RebalanceClient, mirroring the
// userAuthFactorClient nil-guard pattern so the CLI never panics on an
// interface conversion.
func rebalanceClient(c *sdk.Client) (transport.RebalanceClient, error) {
	if c == nil {
		return nil, &RebalanceTransportUnsupportedError{}
	}
	rc := c.Rebalance()
	if rc == nil {
		return nil, &RebalanceTransportUnsupportedError{}
	}
	return rc, nil
}

// newRebalanceCmd creates the "rebalance" command group with all shard
// rebalancing subcommands.
func newRebalanceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rebalance",
		Short: "Shard rebalancing and load distribution",
		Long:  "Inspect shard statistics, generate rebalance plans, execute moves, and report cluster imbalance.",
	}

	cmd.AddCommand(
		newRebalanceStatsCmd(),
		newRebalancePlanCmd(),
		newRebalanceExecuteCmd(),
		newRebalanceReportCmd(),
	)

	return cmd
}

// newRebalanceStatsCmd creates the "rebalance stats" subcommand.
func newRebalanceStatsCmd() *cobra.Command {
	var shardID uint64

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show statistics for a shard",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := rebalanceClient(client)
			if err != nil {
				return err
			}
			stats, err := rc.GetShardStats(cmd.Context(), shardID)
			if err != nil {
				return err
			}
			return printShardStats(stats)
		},
	}

	cmd.Flags().Uint64Var(&shardID, "shard", 0, "Shard ID to inspect")
	_ = cmd.MarkFlagRequired("shard")

	return cmd
}

// newRebalancePlanCmd creates the "rebalance plan" subcommand.
func newRebalancePlanCmd() *cobra.Command {
	var (
		maxMoves      int
		dryRun        bool
		targetBalance float64
	)

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Generate a rebalance plan",
		Long:  "Analyze shard distribution across nodes and generate an optimal move plan.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.RebalancePlanRequest{
				MaxMoves:      maxMoves,
				DryRun:        dryRun,
				TargetBalance: targetBalance,
			}

			rc, err := rebalanceClient(client)
			if err != nil {
				return err
			}
			plan, err := rc.CreateRebalancePlan(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printRebalancePlan(plan)
		},
	}

	cmd.Flags().IntVar(&maxMoves, "max-moves", 0, "Maximum number of shard moves in the plan (0 = no limit)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Generate the plan without executing it")
	cmd.Flags().Float64Var(&targetBalance, "target-balance", 0, "Target balance percentage 0-100 (0 = perfectly balanced)")

	return cmd
}

// newRebalanceExecuteCmd creates the "rebalance execute" subcommand. The
// rebalance plan is read from stdin as JSON, enabling it to be piped
// directly from "rebalance plan --output json".
func newRebalanceExecuteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "execute",
		Short: "Execute a rebalance plan",
		Long:  "Read a rebalance plan from stdin (JSON) and execute it against the cluster.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return &FileReadError{Path: "stdin", Err: err}
			}

			var plan transport.RebalancePlan
			if err := json.Unmarshal(data, &plan); err != nil {
				return &InvalidArgError{Arg: "stdin", Reason: "failed to decode rebalance plan JSON: " + err.Error()}
			}

			rc, err := rebalanceClient(client)
			if err != nil {
				return err
			}
			result, err := rc.ExecuteRebalance(cmd.Context(), &plan)
			if err != nil {
				return err
			}

			if printErr := printRebalanceResult(result); printErr != nil {
				return printErr
			}
			if result.MovesFailed > 0 {
				// The proposal itself succeeded and per-move detail was
				// already printed above; a non-nil error here only serves
				// to give scripted/automation callers a non-zero exit code
				// for a partial rebalance.
				return &RebalancePartialFailureError{FailedCount: result.MovesFailed}
			}
			return nil
		},
	}
}

// newRebalanceReportCmd creates the "rebalance report" subcommand.
func newRebalanceReportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "report",
		Short: "Show cluster imbalance report",
		Long:  "Display per-node load distribution and identify cluster hotspots.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := rebalanceClient(client)
			if err != nil {
				return err
			}
			report, err := rc.GetImbalanceReport(cmd.Context())
			if err != nil {
				return err
			}
			return printImbalanceReport(report)
		},
	}
}

// ---------------------------------------------------------------------------
// Output helpers
// ---------------------------------------------------------------------------

// printShardStats prints a ShardStatsResponse in the current output format.
func printShardStats(s *transport.ShardStatsResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(s)
	case "yaml":
		return printYAML(s)
	default:
		_, _ = fmt.Fprintf(outputWriter, "ShardID:      %d\n", s.ShardID)
		_, _ = fmt.Fprintf(outputWriter, "KeyCount:     %d\n", s.KeyCount)
		_, _ = fmt.Fprintf(outputWriter, "ByteCount:    %s\n", formatBytes(int64(s.ByteCount)))
		_, _ = fmt.Fprintf(outputWriter, "LeaderID:     %d\n", s.LeaderID)
		_, _ = fmt.Fprintf(outputWriter, "ReplicaCount: %d\n", s.ReplicaCount)
		return nil
	}
}

// printRebalancePlan prints a RebalancePlan in the current output format.
func printRebalancePlan(plan *transport.RebalancePlan) error {
	switch outputFormat {
	case "json":
		return printJSON(plan)
	case "yaml":
		return printYAML(plan)
	default:
		_, _ = fmt.Fprintf(outputWriter, "CurrentImbalance:  %.4f\n", plan.CurrentImbalance)
		_, _ = fmt.Fprintf(outputWriter, "EstimatedDuration: %s\n", plan.EstimatedDuration)
		_, _ = fmt.Fprintf(outputWriter, "Moves:             %d\n", len(plan.Moves))
		if len(plan.Moves) > 0 {
			_, _ = fmt.Fprintln(outputWriter)
			headers := []string{"SHARD", "FROM", "TO", "ESTIMATED_SIZE"}
			rows := make([][]string, len(plan.Moves))
			for i, m := range plan.Moves {
				rows[i] = []string{
					strconv.FormatUint(m.ShardID, 10),
					m.FromNode,
					m.ToNode,
					formatBytes(int64(m.EstimatedBytes)),
				}
			}
			printTable(headers, rows)
		}
		return nil
	}
}

// printRebalanceResult prints a RebalanceResult in the current output format.
func printRebalanceResult(result *transport.RebalanceResult) error {
	switch outputFormat {
	case "json":
		return printJSON(result)
	case "yaml":
		return printYAML(result)
	default:
		_, _ = fmt.Fprintf(outputWriter, "MovesCompleted: %d\n", result.MovesCompleted)
		_, _ = fmt.Fprintf(outputWriter, "MovesFailed:    %d\n", result.MovesFailed)
		_, _ = fmt.Fprintf(outputWriter, "Duration:       %s\n", result.Duration)
		_, _ = fmt.Fprintf(outputWriter, "FinalImbalance: %.4f\n", result.FinalImbalance)
		return nil
	}
}

// printImbalanceReport prints an ImbalanceReport in the current output format.
func printImbalanceReport(report *transport.ImbalanceReport) error {
	switch outputFormat {
	case "json":
		return printJSON(report)
	case "yaml":
		return printYAML(report)
	default:
		_, _ = fmt.Fprintf(outputWriter, "ImbalanceScore: %.4f\n", report.ImbalanceScore)
		if len(report.Hotspots) > 0 {
			_, _ = fmt.Fprintf(outputWriter, "Hotspots:       %s\n", strings.Join(report.Hotspots, ", "))
		}
		if len(report.Nodes) > 0 {
			_, _ = fmt.Fprintln(outputWriter)
			headers := []string{"NODE_ID", "ADDRESS", "SHARDS", "LEADERS", "TOTAL_KEYS", "TOTAL_BYTES"}
			rows := make([][]string, len(report.Nodes))
			for i, n := range report.Nodes {
				rows[i] = []string{
					n.NodeID,
					n.Address,
					strconv.Itoa(n.ShardCount),
					strconv.Itoa(n.LeaderCount),
					strconv.FormatUint(n.TotalKeys, 10),
					formatBytes(int64(n.TotalBytes)),
				}
			}
			printTable(headers, rows)
		}
		return nil
	}
}

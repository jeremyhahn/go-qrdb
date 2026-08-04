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
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// newAdminCmd returns the admin parent command with subcommands for
// cluster health, status, peer membership, and operational tasks.
func newAdminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Cluster administration commands",
		Long:  "Control-plane operations for health monitoring, membership management, and operational tasks.",
	}

	cmd.AddCommand(
		newAdminHealthCmd(),
		newAdminStatusCmd(),
		newAdminPeersCmd(),
		newAdminMemberCmd(),
		newAdminSnapshotCmd(),
		newAdminDrainCmd(),
		newAdminTopologyCmd(),
		newAdminCapabilitiesCmd(),
	)

	return cmd
}

// newAdminHealthCmd returns the admin health subcommand.
func newAdminHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check node health status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Admin().Health(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "Status: %s\nLeader: %t\n", resp.Status, resp.IsLeader)
				return nil
			}
		},
	}
}

// newAdminStatusCmd returns the admin status subcommand.
func newAdminStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show detailed node status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Admin().Status(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "NodeID:      %s\n", resp.NodeID)
				_, _ = fmt.Fprintf(outputWriter, "RaftAddress: %s\n", resp.RaftAddress)
				_, _ = fmt.Fprintf(outputWriter, "IsLeader:    %t\n", resp.IsLeader)
				_, _ = fmt.Fprintf(outputWriter, "LeaderID:    %s\n", strconv.FormatUint(resp.LeaderID, 10))
				_, _ = fmt.Fprintf(outputWriter, "ShardCount:  %d\n", resp.ShardCount)
				_, _ = fmt.Fprintf(outputWriter, "IsDraining:  %t\n", resp.IsDraining)
				return nil
			}
		},
	}
}

// newAdminPeersCmd returns the admin peers subcommand.
func newAdminPeersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "peers",
		Short: "List shard peer membership",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Admin().Peers(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"SHARD_ID", "REPLICA_ID", "ADDRESS", "IS_LEADER"}
				var rows [][]string
				for _, shard := range resp.Shards {
					for _, peer := range shard.Peers {
						rows = append(rows, []string{
							strconv.FormatUint(shard.ShardID, 10),
							strconv.FormatUint(peer.ReplicaID, 10),
							peer.Address,
							strconv.FormatBool(peer.IsLeader),
						})
					}
				}
				printTable(headers, rows)
				return nil
			}
		},
	}
}

// newAdminMemberCmd returns the admin member parent command with
// add and remove subcommands.
func newAdminMemberCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "member",
		Short: "Manage shard membership",
	}

	cmd.AddCommand(
		newAdminMemberAddCmd(),
		newAdminMemberRemoveCmd(),
	)

	return cmd
}

// newAdminMemberAddCmd returns the admin member add subcommand.
func newAdminMemberAddCmd() *cobra.Command {
	var shardID uint64
	var replicaID uint64

	cmd := &cobra.Command{
		Use:   "add <address>",
		Short: "Add a replica to a shard",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			address := args[0]

			if err := client.Admin().AddMember(cmd.Context(), shardID, replicaID, address); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Shard ID")
	cmd.Flags().Uint64Var(&replicaID, "replica-id", 0, "Replica ID")
	_ = cmd.MarkFlagRequired("shard-id")
	_ = cmd.MarkFlagRequired("replica-id")

	return cmd
}

// newAdminMemberRemoveCmd returns the admin member remove subcommand.
func newAdminMemberRemoveCmd() *cobra.Command {
	var shardID uint64

	cmd := &cobra.Command{
		Use:   "remove <replica-id>",
		Short: "Remove a replica from a shard",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			replicaID, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return &InvalidReplicaIDError{Input: args[0]}
			}

			if err := client.Admin().RemoveMember(cmd.Context(), shardID, replicaID); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Shard ID")
	_ = cmd.MarkFlagRequired("shard-id")

	return cmd
}

// newAdminSnapshotCmd returns the admin snapshot subcommand.
func newAdminSnapshotCmd() *cobra.Command {
	var shardID uint64

	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Request a shard snapshot",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := client.Admin().RequestSnapshot(cmd.Context(), shardID); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Shard ID")
	_ = cmd.MarkFlagRequired("shard-id")

	return cmd
}

// newAdminDrainCmd returns the admin drain subcommand.
func newAdminDrainCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "drain",
		Short: "Transition node into drain mode",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := client.Admin().Drain(cmd.Context()); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}
}

// newAdminTopologyCmd returns the admin topology subcommand.
func newAdminTopologyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "topology",
		Short: "Show the full cluster topology as seen from this node",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Admin().GetTopology(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "NodeID:      %s\n", resp.NodeID)
				_, _ = fmt.Fprintf(outputWriter, "RaftAddress: %s\n", resp.RaftAddress)
				_, _ = fmt.Fprintf(outputWriter, "ShardCount:  %d\n\n", resp.ShardCount)

				headers := []string{"SHARD_ID", "REPLICA_ID", "IS_LEADER", "LEADER_ID", "NODES"}
				rows := make([][]string, len(resp.Shards))
				for i, shard := range resp.Shards {
					rows[i] = []string{
						strconv.FormatUint(shard.ShardID, 10),
						strconv.FormatUint(shard.ReplicaID, 10),
						strconv.FormatBool(shard.IsLeader),
						strconv.FormatUint(shard.LeaderID, 10),
						formatTopologyNodes(shard.Nodes),
					}
				}
				printTable(headers, rows)
				return nil
			}
		},
	}
}

// newAdminCapabilitiesCmd returns the admin capabilities subcommand.
func newAdminCapabilitiesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "capabilities",
		Short: "Show the compile-time feature set of the connected node",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Admin().Capabilities(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"FEATURE", "ENABLED"}
				rows := [][]string{
					{"kv", strconv.FormatBool(resp.KV)},
					{"mvcc", strconv.FormatBool(resp.MVCC)},
					{"transaction", strconv.FormatBool(resp.Transaction)},
					{"batch", strconv.FormatBool(resp.Batch)},
					{"watch", strconv.FormatBool(resp.Watch)},
					{"lease", strconv.FormatBool(resp.Lease)},
					{"index", strconv.FormatBool(resp.Index)},
					{"timeSeries", strconv.FormatBool(resp.TimeSeries)},
					{"vector", strconv.FormatBool(resp.Vector)},
				}
				printTable(headers, rows)
				return nil
			}
		},
	}
}

// formatTopologyNodes renders a shard's replicaID->address membership map
// as a comma-separated "replicaID=address" list for table output, sorted
// by replica ID for deterministic display.
func formatTopologyNodes(nodes map[uint64]string) string {
	if len(nodes) == 0 {
		return "-"
	}
	ids := make([]uint64, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatUint(id, 10) + "=" + nodes[id]
	}
	return strings.Join(parts, ",")
}

// InvalidReplicaIDError is returned when a replica ID argument cannot
// be parsed as a uint64.
type InvalidReplicaIDError struct {
	Input string
}

// Error returns a human-readable description.
func (e *InvalidReplicaIDError) Error() string {
	return "invalid replica ID: " + e.Input + ": must be a positive integer"
}

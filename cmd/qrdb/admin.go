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
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
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
		newAdminReplicaStatesCmd(),
		newAdminCapabilitiesCmd(),
		newAdminNodeCompactionCmd(),
		newAdminUpdateNodeCompactionCmd(),
		newAdminJoinTokenCmd(),
		newAdminJoinRequestCmd(),
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
		newAdminMemberAddObserverCmd(),
		newAdminMemberRemoveCmd(),
		newAdminMemberPromoteCmd(),
	)

	return cmd
}

// newAdminMemberAddCmd returns the admin member add subcommand.
func newAdminMemberAddCmd() *cobra.Command {
	var shardID uint64
	var replicaID uint64
	var identity string

	cmd := &cobra.Command{
		Use:   "add <address>",
		Short: "Add a replica to a shard",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			address := args[0]

			if err := client.Admin().AddMember(cmd.Context(), shardID, replicaID, address, identity); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Shard ID")
	cmd.Flags().Uint64Var(&replicaID, "replica-id", 0, "Replica ID")
	cmd.Flags().StringVar(&identity, "identity", "", "Raft member identity (spki:<hex>, from qrdbd node-identity --cert); required for a node no committed membership names")
	_ = cmd.MarkFlagRequired("shard-id")
	_ = cmd.MarkFlagRequired("replica-id")

	return cmd
}

// ObserverMemberAddUnsupportedError is returned when the connected
// transport does not implement transport.ObserverMemberAdder.
type ObserverMemberAddUnsupportedError struct{}

// Error returns a human-readable description.
func (e *ObserverMemberAddUnsupportedError) Error() string {
	return "admin member add-observer: the connected transport does not support adding observer members"
}

// newAdminMemberAddObserverCmd returns the admin member add-observer
// subcommand: adds a replica as a non-voting observer rather than a voter.
// Promote it to a voter later with `qrdb admin member promote`.
func newAdminMemberAddObserverCmd() *cobra.Command {
	var shardID uint64
	var replicaID uint64
	var identity string

	cmd := &cobra.Command{
		Use:   "add-observer <address>",
		Short: "Add a replica to a shard as a non-voting observer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			address := args[0]

			adder, ok := client.Admin().(transport.ObserverMemberAdder)
			if !ok {
				return &ObserverMemberAddUnsupportedError{}
			}

			if err := adder.AddObserverMember(cmd.Context(), shardID, replicaID, address, identity); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Shard ID")
	cmd.Flags().Uint64Var(&replicaID, "replica-id", 0, "Replica ID")
	cmd.Flags().StringVar(&identity, "identity", "", "Raft member identity (spki:<hex>, from qrdbd node-identity --cert); required for a node no committed membership names")
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

// newAdminMemberPromoteCmd returns the admin member promote subcommand.
func newAdminMemberPromoteCmd() *cobra.Command {
	var shardID uint64

	cmd := &cobra.Command{
		Use:   "promote <replica-id>",
		Short: "Promote a committed observer to a voter",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			replicaID, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return &InvalidReplicaIDError{Input: args[0]}
			}

			if err := client.Admin().PromoteMember(cmd.Context(), shardID, replicaID); err != nil {
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

func newAdminReplicaStatesCmd() *cobra.Command {
	var shardID uint64
	cmd := &cobra.Command{
		Use:   "replica-states",
		Short: "Show the applied index and state-machine hash of every replica on this node",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Admin().ReplicaStates(cmd.Context(), shardID)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"SHARD", "REPLICA", "APPLIED INDEX", "HASH"}
				rows := make([][]string, len(resp.Replicas))
				for i, rs := range resp.Replicas {
					rows[i] = []string{
						strconv.FormatUint(rs.ShardID, 10),
						strconv.FormatUint(rs.ReplicaID, 10),
						strconv.FormatUint(rs.AppliedIndex, 10),
						fmt.Sprintf("%016x", rs.Hash),
					}
				}
				printTable(headers, rows)
				return nil
			}
		},
	}
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "report only this shard (0 reports every replica on the node)")
	return cmd
}

// newAdminCapabilitiesCmd returns the admin capabilities subcommand.
func newAdminCapabilitiesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "capabilities",
		Short: "Show the effective feature set of the connected node (build tags x run mode x config)",
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
					{"mode", resp.Mode},
					{"kv", strconv.FormatBool(resp.KV)},
					{"mvcc", strconv.FormatBool(resp.MVCC)},
					{"transaction", strconv.FormatBool(resp.Transaction)},
					{"batch", strconv.FormatBool(resp.Batch)},
					{"watch", strconv.FormatBool(resp.Watch)},
					{"lease", strconv.FormatBool(resp.Lease)},
					{"lock", strconv.FormatBool(resp.Lock)},
					{"index", strconv.FormatBool(resp.Index)},
					{"cluster", strconv.FormatBool(resp.Cluster)},
					{"backup", strconv.FormatBool(resp.Backup)},
					{"rebalance", strconv.FormatBool(resp.Rebalance)},
					{"timeSeries", strconv.FormatBool(resp.TimeSeries)},
					{"vector", strconv.FormatBool(resp.Vector)},
					{"sql", strconv.FormatBool(resp.SQL)},
					{"mysql", strconv.FormatBool(resp.MySQL)},
					{"postgresql", strconv.FormatBool(resp.PostgreSQL)},
					{"etcd", strconv.FormatBool(resp.Etcd)},
					{"fts", strconv.FormatBool(resp.FTS)},
				}
				for _, gap := range resp.UnencryptedAtRest {
					rows = append(rows, []string{"unencryptedAtRest", gap})
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

// newAdminJoinTokenCmd returns the admin join-token parent command.
func newAdminJoinTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "join-token",
		Short: "Manage member-side CSR join tokens",
		Long:  "Mint single-use, identity-bound tokens admitting a new node to the cluster (permission cluster:join_token).",
	}

	cmd.AddCommand(newAdminJoinTokenMintCmd())
	cmd.AddCommand(newAdminJoinTokenRetireCmd())

	return cmd
}

// JoinTokenMintUnsupportedError is returned when the connected transport
// does not implement transport.ClusterJoinTokenMinter: see each SDK
// transport package's own doc comment for which do.
type JoinTokenMintUnsupportedError struct{}

// Error returns a human-readable description.
func (e *JoinTokenMintUnsupportedError) Error() string {
	return "admin join-token mint: the connected transport does not support minting join tokens"
}

// newAdminJoinTokenMintCmd returns the admin join-token mint subcommand.
func newAdminJoinTokenMintCmd() *cobra.Command {
	var ttl time.Duration

	cmd := &cobra.Command{
		Use:   "mint <node-id>",
		Short: "Mint a single-use join token bound to a node identity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			nodeID := args[0]

			minter, ok := client.Admin().(transport.ClusterJoinTokenMinter)
			if !ok {
				return &JoinTokenMintUnsupportedError{}
			}

			resp, err := minter.MintJoinToken(cmd.Context(), &transport.MintJoinTokenRequest{
				NodeID:     nodeID,
				TTLSeconds: int64(ttl.Seconds()),
			})
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "Token:      %s\n", resp.Token)
				_, _ = fmt.Fprintf(outputWriter, "ExpiresAt:  %s\n", resp.ExpiresAt.Format(time.RFC3339))
				return nil
			}
		},
	}

	cmd.Flags().DurationVar(&ttl, "ttl", 0, "Token time-to-live (default: the server's default TTL)")

	return cmd
}

// JoinRequestReviewUnsupportedError is returned when the connected
// transport does not implement transport.JoinRequestReviewer.
type JoinRequestReviewUnsupportedError struct{}

// Error returns a human-readable description.
func (e *JoinRequestReviewUnsupportedError) Error() string {
	return "admin join-request: the connected transport does not support reviewing join requests"
}

// newAdminJoinRequestCmd returns the admin join-request parent command:
// lists, approves and rejects CSRs a joiner queued through the tokenless
// member-side CSR approval path (permission cluster:join_token).
func newAdminJoinRequestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "join-request",
		Short: "Review tokenless member-side CSR join requests",
		Long: "List, approve and reject CSRs a joiner queued by submitting no join token " +
			"(permission cluster:join_token).",
	}

	cmd.AddCommand(
		newAdminJoinRequestListCmd(),
		newAdminJoinRequestApproveCmd(),
		newAdminJoinRequestRejectCmd(),
	)

	return cmd
}

// newAdminJoinRequestListCmd returns the admin join-request list subcommand.
func newAdminJoinRequestListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List join requests awaiting a decision",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reviewer, ok := client.Admin().(transport.JoinRequestReviewer)
			if !ok {
				return &JoinRequestReviewUnsupportedError{}
			}

			pending, err := reviewer.ListPendingJoinRequests(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(pending)
			case "yaml":
				return printYAML(pending)
			default:
				if len(pending) == 0 {
					_, _ = fmt.Fprintln(outputWriter, "No join requests awaiting a decision.")
					return nil
				}
				for _, p := range pending {
					_, _ = fmt.Fprintf(outputWriter, "ID:           %s\n", p.ID)
					_, _ = fmt.Fprintf(outputWriter, "NodeURI:      %s\n", p.NodeURI)
					_, _ = fmt.Fprintf(outputWriter, "ReplicaID:    %d\n", p.ReplicaID)
					_, _ = fmt.Fprintf(outputWriter, "SubmittedAt:  %s\n", p.SubmittedAt.Format(time.RFC3339))
					_, _ = fmt.Fprintf(outputWriter, "ExpiresAt:    %s\n", p.ExpiresAt.Format(time.RFC3339))
					_, _ = fmt.Fprintln(outputWriter)
				}
				return nil
			}
		},
	}
}

// newAdminJoinRequestApproveCmd returns the admin join-request approve
// subcommand: binds a queued CSR to an operator-chosen node identity. The
// leaf is signed the next time the joiner's own retry reaches the join
// listener, not by this command.
func newAdminJoinRequestApproveCmd() *cobra.Command {
	var nodeID string

	cmd := &cobra.Command{
		Use:   "approve <id>",
		Short: "Bind a queued join request to a node identity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			reviewer, ok := client.Admin().(transport.JoinRequestReviewer)
			if !ok {
				return &JoinRequestReviewUnsupportedError{}
			}

			if err := reviewer.ApproveJoinRequest(cmd.Context(), id, nodeID); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&nodeID, "node-id", "", "The node identity to bind the eventual leaf to")
	_ = cmd.MarkFlagRequired("node-id")

	return cmd
}

// newAdminJoinRequestRejectCmd returns the admin join-request reject
// subcommand: records a terminal refusal of a queued CSR.
func newAdminJoinRequestRejectCmd() *cobra.Command {
	var reason string

	cmd := &cobra.Command{
		Use:   "reject <id>",
		Short: "Reject a queued join request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			reviewer, ok := client.Admin().(transport.JoinRequestReviewer)
			if !ok {
				return &JoinRequestReviewUnsupportedError{}
			}

			if err := reviewer.RejectJoinRequest(cmd.Context(), id, reason); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&reason, "reason", "", "An operator-supplied reason, recorded for audit")

	return cmd
}

// newAdminJoinTokenRetireCmd returns the admin join-token retire
// subcommand.
func newAdminJoinTokenRetireCmd() *cobra.Command {
	var reason string

	cmd := &cobra.Command{
		Use:   "retire <node-id>",
		Short: "Permanently refuse any future admission for a node identity",
		Long: "Permanently refuse any future admission for a node identity -- a fresh mint, or a " +
			"renewal of one whose admission has lapsed -- after a member is removed from the " +
			"cluster or its certificate must be treated as revoked (permission cluster:join_identity_retire).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			nodeID := args[0]

			if err := client.Admin().RetireJoinIdentity(cmd.Context(), nodeID, reason); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&reason, "reason", "", "Optional free-text operator note, carried for audit only")

	return cmd
}

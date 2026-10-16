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
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// StateHashUnsupportedError is returned when the connected transport does
// not implement transport.ReplicaStateHasher.
type StateHashUnsupportedError struct{}

// Error returns a human-readable description.
func (e *StateHashUnsupportedError) Error() string {
	return "admin state-hash: the connected transport does not support replica state hashes"
}

// newAdminStateHashCmd returns the admin state-hash subcommand: the applied
// index and state digest of every replica the connected node runs.
func newAdminStateHashCmd() *cobra.Command {
	var shards []string

	cmd := &cobra.Command{
		Use:   "state-hash",
		Short: "Show every local replica's applied index and state digest",
		Long: "Show, for every shard the connected node replicates, the replica's applied index and " +
			"the SHA-256 digest of its state at exactly that index, isolated tenants' shards " +
			"included. The node answers from its own state machines without a proposal; run it " +
			"against each node and compare: two replicas of one shard at the same applied index " +
			"must show the same hash. Requires admin:state_hash, which only the system tenant can hold.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			shardIDs, err := parseShardFlags(shards)
			if err != nil {
				return err
			}
			hasher, ok := client.Admin().(transport.ReplicaStateHasher)
			if !ok {
				return &StateHashUnsupportedError{}
			}
			resp, err := hasher.StateHashes(cmd.Context(), shardIDs)
			if err != nil {
				return err
			}
			return printStateHashes(resp)
		},
	}
	cmd.Flags().StringArrayVar(&shards, "shard", nil, "Only this shard ID (repeatable); default every shard")

	return cmd
}

// parseShardFlags parses the --shard values, refusing rather than dropping a
// malformed one: a dropped value would widen the selection asked for.
func parseShardFlags(values []string) ([]uint64, error) {
	if len(values) == 0 {
		return nil, nil
	}
	ids := make([]uint64, 0, len(values))
	for _, v := range values {
		id, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return nil, &InvalidFlagValueError{Flag: "shard", Value: v, Want: "an unsigned shard ID"}
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// printStateHashes writes the replica state hashes in the output format.
func printStateHashes(resp *transport.StateHashesResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	default:
		rows := make([][]string, 0, len(resp.Replicas))
		for _, r := range resp.Replicas {
			rows = append(rows, []string{
				orDash(r.TenantID),
				strconv.FormatUint(r.ShardID, 10),
				strconv.FormatUint(r.ReplicaID, 10),
				strconv.FormatUint(r.AppliedIndex, 10),
				strconv.FormatUint(r.Records, 10),
				orDash(r.Hash),
				orDash(r.Error),
			})
		}
		printTable([]string{"TENANT", "SHARD_ID", "REPLICA_ID", "APPLIED_INDEX", "RECORDS", "HASH", "ERROR"}, rows)
		return nil
	}
}

// orDash returns s, or "-" when s is empty, so an empty table cell stays a
// visible column.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

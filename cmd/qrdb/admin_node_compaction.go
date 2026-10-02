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

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newAdminNodeCompactionCmd returns the admin node-compaction subcommand: the
// platform's node-keyspace compaction policy.
func newAdminNodeCompactionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "node-compaction",
		Short: "Show the platform's node-keyspace compaction policy",
		Long: "Show the scheduled-compaction policy of the node keyspace: the keys outside every " +
			"database namespace. Requires admin:node_compaction, which only the system tenant can hold.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			policy, err := client.Admin().NodeCompaction(cmd.Context())
			if err != nil {
				return err
			}
			return printNodeCompaction(policy)
		},
	}
}

// newAdminUpdateNodeCompactionCmd returns the admin update-node-compaction
// subcommand.
func newAdminUpdateNodeCompactionCmd() *cobra.Command {
	var flags compactionPolicyFlags

	cmd := &cobra.Command{
		Use:   "update-node-compaction",
		Short: "Set the platform's node-keyspace compaction policy",
		Long: "Set the schedule on which the node keyspace -- the keys outside every database " +
			"namespace -- is compacted: off, revision (keep the last --retain-revisions revisions) " +
			"or period (keep the history newer than --retain-period). It is off unless set, never " +
			"touches a database's keys, and requires admin:node_compaction, which only the system " +
			"tenant can hold.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			policy, err := client.Admin().SetNodeCompaction(cmd.Context(), flags.policy())
			if err != nil {
				return err
			}
			return printNodeCompaction(policy)
		},
	}
	flags.bind(cmd)

	return cmd
}

// printNodeCompaction writes the node-keyspace policy in the output format.
func printNodeCompaction(policy *transport.DatabaseCompactionConfig) error {
	out := struct {
		Compaction *transport.DatabaseCompactionConfig `json:"compaction,omitempty" yaml:"compaction,omitempty"`
	}{Compaction: policy}
	switch outputFormat {
	case "json":
		return printJSON(out)
	case "yaml":
		return printYAML(out)
	default:
		if policy == nil || policy.Mode == "" {
			_, _ = fmt.Fprintln(outputWriter, "Compaction: off")
			return nil
		}
		printCompactionPolicy(policy)
		return nil
	}
}

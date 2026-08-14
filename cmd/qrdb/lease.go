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
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newLeaseCmd returns the lease parent command with subcommands for
// distributed lease management: grant, revoke, keep-alive, ttl, and list.
func newLeaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lease",
		Short: "Distributed lease management",
		Long:  "Create, revoke, renew, inspect, and list distributed TTL leases.",
	}

	cmd.AddCommand(
		newLeaseGrantCmd(),
		newLeaseRevokeCmd(),
		newLeaseKeepAliveCmd(),
		newLeaseTTLCmd(),
		newLeaseListCmd(),
	)

	return cmd
}

// newLeaseGrantCmd returns the lease grant subcommand.
func newLeaseGrantCmd() *cobra.Command {
	var ttl int64

	cmd := &cobra.Command{
		Use:   "grant",
		Short: "Create a new lease with a TTL",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info, err := client.Lease().LeaseGrant(cmd.Context(), ttl)
			if err != nil {
				return err
			}
			return printLeaseInfo(info)
		},
	}

	cmd.Flags().Int64Var(&ttl, "ttl", 0, "Time-to-live in seconds")
	_ = cmd.MarkFlagRequired("ttl")

	return cmd
}

// newLeaseRevokeCmd returns the lease revoke subcommand.
func newLeaseRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <lease-id>",
		Short: "Revoke an existing lease",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			leaseID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &InvalidLeaseIDError{Input: args[0]}
			}

			if err := client.Lease().LeaseRevoke(cmd.Context(), leaseID); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}
}

// newLeaseKeepAliveCmd returns the lease keep-alive subcommand.
func newLeaseKeepAliveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keep-alive <lease-id>",
		Short: "Reset the TTL on an existing lease",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			leaseID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &InvalidLeaseIDError{Input: args[0]}
			}

			info, err := client.Lease().LeaseKeepAlive(cmd.Context(), leaseID)
			if err != nil {
				return err
			}

			return printLeaseInfo(info)
		},
	}
}

// newLeaseTTLCmd returns the lease ttl subcommand.
func newLeaseTTLCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ttl <lease-id>",
		Short: "Show the current state of a lease",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			leaseID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return &InvalidLeaseIDError{Input: args[0]}
			}

			info, err := client.Lease().LeaseTimeToLive(cmd.Context(), leaseID)
			if err != nil {
				return err
			}

			return printLeaseInfo(info)
		},
	}
}

// newLeaseListCmd returns the lease list subcommand.
func newLeaseListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all active leases",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			leases, err := client.Lease().LeaseList(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(leases)
			case "yaml":
				return printYAML(leases)
			default:
				headers := []string{"ID", "TTL", "GRANTED_TTL", "REMAINING_TTL", "KEYS"}
				var rows [][]string
				for _, l := range leases {
					rows = append(rows, leaseInfoRow(l))
				}
				printTable(headers, rows)
				return nil
			}
		},
	}
}

// printLeaseInfo prints a single LeaseInfo in the current output format.
func printLeaseInfo(info *transport.LeaseInfo) error {
	switch outputFormat {
	case "json":
		return printJSON(info)
	case "yaml":
		return printYAML(info)
	default:
		_, _ = fmt.Fprintf(outputWriter, "ID:           %s\n", strconv.FormatInt(info.ID, 10))
		_, _ = fmt.Fprintf(outputWriter, "TTL:          %s\n", strconv.FormatInt(info.TTL, 10))
		_, _ = fmt.Fprintf(outputWriter, "GrantedTTL:   %s\n", strconv.FormatInt(info.GrantedTTL, 10))
		_, _ = fmt.Fprintf(outputWriter, "RemainingTTL: %s\n", strconv.FormatInt(info.RemainingTTL, 10))
		_, _ = fmt.Fprintf(outputWriter, "Keys:         %s\n", formatLeaseKeys(info.Keys))
		return nil
	}
}

// leaseInfoRow converts a LeaseInfo into a table row.
func leaseInfoRow(info *transport.LeaseInfo) []string {
	return []string{
		strconv.FormatInt(info.ID, 10),
		strconv.FormatInt(info.TTL, 10),
		strconv.FormatInt(info.GrantedTTL, 10),
		strconv.FormatInt(info.RemainingTTL, 10),
		formatLeaseKeys(info.Keys),
	}
}

// formatLeaseKeys converts a slice of byte-slice keys to a
// comma-separated string for human-readable output.
func formatLeaseKeys(keys [][]byte) string {
	strs := make([]string, len(keys))
	for i, k := range keys {
		strs[i] = string(k)
	}
	return strings.Join(strs, ", ")
}

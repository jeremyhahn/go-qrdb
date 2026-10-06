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

// newVolumePoolCmd creates the "volume-pool" command group: a qrdb-owned
// control-plane group, like "cluster" and "rebalance" (docs/volume-pools/
// cli.md). Every verb needs a system-tenant identity holding the matching
// volume_pools:* permission.
func newVolumePoolCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volume-pool",
		Short: "Volume pool management",
		Long:  "Create, inspect, update and delete named volume pools in the replicated catalog, and list the tenants assigned to one.",
	}

	cmd.AddCommand(
		newVolumePoolListCmd(),
		newVolumePoolGetCmd(),
		newVolumePoolCreateCmd(),
		newVolumePoolUpdateCmd(),
		newVolumePoolDeleteCmd(),
		newVolumePoolTenantsCmd(),
	)

	return cmd
}

// newVolumePoolListCmd creates the "volume-pool list" subcommand.
func newVolumePoolListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every pool in the catalog",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			pools, err := client.VolumePools().ListPools(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(pools)
			case "yaml":
				return printYAML(pools)
			default:
				headers := []string{"NAME", "PROVIDER", "CAPACITY", "RESERVED", "USED", "TENANTS", "NODES (ready/locked/unavail/unmapped)"}
				rows := make([][]string, 0, len(pools))
				for _, p := range pools {
					rows = append(rows, volumePoolRow(p))
				}
				printTable(headers, rows)
				return nil
			}
		},
	}
}

// newVolumePoolGetCmd creates the "volume-pool get" subcommand.
func newVolumePoolGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <pool>",
		Short: "Inspect one pool, with one row per node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			detail, err := client.VolumePools().GetPool(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printVolumePoolDetail(detail)
		},
	}
}

// newVolumePoolCreateCmd creates the "volume-pool create" subcommand.
func newVolumePoolCreateCmd() *cobra.Command {
	var (
		provider    string
		description string
		capacity    string
		overcommit  float64
	)

	cmd := &cobra.Command{
		Use:   "create <pool>",
		Short: "Add a pool to the catalog",
		Long:  "Add a pool to the catalog. The pool is not served until each node maps it in its own configuration and restarts or reloads.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			capacityBytes, err := parseByteSize(capacity)
			if err != nil {
				return err
			}
			req := &transport.CreateVolumePoolRequest{
				Name:          args[0],
				Provider:      provider,
				Description:   description,
				CapacityBytes: capacityBytes,
				Overcommit:    overcommit,
			}
			pool, err := client.VolumePools().CreatePool(cmd.Context(), req)
			if err != nil {
				return err
			}
			return printVolumePool(*pool)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "", "The volume provider every node uses for this pool; must be registered on the node handling the request. Default \"dir\".")
	cmd.Flags().StringVar(&description, "description", "", "Free text")
	cmd.Flags().StringVar(&capacity, "capacity", "", "Allocatable capacity (e.g. 2TiB, 500GiB, or a byte count); unset means the smallest size a healthy node reports")
	cmd.Flags().Float64Var(&overcommit, "overcommit", 0, "Reservations may reach capacity x overcommit; must be >= 1.0. Default 1.0.")

	return cmd
}

// newVolumePoolUpdateCmd creates the "volume-pool update" subcommand.
func newVolumePoolUpdateCmd() *cobra.Command {
	var (
		description string
		capacity    string
		overcommit  float64
	)

	cmd := &cobra.Command{
		Use:   "update <pool>",
		Short: "Change a pool's description, capacity or overcommit",
		Long:  "Change a pool's description, capacity or overcommit. The provider is immutable. Lowering capacity below the current reservation is refused.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &transport.UpdateVolumePoolRequest{Name: args[0]}
			if cmd.Flags().Changed("description") {
				req.Description = &description
			}
			if cmd.Flags().Changed("capacity") {
				capacityBytes, err := parseByteSize(capacity)
				if err != nil {
					return err
				}
				req.CapacityBytes = &capacityBytes
			}
			if cmd.Flags().Changed("overcommit") {
				req.Overcommit = &overcommit
			}
			pool, err := client.VolumePools().UpdatePool(cmd.Context(), req)
			if err != nil {
				return err
			}
			return printVolumePool(*pool)
		},
	}

	cmd.Flags().StringVar(&description, "description", "", "Free text")
	cmd.Flags().StringVar(&capacity, "capacity", "", "Allocatable capacity; 0 returns to the derived value")
	cmd.Flags().Float64Var(&overcommit, "overcommit", 0, "Must be >= 1.0")

	return cmd
}

// newVolumePoolDeleteCmd creates the "volume-pool delete" subcommand.
func newVolumePoolDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <pool>",
		Short: "Remove a pool from the catalog",
		Long:  "Remove a pool from the catalog. Refused while any tenant is assigned to it; the error names the tenants. It changes nothing on disk.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return client.VolumePools().DeletePool(cmd.Context(), args[0])
		},
	}
}

// newVolumePoolTenantsCmd creates the "volume-pool tenants" subcommand.
func newVolumePoolTenantsCmd() *cobra.Command {
	var (
		pageSize  int
		pageToken string
	)

	cmd := &cobra.Command{
		Use:   "tenants <pool>",
		Short: "List the tenants assigned to a pool",
		Long:  "List the tenants assigned to a pool, with each one's reservation and measured use.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.VolumePools().ListPoolTenants(cmd.Context(), &transport.ListVolumePoolTenantsRequest{
				Pool:      args[0],
				PageSize:  pageSize,
				PageToken: pageToken,
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
				headers := []string{"TENANT_ID", "RESERVED", "USED"}
				rows := make([][]string, 0, len(resp.Tenants))
				for _, t := range resp.Tenants {
					rows = append(rows, []string{t.TenantID, formatBytes(t.ReservedBytes), "-"})
				}
				printTable(headers, rows)
				if resp.NextPageToken != "" {
					_, _ = fmt.Fprintf(outputWriter, "next_page_token: %s\n", resp.NextPageToken)
				}
				return nil
			}
		},
	}

	cmd.Flags().IntVar(&pageSize, "page-size", 0, "Rows per page; 0 uses the server default")
	cmd.Flags().StringVar(&pageToken, "page-token", "", "Resume after a previous page")

	return cmd
}

// volumePoolRow converts a VolumePool into a table row for tabular output.
func volumePoolRow(p transport.VolumePool) []string {
	return []string{
		p.Name,
		p.Provider,
		formatBytes(p.CapacityBytes),
		formatBytes(p.ReservedBytes),
		formatBytes(p.UsedBytes),
		strconv.Itoa(p.TenantCount),
		fmt.Sprintf("%d/%d/%d/%d", p.Nodes.Ready, p.Nodes.Locked, p.Nodes.Unavailable, p.Nodes.Unmapped),
	}
}

// printVolumePool prints a single VolumePool in the current output format.
func printVolumePool(p transport.VolumePool) error {
	switch outputFormat {
	case "json":
		return printJSON(p)
	case "yaml":
		return printYAML(p)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Name:       %s\n", p.Name)
		_, _ = fmt.Fprintf(outputWriter, "Provider:   %s\n", p.Provider)
		if p.Description != "" {
			_, _ = fmt.Fprintf(outputWriter, "Description: %s\n", p.Description)
		}
		_, _ = fmt.Fprintf(outputWriter, "Capacity:   %s\n", formatBytes(p.CapacityBytes))
		_, _ = fmt.Fprintf(outputWriter, "Reserved:   %s\n", formatBytes(p.ReservedBytes))
		_, _ = fmt.Fprintf(outputWriter, "Used:       %s\n", formatBytes(p.UsedBytes))
		_, _ = fmt.Fprintf(outputWriter, "Tenants:    %d\n", p.TenantCount)
		_, _ = fmt.Fprintf(outputWriter, "Overcommit: %.2f\n", p.Overcommit)
		return nil
	}
}

// printVolumePoolDetail prints a VolumePoolDetail (the "get" response) in
// the current output format: the pool summary plus one row per node that
// has reported it. A node that has not reported within the lease TTL is
// shown as "no report", never with stale numbers.
func printVolumePoolDetail(d *transport.VolumePoolDetail) error {
	switch outputFormat {
	case "json":
		return printJSON(d)
	case "yaml":
		return printYAML(d)
	default:
		if err := printVolumePool(d.VolumePool); err != nil {
			return err
		}
		if len(d.NodeReports) == 0 {
			return nil
		}
		_, _ = fmt.Fprintln(outputWriter, "")
		headers := []string{"NODE", "STATE", "SIZE", "FREE", "REPORTED_AT", "DETAIL"}
		rows := make([][]string, 0, len(d.NodeReports))
		for _, nr := range d.NodeReports {
			if nr.State == "no_report" {
				rows = append(rows, []string{nr.NodeID, "no report", "-", "-", "-", nr.Detail})
				continue
			}
			rows = append(rows, []string{
				nr.NodeID,
				nr.State,
				formatBytes(nr.SizeBytes),
				formatBytes(nr.FreeBytes),
				nr.ReportedAt.String(),
				nr.Detail,
			})
		}
		printTable(headers, rows)
		return nil
	}
}

// parseByteSize parses a size string as either a bare byte count or a
// value with an IEC binary unit suffix (KiB, MiB, GiB, TiB, PiB; case
// insensitive). An empty string is 0 bytes (unset: derived capacity).
func parseByteSize(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	units := []struct {
		suffix string
		factor int64
	}{
		{"PiB", 1 << 50},
		{"TiB", 1 << 40},
		{"GiB", 1 << 30},
		{"MiB", 1 << 20},
		{"KiB", 1 << 10},
		{"B", 1},
	}
	upper := strings.ToUpper(s)
	for _, u := range units {
		su := strings.ToUpper(u.suffix)
		if strings.HasSuffix(upper, su) {
			numPart := strings.TrimSpace(s[:len(s)-len(u.suffix)])
			n, err := strconv.ParseFloat(numPart, 64)
			if err != nil {
				return 0, &InvalidByteSizeError{Value: s, Err: err}
			}
			return int64(n * float64(u.factor)), nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, &InvalidByteSizeError{Value: s, Err: err}
	}
	return n, nil
}

// InvalidByteSizeError is returned by parseByteSize for a malformed
// --capacity value.
type InvalidByteSizeError struct {
	Value string
	Err   error
}

// Error implements the error interface.
func (e *InvalidByteSizeError) Error() string {
	return "qrdb: invalid byte size " + strconv.Quote(e.Value) + ": " + e.Err.Error()
}

// Unwrap supports errors.Is / errors.As against the wrapped cause.
func (e *InvalidByteSizeError) Unwrap() error { return e.Err }

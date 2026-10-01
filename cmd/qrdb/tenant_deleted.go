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
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newTenantListDeletedCmd creates the "tenant list-deleted" subcommand: one
// page of retired tenant ids with their deletion records.
func newTenantListDeletedCmd() *cobra.Command {
	var (
		pageSize  int32
		pageToken string
	)
	cmd := &cobra.Command{
		Use:   "list-deleted",
		Short: "List retired tenant ids",
		Long: "List tenant ids that were deleted and can never be used again, with when each was retired. " +
			"Results are paged in ascending id order; pass the printed next page token to --page-token to continue.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Tenants().ListDeletedTenants(cmd.Context(), &transport.ListDeletedTenantsRequest{
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
				headers := []string{"ID", "NAME", "ISOLATION", "DELETED_AT"}
				rows := make([][]string, 0, len(resp.Tenants))
				for _, t := range resp.Tenants {
					rows = append(rows, []string{t.ID, t.Name, string(t.IsolationType), formatDeletedAt(t.DeletedAt)})
				}
				printTable(headers, rows)
				if resp.NextPageToken != "" {
					_, _ = fmt.Fprintf(outputWriter, "\nNext page token: %s\n", resp.NextPageToken)
				}
				return nil
			}
		},
	}
	cmd.Flags().Int32Var(&pageSize, "page-size", 0, "Records per page (0 = server default)")
	cmd.Flags().StringVar(&pageToken, "page-token", "", "Next page token from the previous page")
	return cmd
}

// newTenantReconcileDeletedCmd creates the "tenant reconcile-deleted"
// subcommand: reclaim, on the node the client is connected to, the on-disk
// residue of retired tenant ids.
func newTenantReconcileDeletedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reconcile-deleted",
		Short: "Reclaim retired tenants' directories on the connected node",
		Long: "Remove, from the node this client is connected to, the directories of tenant ids that were deleted " +
			"while the node was down or not hosting them. A directory a live tenant claims is never removed; it is " +
			"reported as skipped. The command acts on one node: run it against each node of a cluster.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Tenants().ReconcileDeletedTenants(cmd.Context())
			if resp == nil {
				return err
			}
			// A pass that failed for some ids still reports what it did for
			// the rest: print the report, then fail with the per-id error.
			if perr := printReconcileReport(resp); perr != nil {
				return perr
			}
			return err
		},
	}
}

// printReconcileReport prints one reconcile pass in the selected output
// format. A failed id is a row with the step and the node's message as its
// reason.
func printReconcileReport(resp *transport.ReconcileDeletedTenantsResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Inspected: %d retired tenant ids\n", resp.Inspected)
		headers := []string{"ACTION", "TENANT", "PATH", "REASON"}
		rows := make([][]string, 0, len(resp.Reclaimed)+len(resp.Skipped)+len(resp.Failed))
		for _, r := range resp.Reclaimed {
			rows = append(rows, []string{"reclaimed", r.TenantID, r.Path, ""})
		}
		for _, s := range resp.Skipped {
			rows = append(rows, []string{"skipped", s.TenantID, s.Path, s.Reason})
		}
		for _, f := range resp.Failed {
			rows = append(rows, []string{"failed", f.TenantID, f.Path, f.Op + ": " + f.Error})
		}
		if len(rows) > 0 {
			printTable(headers, rows)
		}
		return nil
	}
}

// formatDeletedAt renders a deletion time, or "unknown" for a record whose
// time could not be read -- the id is retired either way.
func formatDeletedAt(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format(time.RFC3339)
}

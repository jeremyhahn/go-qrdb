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
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newAuditCmd creates the "audit" command group with subcommands for
// querying the compliance-grade audit event log.
func newAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Compliance audit log operations",
		Long:  "Query the compliance-grade audit event log. Requires the audit:query permission.",
	}

	cmd.AddCommand(newAuditQueryCmd())
	cmd.AddCommand(newAuditVerifyCmd())

	return cmd
}

// newAuditQueryCmd creates the "audit query" subcommand.
func newAuditQueryCmd() *cobra.Command {
	var (
		from       string
		to         string
		actor      string
		resource   string
		action     string
		outcome    string
		tenantID   string
		limit      int
		cursor     string
		replicated bool
	)

	cmd := &cobra.Command{
		Use:   "query",
		Short: "Query audit events",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			filter := transport.AuditFilter{
				Actor:      actor,
				Resource:   resource,
				Action:     action,
				Outcome:    outcome,
				TenantID:   tenantID,
				Limit:      limit,
				Cursor:     cursor,
				Replicated: replicated,
			}

			if from != "" {
				t, err := parseAuditTime("--from", from)
				if err != nil {
					return err
				}
				filter.From = t
			}
			if to != "" {
				t, err := parseAuditTime("--to", to)
				if err != nil {
					return err
				}
				filter.To = t
			}

			ac := client.Audit()
			if ac == nil {
				return &AuditClientUnavailableError{}
			}

			page, err := ac.QueryAudit(cmd.Context(), filter)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(page)
			case "yaml":
				return printYAML(page)
			default:
				headers := []string{"TIMESTAMP", "ACTOR", "TENANT_ID", "ROLE", "RESOURCE", "ACTION", "OUTCOME"}
				rows := make([][]string, len(page.Events))
				for i, ev := range page.Events {
					rows[i] = auditEventRow(ev)
				}
				printTable(headers, rows)
				_, _ = fmt.Fprintf(outputWriter, "\nTotal: %s\n", strconv.Itoa(page.Total))
				if page.Next != "" {
					_, _ = fmt.Fprintf(outputWriter, "Next: %s\n", page.Next)
				}
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&from, "from", "", "Start of time range (RFC3339, e.g. 2026-08-01T00:00:00Z)")
	cmd.Flags().StringVar(&to, "to", "", "End of time range (RFC3339)")
	cmd.Flags().StringVar(&actor, "actor", "", "Filter by actor identity")
	cmd.Flags().StringVar(&resource, "resource", "", "Filter by resource")
	cmd.Flags().StringVar(&action, "action", "", "Filter by action")
	cmd.Flags().StringVar(&outcome, "outcome", "", "Filter by outcome (\"success\" or a \"failure\"/\"denied\" prefix)")
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Filter by tenant ID")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum number of events to return (server default 100, max 1000)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Pagination cursor from a previous response")
	cmd.Flags().BoolVar(&replicated, "replicated", false,
		"Read the cluster-wide replicated audit chain instead of the node-local trail; "+
			"a tenant not entitled to it sees an empty result, not an error")

	return cmd
}

// newAuditVerifyCmd creates the "audit verify" subcommand.
func newAuditVerifyCmd() *cobra.Command {
	var (
		replicated bool
		tenantID   string
	)

	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify the audit log's tamper-evidence hash chain",
		Long: "Verify the audit log's tamper-evidence hash chain across the active file and every " +
			"rotation archive, or (with --replicated) the hash-chain continuity of the cluster-wide " +
			"replicated audit chain for one tenant. Requires the audit:verify permission. Exits " +
			"non-zero when the chain does not verify -- that is a tamper finding, distinct from a " +
			"request failure.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ac := client.Audit()
			if ac == nil {
				return &AuditClientUnavailableError{}
			}

			if replicated {
				return runAuditVerifyReplicated(cmd, ac, tenantID)
			}

			result, err := ac.VerifyChain(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				if err := printJSON(result); err != nil {
					return err
				}
			case "yaml":
				if err := printYAML(result); err != nil {
					return err
				}
			default:
				printAuditVerifyResult(result)
			}

			if !result.Verified {
				return &AuditChainTamperedError{Reason: result.Reason}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&replicated, "replicated", false,
		"Verify the cluster-wide replicated audit chain instead of the node-local trail")
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Tenant to verify (--replicated only); defaults to the caller's own")

	return cmd
}

// runAuditVerifyReplicated handles "qrdb audit verify --replicated".
func runAuditVerifyReplicated(cmd *cobra.Command, ac transport.AuditClient, tenantID string) error {
	result, err := ac.VerifyReplicatedChain(cmd.Context(), tenantID)
	if err != nil {
		return err
	}

	switch outputFormat {
	case "json":
		if err := printJSON(result); err != nil {
			return err
		}
	case "yaml":
		if err := printYAML(result); err != nil {
			return err
		}
	default:
		printChainContinuityResult(result)
	}

	if !result.Verified {
		return &AuditChainTamperedError{Reason: result.Reason}
	}
	return nil
}

// printAuditVerifyResult renders an AuditVerifyResult as human-readable text.
func printAuditVerifyResult(result *transport.AuditVerifyResult) {
	_, _ = fmt.Fprintf(outputWriter, "Verified: %t\n", result.Verified)
	if result.Reason != "" {
		_, _ = fmt.Fprintf(outputWriter, "Reason: %s\n", result.Reason)
	}
	_, _ = fmt.Fprintf(outputWriter, "Files: %s\n", strconv.Itoa(result.Files))
	_, _ = fmt.Fprintf(outputWriter, "Records: %s\n", strconv.FormatInt(result.Records, 10))
	_, _ = fmt.Fprintf(outputWriter, "Genesis: %t\n", result.Genesis)
	if len(result.Order) > 0 {
		_, _ = fmt.Fprintf(outputWriter, "Order: %s\n", strings.Join(result.Order, ", "))
	}
	if result.Seed != "" {
		_, _ = fmt.Fprintf(outputWriter, "Seed: %s\n", result.Seed)
	}
	if result.Handoff != "" {
		_, _ = fmt.Fprintf(outputWriter, "Handoff: %s\n", result.Handoff)
	}
}

// printChainContinuityResult renders a ChainContinuityResult (the
// cluster-wide replicated audit chain's verification outcome) as
// human-readable text.
func printChainContinuityResult(result *transport.ChainContinuityResult) {
	_, _ = fmt.Fprintf(outputWriter, "Verified: %t\n", result.Verified)
	if result.Reason != "" {
		_, _ = fmt.Fprintf(outputWriter, "Reason: %s\n", result.Reason)
	}
	_, _ = fmt.Fprintf(outputWriter, "Records: %s\n", strconv.FormatInt(result.Records, 10))
	_, _ = fmt.Fprintf(outputWriter, "Head: %s\n", strconv.FormatUint(result.Head, 10))
	_, _ = fmt.Fprintf(outputWriter, "Low: %s\n", strconv.FormatUint(result.Low, 10))
	_, _ = fmt.Fprintf(outputWriter, "Genesis: %t\n", result.Genesis)
}

// AuditChainTamperedError is returned by "qrdb audit verify" when the audit
// log's hash chain does not verify -- a tamper finding, not a request
// failure. It carries a non-zero CLI exit status precisely because the
// distinction matters to an operator or a compliance harness's script.
type AuditChainTamperedError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *AuditChainTamperedError) Error() string {
	return "qrdb: audit: chain verification failed: " + e.Reason
}

// parseAuditTime parses an RFC3339 timestamp supplied to a --from/--to flag.
func parseAuditTime(flag, value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, &InvalidTimeRangeError{Flag: flag, Input: value, Err: err}
	}
	return t, nil
}

// auditEventRow converts an AuditEvent into a table row.
func auditEventRow(ev transport.AuditEvent) []string {
	return []string{
		ev.Timestamp.UTC().Format(time.RFC3339),
		ev.Actor,
		ev.TenantID,
		ev.Role,
		ev.Resource,
		ev.Action,
		ev.Outcome,
	}
}

// AuditClientUnavailableError is returned when the active transport does
// not expose an audit client, either because it does not implement
// transport.AuditProvider or because no audit logger has been configured
// on the server side.
type AuditClientUnavailableError struct{}

// Error returns a human-readable description.
func (e *AuditClientUnavailableError) Error() string {
	return "qrdb: audit: no audit client available on the active connection"
}

// InvalidTimeRangeError is returned when a --from or --to audit query
// flag cannot be parsed as an RFC3339 timestamp.
type InvalidTimeRangeError struct {
	Flag  string
	Input string
	Err   error
}

// Error returns a human-readable description.
func (e *InvalidTimeRangeError) Error() string {
	return "qrdb: invalid " + e.Flag + " value " + e.Input + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *InvalidTimeRangeError) Unwrap() error {
	return e.Err
}

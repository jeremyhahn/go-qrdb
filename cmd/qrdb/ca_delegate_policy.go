// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newCADelegatePolicyCmd groups the standing delegation policies of the
// caller's own tenant: while a policy stands, the node mints short-lived
// delegation certificates for its principal; deleting it revokes that.
func newCADelegatePolicyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Manage the standing delegation policies of your own tenant",
		Long: `A delegation policy lets the node mint short-lived delegation certificates for
one service principal of your tenant, with the scope and lifetime it names,
for as long as the policy stands. Writing, listing and deleting a policy needs
certs:delegate; deleting one stops every further mint for its principal.`,
	}
	cmd.AddCommand(newCADelegatePolicyPutCmd(), newCADelegatePolicyListCmd(), newCADelegatePolicyDeleteCmd())
	return cmd
}

func newCADelegatePolicyPutCmd() *cobra.Command {
	var (
		principal string
		scope     []string
		ttl       string
	)
	cmd := &cobra.Command{
		Use:   "put",
		Short: "Write the delegation policy for a service principal, replacing any it has",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := client.Certs().PutDelegationPolicy(cmd.Context(), &transport.DelegationPolicyRequest{
				Principal: principal,
				Scope:     scope,
				TTL:       ttl,
			})
			if err != nil {
				return err
			}
			return printDelegationPolicy(p)
		},
	}
	cmd.Flags().StringVar(&principal, "principal", "", "The service principal the policy names")
	_ = cmd.MarkFlagRequired("principal")
	cmd.Flags().StringSliceVar(&scope, "scope", nil, "Permissions delegated (repeat or comma-separate, e.g. kv:get,kv:put)")
	_ = cmd.MarkFlagRequired("scope")
	cmd.Flags().StringVar(&ttl, "ttl", "", "Lifetime of each minted certificate (default 1h, at most 24h)")
	return cmd
}

func newCADelegatePolicyListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the delegation policies of your own tenant",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			policies, err := client.Certs().ListDelegationPolicies(cmd.Context())
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(policies)
			case "yaml":
				return printYAML(policies)
			default:
				rows := make([][]string, len(policies))
				for i, p := range policies {
					rows[i] = []string{p.Principal, strings.Join(p.Scope, ","), p.TTL}
				}
				printTable([]string{"PRINCIPAL", "SCOPE", "TTL"}, rows)
				return nil
			}
		},
	}
}

func newCADelegatePolicyDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <principal>",
		Short: "Delete a principal's delegation policy, so no further certificate is minted for it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.Certs().DeleteDelegationPolicy(cmd.Context(), args[0]); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(outputWriter, "Delegation policy for %s deleted\n", args[0])
			return nil
		},
	}
}

// printDelegationPolicy prints one policy in the selected output format.
func printDelegationPolicy(p *transport.DelegationPolicy) error {
	switch outputFormat {
	case "json":
		return printJSON(p)
	case "yaml":
		return printYAML(p)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Principal: %s (tenant %s)\n", p.Principal, p.TenantID)
		_, _ = fmt.Fprintf(outputWriter, "Scope: %s\n", strings.Join(p.Scope, ","))
		_, _ = fmt.Fprintf(outputWriter, "TTL: %s\n", p.TTL)
		return nil
	}
}

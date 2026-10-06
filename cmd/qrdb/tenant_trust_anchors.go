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
	"os"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newTenantTrustAnchorsCmd creates the "tenant trust-anchors" group: a
// tenant's registered trust anchors, the outside CAs whose certificates may
// be enrolled on -- and log in as -- that tenant's accounts and no other
// tenant's. Every verb acts on your own tenant.
func newTenantTrustAnchorsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trust-anchors",
		Short: "Manage your tenant's trust anchors",
	}
	cmd.AddCommand(
		newTenantTrustAnchorsAddCmd(),
		newTenantTrustAnchorsListCmd(),
		newTenantTrustAnchorsRemoveCmd(),
	)
	return cmd
}

func newTenantTrustAnchorsAddCmd() *cobra.Command {
	var tenantID, file string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Register CA certificates as trust anchors",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := os.ReadFile(file)
			if err != nil {
				return &FileReadError{Path: file, Err: err}
			}
			list, err := client.Tenants().AddTrustAnchors(cmd.Context(), &transport.AddTrustAnchorsRequest{
				TenantID: tenantID,
				CertsPEM: string(data),
			})
			if err != nil {
				return err
			}
			return printTrustAnchors(list)
		},
	}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Your tenant")
	_ = cmd.MarkFlagRequired("tenant-id")
	cmd.Flags().StringVar(&file, "file", "", "A PEM bundle of CA certificates")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func newTenantTrustAnchorsListCmd() *cobra.Command {
	var tenantID string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your tenant's trust anchors",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			list, err := client.Tenants().ListTrustAnchors(cmd.Context(), tenantID)
			if err != nil {
				return err
			}
			return printTrustAnchors(list)
		},
	}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Your tenant")
	_ = cmd.MarkFlagRequired("tenant-id")
	return cmd
}

func newTenantTrustAnchorsRemoveCmd() *cobra.Command {
	var tenantID, fingerprint string
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Withdraw a trust anchor; it stops vouching for logins at once",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := client.Tenants().RemoveTrustAnchor(cmd.Context(), tenantID, fingerprint); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Your tenant")
	_ = cmd.MarkFlagRequired("tenant-id")
	cmd.Flags().StringVar(&fingerprint, "fingerprint", "", "The anchor's fingerprint (hex SHA-256 of its DER)")
	_ = cmd.MarkFlagRequired("fingerprint")
	return cmd
}

// printTrustAnchors prints a tenant's trust anchors in the current output
// format.
func printTrustAnchors(list *transport.TrustAnchorList) error {
	switch outputFormat {
	case "json":
		return printJSON(list)
	case "yaml":
		return printYAML(list)
	default:
		rows := make([][]string, 0, len(list.Anchors))
		for _, a := range list.Anchors {
			rows = append(rows, []string{a.Fingerprint, a.Subject, a.NotAfter.UTC().Format("2006-01-02")})
		}
		printTable([]string{"FINGERPRINT", "SUBJECT", "NOT AFTER"}, rows)
		return nil
	}
}

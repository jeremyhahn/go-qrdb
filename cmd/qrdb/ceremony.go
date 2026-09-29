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
	"os"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newCeremonyCmd returns the ceremony parent command with subcommands
// for initialization ceremony management.
func newCeremonyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ceremony",
		Short: "Initialization ceremony management",
		Long:  "Manage the security officer initialization ceremony for key distribution.",
	}

	cmd.AddCommand(
		newCeremonyInitCmd(),
		newCeremonyStatusCmd(),
		newCeremonySignCSRCmd(),
	)

	return cmd
}

// newCeremonyInitCmd returns a redirect stub. Per
// docs/08-bootstrap-auth-architecture.md §6 the ceremony init
// subcommand is collapsed into the root "init" command. Operators
// running the legacy path receive a CanonicalRedirectError.
func newCeremonyInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "init",
		Short:              "REMOVED: use the root 'init' command instead",
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return &CanonicalRedirectError{
				Old: "qrdb ceremony init",
				New: "qrdb init --barrier-strategy <name> --so-pin ... --user-pin ... [...]",
			}
		},
	}
}

// newCeremonyStatusCmd returns the ceremony status subcommand.
func newCeremonyStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show ceremony status",
		Long:  "Display the current state of the initialization ceremony.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Ceremony().CeremonyStatus(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "State:   %s\n", resp.State)
				_, _ = fmt.Fprintf(outputWriter, "SPKIPin: %s\n", resp.SPKIPin)
				return nil
			}
		},
	}
}

// newCeremonySignCSRCmd returns the ceremony sign-csr subcommand.
func newCeremonySignCSRCmd() *cobra.Command {
	var (
		username string
		soPin    string
		csrFile  string
		role     string
	)

	cmd := &cobra.Command{
		Use:   "sign-csr",
		Short: "Sign a certificate signing request",
		Long:  "Submit a CSR file for signing during or after the ceremony.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			csrData, err := os.ReadFile(csrFile)
			if err != nil {
				return &FileReadError{Path: csrFile, Err: err}
			}

			resp, err := client.Ceremony().CeremonySignCSR(cmd.Context(), &transport.SignCSRInitRequest{
				Username: username,
				SOPin:    soPin,
				CSRPEM:   string(csrData),
				Role:     role,
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
				_, _ = fmt.Fprintf(outputWriter, "CertPEM:\n%s\n", resp.CertPEM)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "Officer username")
	cmd.Flags().StringVar(&soPin, "so-pin", "", "Security officer PIN")
	cmd.Flags().StringVar(&csrFile, "csr-file", "", "Path to the CSR PEM file")
	cmd.Flags().StringVar(&role, "role", "", "Role for the signed certificate")
	_ = cmd.MarkFlagRequired("username")
	_ = cmd.MarkFlagRequired("so-pin")
	_ = cmd.MarkFlagRequired("csr-file")
	_ = cmd.MarkFlagRequired("role")

	return cmd
}

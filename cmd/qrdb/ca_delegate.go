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
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newCADelegateCmd builds "ca delegate": a short-lived delegation
// certificate, signed by your tenant's own CA, naming a service principal and
// the permissions delegated to it. A request made under it holds the scope
// intersected with the principal's own role. Like "ca issue", the private key
// never leaves this machine: with --csr it is wherever the CSR came from;
// without, a fresh key is generated here and written to --out.
func newCADelegateCmd() *cobra.Command {
	var (
		principal string
		scope     []string
		csrFile   string
		ttl       string
		outDir    string
	)

	cmd := &cobra.Command{
		Use:   "delegate",
		Short: "Issue a delegation certificate to a service principal of your own tenant",
		Long: `Issue a short-lived client certificate, signed by your own tenant's CA, that
lets a service principal act inside your tenant with the delegated scope. A
request made under it holds the scope intersected with the principal's own
role. Revoke it early with "ca revoke --serial".

  --csr FILE   issue for an existing CSR (its key stays with whoever made it)
  (no --csr)   generate a fresh key here, write key.pem and cert.pem to --out`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var csrPEM, keyPEM []byte
			if csrFile != "" {
				data, err := os.ReadFile(csrFile)
				if err != nil {
					return &FileReadError{Path: csrFile, Err: err}
				}
				csrPEM = data
			} else {
				if outDir == "" {
					return &MissingOutDirError{Reason: "without --csr a key is generated here, and it is written only to --out"}
				}
				var err error
				if keyPEM, csrPEM, err = newClientKeyAndCSR(principal); err != nil {
					return err
				}
			}

			issued, err := client.Certs().IssueDelegation(cmd.Context(), &transport.IssueDelegationRequest{
				Principal: principal,
				Scope:     scope,
				CSRPEM:    string(csrPEM),
				TTL:       ttl,
			})
			if err != nil {
				return err
			}
			return finishDelegation(outDir, keyPEM, issued)
		},
	}

	cmd.Flags().StringVar(&principal, "principal", "", "The service principal the delegation names")
	_ = cmd.MarkFlagRequired("principal")
	cmd.Flags().StringSliceVar(&scope, "scope", nil, "Permissions delegated (repeat or comma-separate, e.g. kv:get,kv:put)")
	_ = cmd.MarkFlagRequired("scope")
	cmd.Flags().StringVar(&csrFile, "csr", "", "A PEM certificate signing request to issue for")
	cmd.Flags().StringVar(&ttl, "ttl", "", "Delegation lifetime (default 1h, at most 24h)")
	cmd.Flags().StringVar(&outDir, "out", "", "Output directory for cert.pem (and key.pem when the key is generated here)")

	cmd.AddCommand(newCADelegatePolicyCmd())
	return cmd
}

// printDelegation prints an issued delegation in the current output format.
func printDelegation(d *transport.IssuedDelegation) error {
	switch outputFormat {
	case "json":
		return printJSON(d)
	case "yaml":
		return printYAML(d)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Principal: %s (tenant %s)\n", d.Principal, d.TenantID)
		_, _ = fmt.Fprintf(outputWriter, "Scope: %s\n", strings.Join(d.Scope, ","))
		_, _ = fmt.Fprintf(outputWriter, "Serial: %s\n", d.Serial)
		_, _ = fmt.Fprintf(outputWriter, "Not after: %s\n\n", d.NotAfter.UTC().Format("2006-01-02T15:04:05Z"))
		_, _ = fmt.Fprintf(outputWriter, "--- Certificate ---\n%s\n", d.CertPEM)
		return nil
	}
}

// finishDelegation writes an issued delegation (and the key generated for it
// here, if any) to outDir, or prints it when no directory was given.
func finishDelegation(outDir string, keyPEM []byte, d *transport.IssuedDelegation) error {
	if outDir == "" {
		return printDelegation(d)
	}
	files := map[string][]byte{"cert.pem": []byte(d.CertPEM)}
	if keyPEM != nil {
		files["key.pem"] = keyPEM
	}
	if err := writePEMFiles(outDir, files); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(outputWriter, "Certificate written to %s\n", filepath.Join(outDir, "cert.pem"))
	if keyPEM != nil {
		_, _ = fmt.Fprintf(outputWriter, "Private key written to %s\n", filepath.Join(outDir, "key.pem"))
	}
	_, _ = fmt.Fprintf(outputWriter, "Serial: %s\n", d.Serial)
	return nil
}

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
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newCertsGenerateCmd creates the "certs generate" subcommand. Reused by
// ca.go: the "certs" command group itself was removed (renamed to "ca"
// per docs/08-bootstrap-auth-architecture.md §6), but these builders
// remain the canonical implementation the "ca" subcommands wrap.
func newCertsGenerateCmd() *cobra.Command {
	var (
		username string
		tenantID string
		ttl      string
		outDir   string
	)

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate a client certificate",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.GenerateCertRequest{
				Username: username,
				TenantID: tenantID,
				TTL:      ttl,
			}

			bundle, err := client.Certs().GenerateCert(cmd.Context(), req)
			if err != nil {
				return err
			}

			// Write PEM files to disk when --out is specified.
			if outDir != "" {
				if err := writeCertBundle(outDir, bundle); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(outputWriter, "Certificate written to %s/cert.pem\n", outDir)
				_, _ = fmt.Fprintf(outputWriter, "Private key written to %s/key.pem\n", outDir)
				_, _ = fmt.Fprintf(outputWriter, "Serial: %s\n", bundle.Serial)
				return nil
			}

			return printCertBundle(bundle)
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "Username for the certificate subject")
	_ = cmd.MarkFlagRequired("username")
	cmd.Flags().StringVar(&tenantID, "tenant-id", tenantid.System, "Tenant ID")
	cmd.Flags().StringVar(&ttl, "ttl", "", "Certificate validity duration (e.g. 8760h)")
	cmd.Flags().StringVar(&outDir, "out", "", "Output directory for cert.pem and key.pem")

	return cmd
}

// newCertsExportCmd creates the "certs export" subcommand.
func newCertsExportCmd() *cobra.Command {
	var (
		certPEMFile string
		keyPEMFile  string
		passphrase  string
		outFile     string
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export a certificate as PKCS#12",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			certPEM, err := os.ReadFile(certPEMFile)
			if err != nil {
				return &FileReadError{Path: certPEMFile, Err: err}
			}

			keyPEM, err := os.ReadFile(keyPEMFile)
			if err != nil {
				return &FileReadError{Path: keyPEMFile, Err: err}
			}

			req := &transport.ExportCertRequest{
				CertPEM:    string(certPEM),
				KeyPEM:     string(keyPEM),
				Passphrase: passphrase,
			}

			resp, err := client.Certs().ExportCert(cmd.Context(), req)
			if err != nil {
				return err
			}

			// Decode base64 PKCS#12 and write to output file.
			pkcs12Data, err := base64.StdEncoding.DecodeString(resp.PKCS12)
			if err != nil {
				return &PKCS12DecodeError{Err: err}
			}

			if err := os.WriteFile(outFile, pkcs12Data, 0600); err != nil {
				return &FileWriteError{Path: outFile, Err: err}
			}

			_, _ = fmt.Fprintf(outputWriter, "PKCS#12 archive written to %s\n", outFile)
			return nil
		},
	}

	cmd.Flags().StringVar(&certPEMFile, "cert-pem", "", "Path to certificate PEM file")
	_ = cmd.MarkFlagRequired("cert-pem")
	cmd.Flags().StringVar(&keyPEMFile, "key-pem", "", "Path to private key PEM file")
	_ = cmd.MarkFlagRequired("key-pem")
	cmd.Flags().StringVar(&passphrase, "passphrase", "", "Passphrase for the PKCS#12 archive")
	_ = cmd.MarkFlagRequired("passphrase")
	cmd.Flags().StringVar(&outFile, "out", "", "Output file path for PKCS#12 archive")
	_ = cmd.MarkFlagRequired("out")

	return cmd
}

// newCertsRevokeCmd creates the "certs revoke" subcommand.
func newCertsRevokeCmd() *cobra.Command {
	var serial string

	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke a certificate by serial number",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := client.Certs().RevokeCert(cmd.Context(), serial)
			if err != nil {
				return err
			}

			// A mistyped serial and a deliberate revocation of a
			// peer-issued certificate both succeed (deny-by-serial); this
			// is the only way an operator can tell them apart. See
			// go-qrdb#287.
			if result.RecordFound {
				_, _ = fmt.Fprintln(outputWriter, "OK: certificate record found and marked revoked")
			} else {
				_, _ = fmt.Fprintln(outputWriter, "OK: no local record for this serial (deny-by-serial revocation)")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&serial, "serial", "", "Certificate serial number")
	_ = cmd.MarkFlagRequired("serial")

	return cmd
}

// newCertsListCmd creates the "certs list" subcommand.
func newCertsListCmd() *cobra.Command {
	var tenantID string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List issued certificates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Certs().ListCerts(cmd.Context(), tenantID)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"SERIAL", "USERNAME", "TENANT_ID", "ISSUED_AT", "EXPIRES_AT", "REVOKED"}
				var rows [][]string
				for _, c := range resp.Certs {
					rows = append(rows, certRecordRow(c))
				}
				printTable(headers, rows)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&tenantID, "tenant-id", tenantid.System, "Filter by tenant ID")

	return cmd
}

// printCertBundle prints a CertBundle in the current output format.
func printCertBundle(b *transport.CertBundle) error {
	switch outputFormat {
	case "json":
		return printJSON(b)
	case "yaml":
		return printYAML(b)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Serial: %s\n\n", b.Serial)
		_, _ = fmt.Fprintf(outputWriter, "--- Certificate ---\n%s\n", b.CertPEM)
		_, _ = fmt.Fprintf(outputWriter, "--- Private Key ---\n%s\n", b.KeyPEM)
		return nil
	}
}

// certRecordRow converts a CertRecord into a table row for tabular output.
func certRecordRow(c *transport.CertRecord) []string {
	return []string{
		c.Serial,
		c.Username,
		c.TenantID,
		c.IssuedAt,
		c.ExpiresAt,
		strconv.FormatBool(c.Revoked),
	}
}

// writeCertBundle writes a CertBundle's PEM files to the specified directory.
func writeCertBundle(dir string, bundle *transport.CertBundle) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return &DirectoryCreateError{Path: dir, Err: err}
	}

	certPath := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(certPath, []byte(bundle.CertPEM), 0600); err != nil {
		return &FileWriteError{Path: certPath, Err: err}
	}

	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(keyPath, []byte(bundle.KeyPEM), 0600); err != nil {
		return &FileWriteError{Path: keyPath, Err: err}
	}

	return nil
}

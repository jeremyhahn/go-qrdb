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
	"strconv"

	"github.com/spf13/cobra"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newCertsIssueCmd creates the "certs issue" subcommand (canonical name
// "ca issue"): a certificate, signed by your tenant's own CA, for an existing
// account of your own tenant, bound to the account. The private key never
// leaves this machine: with --csr it is wherever the CSR came from; without,
// a fresh key is generated here and written to --out beside the certificate.
//
// The "certs" command group itself was removed (renamed to "ca" per
// docs/certificate-authority/cli.md, and the orphaned "certs"
// aggregator deleted outright per the no-backward-compatibility ruling); this
// builder, and the enroll/csr/export/revoke/list builders below it, remain
// the canonical implementation newCACmd wraps.
func newCertsIssueCmd() *cobra.Command {
	var (
		username string
		csrFile  string
		ttl      string
		outDir   string
	)

	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Issue a client certificate for an account of your own tenant",
		Long: `Issue a client certificate, signed by your own tenant's CA, for an existing
account of your own tenant, and bind it to the account. The server only ever
sees a certificate signing request: the private key stays here.

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
				if keyPEM, csrPEM, err = newClientKeyAndCSR(username); err != nil {
					return err
				}
			}

			issued, err := client.Certs().IssueCert(cmd.Context(), &transport.IssueCertRequest{
				Username: username,
				CSRPEM:   string(csrPEM),
				TTL:      ttl,
			})
			if err != nil {
				return err
			}
			return finishEnrolledCert(outDir, keyPEM, issued)
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "The existing account the certificate is for")
	_ = cmd.MarkFlagRequired("username")
	cmd.Flags().StringVar(&csrFile, "csr", "", "A PEM certificate signing request to issue for")
	cmd.Flags().StringVar(&ttl, "ttl", "", "Certificate validity duration (e.g. 8760h)")
	cmd.Flags().StringVar(&outDir, "out", "", "Output directory for cert.pem (and key.pem when the key is generated here)")

	return cmd
}

// newCertsEnrollCmd creates the "certs enroll" subcommand (canonical name
// "ca enroll"): binds a bring-your-own certificate, which must chain to one of
// your tenant's trust anchors, to an existing account of your own tenant.
func newCertsEnrollCmd() *cobra.Command {
	var (
		username  string
		certFile  string
		chainFile string
	)

	cmd := &cobra.Command{
		Use:   "enroll",
		Short: "Enrol a bring-your-own certificate on an account of your own tenant",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			certPEM, err := os.ReadFile(certFile)
			if err != nil {
				return &FileReadError{Path: certFile, Err: err}
			}
			var chainPEM []byte
			if chainFile != "" {
				if chainPEM, err = os.ReadFile(chainFile); err != nil {
					return &FileReadError{Path: chainFile, Err: err}
				}
			}
			enrolled, err := client.Certs().EnrollCert(cmd.Context(), &transport.EnrollCertRequest{
				Username: username,
				CertPEM:  string(certPEM),
				ChainPEM: string(chainPEM),
			})
			if err != nil {
				return err
			}
			return printEnrolledCert(enrolled)
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "The existing account the certificate is enrolled on")
	_ = cmd.MarkFlagRequired("username")
	cmd.Flags().StringVar(&certFile, "cert", "", "The PEM certificate, leaf first")
	_ = cmd.MarkFlagRequired("cert")
	cmd.Flags().StringVar(&chainFile, "chain", "", "Optional PEM intermediates for the certificate's chain")

	return cmd
}

// newCertsCSRCmd creates the "certs csr" subcommand (canonical name
// "ca csr"): generates a key and a certificate signing request HERE, with no
// server involved. It is how a tenant's first admin produces the CSR the
// platform operator passes to "tenant create --admin-csr", and how anyone
// produces one for "ca issue --csr".
func newCertsCSRCmd() *cobra.Command {
	var (
		username string
		outDir   string
	)

	cmd := &cobra.Command{
		Use:   "csr",
		Short: "Generate a key and a certificate signing request locally",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			keyPEM, csrPEM, err := newClientKeyAndCSR(username)
			if err != nil {
				return err
			}
			if err := writePEMFiles(outDir, map[string][]byte{"key.pem": keyPEM, "csr.pem": csrPEM}); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(outputWriter, "Private key written to %s\n", filepath.Join(outDir, "key.pem"))
			_, _ = fmt.Fprintf(outputWriter, "CSR written to %s\n", filepath.Join(outDir, "csr.pem"))
			return nil
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "The account name the CSR is for (its Subject CN; the issuer names the account regardless)")
	_ = cmd.MarkFlagRequired("username")
	cmd.Flags().StringVar(&outDir, "out", "", "Output directory for key.pem and csr.pem")
	_ = cmd.MarkFlagRequired("out")

	return cmd
}

// newCertsExportCmd creates the "certs export" subcommand (canonical name
// "ca export-bundle"): it builds the PKCS#12 archive ENTIRELY LOCALLY, from
// PEM files already on this machine, and never calls the server at all. No
// node may receive a private key to encode on a caller's behalf (go-qrdb
// security review): encoding a PKCS#12 archive needs the key, so the only
// place it can safely happen is wherever the key already is.
func newCertsExportCmd() *cobra.Command {
	var (
		certPEMFile string
		keyPEMFile  string
		caPEMFile   string
		passphrase  string
		outFile     string
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Build a PKCS#12 archive locally from a certificate and key (no server call)",
		Long: `Build a PKCS#12 (.p12) archive from a certificate and its private key, both
already on this machine, and write it to --out. This never contacts the
server: the key leaves this machine inside the archive you asked for, never
over the wire.

  --ca-pem FILE   optionally embed an issuer certificate in the archive's chain`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			certPEM, err := os.ReadFile(certPEMFile)
			if err != nil {
				return &FileReadError{Path: certPEMFile, Err: err}
			}

			keyPEM, err := os.ReadFile(keyPEMFile)
			if err != nil {
				return &FileReadError{Path: keyPEMFile, Err: err}
			}

			var caPEM []byte
			if caPEMFile != "" {
				if caPEM, err = os.ReadFile(caPEMFile); err != nil {
					return &FileReadError{Path: caPEMFile, Err: err}
				}
			}

			pkcs12Data, err := sdk.EncodePKCS12(certPEM, keyPEM, caPEM, passphrase)
			if err != nil {
				return err
			}

			// Confine the write to the resolved parent directory via os.Root
			// rather than handing the user-supplied --out path straight to
			// os.WriteFile: the directory is opened once, pinned, and the
			// write can only land inside it, with no later component able
			// to escape it through a symlink or a traversal segment.
			outDir := filepath.Dir(outFile)
			root, err := os.OpenRoot(outDir)
			if err != nil {
				return &FileWriteError{Path: outFile, Err: err}
			}
			defer func() { _ = root.Close() }()
			if err := root.WriteFile(filepath.Base(outFile), pkcs12Data, 0600); err != nil {
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
	cmd.Flags().StringVar(&caPEMFile, "ca-pem", "", "Optional path to an issuer certificate PEM file to embed in the archive's chain")
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

	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Tenant to list (must be the caller's own); defaults to the caller's own")

	return cmd
}

// printEnrolledCert prints a certificate bound to an account in the current
// output format.
func printEnrolledCert(e *transport.EnrolledCert) error {
	switch outputFormat {
	case "json":
		return printJSON(e)
	case "yaml":
		return printYAML(e)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Account: %s (tenant %s)\n", e.Username, e.TenantID)
		if e.Serial != "" {
			_, _ = fmt.Fprintf(outputWriter, "Serial: %s\n", e.Serial)
		}
		_, _ = fmt.Fprintf(outputWriter, "Thumbprint: %s\n", e.Thumbprint)
		_, _ = fmt.Fprintf(outputWriter, "Not after: %s\n\n", e.NotAfter.UTC().Format("2006-01-02T15:04:05Z"))
		_, _ = fmt.Fprintf(outputWriter, "--- Certificate ---\n%s\n", e.CertPEM)
		return nil
	}
}

// finishEnrolledCert writes an issued certificate (and the key generated for
// it here, if any) to outDir, or prints it when no directory was given.
func finishEnrolledCert(outDir string, keyPEM []byte, e *transport.EnrolledCert) error {
	if outDir == "" {
		return printEnrolledCert(e)
	}
	files := map[string][]byte{"cert.pem": []byte(e.CertPEM)}
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
	if e.Serial != "" {
		_, _ = fmt.Fprintf(outputWriter, "Serial: %s\n", e.Serial)
	}
	return nil
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

// writePEMFiles writes each named file into dir, creating dir 0700 and every
// file 0600: a key among them is a secret.
func writePEMFiles(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return &DirectoryCreateError{Path: dir, Err: err}
	}
	for name, data := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			return &FileWriteError{Path: path, Err: err}
		}
	}
	return nil
}

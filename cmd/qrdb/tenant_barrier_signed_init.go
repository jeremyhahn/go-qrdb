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
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/secure"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// signedInitBeginOutput is what barrier-init-begin prints: the response, with
// the reference and the statement to sign also in hex, so a key that signs
// outside this CLI signs the right bytes and complete redeems the right
// reference.
type signedInitBeginOutput struct {
	*transport.TenantBarrierSignedInitBeginResponse
	ReferenceHex string `json:"reference_hex" yaml:"reference_hex"`
	StatementHex string `json:"statement_hex" yaml:"statement_hex"`
}

// newTenantBarrierInitBeginCmd returns `barrier-init-begin`, the first half of
// a signed init for a first admin whose key signs outside this CLI.
func newTenantBarrierInitBeginCmd() *cobra.Command {
	var (
		tenantID      string
		strategy      string
		keyRef        string
		threshold     int
		shares        int
		officers      []string
		custodians    []string
		policy        string
		publicKeyFile string
		statementOut  string
	)

	cmd := &cobra.Command{
		Use:   "barrier-init-begin",
		Short: "Begin a signed barrier init (for a first admin's key that signs outside this CLI)",
		Long: "Begin a two-phase signed initialization of the per-tenant barrier, for a first\n" +
			"admin whose key cannot sign over this connection (an HSM, a smart card, another\n" +
			"host). The node prepares the init statement naming --public-key as its signer and\n" +
			"prints it with a single-use reference; sign the statement detached (ECDSA or RSA\n" +
			"PKCS #1 v1.5 over its SHA-256 digest, or pure Ed25519) and redeem the reference\n" +
			"with `tenant barrier-init-complete` before it expires. --statement-out writes the\n" +
			"raw statement bytes for the signing tool.\n\n" +
			"The statement lives on --server's node, so begin and complete go to the same node.\n" +
			"One node's completed init is enough under every strategy but passphrase: the other\n" +
			"serving nodes initialize their own keys from it. A passphrase barrier served by\n" +
			"several nodes is begun and completed on each, as `tenant barrier-init` does for you.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			roster, err := tenantBarrierRoster(officers, custodians, shares)
			if err != nil {
				return err
			}
			spki, err := readSignerPublicKey(publicKeyFile)
			if err != nil {
				return err
			}
			resp, err := client.Tenants().TenantBarrierSignedInitBegin(cmd.Context(), &transport.TenantBarrierSignedInitBeginRequest{
				TenantID:  tenantID,
				Strategy:  strategy,
				KeyRef:    keyRef,
				Threshold: threshold,
				Shares:    shares,
				Officers:  roster,
				Policy:    policy,
				PublicKey: spki,
			})
			if err != nil {
				return err
			}
			if statementOut != "" {
				if err := os.WriteFile(statementOut, resp.Statement, 0o600); err != nil {
					return &FileWriteError{Path: statementOut, Err: err}
				}
			}
			return printSignedInitBegin(&signedInitBeginOutput{
				TenantBarrierSignedInitBeginResponse: resp,
				ReferenceHex:                         hex.EncodeToString(resp.Reference),
				StatementHex:                         hex.EncodeToString(resp.Statement),
			})
		},
	}

	cmd.Flags().StringVar(&tenantID, "id", "", "Tenant ID")
	cmd.Flags().StringVar(&publicKeyFile, "public-key", "",
		"PEM file with the signing key's PUBLIC KEY or its CERTIFICATE: the tenant's pending first admin's")
	cmd.Flags().StringVar(&strategy, "strategy", "",
		"Seal strategy: none | passphrase | shamir | a registered hardware id (empty = passphrase)")
	cmd.Flags().StringVar(&keyRef, "key-ref", "", "The strategy's reference to the tenant's key, for a strategy that names one")
	cmd.Flags().IntVar(&threshold, "threshold", 0, "Minimum shares required to unseal (shamir strategy only; 0 = default)")
	cmd.Flags().IntVar(&shares, "shares", 0, "Total number of shares to generate (shamir strategy only; 0 = default)")
	cmd.Flags().StringArrayVar(&officers, "officers", nil,
		"Security Officer share holder: username=path/to/csr.pem (repeatable; shamir strategy only)")
	cmd.Flags().StringArrayVar(&custodians, "custodians", nil,
		"Key custodian share holder: username=path/to/csr.pem (repeatable; shamir strategy only)")
	cmd.Flags().StringVar(&policy, "policy", "",
		"Factor policy for --officers/--custodians: designated (default) or any-enrolled")
	cmd.Flags().StringVar(&statementOut, "statement-out", "", "File to write the raw statement bytes to")
	for _, name := range []string{"id", "public-key"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

// newTenantBarrierInitCompleteCmd returns `barrier-init-complete`.
func newTenantBarrierInitCompleteCmd() *cobra.Command {
	var tenantID, referenceHex, sigFile, passphrase string

	cmd := &cobra.Command{
		Use:   "barrier-init-complete",
		Short: "Complete a signed barrier init with an externally produced signature",
		Long: "Redeem the reference `tenant barrier-init-begin` printed with the detached\n" +
			"signature over its statement, and initialize the barrier. The reference is spent\n" +
			"by the attempt whatever its outcome: a refused signature needs a new begin.\n" +
			"--passphrase is the passphrase strategy's credential.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ref, err := hex.DecodeString(referenceHex)
			if err != nil {
				return &InvalidArgError{Arg: "--reference-hex", Reason: "not hex"}
			}
			sig, err := os.ReadFile(sigFile)
			if err != nil {
				return &FileReadError{Path: sigFile, Err: err}
			}
			req := &transport.TenantBarrierSignedInitCompleteRequest{
				TenantID:   tenantID,
				Reference:  ref,
				Signature:  sig,
				Passphrase: secretBytes(passphrase),
			}
			defer secure.Wipe(req.Passphrase)
			resp, err := client.Tenants().TenantBarrierSignedInitComplete(cmd.Context(), req)
			if err != nil {
				return err
			}
			return printTenantBarrierInitResponse(resp)
		},
	}

	cmd.Flags().StringVar(&tenantID, "id", "", "Tenant ID")
	cmd.Flags().StringVar(&referenceHex, "reference-hex", "", "Reference from barrier-init-begin")
	cmd.Flags().StringVar(&sigFile, "sig-file", "", "Raw detached signature over the statement")
	cmd.Flags().StringVar(&passphrase, "passphrase", "",
		"Passphrase used to seal the barrier (required for the passphrase strategy; ignored by the others)")
	for _, name := range []string{"id", "reference-hex", "sig-file"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

// readSignerPublicKey is the DER SubjectPublicKeyInfo in the PEM file at
// path: a PUBLIC KEY block, or a CERTIFICATE whose key it is.
func readSignerPublicKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &FileReadError{Path: path, Err: err}
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, &InvalidArgError{Arg: "--public-key", Reason: "no PEM block"}
	}
	switch block.Type {
	case "PUBLIC KEY":
		if _, err := x509.ParsePKIXPublicKey(block.Bytes); err != nil {
			return nil, &InvalidArgError{Arg: "--public-key", Reason: "not a SubjectPublicKeyInfo: " + err.Error()}
		}
		return block.Bytes, nil
	case "CERTIFICATE":
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, &InvalidArgError{Arg: "--public-key", Reason: "not a certificate: " + err.Error()}
		}
		return cert.RawSubjectPublicKeyInfo, nil
	default:
		return nil, &InvalidArgError{Arg: "--public-key", Reason: "PEM block " + block.Type + " is neither PUBLIC KEY nor CERTIFICATE"}
	}
}

func printSignedInitBegin(out *signedInitBeginOutput) error {
	switch outputFormat {
	case "json":
		return printJSON(out)
	case "yaml":
		return printYAML(out)
	default:
		_, _ = fmt.Fprintf(outputWriter, "TenantID:     %s\n", out.TenantID)
		_, _ = fmt.Fprintf(outputWriter, "Strategy:     %s\n", out.Strategy)
		_, _ = fmt.Fprintf(outputWriter, "ReferenceHex: %s\n", out.ReferenceHex)
		_, _ = fmt.Fprintf(outputWriter, "StatementHex: %s\n", out.StatementHex)
		_, _ = fmt.Fprintf(outputWriter, "ExpiresAt:    %s\n", out.ExpiresAt.Format("2006-01-02T15:04:05Z"))
		if out.ShareCount > 0 {
			_, _ = fmt.Fprintf(outputWriter, "Threshold:    %d\n", out.Threshold)
			_, _ = fmt.Fprintf(outputWriter, "TotalShares:  %d\n", out.TotalShares)
			for _, h := range out.Holders {
				_, _ = fmt.Fprintf(outputWriter, "Holder:       %s (%s)\n", h.Username, h.Role)
			}
		}
		return nil
	}
}

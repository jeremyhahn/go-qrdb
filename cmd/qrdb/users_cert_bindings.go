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
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newUsersCertBindingCmd groups the verbs that enrol, list and withdraw the
// mTLS certificate bindings of a user. Under the default
// auth.mtls_binding_policy ("required") a certificate authenticates as an
// account only once its SPKI thumbprint is enrolled on it, so these verbs are
// how an operator admits a certificate.
func newUsersCertBindingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cert-binding",
		Short: "Enrol, list and withdraw a user's mTLS certificate bindings",
		Long: "Enrol, list and withdraw the mTLS certificate bindings of a user. " +
			"Under the default auth.mtls_binding_policy (required) a certificate " +
			"authenticates as an account only once its SPKI thumbprint is enrolled on it.",
	}

	cmd.AddCommand(
		newUsersCertBindingAddCmd(),
		newUsersCertBindingListCmd(),
		newUsersCertBindingRemoveCmd(),
	)

	return cmd
}

// newUsersCertBindingAddCmd enrols a certificate binding on a user.
func newUsersCertBindingAddCmd() *cobra.Command {
	var (
		certFile string
		notAfter string
	)

	cmd := &cobra.Command{
		Use:   "add <user-id> [thumbprint]",
		Short: "Enrol a certificate's SPKI thumbprint on a user",
		Long: "Enrol a certificate binding on a user. Name the key by its base64url " +
			"SHA-256 SPKI thumbprint, or pass --cert with the PEM certificate and the " +
			"thumbprint, and by default the expiry, are taken from it. Enrolling an " +
			"identical binding again succeeds and changes nothing. A thumbprint is " +
			"base64url, so it can begin with '-': put every flag first and the " +
			"thumbprint after '--', which ends option parsing.",
		Example: "  qrdb users cert-binding add 42 --cert alice.pem\n" +
			"  qrdb users cert-binding add 42 --not-after 2030-01-02T03:04:05Z -- -mfgDIAA4OnmhPXEgQ0d46rNYBLntyNnLUbId3NJ_eg",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseUserID(args[0])
			if err != nil {
				return err
			}

			var thumbprint string
			var notAfterUnix int64
			switch {
			case len(args) == 2 && certFile != "":
				return &InvalidArgError{Arg: "thumbprint", Reason: "give a thumbprint or --cert, not both"}
			case len(args) == 2:
				thumbprint = args[1]
			case certFile != "":
				cert, cerr := readCertificatePEM(certFile)
				if cerr != nil {
					return cerr
				}
				thumbprint = spkiThumbprint(cert)
				notAfterUnix = cert.NotAfter.Unix()
				// A non-positive expiry is the wire's "never expires"
				// sentinel; a certificate's expiry must not become it.
				if notAfter == "" && notAfterUnix <= 0 {
					return &InvalidFlagValueError{Flag: "cert", Value: certFile, Want: "a certificate that expires after the Unix epoch"}
				}
			default:
				return &MissingArgError{Arg: "thumbprint or --cert"}
			}

			if notAfter != "" {
				ts, perr := time.Parse(time.RFC3339, notAfter)
				if perr != nil {
					return &InvalidFlagValueError{Flag: "not-after", Value: notAfter, Want: "an RFC 3339 timestamp"}
				}
				// The no-expiry sentinel is reserved for an omitted
				// expiry: an explicit date at or before the epoch would
				// otherwise enrol a permanent binding, or clear the
				// expiry of one already enrolled.
				if notAfterUnix = ts.Unix(); notAfterUnix <= 0 {
					return &InvalidFlagValueError{Flag: "not-after", Value: notAfter, Want: "an RFC 3339 timestamp after the Unix epoch"}
				}
			}

			u, err := client.Users().AddCertBinding(cmd.Context(), id, thumbprint, notAfterUnix)
			if err != nil {
				return err
			}
			return printCertBindings(u)
		},
	}

	cmd.Flags().StringVar(&certFile, "cert", "", "PEM certificate whose SPKI thumbprint and expiry to enrol, instead of a thumbprint argument")
	cmd.Flags().StringVar(&notAfter, "not-after", "", "When the binding stops authenticating, RFC 3339. Defaults to the --cert expiry; without --cert the binding never expires")

	return cmd
}

// newUsersCertBindingListCmd lists a user's certificate bindings, expired
// ones included.
func newUsersCertBindingListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <user-id>",
		Short: "List a user's certificate bindings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseUserID(args[0])
			if err != nil {
				return err
			}

			u, err := client.Users().GetUser(cmd.Context(), id)
			if err != nil {
				return err
			}
			return printCertBindings(u)
		},
	}
}

// newUsersCertBindingRemoveCmd withdraws a certificate binding from a user.
func newUsersCertBindingRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <user-id> <thumbprint>",
		Short: "Withdraw a certificate binding from a user",
		Long: "Withdraw a certificate binding from a user, so a certificate for that " +
			"key no longer authenticates as the account. Withdrawing an account's last " +
			"binding widens it under the enrolled policy, and under the required policy " +
			"the server refuses to withdraw the bootstrap admin's last live binding: " +
			"enrol the replacement first. A thumbprint is base64url, so it can begin " +
			"with '-': put every flag first and the thumbprint after '--', which ends " +
			"option parsing.",
		Example: "  qrdb users cert-binding remove 42 -- -mfgDIAA4OnmhPXEgQ0d46rNYBLntyNnLUbId3NJ_eg",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseUserID(args[0])
			if err != nil {
				return err
			}

			u, err := client.Users().RemoveCertBinding(cmd.Context(), id, args[1])
			if err != nil {
				return err
			}
			return printCertBindings(u)
		},
	}
}

// printCertBindings prints the certificate bindings of a user in the
// selected output format.
func printCertBindings(u *transport.User) error {
	bindings := u.CertBindings
	if bindings == nil {
		bindings = []transport.CertBinding{}
	}
	switch outputFormat {
	case "json":
		return printJSON(bindings)
	case "yaml":
		return printYAML(bindings)
	default:
		headers := []string{"THUMBPRINT", "KIND", "NOT_AFTER"}
		rows := make([][]string, 0, len(bindings))
		for _, b := range bindings {
			rows = append(rows, []string{b.Thumbprint, b.Kind, b.NotAfter})
		}
		printTable(headers, rows)
		return nil
	}
}

// readCertificatePEM reads and parses the first PEM certificate in path.
func readCertificatePEM(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &FileReadError{Path: path, Err: err}
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, &InvalidFlagValueError{Flag: "cert", Value: path, Want: "a PEM CERTIFICATE file"}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, &InvalidFlagValueError{Flag: "cert", Value: path, Want: "a parseable X.509 certificate"}
	}
	return cert, nil
}

// spkiThumbprint is the base64url (unpadded) SHA-256 of the certificate's
// SubjectPublicKeyInfo: the canonical binding thumbprint the server matches
// a presented certificate by.
func spkiThumbprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

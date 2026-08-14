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
		newCeremonyClaimCertCmd(),
		newCeremonyClaimShareCmd(),
		newCeremonySignCSRCmd(),
		newCeremonyClaimShareBeginCmd(),
		newCeremonyClaimShareCompleteCmd(),
		newCeremonySubmitUnsealShareCmd(),
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

// newCeremonyClaimCertCmd returns the ceremony claim-cert parent command
// with begin and complete subcommands.
func newCeremonyClaimCertCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claim-cert",
		Short: "Certificate claim operations",
		Long:  "Begin and complete certificate claim challenges for officers.",
	}

	cmd.AddCommand(
		newCeremonyClaimCertBeginCmd(),
		newCeremonyClaimCertCompleteCmd(),
	)

	return cmd
}

// newCeremonyClaimCertBeginCmd returns the ceremony claim-cert begin subcommand.
func newCeremonyClaimCertBeginCmd() *cobra.Command {
	var username string

	cmd := &cobra.Command{
		Use:   "begin",
		Short: "Begin a certificate claim challenge",
		Long:  "Start a certificate claim challenge for an officer, returning a nonce to sign.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Ceremony().CeremonyClaimCertBegin(cmd.Context(), &transport.ClaimCertBeginRequest{
				Username: username,
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
				_, _ = fmt.Fprintf(outputWriter, "NonceHex:  %s\n", resp.NonceHex)
				_, _ = fmt.Fprintf(outputWriter, "ExpiresAt: %s\n", resp.ExpiresAt)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "Officer username")
	_ = cmd.MarkFlagRequired("username")

	return cmd
}

// newCeremonyClaimCertCompleteCmd returns the ceremony claim-cert complete subcommand.
func newCeremonyClaimCertCompleteCmd() *cobra.Command {
	var (
		username  string
		nonceHex  string
		signature string
	)

	cmd := &cobra.Command{
		Use:   "complete",
		Short: "Complete a certificate claim challenge",
		Long:  "Submit the signed nonce to complete the certificate claim and receive the issued certificate.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Ceremony().CeremonyClaimCertComplete(cmd.Context(), &transport.ClaimCertCompleteRequest{
				Username:  username,
				NonceHex:  nonceHex,
				Signature: signature,
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
				_, _ = fmt.Fprintf(outputWriter, "CACertPEM:\n%s\n", resp.CACertPEM)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "Officer username")
	cmd.Flags().StringVar(&nonceHex, "nonce", "", "Challenge nonce hex from begin step")
	cmd.Flags().StringVar(&signature, "signature", "", "Base64-encoded signature of the nonce")
	_ = cmd.MarkFlagRequired("username")
	_ = cmd.MarkFlagRequired("nonce")
	_ = cmd.MarkFlagRequired("signature")

	return cmd
}

// newCeremonyClaimShareCmd returns the ceremony claim-share subcommand. Share
// claim is a two-round-trip challenge-response: "begin" issues a nonce bound to
// the caller's mTLS certificate thumbprint and "complete" exchanges a signature
// over that nonce for the share.
func newCeremonyClaimShareCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claim-share",
		Short: "Claim a Shamir share",
		Long:  "Claim the Shamir share for an officer over the nonce challenge-response handshake. Requires an mTLS client certificate matching the officer's enrolment record.",
	}

	cmd.AddCommand(
		newCeremonyBeginClaimShareCmd(),
		newCeremonyCompleteClaimShareCmd(),
	)

	return cmd
}

// newCeremonyBeginClaimShareCmd returns the ceremony claim-share begin
// subcommand. No thumbprint flag exists: the server derives the caller
// thumbprint from the mTLS peer certificate.
func newCeremonyBeginClaimShareCmd() *cobra.Command {
	var username string

	cmd := &cobra.Command{
		Use:   "begin",
		Short: "Start a Shamir share claim challenge",
		Long:  "Request a challenge nonce the officer must sign with the private key matching their enrolled certificate.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Ceremony().CeremonyBeginClaimShare(cmd.Context(), &transport.BeginClaimShareRequest{
				Username: username,
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
				_, _ = fmt.Fprintf(outputWriter, "Nonce: %s\n", resp.NonceHex)
				_, _ = fmt.Fprintf(outputWriter, "ExpiresAt: %s\n", resp.ExpiresAt)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "Officer username")
	_ = cmd.MarkFlagRequired("username")

	return cmd
}

// newCeremonyCompleteClaimShareCmd returns the ceremony claim-share complete
// subcommand.
func newCeremonyCompleteClaimShareCmd() *cobra.Command {
	var (
		username  string
		nonceHex  string
		signature string
	)

	cmd := &cobra.Command{
		Use:   "complete",
		Short: "Complete a Shamir share claim challenge",
		Long:  "Submit the signed nonce to complete the share claim and receive the Shamir share exactly once.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sig, decErr := base64.StdEncoding.DecodeString(signature)
			if decErr != nil {
				return &Base64DecodeError{Field: "signature", Err: decErr}
			}

			resp, err := client.Ceremony().CeremonyCompleteClaimShare(cmd.Context(), &transport.CompleteClaimShareRequest{
				Username:  username,
				NonceHex:  nonceHex,
				Signature: sig,
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
				_, _ = fmt.Fprintf(outputWriter, "ShareIndex: %d\n", resp.ShareIndex)
				_, _ = fmt.Fprintf(outputWriter, "ShareJSON: %s\n", resp.ShareJSON)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "Officer username")
	cmd.Flags().StringVar(&nonceHex, "nonce", "", "Challenge nonce hex from the begin step")
	cmd.Flags().StringVar(&signature, "signature", "", "Base64-encoded signature over the raw nonce bytes")
	_ = cmd.MarkFlagRequired("username")
	_ = cmd.MarkFlagRequired("nonce")
	_ = cmd.MarkFlagRequired("signature")

	return cmd
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

// newCeremonyClaimShareBeginCmd returns the ceremony claim-share-begin
// subcommand, which issues an authentication-factor challenge that a
// custodian must satisfy before their Shamir share is released.
func newCeremonyClaimShareBeginCmd() *cobra.Command {
	var custodianID string

	cmd := &cobra.Command{
		Use:   "claim-share-begin",
		Short: "Begin a factor-verified share claim",
		Long:  "Issue an authentication-factor challenge for a custodian prior to releasing their Shamir share.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Ceremony().CeremonyClaimShareBegin(cmd.Context(), &transport.CeremonyClaimShareBeginRequest{
				CustodianID: custodianID,
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
				_, _ = fmt.Fprintf(outputWriter, "CustodianID: %s\n", resp.CustodianID)
				_, _ = fmt.Fprintf(outputWriter, "Nonce: %s\n", base64.StdEncoding.EncodeToString(resp.Nonce))
				_, _ = fmt.Fprintf(outputWriter, "ExpiresAt: %s\n", resp.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"))
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&custodianID, "custodian-id", "", "Custodian identifier")
	_ = cmd.MarkFlagRequired("custodian-id")

	return cmd
}

// newCeremonyClaimShareCompleteCmd returns the ceremony
// claim-share-complete subcommand, which verifies the factor response and
// releases the custodian's Shamir share record.
func newCeremonyClaimShareCompleteCmd() *cobra.Command {
	var (
		custodianID    string
		challengeID    string
		factorResponse string
	)

	cmd := &cobra.Command{
		Use:   "claim-share-complete",
		Short: "Complete a factor-verified share claim",
		Long:  "Verify a custodian's authentication-factor response and release their Shamir share record.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			respBytes, err := base64.StdEncoding.DecodeString(factorResponse)
			if err != nil {
				return &Base64DecodeError{Field: "factor-response", Err: err}
			}
			info, err := client.Ceremony().CeremonyClaimShareComplete(cmd.Context(), &transport.CeremonyClaimShareCompleteRequest{
				CustodianID:    custodianID,
				ChallengeID:    challengeID,
				FactorResponse: respBytes,
			})
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(info)
			case "yaml":
				return printYAML(info)
			default:
				_, _ = fmt.Fprintf(outputWriter, "ID: %s\n", info.ID)
				_, _ = fmt.Fprintf(outputWriter, "Username: %s\n", info.Username)
				_, _ = fmt.Fprintf(outputWriter, "ShareIndex: %d\n", info.ShareIndex)
				_, _ = fmt.Fprintf(outputWriter, "ShareClaimed: %t\n", info.ShareClaimed)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&custodianID, "custodian-id", "", "Custodian identifier")
	cmd.Flags().StringVar(&challengeID, "challenge-id", "", "Challenge identifier from claim-share-begin")
	cmd.Flags().StringVar(&factorResponse, "factor-response", "", "Base64-encoded authentication-factor response")
	_ = cmd.MarkFlagRequired("custodian-id")
	_ = cmd.MarkFlagRequired("challenge-id")

	return cmd
}

// newCeremonySubmitUnsealShareCmd returns the ceremony
// submit-unseal-share subcommand, which submits a custodian's Shamir share
// toward the unseal quorum.
func newCeremonySubmitUnsealShareCmd() *cobra.Command {
	var (
		custodianID    string
		challengeID    string
		factorResponse string
		share          string
	)

	cmd := &cobra.Command{
		Use:   "submit-unseal-share",
		Short: "Submit a share toward the unseal quorum",
		Long:  "Submit a custodian's Shamir share toward the unseal quorum; the barrier unseals once the threshold is reached.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			shareBytes, err := base64.StdEncoding.DecodeString(share)
			if err != nil {
				return &Base64DecodeError{Field: "share", Err: err}
			}
			var factorBytes []byte
			if factorResponse != "" {
				factorBytes, err = base64.StdEncoding.DecodeString(factorResponse)
				if err != nil {
					return &Base64DecodeError{Field: "factor-response", Err: err}
				}
			}
			resp, err := client.Ceremony().CeremonySubmitUnsealShare(cmd.Context(), &transport.CeremonySubmitUnsealShareRequest{
				CustodianID:    custodianID,
				ChallengeID:    challengeID,
				FactorResponse: factorBytes,
				ShareBytes:     shareBytes,
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
				_, _ = fmt.Fprintf(outputWriter, "Submitted: %d\n", resp.Submitted)
				_, _ = fmt.Fprintf(outputWriter, "Required: %d\n", resp.Required)
				_, _ = fmt.Fprintf(outputWriter, "Unsealed: %t\n", resp.Unsealed)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&custodianID, "custodian-id", "", "Custodian identifier")
	cmd.Flags().StringVar(&challengeID, "challenge-id", "", "Challenge identifier from claim-share-begin")
	cmd.Flags().StringVar(&factorResponse, "factor-response", "", "Base64-encoded authentication-factor response (optional)")
	cmd.Flags().StringVar(&share, "share", "", "Base64-encoded Shamir share")
	_ = cmd.MarkFlagRequired("custodian-id")
	_ = cmd.MarkFlagRequired("share")

	return cmd
}

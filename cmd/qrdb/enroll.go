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
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newEnrollCmd creates the top-level "enroll" command: the redeemer's half
// of "qrdb users invite". It is deliberately NOT under "users" or "ca" --
// unlike every other command, it runs before its caller holds any
// credential at all, which is also why root.go's PersistentPreRunE skips
// building the ordinary connected client for it.
func newEnrollCmd() *cobra.Command {
	var (
		tenantID  string
		token     string
		username  string
		csrFile   string
		certFile  string
		chainFile string
		outDir    string
	)

	cmd := &cobra.Command{
		Use:   "enroll",
		Short: "Redeem an invitation: generate or bring a key, and activate your account",
		Long: `Redeem a one-time invitation token issued by "qrdb users invite". This
command makes no prior connection and takes no --cert-file/--key-file: you
hold no credential yet, which is the whole point -- the inviter never
touches your key.

  --csr FILE   submit an existing CSR (its key stays with whoever made it)
  (no --csr)   generate a fresh key here, write key.pem and cert.pem to --out
  --cert FILE  bring your own certificate over the CSR's key instead of
               letting the tenant's CA sign it (--chain for its intermediates)

--server and --transport select where to redeem: every transport carries
this verb (rest, grpc, quic, mcp and unix).`,
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

			var certPEM, chainPEM []byte
			if certFile != "" {
				var err error
				if certPEM, err = os.ReadFile(certFile); err != nil {
					return &FileReadError{Path: certFile, Err: err}
				}
				if chainFile != "" {
					if chainPEM, err = os.ReadFile(chainFile); err != nil {
						return &FileReadError{Path: chainFile, Err: err}
					}
				}
			}

			invitations, closeTransport, err := newInvitationClient(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = closeTransport() }()

			enrolled, err := invitations.RedeemInvitation(cmd.Context(), &transport.RedeemInvitationRequest{
				TenantID: tenantID,
				Token:    token,
				CSRPEM:   string(csrPEM),
				CertPEM:  string(certPEM),
				ChainPEM: string(chainPEM),
			})
			if err != nil {
				return err
			}

			return finishEnrolledCert(outDir, keyPEM, enrolled)
		},
	}

	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "The tenant the invitation was issued in")
	_ = cmd.MarkFlagRequired("tenant-id")
	cmd.Flags().StringVar(&token, "token", "", "The one-time invitation token")
	_ = cmd.MarkFlagRequired("token")
	cmd.Flags().StringVar(&username, "username", "", "Common name for a freshly generated key's CSR (cosmetic only: your account comes from the invitation, never from the CSR)")
	cmd.Flags().StringVar(&csrFile, "csr", "", "A PEM certificate signing request to submit, over a key of your own")
	cmd.Flags().StringVar(&certFile, "cert", "", "Optional: your own PEM certificate over the CSR's key, leaf first")
	cmd.Flags().StringVar(&chainFile, "chain", "", "Optional PEM intermediates for --cert")
	cmd.Flags().StringVar(&outDir, "out", "", "Output directory for cert.pem (and key.pem when the key is generated here)")

	return cmd
}

// newInvitationClient builds a transport.InvitationClient from the root
// persistent flags (--server, --transport, --ca-file, --timeout). See
// buildInvitationClient for why it connects no differently than any other
// call: RedeemInvitation is the one call a transport serves to an
// unconnected, uncredentialed caller.
func newInvitationClient(cmd *cobra.Command) (transport.InvitationClient, func() error, error) {
	root := cmd.Root().PersistentFlags()
	serverFlag, _ := root.GetString("server")
	transportFlag, _ := root.GetString("transport")
	caFile, _ := root.GetString("ca-file")
	timeout, _ := root.GetDuration("timeout")

	if transportFlag == transportEmbedded {
		return nil, nil, &EmbeddedTransportUnavailableError{}
	}

	effectiveTransport := transportFlag
	if !root.Changed("transport") && serverHasScheme(serverFlag) {
		effectiveTransport = ""
	}
	transportType, address := resolveTransport(serverFlag, effectiveTransport)

	return buildInvitationClient(transportType, address, caFile, timeout)
}

// buildInvitationClient builds a transport.InvitationClient for transportType
// against address, without connecting: RedeemInvitation is the one call a
// transport serves to an unconnected, uncredentialed caller (see
// transport.InvitationClient), and requiring an ordinary Connect first would
// defeat the whole point on mcp, where the main endpoint answers nothing --
// not even initialize -- to a caller with no credential. The returned closer
// releases the transport's resources; it is never a sealed/authenticated
// connection to tear down.
//
// Shared by the CLI's "enroll" command (newInvitationClient, above) and the
// desktop GUI's InvitationBinding.Redeem, which has no cobra flags to read
// and so calls this directly with the server/transport/CA path the redeemer
// typed into the enrolment dialog.
func buildInvitationClient(transportType, address, caFile string, timeout time.Duration) (transport.InvitationClient, func() error, error) {
	if transportType == transportEmbedded {
		return nil, nil, &EmbeddedTransportUnavailableError{}
	}

	var opts []transport.Option
	opts = append(opts, transport.WithAddress(address))
	if timeout > 0 {
		opts = append(opts, transport.WithTimeout(timeout))
	}
	if caFile != "" {
		opts = append(opts, transport.WithTLS("", "", caFile))
	}

	t, err := newTransport(transportType, opts...)
	if err != nil {
		return nil, nil, &TransportError{Transport: transportType, Err: err}
	}

	invitations, ok := t.(transport.InvitationClient)
	if !ok {
		_ = t.Close()
		return nil, nil, &transport.CapabilityUnsupportedError{
			Message: transportType + ": invitation redemption is not carried on this transport",
		}
	}
	return invitations, t.Close, nil
}

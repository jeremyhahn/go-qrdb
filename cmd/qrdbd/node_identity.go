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

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
)

// newNodeIdentityCmd returns the node-identity command: it prints the Raft
// member identity of a node certificate file, the value the other replicas
// list for that node in --member-identities / raft.member_identities.
//
// It reads a certificate — public material — and needs no private key, so an
// operator can collect every node's identity from the certificates the CA
// issued before any node starts.
func newNodeIdentityCmd() *cobra.Command {
	var certFile string
	cmd := &cobra.Command{
		Use:   "node-identity",
		Short: "Print the Raft member identity of a node certificate",
		Long: "Print the Raft member identity (spki:<sha256-hex> of the certificate's public key) " +
			"of a node's transport certificate. Every other replica lists it in --member-identities " +
			"as <replica-id>=<identity>.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			certPEM, err := os.ReadFile(certFile)
			if err != nil {
				return &cluster.MemberIdentityCertificateError{Reason: "read " + certFile, Err: err}
			}
			identity, err := cluster.MemberIdentityFromPEM(certPEM)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), identity)
			return err
		},
	}
	cmd.Flags().StringVar(&certFile, "cert", "", "Path to the node's PEM transport certificate (its --tls-cert-file)")
	_ = cmd.MarkFlagRequired("cert")
	return cmd
}

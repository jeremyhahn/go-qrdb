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

// ca.go implements the canonical "ca" command group per
// docs/08-bootstrap-auth-architecture.md §6 ("certs" group renamed to
// "ca"). The command is the canonical post-init CA surface and
// re-uses the existing certs subcommand handlers via SDK delegation.
package main

import (
	"github.com/spf13/cobra"
)

// newCACmd builds the "ca" command group. It exposes:
//
//	info | bundle | list | issue | revoke | export-bundle
//
// per the locked canonical CLI surface.
//
// The command names "issue", "revoke", "list", "export-bundle" map
// 1:1 to the existing certs handlers. "info" and "bundle" are currently
// aliases of "list" (issued-certificate listing): standalone root-CA
// introspection (DN/fingerprint/validity) and a CA+chain bundle export are
// not yet exposed through the SDK CertClient contract on any transport
// (pkg/cert.CertProvider.CACertPEM() exists server-side, but the CLI must
// go through the SDK — see cmd/qrdb/CLAUDE.md — and adding it there is a
// cross-transport (REST/gRPC/QUIC/MCP/unix) SDK surface change, not a CLI
// fix). Until that ships, "info" and "bundle" are documented here as what
// they actually do today rather than advertising CA-only semantics they
// do not deliver.
func newCACmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ca",
		Short: "CA operations (info, bundle, list, issue, enroll, csr, revoke, export-bundle)",
		Long: `Post-init Certificate Authority operations.

The ca command group is the canonical surface for CA management per
docs/08-bootstrap-auth-architecture.md §6. It replaces the legacy
"certs" group. Subcommands:

  info           Alias for "list": list issued certificates (CA-only
                 metadata such as DN/fingerprint/validity is not yet
                 exposed through the SDK; see newCACmd doc comment)
  bundle         Alias for "list": list issued certificates (a standalone
                 CA+chain bundle export is not yet exposed through the
                 SDK; see newCACmd doc comment)
  list           List issued certificates
  issue          Issue a client certificate for an account of your tenant
                 (from --csr, or a key generated here; the key never leaves
                 this machine)
  enroll         Enrol a bring-your-own certificate on an account of your
                 tenant (it must chain to one of the tenant's trust anchors)
  csr            Generate a key and a CSR locally (no server involved)
  revoke         Revoke an issued certificate by serial
  export-bundle  Export a cert+key bundle as PKCS#12`,
	}

	cmd.AddCommand(newCAInfoCmd())
	cmd.AddCommand(newCABundleCmd())
	cmd.AddCommand(newCAListCmd())
	cmd.AddCommand(newCAIssueCmd())
	cmd.AddCommand(newCAEnrollCmd())
	cmd.AddCommand(newCACSRCmd())
	cmd.AddCommand(newCARevokeCmd())
	cmd.AddCommand(newCAExportBundleCmd())
	return cmd
}

// newCAInfoCmd is a thin alias around the certs list handler. It does NOT
// yet return CA-only metadata (DN/fingerprint/validity); see the newCACmd
// doc comment for why.
func newCAInfoCmd() *cobra.Command {
	cmd := newCertsListCmd()
	cmd.Use = "info"
	cmd.Short = "List issued certificates (alias for \"list\"; CA-only metadata not yet available)"
	return cmd
}

// newCABundleCmd is a thin alias around the certs list handler. It does NOT
// yet return a standalone CA+chain bundle; see the newCACmd doc comment for
// why.
func newCABundleCmd() *cobra.Command {
	cmd := newCertsListCmd()
	cmd.Use = "bundle"
	cmd.Short = "List issued certificates (alias for \"list\"; CA+chain bundle export not yet available)"
	return cmd
}

// newCAListCmd is a thin alias for "certs list".
func newCAListCmd() *cobra.Command {
	cmd := newCertsListCmd()
	cmd.Use = "list"
	return cmd
}

// newCAIssueCmd is "certs issue" under the canonical group.
func newCAIssueCmd() *cobra.Command {
	return newCertsIssueCmd()
}

// newCAEnrollCmd is "certs enroll" under the canonical group.
func newCAEnrollCmd() *cobra.Command {
	return newCertsEnrollCmd()
}

// newCACSRCmd is "certs csr" under the canonical group.
func newCACSRCmd() *cobra.Command {
	return newCertsCSRCmd()
}

// newCARevokeCmd is a thin alias for "certs revoke".
func newCARevokeCmd() *cobra.Command {
	cmd := newCertsRevokeCmd()
	cmd.Use = "revoke"
	return cmd
}

// newCAExportBundleCmd is a thin alias for "certs export" renamed to
// the canonical "export-bundle".
func newCAExportBundleCmd() *cobra.Command {
	cmd := newCertsExportCmd()
	cmd.Use = "export-bundle"
	cmd.Short = "Export a cert+key bundle as PKCS#12"
	return cmd
}

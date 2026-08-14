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

// certs_redirect.go preserves the legacy "qrdb certs" command path so
// operators running scripts against the renamed "ca" group see a clear
// migration error rather than "unknown command".
package main

import (
	"github.com/spf13/cobra"
)

// newCertsRedirectCmd builds a stub "certs" command tree that emits
// CanonicalRedirectError for every subcommand. Per
// docs/08-bootstrap-auth-architecture.md §6 the "certs" group is
// renamed to "ca".
func newCertsRedirectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "certs",
		// A deprecation stub must emit its redirect without first trying
		// to connect to a server: the redirect is the whole point, and it
		// has to work even when no daemon is reachable.
		Annotations: map[string]string{annotationSkipClient: "true"},
		Short:       "RENAMED: use the 'ca' command group instead",
		Long: `The "qrdb certs" group has been renamed to "qrdb ca" per
docs/08-bootstrap-auth-architecture.md §6. Subcommand mapping:

  qrdb certs generate  ->  qrdb ca issue
  qrdb certs export    ->  qrdb ca export-bundle
  qrdb certs list      ->  qrdb ca list
  qrdb certs revoke    ->  qrdb ca revoke

This stub remains for runbook discoverability and emits a typed
error pointing at the new path.`,
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return &CanonicalRedirectError{Old: "qrdb certs", New: "qrdb ca"}
		},
	}
	return cmd
}

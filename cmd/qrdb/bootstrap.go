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

// bootstrap.go implements the canonical "bootstrap" client-side TOFU
// command group per docs/08-bootstrap-auth-architecture.md §6.
//
// "init" is server-side first-touch ceremony; "bootstrap" establishes
// CA trust to a remote node from a fresh client. The two are distinct
// commands by design.
package main

import (
	"github.com/spf13/cobra"
)

// newBootstrapCmd builds the canonical "bootstrap" command group.
//
// auto    Pick the strongest available trust mode automatically.
// dane    DANE TLSA pinning.
// noise   Noise XK out-of-band trust establishment.
// spki    SPKI SHA-256 pin.
// direct  Direct CA bundle copy (operator-supplied trust root).
//
// The command surface mirrors the xkmsctl bootstrap group plus the
// new "direct" mode required by the locked canonical surface.
func newBootstrapCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Trust bootstrap (auto, dane, noise, spki, direct)",
		Long: `Trust bootstrap for a fresh client establishing CA trust to a remote
node. Distinct from "init" which is server-side first-touch.

  auto    Pick the strongest available trust mode automatically
  dane    DANE TLSA pinning (RFC 6698)
  noise   Noise XK out-of-band trust handshake
  spki    SPKI SHA-256 pin
  direct  Direct CA bundle copy (operator-supplied trust root)`,
	}

	cmd.AddCommand(newBootstrapModeCmd("auto", "Auto-pick the strongest available trust mode"))
	cmd.AddCommand(newBootstrapModeCmd("dane", "DANE TLSA pinning"))
	cmd.AddCommand(newBootstrapModeCmd("noise", "Noise XK out-of-band trust handshake"))
	cmd.AddCommand(newBootstrapModeCmd("spki", "SPKI SHA-256 pin"))
	cmd.AddCommand(newBootstrapModeCmd("direct", "Direct CA bundle copy"))
	return cmd
}

// newBootstrapModeCmd creates a single bootstrap mode subcommand. The
// underlying TOFU implementation lives in xkms; qrdb exposes the
// command shape so the canonical surface is identical across binaries.
func newBootstrapModeCmd(mode, short string) *cobra.Command {
	return &cobra.Command{
		Use:   mode,
		Short: short,
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return &PendingBootstrapBackendError{Mode: mode}
		},
	}
}

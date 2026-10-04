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

//go:build integration

// bootstrap_cli_test.go drives `qrdb bootstrap {auto,dane,noise,spki,direct}`
// -- the client-side TOFU trust-establishment command group -- across
// every CLI-selectable protocol.
//
// Standalone-qrdb contract (verified by reading cmd/qrdb/bootstrap.go, not
// assumed): every bootstrap mode subcommand is a pure stub. newBootstrapModeCmd's
// RunE ignores both args and any parsed flags and unconditionally returns
// &PendingBootstrapBackendError{Mode: mode} -- it never calls the package-level
// `client` (the SDK connection built by root.go's PersistentPreRunE) at all. The
// real Noise-XK/DANE/SPKI-pin/direct-CA-bundle TOFU implementations live in
// go-xkms per this repo's locked layer ownership (see CLAUDE.md's "Layer
// Ownership" section: qrdb explicitly does NOT ship trust-bootstrap dispatch).
// This file therefore asserts the DOCUMENTED stub contract -- a typed,
// consistent redirect error naming the mode -- rather than any positive TOFU
// behavior qrdb does not implement.
//
// Because root.go's PersistentPreRunE builds and connects the SDK client
// BEFORE any subcommand's own RunE executes, `qrdb bootstrap <mode>` still
// requires a reachable daemon on the selected protocol even though the
// handler itself never uses the connection. This file reuses
// multi_protocol_helpers_test.go's startMultiProtocolNode fixture (already
// initialized, unsealed, serving all five transports) purely as that
// reachable target.
package integration_test

import (
	"strings"
	"testing"
)

// bootstrapModes is the canonical set of `qrdb bootstrap` subcommands per
// cmd/qrdb/bootstrap.go's newBootstrapCmd.
var bootstrapModes = []string{"auto", "dane", "noise", "spki", "direct"}

// TestBootstrap_CLI_PerProtocol drives every bootstrap mode against every
// CLI-selectable protocol and asserts the typed PendingBootstrapBackendError
// redirect message, proving the stub contract is identical regardless of
// which transport carried the (otherwise unused) connection.
func TestBootstrap_CLI_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			for _, mode := range bootstrapModes {
				t.Run(mode, func(t *testing.T) {
					out, err := runCLIProto(t, node, proto, "bootstrap", mode)
					if err == nil {
						t.Fatalf("expected qrdb bootstrap %s to fail (stub); output: %s", mode, out)
					}
					want := "bootstrap " + mode + " is owned by xkms; invoke via xkmsctl bootstrap " + mode +
						" or platformctl bootstrap " + mode
					if !strings.Contains(out, want) {
						t.Errorf("[%s/%s] expected output to contain %q, got: %s", proto.name, mode, want, out)
					}
				})
			}
		})
	}
}

// TestBootstrap_CLI_ArbitraryPositionalArgs proves cobra.ArbitraryArgs on
// every bootstrap mode subcommand tolerates trailing positional arguments
// (e.g. a target host/address an operator might intuitively pass) without
// changing the stub's error contract. Exercised on Unix and MCP -- the two
// protocols with no other bootstrap coverage of any kind elsewhere in this
// package -- rather than repeated across all five, since positional-arg
// tolerance is parsed entirely client-side by cobra before RunE and is
// therefore transport-independent.
func TestBootstrap_CLI_ArbitraryPositionalArgs(t *testing.T) {
	node := startMultiProtocolNode(t)
	protocols := []multiProtocol{
		{name: "unix", scheme: "unix"},
		{name: "mcp", scheme: "mcp"},
	}

	for _, proto := range protocols {
		t.Run(proto.name, func(t *testing.T) {
			out, err := runCLIProto(t, node, proto, "bootstrap", "direct", "node.example.com:63002", "extra-arg")
			if err == nil {
				t.Fatalf("expected qrdb bootstrap direct with positional args to fail (stub); output: %s", out)
			}
			if !strings.Contains(out, "bootstrap direct is owned by xkms") {
				t.Errorf("[%s] expected the stub redirect message even with positional args, got: %s", proto.name, out)
			}
		})
	}
}

// TestBootstrap_CLI_UndocumentedFlagsRejected pins the CURRENT, fully
// intentional flag surface of a bootstrap mode subcommand: newBootstrapModeCmd
// (cmd/qrdb/bootstrap.go) registers Args: cobra.ArbitraryArgs (positional
// args only) and calls cmd.Flags() nowhere, so no mode registers any flag at
// all -- not "spki --pin" or "direct --ca-bundle" or anything else, and
// bootstrap.go's own Long help text does not advertise any either. Passing
// one therefore fails cobra's flag parsing with "unknown flag" BEFORE RunE
// ever runs, which is a different, less specific error than the documented
// PendingBootstrapBackendError stub every other case in this file asserts.
// This is consistent with the mode being an intentional not-yet-implemented
// stub (a flag with no consumer would be premature), so this test pins the
// OBSERVED behavior rather than treating it as a defect to fix.
func TestBootstrap_CLI_UndocumentedFlagsRejected(t *testing.T) {
	node := startMultiProtocolNode(t)
	proto := multiProtocol{name: "rest", scheme: "https"}

	out, err := runCLIProto(t, node, proto, "bootstrap", "spki", "--pin", "deadbeef")
	if err == nil {
		t.Fatalf("expected qrdb bootstrap spki --pin to fail (flag not registered); output: %s", out)
	}
	if !strings.Contains(out, "unknown flag") {
		t.Errorf("expected cobra's 'unknown flag' parse error (no mode subcommand in "+
			"cmd/qrdb/bootstrap.go registers a --pin flag, or any flag at all), got: %s", out)
	}
}

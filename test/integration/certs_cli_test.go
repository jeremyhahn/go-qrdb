// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

// certs_cli_test.go drives the `qrdb certs` COMMAND TREE (generate, export,
// revoke, list) through the real client CLI binary (cmd/qrdb), against every
// transport (REST, gRPC, QUIC, MCP, Unix) sharing the
// multi_protocol_helpers_test.go harness.
//
// "qrdb certs" is NOT a functional command surface: per
// docs/08-bootstrap-auth-architecture.md §6 it was renamed to "qrdb ca"
// (exercised in ca_cli_test.go), and cmd/qrdb/certs_redirect.go replaced it
// with a deprecation stub that emits CanonicalRedirectError for every
// subcommand -- without even attempting to connect to a server
// (Annotations[annotationSkipClient] = "true", DisableFlagParsing = true,
// Args = cobra.ArbitraryArgs). This file asserts that documented contract:
// every "certs" subcommand fails identically over every transport (the
// --server target is irrelevant since the stub never dials it), confirming
// the redirect is uniformly wired regardless of which transport the caller
// happens to have configured.
package integration_test

import (
	"strings"
	"testing"
)

// TestCerts_CLI_Redirect_PerProtocol drives every documented `qrdb certs`
// subcommand through the CLI binary against every transport sharing one
// multiProtoNode, asserting each one emits the canonical "certs" -> "ca"
// redirect error rather than attempting the legacy operation.
func TestCerts_CLI_Redirect_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testCertsRedirectOverProtocol(t, node, proto)
		})
	}
}

func testCertsRedirectOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	cases := []struct {
		name string
		args []string
	}{
		{name: "generate", args: []string{"certs", "generate", "--username", "someone"}},
		{name: "export", args: []string{"certs", "export", "--cert-pem", "/nonexistent/cert.pem", "--key-pem", "/nonexistent/key.pem", "--passphrase", "x", "--out", "/nonexistent/out.p12"}},
		{name: "revoke", args: []string{"certs", "revoke", "--serial", "deadbeef"}},
		{name: "list", args: []string{"certs", "list"}},
		{name: "no-args", args: []string{"certs"}},
	}

	for _, tc := range cases {
		out, err := runCLIProto(t, node, proto, tc.args...)
		if err == nil {
			t.Fatalf("[%s] certs %s should fail with the canonical redirect error, got: %s", proto.name, tc.name, out)
		}
		if !strings.Contains(out, "qrdb certs") || !strings.Contains(out, "qrdb ca") {
			t.Fatalf("[%s] certs %s error should name both the deprecated 'qrdb certs' path and the canonical 'qrdb ca' replacement, got: %s", proto.name, tc.name, out)
		}
	}
}

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

// invitation_cli_test.go drives `qrdb users invite` (authenticated) and
// `qrdb enroll` (the PUBLIC redeemer's half, no --cert-file/--key-file at
// all) through the real client CLI binary against every transport sharing
// the multi_protocol_helpers_test.go harness: invitation self-enrolment is
// carried on all five (REST, gRPC, QUIC, MCP, Unix).
package integration_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// invitationCLIJSON mirrors transport.Invitation, decoded from the unwrapped
// JSON `users invite` print.
type invitationCLIJSON struct {
	ID                uint64 `json:"id,string"`
	Token             string `json:"token"`
	ExpiresAt         string `json:"expiresAt"`
	UserID            uint64 `json:"userID,string"`
	Username          string `json:"username"`
	TenantID          string `json:"tenantID"`
	Created           bool   `json:"created"`
	KeyPinFingerprint string `json:"keyPinFingerprint,omitempty"`
}

// invitationProtocols is every transport that carries invitation
// self-enrolment end to end.
var invitationProtocols = []multiProtocol{
	{name: "rest", scheme: "https"},
	{name: "grpc", scheme: "grpcs"},
	{name: "quic", scheme: "quics"},
	{name: "mcp", scheme: "mcp"},
	{name: "unix", scheme: "unix"},
}

// unauthenticatedArgsFor returns the root-level qrdb CLI flags that select
// the given protocol against node with NO client certificate at all --
// unlike cliArgsFor, this is the redeemer's half, who holds no credential
// yet.
func unauthenticatedArgsFor(node *multiProtoNode, proto multiProtocol) []string {
	return []string{
		"--server", node.serverURLFor(proto),
		"--ca-file", node.ca.CertPath,
		"--timeout", "20s",
	}
}

// TestInvitation_CLI_InviteAndEnroll_PerProtocol drives the full invite ->
// enroll round trip through the CLI binary against every transport.
func TestInvitation_CLI_InviteAndEnroll_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range invitationProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testInvitationRoundTripOverProtocol(t, node, proto)
		})
	}
}

func testInvitationRoundTripOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	username := fmt.Sprintf("inv-%s-%d", proto.name, time.Now().UnixNano())
	email := username + "@invitation-cli-test.example"

	// --- invite: a fresh account, as JSON ----------------------------------
	out, err := runCLIProto(t, node, proto, "--output", "json", "users", "invite",
		"--username", username,
		"--email", email,
	)
	if err != nil {
		t.Fatalf("[%s] users invite: %v\noutput: %s", proto.name, err, out)
	}
	var inv invitationCLIJSON
	if jerr := json.Unmarshal([]byte(out), &inv); jerr != nil {
		t.Fatalf("[%s] decode users invite JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if inv.Token == "" || !inv.Created || inv.Username != username || inv.TenantID == "" {
		t.Fatalf("[%s] users invite result = %+v, want a token, Created=true, username %q and a non-empty tenant", proto.name, inv, username)
	}

	// --- re-invite: username alone, the SAME still-pending account, a FRESH
	// token, Created=false --------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "users", "invite", "--username", username)
	if err != nil {
		t.Fatalf("[%s] users invite (re-invite): %v\noutput: %s", proto.name, err, out)
	}
	var reinvite invitationCLIJSON
	if jerr := json.Unmarshal([]byte(out), &reinvite); jerr != nil {
		t.Fatalf("[%s] decode re-invite JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if reinvite.Created {
		t.Fatalf("[%s] re-invite Created = true, want false (account already existed pending)", proto.name)
	}
	if reinvite.UserID != inv.UserID {
		t.Fatalf("[%s] re-invite UserID = %d, want the same account %d", proto.name, reinvite.UserID, inv.UserID)
	}
	if reinvite.Token == inv.Token {
		t.Fatalf("[%s] re-invite returned the SAME token as the original invite; it must be fresh", proto.name)
	}

	// --- enroll: redeem the fresh (re-invite) token with NO credential -----
	// at all -- no --cert-file/--key-file -- generating a key locally. This
	// calls runCLI directly, NOT runCLIProto: runCLIProto prepends the
	// admin's mTLS identity (cliArgsFor), which would defeat the entire
	// point of this call.
	outDir := t.TempDir()
	out, err = runCLI(t, node.cli, unauthenticatedEnrollArgs(node, proto, reinvite.TenantID, reinvite.Token, username, outDir)...)
	if err != nil {
		t.Fatalf("[%s] enroll: %v\noutput: %s", proto.name, err, out)
	}
	certPath := filepath.Join(outDir, "cert.pem")
	keyPath := filepath.Join(outDir, "key.pem")
	certPEM, statErr := os.ReadFile(certPath)
	if statErr != nil {
		t.Fatalf("[%s] enroll did not write %s: %v", proto.name, certPath, statErr)
	}
	if !strings.Contains(string(certPEM), "BEGIN CERTIFICATE") {
		t.Fatalf("[%s] enroll wrote a cert.pem with no certificate: %s", proto.name, certPEM)
	}
	if _, statErr := os.Stat(keyPath); statErr != nil {
		t.Fatalf("[%s] enroll did not write %s: %v", proto.name, keyPath, statErr)
	}

	// --- redeeming the SAME token again is refused: single-use -------------
	out, err = runCLI(t, node.cli, unauthenticatedEnrollArgs(node, proto, reinvite.TenantID, reinvite.Token, username, t.TempDir())...)
	if err == nil {
		t.Fatalf("[%s] enroll (already redeemed) succeeded, want a refusal: %s", proto.name, out)
	}

	// --- redeeming an unknown token gets the SAME opaque refusal shape ------
	out, err = runCLI(t, node.cli, unauthenticatedEnrollArgs(node, proto, reinvite.TenantID, "not-a-real-token", username, t.TempDir())...)
	if err == nil {
		t.Fatalf("[%s] enroll (unknown token) succeeded, want a refusal: %s", proto.name, out)
	}
}

// unauthenticatedEnrollArgs builds the full "enroll" CLI invocation against
// proto with no client certificate, redeeming token in tenantID. It relies
// entirely on --server's URL scheme to select the transport (no explicit
// --transport), the same auto-detect contract cliArgsFor's callers use: a
// scheme-prefixed --server combined with an explicit --transport is a
// different, bare-address contract (resolveTransport), and mixing the two
// here would hand quic/mcp's dialer a server value still carrying its
// scheme prefix.
func unauthenticatedEnrollArgs(node *multiProtoNode, proto multiProtocol, tenantID, token, username, outDir string) []string {
	args := append([]string{}, unauthenticatedArgsFor(node, proto)...)
	args = append(args,
		"enroll",
		"--tenant-id", tenantID,
		"--token", token,
		"--username", username,
		"--out", outDir,
	)
	return args
}

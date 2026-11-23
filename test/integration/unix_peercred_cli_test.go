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

//go:build integration && linux

package integration_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestUnixPeerCredential_CLI_BindsMappedUser drives the real qrdbd and CLI
// binaries over the Unix socket with no client certificate: the kernel's
// SO_PEERCRED uid of the test process is mapped by unix.peer_credentials to
// an account, and that account's roles -- never the mapping -- decide.
//
//  1. Before the account exists the certificate-less caller holds no roles:
//     a read is refused.
//  2. Once the admin creates the account with the built-in "operator" role, the
//     same certificate-less caller reads the key the admin wrote.
//  3. An operation the "operator" role does not grant (creating a user) is
//     still refused: the mapping confers nothing beyond the account's roles.
func TestUnixPeerCredential_CLI_BindsMappedUser(t *testing.T) {
	const account = "pc-cli-user"
	extraYAML := fmt.Sprintf("unix:\n  peer_credentials:\n    - uid: %d\n      tenant: system\n      user: %s\n", os.Getuid(), account)
	node := startMultiProtocolNodeWithConfig(t, "", extraYAML)

	var unixProto multiProtocol
	for _, p := range multiProtocols {
		if p.name == "unix" {
			unixProto = p
		}
	}
	peerCLI := func(args ...string) (string, error) {
		t.Helper()
		full := append([]string{"--server", node.serverURLFor(unixProto), "--ca-file", node.ca.CertPath, "--timeout", "20s"}, args...)
		return runCLI(t, node.cli, full...)
	}

	const key, value = "peercred-cli-key", "peercred-cli-value"
	if out, err := runCLIProto(t, node, unixProto, "kv", "put", key, value); err != nil {
		t.Fatalf("admin kv put: %v\n%s", err, out)
	}

	if out, err := peerCLI("kv", "get", key); err == nil {
		t.Fatalf("certificate-less kv get before the mapped account exists succeeded, want refused:\n%s", out)
	}

	// The built-in operator role holds the kv data verbs and no users:create.
	userRole := builtInRoleID(t, node, unixProto, "operator")
	if out, err := runCLIProto(t, node, unixProto, "--output", "json", "users", "create", "--without-email-login-key",
		"--username", account, "--roles", userRole); err != nil {
		t.Fatalf("admin users create %s: %v\n%s", account, err, out)
	}

	out, err := peerCLI("kv", "get", key)
	if err != nil {
		t.Fatalf("certificate-less kv get by the mapped uid: %v, want the bound account's role to admit it\n%s", err, out)
	}
	if !strings.Contains(out, value) {
		t.Fatalf("certificate-less kv get printed %q, want %q", out, value)
	}

	if out, err := peerCLI("users", "create", "--without-email-login-key", "--username", "pc-cli-escalation"); err == nil {
		t.Fatalf("certificate-less users create by the mapped uid succeeded, want refused (the operator role grants no users:create):\n%s", out)
	}
}

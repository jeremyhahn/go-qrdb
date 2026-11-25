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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// TestUnixPeerCredential_CLI_BindsMappedUser drives the real qrdbd and CLI
// binaries over the Unix socket with no client certificate: the kernel's
// SO_PEERCRED uid of the test process is mapped by unix.peer_credentials to
// an account, and that account's roles -- never the mapping -- decide.
//
//  1. Before the account exists the certificate-less caller is refused at
//     authentication (peer_credential_account_missing): a mapping whose
//     account does not resolve identifies no one.
//  2. Once the admin creates the account with the built-in "operator" role, the
//     same certificate-less caller reads the key the admin wrote.
//  3. An operation the "operator" role does not grant (creating a user) is
//     still refused: the mapping confers nothing beyond the account's roles.
//  4. The admin stores an ABAC policy allowing reads to any authenticated
//     subject, then deletes the account: the certificate-less read is refused
//     at authentication again, so deleting the mapped account revokes access
//     even under an authenticated-subject allow.
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

	// requireAccountMissing fails unless the certificate-less read is refused
	// at authentication: the mapped account does not resolve.
	requireAccountMissing := func(when string) {
		t.Helper()
		out, err := peerCLI("kv", "get", key)
		if err == nil {
			t.Fatalf("certificate-less kv get %s succeeded, want refused:\n%s", when, out)
		}
		if !strings.Contains(out, "peer_credential_account_missing") && !strings.Contains(out, auth.ErrPeerCredentialAccountMissing.Message) {
			t.Fatalf("certificate-less kv get %s refused with %q, want the peer_credential_account_missing authentication refusal", when, out)
		}
	}

	requireAccountMissing("before the mapped account exists")

	// The built-in operator role holds the kv data verbs and no users:create.
	userRole := builtInRoleID(t, node, unixProto, "operator")
	createdOut, err := runCLIProto(t, node, unixProto, "--output", "json", "users", "create", "--without-email-login-key",
		"--username", account, "--roles", userRole)
	if err != nil {
		t.Fatalf("admin users create %s: %v\n%s", account, err, createdOut)
	}
	var created userCLIJSON
	if jerr := json.Unmarshal([]byte(createdOut), &created); jerr != nil || created.ID == 0 {
		t.Fatalf("decode users create JSON: %v\noutput: %s", jerr, createdOut)
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

	// 4. An authenticated-subject ABAC allow, then the account is deleted.
	doc := filepath.Join(t.TempDir(), "authenticated.yaml")
	if err := os.WriteFile(doc, []byte(peerCredAuthenticatedAllowDocument), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLIProto(t, node, unixProto, "abac", "policy", "create", "--file", doc); err != nil {
		t.Fatalf("admin abac policy create: %v\n%s", err, out)
	}
	if out, err := runCLIProto(t, node, unixProto, "users", "delete", strconv.FormatUint(created.ID, 10)); err != nil {
		t.Fatalf("admin users delete %s: %v\n%s", account, err, out)
	}
	requireAccountMissing("after the mapped account was deleted, under an authenticated-subject ABAC allow")
}

// peerCredAuthenticatedAllowDocument is an ABAC policy whose one rule allows
// a read of any resource to any authenticated subject.
const peerCredAuthenticatedAllowDocument = `apiVersion: abac.qrdb.io/v1
kind: Policy
metadata:
  name: peercred-authenticated-allow
  priority: 10
spec:
  enabled: true
  subjects:
    any: true
  rules:
    - name: authenticated-reads
      effect: allow
      actions: [read]
      resources: ["**"]
      when:
        - expression: 'subject.authenticated'
`

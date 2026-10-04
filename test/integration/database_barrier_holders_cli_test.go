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

// database_barrier_holders_cli_test.go drives the database-scoped Shamir
// and passphrase barrier lifecycles (database barrier-officer /
// barrier-init-shamir / barrier-quorum / barrier-rekey / seal / unseal --
// cmd/qrdb/barrier_holders.go's databaseHolders target and
// cmd/qrdb/database.go's seal/unseal verbs) as a real qrdb CLI subprocess,
// over every CLI-selectable transport (REST, gRPC, QUIC, MCP, Unix). Each
// test starts its own single shared node (one node, five subtests, one per
// transport) rather than a node per subtest -- see each test's own comment
// for why the two need different nodes. The per-database barrier each
// subtest creates is an entirely independent registry
// (pkg/database.DatabaseBarrierManager) named by protocol, so the five
// subtests never interact with each other or with the node's own system
// barrier.
//
// Unlike the system and tenant barriers, a database barrier's rekey and
// officer revocation are authorized by RBAC alone (holderKind.soPinGated
// is false for holderDatabase; see cmd/qrdb/barrier_holders.go and
// docs/shamir-custody/cli.md), so there is no SO-PIN provisioning step
// here: a --so-pin flag is not even registered on `database barrier-rekey`
// / `database barrier-officer revoke`.
package integration_test

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
)

// claimBeginJSON decodes `database barrier-officer claim-begin --output json`.
type claimBeginJSON struct {
	NonceHex string `json:"nonce_hex"`
}

// claimEnvelopeSplitDatabase claims off's database-scope envelope through
// the two-step claim-begin / claim-complete verbs instead of the combined
// `claim`, exercising both leaf commands: it signs the exact
// ceremony.ShareClaimMessage a key that signs outside this CLI would, over
// the database's claim label.
func claimEnvelopeSplitDatabase(t *testing.T, cli, dir string, ep endpoint, dbName string, off *officer) {
	t.Helper()
	beginArgs := append(off.flags(ep), "--output", "json", "database", "barrier-officer", "claim-begin",
		"--database", dbName, "--username", off.username)
	stdout, stderr, err := runCLISplit(t, cli, beginArgs...)
	if err != nil {
		t.Fatalf("claim-begin %s: %v\nstdout:%s\nstderr:%s", off.username, err, stdout, stderr)
	}
	var begin claimBeginJSON
	if err := json.Unmarshal([]byte(stdout), &begin); err != nil {
		t.Fatalf("claim-begin %s: decode JSON: %v\noutput: %s", off.username, err, stdout)
	}
	nonce, err := hex.DecodeString(begin.NonceHex)
	if err != nil {
		t.Fatalf("claim-begin %s: nonce %q is not hex: %v", off.username, begin.NonceHex, err)
	}
	msg := ceremony.ShareClaimMessage(ceremony.DatabaseClaimLabel(dbName), off.username, nonce)
	sig := signNonceASN1(t, off.key, msg)
	sigPath := filepath.Join(dir, off.username+"-claim-sig.der")
	if err := os.WriteFile(sigPath, sig, 0o600); err != nil {
		t.Fatalf("write claim signature for %s: %v", off.username, err)
	}
	off.envelope = filepath.Join(dir, fmt.Sprintf("%s-%d.env", off.username, time.Now().UnixNano()))
	completeArgs := append(off.flags(ep), "database", "barrier-officer", "claim-complete",
		"--database", dbName, "--username", off.username, "--nonce-hex", begin.NonceHex,
		"--sig-file", sigPath, "--out", off.envelope)
	if stdout, stderr, err := runCLISplit(t, cli, completeArgs...); err != nil {
		t.Fatalf("claim-complete %s: %v\nstdout:%s\nstderr:%s", off.username, err, stdout, stderr)
	}
}

// quorumBeginJSON decodes `database barrier-quorum begin --output json`.
type quorumBeginJSON struct {
	ChallengeID string `json:"challenge_id"`
}

// quorumBeginDatabase issues a quorum challenge for off directly (the leaf
// `begin` verb quorum submit answers internally for the mtls factor), and
// asserts a challenge was actually issued. It does not consume the round:
// a fresh, unrelated challenge id is simply discarded.
func quorumBeginDatabase(t *testing.T, cli string, ep endpoint, dbName, op string, off *officer) {
	t.Helper()
	args := append(off.flags(ep), "--output", "json", "database", "barrier-quorum", "begin",
		"--database", dbName, "--op", op, "--username", off.username, "--factor", "mtls")
	stdout, stderr, err := runCLISplit(t, cli, args...)
	if err != nil {
		t.Fatalf("quorum begin %s: %v\nstdout:%s\nstderr:%s", off.username, err, stdout, stderr)
	}
	var begin quorumBeginJSON
	if err := json.Unmarshal([]byte(stdout), &begin); err != nil {
		t.Fatalf("quorum begin %s: decode JSON: %v\noutput: %s", off.username, err, stdout)
	}
	if begin.ChallengeID == "" {
		t.Fatalf("quorum begin %s: empty challenge id, output: %s", off.username, stdout)
	}
}

// runDBAdmin runs args against the node with the admin identity (flags)
// and returns the combined stdout+stderr.
func runDBAdmin(t *testing.T, cli string, flags []string, args ...string) (string, error) {
	t.Helper()
	stdout, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...), args...)...)
	return stdout + stderr, err
}

// TestCLI_DatabaseBarrier_ShamirHolderLifecycle_AllTransports drives the
// full database-scope Shamir share-holder lifecycle -- register, list,
// unenrol (pre-init), barrier-init-shamir, claim (combined and the
// claim-begin/claim-complete split), quorum begin, quorum submit
// (seal/unseal), barrier-rekey, and officer revoke -- against a dedicated
// -store 2-of-3 Shamir database, once per CLI-selectable transport.
//
// One shared node serves every subtest, started via startFreshSealedNode
// (shared with barrier_permutations_cli_test.go), NOT
// multi_protocol_helpers_test.go's startMultiProtocolNode: a database
// Shamir barrier's officer registration signs a CSR with the node's CA,
// which requires the CA's private key. startMultiProtocolNode passes
// explicit --tls-ca-file/--tls-cert-file/--tls-key-file (an external CA
// with no private key on the node), so database barrier-officer register
// fails closed there with "ceremony: nil CA provided"; startFreshSealedNode
// passes none of those, so the daemon self-generates a CA WITH its private
// key instead (see barrier_cli_test.go's package comment). Each subtest's
// database is named by protocol, so the five run against the one node
// without interacting.
func TestCLI_DatabaseBarrier_ShamirHolderLifecycle_AllTransports(t *testing.T) {
	node := startFreshSealedNode(t, "none", 0, 0)
	bootFlags := node.flagsFor(multiProtocols[0])
	waitForBarrierReachable(t, node.cli, bootFlags, 60*time.Second)
	if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, bootFlags...),
		"barrier", "init", "--barrier-strategy", "none")...); err != nil {
		t.Fatalf("barrier init: %v\nstderr: %s", err, stderr)
	}
	node.waitForFullBoot(t)

	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			flags := node.flagsFor(proto)
			ep := endpoint{serverURL: node.serverURLFor(proto), caFile: node.ca.CertPath}

			workDir := t.TempDir()
			dataDir := t.TempDir()
			dbName := fmt.Sprintf("shamir-holders-%s-%d", proto.name, time.Now().UnixNano())
			scope := databaseScope(dbName)
			t.Cleanup(func() {
				runDBAdmin(t, node.cli, flags, "database", "drop", dbName) //nolint:errcheck
			})

			if createOut, err := runDBAdmin(t, node.cli, flags, "database", "create", dbName,
				"--data-dir", dataDir, "--barrier-enabled", "--barrier-strategy", "shamir",
				"--barrier-shamir-threshold", "2", "--barrier-shamir-total", "3"); err != nil {
				t.Fatalf("database create: %v\noutput: %s", err, createOut)
			}

			alice := registerOfficer(t, node.cli, workDir, flags, scope, "alice")
			bob := registerOfficer(t, node.cli, workDir, flags, scope, "bob")
			registerOfficer(t, node.cli, workDir, flags, scope, "dave")

			listOut, err := runDBAdmin(t, node.cli, flags, "database", "barrier-officer", "list", "--database", dbName)
			if err != nil {
				t.Fatalf("barrier-officer list: %v\noutput: %s", err, listOut)
			}
			for _, name := range []string{"alice", "bob", "dave"} {
				if !strings.Contains(listOut, name) {
					t.Fatalf("barrier-officer list missing %s: %s", name, listOut)
				}
			}

			// The barrier is already split 3 ways (alice, bob, dave): registering
			// a 4th real CSR before init is refused outright, for the enrollment
			// limit, not a missing file. unenrol frees dave's slot for carol.
			erinCSR, _ := generateOfficerCSR(t, workDir, "erin")
			out, err := runDBAdmin(t, node.cli, flags, "database", "barrier-officer", "register",
				"--database", dbName, "--username", "erin", "--csr-file", erinCSR)
			if err == nil {
				t.Fatalf("registering a 4th holder over a 3-of-3 split succeeded: %s", out)
			}
			if !strings.Contains(out, "enrolled") {
				t.Fatalf("expected the enrollment-limit refusal, got: %s", out)
			}

			if _, err := runDBAdmin(t, node.cli, flags, "database", "barrier-officer", "unenrol",
				"--database", dbName, "--username", "dave"); err != nil {
				t.Fatalf("barrier-officer unenrol: %v", err)
			}
			listOut, err = runDBAdmin(t, node.cli, flags, "database", "barrier-officer", "list", "--database", dbName)
			if err != nil {
				t.Fatalf("barrier-officer list after unenrol: %v\noutput: %s", err, listOut)
			}
			if strings.Contains(listOut, "dave") {
				t.Fatalf("dave still listed after unenrol: %s", listOut)
			}

			// dave's freed slot takes carol instead.
			carol := registerOfficer(t, node.cli, workDir, flags, scope, "carol")
			listOut, err = runDBAdmin(t, node.cli, flags, "database", "barrier-officer", "list", "--database", dbName)
			if err != nil {
				t.Fatalf("barrier-officer list after re-enrolling carol: %v\noutput: %s", err, listOut)
			}
			for _, name := range []string{"alice", "bob", "carol"} {
				if !strings.Contains(listOut, name) {
					t.Fatalf("barrier-officer list missing %s: %s", name, listOut)
				}
			}

			initOut, err := runDBAdmin(t, node.cli, flags, "database", "barrier-init-shamir",
				"--database", dbName, "--policy", "designated")
			if err != nil {
				t.Fatalf("barrier-init-shamir: %v\noutput: %s", err, initOut)
			}
			if !strings.Contains(initOut, "2-of-3") {
				t.Fatalf("barrier-init-shamir output = %q, want it to contain 2-of-3", initOut)
			}

			claimEnvelope(t, node.cli, workDir, ep, scope, alice)
			claimEnvelope(t, node.cli, workDir, ep, scope, bob)
			claimEnvelopeSplitDatabase(t, node.cli, workDir, ep, dbName, carol)

			quorumBeginDatabase(t, node.cli, ep, dbName, "unseal", alice)

			status := cliDatabaseBarrierStatus(t, node.cli, flags, dbName)
			if status.Sealed {
				t.Fatalf("barrier-status after init-shamir = %+v, want sealed=false", status)
			}

			quorumAll(t, node.cli, ep, scope, "seal", alice, bob)
			status = cliDatabaseBarrierStatus(t, node.cli, flags, dbName)
			if !status.Sealed {
				t.Fatal("expected the database barrier to be sealed after the seal quorum")
			}

			if out := quorumAll(t, node.cli, ep, scope, "unseal", alice); out.Complete {
				t.Fatalf("one of two unseal submissions completed the round: %+v", out)
			}
			if status = cliDatabaseBarrierStatus(t, node.cli, flags, dbName); !status.Sealed {
				t.Fatal("a below-threshold round must not unseal the database barrier")
			}
			if out := quorumAll(t, node.cli, ep, scope, "unseal", bob); !out.Complete {
				t.Fatalf("second unseal submission did not complete the round: %+v", out)
			}
			if status = cliDatabaseBarrierStatus(t, node.cli, flags, dbName); status.Sealed {
				t.Fatal("expected the database barrier to be unsealed after the unseal quorum")
			}

			rekeyOut, err := runDBAdmin(t, node.cli, flags, "database", "barrier-rekey",
				"--database", dbName, "--threshold", "2", "--policy", "designated")
			if err != nil {
				t.Fatalf("barrier-rekey: %v\noutput: %s", err, rekeyOut)
			}
			if out := quorumAll(t, node.cli, ep, scope, "rekey", alice, bob); !out.Complete {
				t.Fatalf("rekey round = %+v, want complete", out)
			}

			claimEnvelope(t, node.cli, workDir, ep, scope, alice)
			claimEnvelope(t, node.cli, workDir, ep, scope, bob)
			claimEnvelope(t, node.cli, workDir, ep, scope, carol)

			quorumAll(t, node.cli, ep, scope, "seal", alice, bob)
			if status = cliDatabaseBarrierStatus(t, node.cli, flags, dbName); !status.Sealed {
				t.Fatal("expected the database barrier to be sealed after the post-rekey seal quorum")
			}
			quorumAll(t, node.cli, ep, scope, "unseal", alice, bob)
			if status = cliDatabaseBarrierStatus(t, node.cli, flags, dbName); status.Sealed {
				t.Fatal("expected the database barrier to be unsealed with the post-rekey envelopes")
			}

			firstCarol := *carol
			revokeOut, err := runDBAdmin(t, node.cli, flags, "database", "barrier-officer", "revoke",
				"--database", dbName, "--username", "carol", "--threshold", "2")
			if err != nil {
				t.Fatalf("barrier-officer revoke: %v\noutput: %s", err, revokeOut)
			}
			if out := quorumAll(t, node.cli, ep, scope, "rekey", alice, bob); !out.Complete {
				t.Fatalf("revoke-completing rekey round = %+v, want complete", out)
			}

			claimEnvelope(t, node.cli, workDir, ep, scope, alice)
			claimEnvelope(t, node.cli, workDir, ep, scope, bob)

			quorumAll(t, node.cli, ep, scope, "seal", alice, bob)
			if status = cliDatabaseBarrierStatus(t, node.cli, flags, dbName); !status.Sealed {
				t.Fatal("expected the database barrier to be sealed after the post-revoke seal quorum")
			}

			// carol's identity is gone from the roster in force once the
			// revoke-completing rekey commits (pkg/ceremony's Committed), so her
			// pre-revoke envelope cannot even be submitted.
			if _, _, err := quorumSubmit(t, node.cli, ep, scope, "unseal", &firstCarol); err == nil {
				t.Fatal("a revoked holder's envelope was accepted into a quorum round")
			}
			if status = cliDatabaseBarrierStatus(t, node.cli, flags, dbName); !status.Sealed {
				t.Fatal("a rejected revoked-holder submission must not unseal the database barrier")
			}

			quorumAll(t, node.cli, ep, scope, "unseal", alice, bob)
			if status = cliDatabaseBarrierStatus(t, node.cli, flags, dbName); status.Sealed {
				t.Fatal("expected the database barrier to be unsealed with the post-revoke holders")
			}
		})
	}
}

// TestCLI_DatabaseBarrier_PassphraseLifecycle_AllTransports drives the
// dedicated-store passphrase database-barrier lifecycle (create, barrier
// -status, a kv round trip, seal, a blocked read, unseal with a wrong then
// the correct secret, and a restored read) once per CLI-selectable
// transport, extending database_barrier_cli_test.go's REST-only coverage
// of the same lifecycle to gRPC, QUIC, MCP and Unix.
func TestCLI_DatabaseBarrier_PassphraseLifecycle_AllTransports(t *testing.T) {
	node := startMultiProtocolNode(t)
	const passphrase = "db-barrier-perm-passphrase-1"

	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			dataDir := t.TempDir()
			dbName := fmt.Sprintf("pp-holders-%s-%d", proto.name, time.Now().UnixNano())
			flags := node.cliArgsFor(proto)
			t.Cleanup(func() {
				runCLIProto(t, node, proto, "database", "drop", dbName) //nolint:errcheck
			})

			if _, err := runCLIProto(t, node, proto, "database", "create", dbName,
				"--data-dir", dataDir, "--barrier-enabled", "--barrier-strategy", "passphrase",
				"--barrier-passphrase", passphrase); err != nil {
				t.Fatalf("database create: %v", err)
			}

			status := cliDatabaseBarrierStatus(t, node.cli, flags, dbName)
			if status.Sealed || status.Strategy != "passphrase" {
				t.Fatalf("barrier-status after create = %+v, want sealed=false strategy=passphrase", status)
			}

			key, value := "greeting", "hello-"+proto.name
			if _, err := runCLIProto(t, node, proto, "--database", dbName, "kv", "put", key, value); err != nil {
				t.Fatalf("kv put: %v", err)
			}
			out, err := runCLIProto(t, node, proto, "--database", dbName, "kv", "get", key)
			if err != nil || !strings.Contains(out, value) {
				t.Fatalf("kv get before seal = %q, err=%v, want it to contain %q", out, err, value)
			}

			if _, err := runCLIProto(t, node, proto, "database", "seal", dbName); err != nil {
				t.Fatalf("database seal: %v", err)
			}
			if status = cliDatabaseBarrierStatus(t, node.cli, flags, dbName); !status.Sealed {
				t.Fatal("expected the database barrier to be sealed after database seal")
			}
			if out, err := runCLIProto(t, node, proto, "--database", dbName, "kv", "get", key); err == nil {
				t.Fatalf("kv get while sealed succeeded (output: %s), want error", out)
			}

			if _, err := runCLIProto(t, node, proto, "database", "unseal", dbName, "--secret", "wrong-secret"); err == nil {
				t.Fatal("database unseal with the wrong secret succeeded, want error")
			}
			if status = cliDatabaseBarrierStatus(t, node.cli, flags, dbName); !status.Sealed {
				t.Fatal("a wrong-secret unseal must leave the database barrier sealed")
			}

			if _, err := runCLIProto(t, node, proto, "database", "unseal", dbName, "--secret", passphrase); err != nil {
				t.Fatalf("database unseal: %v", err)
			}
			if status = cliDatabaseBarrierStatus(t, node.cli, flags, dbName); status.Sealed {
				t.Fatal("expected the database barrier to be unsealed after the correct secret")
			}
			out, err = runCLIProto(t, node, proto, "--database", dbName, "kv", "get", key)
			if err != nil || !strings.Contains(out, value) {
				t.Fatalf("kv get after unseal = %q, err=%v, want it to contain %q", out, err, value)
			}
		})
	}
}

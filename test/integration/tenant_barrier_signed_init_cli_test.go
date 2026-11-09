//go:build integration

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

package integration_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTenantBarrierSignedInit_CLI_PerProtocol drives the two-phase signed
// tenant barrier init through the real CLI on every transport, for a
// Raft-isolated tenant whose first admin signs the statement outside the
// node: the test process holds the key and the node only ever sees the
// public key and the detached signature.
func TestTenantBarrierSignedInit_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testTenantBarrierSignedInitOverProtocol(t, node, proto)
		})
	}
}

// TestTenantBarrierSignedInit_CLI_ExpiredPerProtocol drives an expired
// reference through the CLI on every transport. The node runs with
// tenant.signed_init_ttl: 1ns, so a reference has lapsed by the time
// barrier-init-complete reaches the node and no test waits: the complete,
// even with the first admin's valid signature, is refused as expired and
// initializes nothing.
func TestTenantBarrierSignedInit_CLI_ExpiredPerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t, "signed_init_ttl: 1ns")

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			tenantID := fmt.Sprintf("six-%s-%d", proto.name, time.Now().UnixNano())
			adminCert, adminKey := createTenantWithAdminCert(t, node, proto, tenantID, "Signed Init Expiry Tenant", "raft", false, tenantID+"-admin")
			pair, err := tls.LoadX509KeyPair(adminCert, adminKey)
			if err != nil {
				t.Fatalf("[%s] load the first admin's key pair: %v", proto.name, err)
			}
			adminCLI := func(args ...string) (string, error) {
				return runCLI(t, node.cli, append(tenantCLIArgsForProto(node, proto, adminCert, adminKey), args...)...)
			}

			out, err := adminCLI("--output", "json", "tenant", "barrier-init-begin", "--id", tenantID, "--strategy", "none", "--public-key", adminCert)
			if err != nil {
				t.Fatalf("[%s] barrier-init-begin: %v\noutput: %s", proto.name, err, out)
			}
			var begun struct {
				ReferenceHex string `json:"reference_hex"`
				StatementHex string `json:"statement_hex"`
			}
			start := strings.Index(out, "{")
			if start < 0 {
				t.Fatalf("[%s] barrier-init-begin printed no JSON: %q", proto.name, out)
			}
			if err := json.Unmarshal([]byte(out[start:]), &begun); err != nil {
				t.Fatalf("[%s] barrier-init-begin output %q: %v", proto.name, out, err)
			}
			statement, err := hex.DecodeString(begun.StatementHex)
			if err != nil || len(statement) == 0 || begun.ReferenceHex == "" {
				t.Fatalf("[%s] barrier-init-begin printed no statement or reference: %q", proto.name, out)
			}

			sigPath := filepath.Join(t.TempDir(), "admin.sig")
			if err := os.WriteFile(sigPath, signDetached(t, pair.PrivateKey.(crypto.Signer), statement), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err = adminCLI("tenant", "barrier-init-complete", "--id", tenantID, "--reference-hex", begun.ReferenceHex, "--sig-file", sigPath)
			if err == nil || !strings.Contains(out+err.Error(), "statement reference expired") {
				t.Fatalf("[%s] complete of an expired reference: err = %v, output %q; want the reference refused as expired", proto.name, err, out)
			}
			if out, err = adminCLI("tenant", "barrier-status", "--id", tenantID); err != nil || !strings.Contains(out, "Initialized: false") {
				t.Fatalf("[%s] barrier-status after an expired reference: err = %v\n%s", proto.name, err, out)
			}
		})
	}
}

func testTenantBarrierSignedInitOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	tenantID := fmt.Sprintf("si-%s-%d", proto.name, time.Now().UnixNano())
	adminCert, adminKey := createTenantWithAdminCert(t, node, proto, tenantID, "Signed Init Tenant", "raft", false, tenantID+"-admin")
	pair, err := tls.LoadX509KeyPair(adminCert, adminKey)
	if err != nil {
		t.Fatalf("[%s] load the first admin's key pair: %v", proto.name, err)
	}
	signer := pair.PrivateKey.(crypto.Signer)

	adminCLI := func(args ...string) (string, error) {
		return runCLI(t, node.cli, append(tenantCLIArgsForProto(node, proto, adminCert, adminKey), args...)...)
	}
	dir := t.TempDir()
	sigFile := func(name string, sig []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, sig, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	begin := func() (refHex string, statement []byte) {
		out, err := adminCLI("--output", "json", "tenant", "barrier-init-begin", "--id", tenantID, "--strategy", "none", "--public-key", adminCert)
		if err != nil {
			t.Fatalf("[%s] barrier-init-begin: %v\noutput: %s", proto.name, err, out)
		}
		var begun struct {
			ReferenceHex string `json:"reference_hex"`
			StatementHex string `json:"statement_hex"`
		}
		start := strings.Index(out, "{")
		if start < 0 {
			t.Fatalf("[%s] barrier-init-begin printed no JSON: %q", proto.name, out)
		}
		if err := json.Unmarshal([]byte(out[start:]), &begun); err != nil {
			t.Fatalf("[%s] barrier-init-begin output %q: %v", proto.name, out, err)
		}
		statement, err = hex.DecodeString(begun.StatementHex)
		if err != nil || len(statement) == 0 || begun.ReferenceHex == "" {
			t.Fatalf("[%s] barrier-init-begin printed no statement or reference: %q", proto.name, out)
		}
		return begun.ReferenceHex, statement
	}
	complete := func(refHex, sigPath string) (string, error) {
		return adminCLI("tenant", "barrier-init-complete", "--id", tenantID, "--reference-hex", refHex, "--sig-file", sigPath)
	}
	assertUninitialized := func(after string) {
		out, err := adminCLI("tenant", "barrier-status", "--id", tenantID)
		if err != nil {
			t.Fatalf("[%s] barrier-status after %s: %v\noutput: %s", proto.name, after, err, out)
		}
		if !strings.Contains(out, "Initialized: false") {
			t.Fatalf("[%s] %s initialized the barrier:\n%s", proto.name, after, out)
		}
	}

	// 1. A complete with no begin: the reference was never issued.
	out, err := complete(hex.EncodeToString(make([]byte, 32)), sigFile("none.sig", []byte("sig")))
	if err == nil || !strings.Contains(out+err.Error(), "statement reference unknown or already used") {
		t.Fatalf("[%s] complete without a begin: err = %v, output %q; want the reference refused", proto.name, err, out)
	}
	assertUninitialized("a complete without a begin")

	// 2. A signature by another key over the begun statement.
	refHex, statement := begin()
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if out, err = complete(refHex, sigFile("wrong.sig", signDetached(t, other, statement))); err == nil {
		t.Fatalf("[%s] complete signed by another key succeeded: %s", proto.name, out)
	}
	assertUninitialized("a signature by another key")

	// 3. A replay: the reference was spent by the refused attempt, so even
	// the right signature cannot redeem it.
	if out, err = complete(refHex, sigFile("replay.sig", signDetached(t, signer, statement))); err == nil || !strings.Contains(out+err.Error(), "statement reference unknown or already used") {
		t.Fatalf("[%s] replayed reference: err = %v, output %q; want the reference refused", proto.name, err, out)
	}
	assertUninitialized("a replayed reference")

	// 4. A fresh begin, signed outside the node by the first admin's key.
	refHex, statement = begin()
	if out, err = complete(refHex, sigFile("admin.sig", signDetached(t, signer, statement))); err != nil {
		t.Fatalf("[%s] complete signed by the first admin: %v\noutput: %s", proto.name, err, out)
	}
	if out, err = adminCLI("tenant", "barrier-status", "--id", tenantID); err != nil || !strings.Contains(out, "Initialized: true") {
		t.Fatalf("[%s] barrier-status after the signed init: err = %v\n%s", proto.name, err, out)
	}

	// The redeemed reference is spent too.
	if out, err = complete(refHex, sigFile("again.sig", signDetached(t, signer, statement))); err == nil {
		t.Fatalf("[%s] a second complete of a redeemed reference succeeded: %s", proto.name, out)
	}
}

// signDetached signs msg the way a signed init statement is verified: ECDSA
// and RSA over its SHA-256 digest, Ed25519 over the bytes themselves.
func signDetached(t *testing.T, signer crypto.Signer, msg []byte) []byte {
	t.Helper()
	var (
		sig []byte
		err error
	)
	if _, ok := signer.Public().(ed25519.PublicKey); ok {
		sig, err = signer.Sign(rand.Reader, msg, crypto.Hash(0))
	} else {
		digest := sha256.Sum256(msg)
		sig, err = signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	}
	if err != nil {
		t.Fatalf("sign the statement: %v", err)
	}
	return sig
}

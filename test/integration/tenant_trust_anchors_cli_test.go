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

// tenant_trust_anchors_cli_test.go proves `qrdb tenant trust-anchors
// add/list/remove` (cmd/qrdb/tenant_trust_anchors.go) on every transport:
// the per-tenant 32-anchor cap, the typed refusals for an empty bundle, a
// non-CA certificate and an unknown fingerprint, strict cross-tenant
// confinement, and -- the feature's central guarantee
// (docs/trust-anchors/architecture.md) -- that a bring-your-own-certificate
// login is ADMITTED while its anchor is registered and REFUSED the instant
// that anchor is withdrawn, with no caching delay.
package integration_test

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// trustAnchorListCLIJSON mirrors the JSON "tenant trust-anchors
// add"/"list" print (transport.TrustAnchorList, cmd/qrdb/tenant_trust_anchors.go's
// printTrustAnchors).
type trustAnchorListCLIJSON struct {
	Anchors []struct {
		Fingerprint string `json:"fingerprint"`
		Subject     string `json:"subject"`
	} `json:"anchors"`
}

// trustAnchorList runs "tenant trust-anchors list --tenant-id tenantID" over
// conn and decodes its JSON output, or fails the test.
func trustAnchorList(t *testing.T, cli string, conn []string, tenantID string) trustAnchorListCLIJSON {
	t.Helper()
	out, err := runQRDB(t, cli, conn, "--output", "json", "tenant", "trust-anchors", "list", "--tenant-id", tenantID)
	if err != nil {
		t.Fatalf("tenant trust-anchors list %s: %v\noutput: %s", tenantID, err, out)
	}
	jsonStart := strings.Index(out, "{")
	if jsonStart < 0 {
		t.Fatalf("tenant trust-anchors list output carries no JSON: %s", out)
	}
	var listed trustAnchorListCLIJSON
	if jerr := json.Unmarshal([]byte(out[jsonStart:]), &listed); jerr != nil {
		t.Fatalf("decode trust-anchors list JSON: %v\noutput: %s", jerr, out)
	}
	return listed
}

// trustAnchorListHasFingerprint reports whether listed contains an anchor
// with the given fingerprint.
func trustAnchorListHasFingerprint(listed trustAnchorListCLIJSON, fingerprint string) bool {
	for _, a := range listed.Anchors {
		if a.Fingerprint == fingerprint {
			return true
		}
	}
	return false
}

// certFingerprint reads the PEM certificate at path and returns the
// lowercase hex SHA-256 of its DER, matching pkg/trustanchor.Fingerprint --
// the identifier "tenant trust-anchors remove --fingerprint" takes.
func certFingerprint(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("certFingerprint: read %s: %v", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatalf("certFingerprint: no PEM block in %s", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("certFingerprint: parse %s: %v", path, err)
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// writeBundlePEM concatenates the PEM certificates at certPaths into one
// file under t.TempDir() and returns its path -- the "--file" argument
// "tenant trust-anchors add" and "tenant create --trust-anchors" both take.
// With no paths, it writes an empty file, exercising the EmptyBundleError
// refusal.
func writeBundlePEM(t *testing.T, certPaths ...string) string {
	t.Helper()
	var buf strings.Builder
	for _, p := range certPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("writeBundlePEM: read %s: %v", p, err)
		}
		buf.Write(data)
	}
	path := filepath.Join(t.TempDir(), "bundle.pem")
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		t.Fatalf("writeBundlePEM: write %s: %v", path, err)
	}
	return path
}

// createTrustAnchorTenantAdmin creates a fresh tenant (CSR-based admin
// credential) and returns the signed admin certificate/key paths -- an
// identity that already holds the default Admin role's
// trust_anchors:add/list/remove grants (pkg/permission/builtin_role_grants.go),
// so no extra role provisioning is needed to manage this tenant's own
// anchors.
func createTrustAnchorTenantAdmin(t *testing.T, node *multiProtoNode, proto multiProtocol, tenantID string) (certPath, keyPath string) {
	t.Helper()
	csrDir := t.TempDir()
	adminUsername := tenantID + "-admin"
	if out, err := runCLIProto(t, node, proto, "ca", "csr", "--username", adminUsername, "--out", csrDir); err != nil {
		t.Fatalf("[%s] ca csr for %s: %v\noutput: %s", proto.name, adminUsername, err, out)
	}
	outDir := t.TempDir()
	if out, err := runCLIProto(t, node, proto, "tenant", "create",
		"--id", tenantID, "--name", tenantID, "--isolation-type", "prefix",
		"--admin-username", adminUsername,
		"--admin-csr", filepath.Join(csrDir, "csr.pem"),
		"--out", outDir,
	); err != nil {
		t.Fatalf("[%s] tenant create %s: %v\noutput: %s", proto.name, tenantID, err, out)
	}
	return filepath.Join(outDir, "admin-cert.pem"), filepath.Join(csrDir, "key.pem")
}

// TestTrustAnchors_CLI_Lifecycle_PerProtocol proves `tenant trust-anchors
// add/list/remove`'s real behavior on every transport: registering a
// bundle, listing it back, withdrawing one anchor, and the typed refusals
// for an unknown fingerprint (AnchorNotFoundError), an empty bundle
// (EmptyBundleError), a non-CA certificate (InvalidAnchorError) and the
// per-tenant 32-anchor cap (TooManyAnchorsError, pkg/trustanchor.bundle.go) --
// which KVRegistry.Add checks atomically over one submitted bundle, so the
// cap is provable in two CLI calls (32 fresh anchors in one bundle, then
// one more) rather than 33.
func TestTrustAnchors_CLI_Lifecycle_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			tenantID := fmt.Sprintf("ta-life-%s", proto.name)
			certPath, keyPath := createTrustAnchorTenantAdmin(t, node, proto, tenantID)
			conn := tenantCLIArgsForProto(node, proto, certPath, keyPath)

			ca1 := generateTestCA(t)
			ca2 := generateTestCA(t)
			fp1 := certFingerprint(t, ca1.CertPath)
			fp2 := certFingerprint(t, ca2.CertPath)

			if out, err := runQRDB(t, node.cli, conn, "tenant", "trust-anchors", "add",
				"--tenant-id", tenantID, "--file", writeBundlePEM(t, ca1.CertPath, ca2.CertPath)); err != nil {
				t.Fatalf("[%s] add 2 anchors: %v\noutput: %s", proto.name, err, out)
			}

			listed := trustAnchorList(t, node.cli, conn, tenantID)
			if len(listed.Anchors) != 2 || !trustAnchorListHasFingerprint(listed, fp1) || !trustAnchorListHasFingerprint(listed, fp2) {
				t.Fatalf("[%s] after adding 2 anchors, list = %+v, want exactly {%s, %s}", proto.name, listed.Anchors, fp1, fp2)
			}

			if out, err := runQRDB(t, node.cli, conn, "tenant", "trust-anchors", "remove",
				"--tenant-id", tenantID, "--fingerprint", fp1); err != nil {
				t.Fatalf("[%s] remove anchor %s: %v\noutput: %s", proto.name, fp1, err, out)
			}
			listed = trustAnchorList(t, node.cli, conn, tenantID)
			if len(listed.Anchors) != 1 || trustAnchorListHasFingerprint(listed, fp1) || !trustAnchorListHasFingerprint(listed, fp2) {
				t.Fatalf("[%s] after removing %s, list = %+v, want only %s", proto.name, fp1, listed.Anchors, fp2)
			}

			// Removing an unknown fingerprint is refused by name, never
			// silently accepted as a no-op.
			if out, err := runQRDB(t, node.cli, conn, "tenant", "trust-anchors", "remove",
				"--tenant-id", tenantID, "--fingerprint", strings.Repeat("be", 32)); err == nil {
				t.Fatalf("[%s] removing an unknown fingerprint must fail, got: %s", proto.name, out)
			} else if !strings.Contains(strings.ToLower(out), "no trust anchor") {
				t.Fatalf("[%s] removing an unknown fingerprint: want the typed AnchorNotFoundError, got: %s", proto.name, out)
			}

			// An empty bundle holds nothing to register.
			if out, err := runQRDB(t, node.cli, conn, "tenant", "trust-anchors", "add",
				"--tenant-id", tenantID, "--file", writeBundlePEM(t)); err == nil {
				t.Fatalf("[%s] adding an empty bundle must fail, got: %s", proto.name, out)
			} else if !strings.Contains(strings.ToLower(out), "no certificate") {
				t.Fatalf("[%s] adding an empty bundle: want the typed EmptyBundleError, got: %s", proto.name, out)
			}

			// A non-CA (ordinary leaf) certificate cannot vouch for anyone.
			leaf := node.ca.issue(t, t.TempDir(), qrdbdharness.WithSubject("not-a-ca", tenantID))
			if out, err := runQRDB(t, node.cli, conn, "tenant", "trust-anchors", "add",
				"--tenant-id", tenantID, "--file", leaf.CertPath); err == nil {
				t.Fatalf("[%s] adding a non-CA certificate as a trust anchor must fail, got: %s", proto.name, out)
			} else if !strings.Contains(strings.ToLower(out), "not a ca certificate") {
				t.Fatalf("[%s] adding a non-CA certificate: want the typed InvalidAnchorError, got: %s", proto.name, out)
			}

			// Clean slate, then saturate the per-tenant cap in one bundle.
			if out, err := runQRDB(t, node.cli, conn, "tenant", "trust-anchors", "remove",
				"--tenant-id", tenantID, "--fingerprint", fp2); err != nil {
				t.Fatalf("[%s] remove remaining anchor %s: %v\noutput: %s", proto.name, fp2, err, out)
			}

			capPaths := make([]string, 32)
			for i := range capPaths {
				capPaths[i] = generateTestCA(t).CertPath
			}
			if out, err := runQRDB(t, node.cli, conn, "tenant", "trust-anchors", "add",
				"--tenant-id", tenantID, "--file", writeBundlePEM(t, capPaths...)); err != nil {
				t.Fatalf("[%s] saturating the 32-anchor cap in one bundle must succeed: %v\noutput: %s", proto.name, err, out)
			}

			overflow := generateTestCA(t)
			if out, err := runQRDB(t, node.cli, conn, "tenant", "trust-anchors", "add",
				"--tenant-id", tenantID, "--file", overflow.CertPath); err == nil {
				t.Fatalf("[%s] a 33rd trust anchor must be refused, got: %s", proto.name, out)
			} else if !strings.Contains(strings.ToLower(out), "may register at most") {
				t.Fatalf("[%s] exceeding the 32-anchor cap: want the typed TooManyAnchorsError, got: %s", proto.name, out)
			}
		})
	}
}

// TestTrustAnchors_CLI_CrossTenantDenied_PerProtocol proves `tenant
// trust-anchors add/list/remove` is self-strict (pkg/permission's
// ScopeSelfStrict, pkg/tenant.Confine): an identity holding every
// trust_anchors:* permission in its own tenant is still refused the moment
// it names a foreign --tenant-id, on every transport, while its own tenant
// remains reachable.
func TestTrustAnchors_CLI_CrossTenantDenied_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	const tenantID = "ta-acme"
	const foreignTenantID = "ta-globex"
	createTenant(t, node, multiProtocols[0], tenantID, "Trust Anchor Tenant", "prefix", false)
	createTenant(t, node, multiProtocols[0], foreignTenantID, "Foreign Trust Anchor Tenant", "prefix", false)

	certPath, keyPath := provisionTenantIdentity(t, node, tenantID, "ta-manager",
		[]string{"trust_anchors:add", "trust_anchors:list", "trust_anchors:remove"})

	bundlePath := writeBundlePEM(t, generateTestCA(t).CertPath)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			conn := tenantCLIArgsForProto(node, proto, certPath, keyPath)

			assertCrossTenantIAMForbidden(t, node, proto, conn, "",
				"tenant", "trust-anchors", "add", "--tenant-id", foreignTenantID, "--file", bundlePath)
			assertCrossTenantIAMForbidden(t, node, proto, conn, "",
				"tenant", "trust-anchors", "list", "--tenant-id", foreignTenantID)
			assertCrossTenantIAMForbidden(t, node, proto, conn, "",
				"tenant", "trust-anchors", "remove", "--tenant-id", foreignTenantID, "--fingerprint", strings.Repeat("be", 32))

			// Its own tenant, by contrast, is admitted.
			if out, err := runQRDB(t, node.cli, conn, "tenant", "trust-anchors", "list", "--tenant-id", tenantID); err != nil {
				t.Fatalf("[%s] listing its own tenant's trust anchors: %v\noutput: %s", proto.name, err, out)
			}
		})
	}
}

// assertBYOCCertRefused polls args over conn until it is refused, treating
// a transient success as the client-trust-pool cache not yet having
// observed the just-committed removal. The feature's invariant
// (docs/trust-anchors/architecture.md) is that the SAME TLS listener drops
// its pool on every write and rebuilds it fresh on the next handshake --
// i.e. the refusal is expected immediately, not eventually -- so this exists
// only to absorb ordinary process/network scheduling jitter between the
// "remove" call committing and the next connection's handshake, never to
// paper over an actually-delayed invalidation.
func assertBYOCCertRefused(t *testing.T, cli string, conn []string, proto multiProtocol, args ...string) {
	t.Helper()
	var out string
	if !pollUntil(10*time.Second, 200*time.Millisecond, func() bool {
		var err error
		out, err = runQRDB(t, cli, conn, args...)
		return err != nil
	}) {
		t.Fatalf("[%s] a certificate whose trust anchor was withdrawn must be REFUSED, got: %s", proto.name, out)
	}
}

// TestTrustAnchors_CLI_BYOCAdmitsThenRefused_PerProtocol proves the
// feature's central guarantee end-to-end, on every transport: a tenant
// bootstrapped in one `tenant create --admin-cert --trust-anchors` call
// (pkg/bootstrap.ProvisionTenant's Step 1b registers the anchor before Step
// 4 verifies the admin certificate against it) admits a bring-your-own-cert
// login signed by that anchor, and the moment the tenant withdraws its own
// anchor, the SAME certificate is refused -- not merely until some cache
// expires.
func TestTrustAnchors_CLI_BYOCAdmitsThenRefused_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			tenantID := fmt.Sprintf("ta-byoc-%s", proto.name)
			foreignCA := generateTestCA(t)
			leaf := foreignCA.issue(t, t.TempDir(), qrdbdharness.WithSubject("biz-admin", tenantID))

			if out, err := runCLIProto(t, node, proto, "tenant", "create",
				"--id", tenantID, "--name", tenantID, "--isolation-type", "prefix",
				"--admin-username", "biz-admin",
				"--admin-cert", leaf.CertPath,
				"--trust-anchors", foreignCA.CertPath,
			); err != nil {
				t.Fatalf("[%s] tenant create with BYOC admin + trust anchor: %v\noutput: %s", proto.name, err, out)
			}

			byocConn := tenantCLIArgsForProto(node, proto, leaf.CertPath, leaf.KeyPath)

			// The anchor vouches: the BYOC certificate authenticates and its
			// account (the tenant's new admin) authorizes its own
			// trust-anchors:list.
			listed := trustAnchorList(t, node.cli, byocConn, tenantID)
			fp := certFingerprint(t, foreignCA.CertPath)
			if len(listed.Anchors) != 1 || !trustAnchorListHasFingerprint(listed, fp) {
				t.Fatalf("[%s] BYOC admin's own trust-anchor list = %+v, want only %s", proto.name, listed.Anchors, fp)
			}

			// It withdraws its own anchor -- its last successful action
			// under this identity.
			if out, err := runQRDB(t, node.cli, byocConn, "tenant", "trust-anchors", "remove",
				"--tenant-id", tenantID, "--fingerprint", fp); err != nil {
				t.Fatalf("[%s] BYOC admin removing its own anchor: %v\noutput: %s", proto.name, err, out)
			}

			assertBYOCCertRefused(t, node.cli, byocConn, proto,
				"tenant", "trust-anchors", "list", "--tenant-id", tenantID)
		})
	}
}

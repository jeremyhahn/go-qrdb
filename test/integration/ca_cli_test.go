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

// ca_cli_test.go drives the `qrdb ca` COMMAND TREE (info, bundle, list,
// issue, revoke, export-bundle) through the real client CLI binary
// (cmd/qrdb), against every transport (REST, gRPC, QUIC, MCP, Unix) sharing
// the multi_protocol_helpers_test.go harness.
//
// The "ca" group is qrdb's canonical post-init CA surface backed by the
// daemon's internal ceremony CA (pkg/cert.DefaultCertProvider, barrier-sealed
// key material, exposed via sdk/go/transport.CertClient on every network
// transport) -- NOT xkms-delegated; see cmd/qrdb/ca.go's doc comment ("The
// command is the canonical post-init CA surface"). Per cmd/qrdb/ca.go,
// "info" and "bundle" are thin aliases of "list" (newCAInfoCmd/newCABundleCmd
// literally return newCertsListCmd() with the Use string renamed) pending
// real per-CA introspection, so this file asserts that documented alias
// behavior rather than distinct CA-metadata output.
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

// caCertBundleCLIJSON mirrors transport.CertBundle, decoded from the
// unwrapped JSON `ca issue` print (cmd/qrdb/certs.go's printCertBundle
// marshals the bare bundle) when --out is not supplied.
type caCertBundleCLIJSON struct {
	CertPEM string `json:"certPEM"`
	KeyPEM  string `json:"keyPEM"`
	Serial  string `json:"serial"`
}

// caCertRecordCLIJSON mirrors transport.CertRecord.
type caCertRecordCLIJSON struct {
	Serial    string `json:"serial"`
	Username  string `json:"username"`
	TenantID  string `json:"tenantID"`
	IssuedAt  string `json:"issuedAt"`
	ExpiresAt string `json:"expiresAt"`
	Revoked   bool   `json:"revoked"`
}

// caListCertsCLIJSON mirrors transport.ListCertsResponse, the (wrapped)
// shape `ca list|info|bundle --output json` actually prints.
type caListCertsCLIJSON struct {
	Certs []caCertRecordCLIJSON `json:"certs"`
}

// TestCA_CLI_Lifecycle_PerProtocol drives the full `qrdb ca` surface --
// issue, list, info, bundle, revoke, export-bundle -- through the CLI binary
// against every transport sharing one multiProtoNode.
func TestCA_CLI_Lifecycle_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testCALifecycleOverProtocol(t, node, proto)
		})
	}
}

func testCALifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	username := fmt.Sprintf("cac-%s-%d", proto.name, time.Now().UnixNano())

	// --- issue (JSON print form: no --out) ----------------------------------
	out, err := runCLIProto(t, node, proto, "--output", "json", "ca", "issue",
		"--username", username,
		"--ttl", "24h",
	)
	if err != nil {
		t.Fatalf("[%s] ca issue (json): %v\noutput: %s", proto.name, err, out)
	}
	var bundle caCertBundleCLIJSON
	if jerr := json.Unmarshal([]byte(out), &bundle); jerr != nil {
		t.Fatalf("[%s] decode ca issue JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if bundle.Serial == "" || !strings.Contains(bundle.CertPEM, "BEGIN CERTIFICATE") || !strings.Contains(bundle.KeyPEM, "PRIVATE KEY") {
		t.Fatalf("[%s] ca issue (json) result = %+v, want non-empty serial and PEM cert/key", proto.name, bundle)
	}
	serial := bundle.Serial

	// --- list: the freshly-issued cert appears, not revoked ----------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "ca", "list")
	if err != nil {
		t.Fatalf("[%s] ca list: %v\noutput: %s", proto.name, err, out)
	}
	var listed caListCertsCLIJSON
	if jerr := json.Unmarshal([]byte(out), &listed); jerr != nil {
		t.Fatalf("[%s] decode ca list JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	rec, found := findCertRecord(listed.Certs, serial)
	if !found || rec.Username != username || rec.Revoked {
		t.Fatalf("[%s] ca list does not contain the expected unrevoked record for serial %s (found=%v): %+v", proto.name, serial, found, rec)
	}

	// --- info / bundle: documented aliases of list, same shape --------------
	for _, alias := range []string{"info", "bundle"} {
		out, err = runCLIProto(t, node, proto, "--output", "json", "ca", alias)
		if err != nil {
			t.Fatalf("[%s] ca %s: %v\noutput: %s", proto.name, alias, err, out)
		}
		var aliasListed caListCertsCLIJSON
		if jerr := json.Unmarshal([]byte(out), &aliasListed); jerr != nil {
			t.Fatalf("[%s] decode ca %s JSON: %v\noutput: %s", proto.name, alias, jerr, out)
		}
		if _, found := findCertRecord(aliasListed.Certs, serial); !found {
			t.Fatalf("[%s] ca %s (alias of list) does not contain serial %s: %+v", proto.name, alias, serial, aliasListed.Certs)
		}
	}

	// --- issue (file form: --out, for export-bundle's PEM inputs) -----------
	outDir := t.TempDir()
	out, err = runCLIProto(t, node, proto, "ca", "issue",
		"--username", username+"-export",
		"--out", outDir,
	)
	if err != nil {
		t.Fatalf("[%s] ca issue (--out): %v\noutput: %s", proto.name, err, out)
	}
	certPath := filepath.Join(outDir, "cert.pem")
	keyPath := filepath.Join(outDir, "key.pem")
	if _, statErr := os.Stat(certPath); statErr != nil {
		t.Fatalf("[%s] ca issue (--out) did not write %s: %v", proto.name, certPath, statErr)
	}
	if _, statErr := os.Stat(keyPath); statErr != nil {
		t.Fatalf("[%s] ca issue (--out) did not write %s: %v", proto.name, keyPath, statErr)
	}

	// --- export-bundle -------------------------------------------------
	p12Path := filepath.Join(outDir, "bundle.p12")
	out, err = runCLIProto(t, node, proto, "ca", "export-bundle",
		"--cert-pem", certPath,
		"--key-pem", keyPath,
		"--passphrase", "ca-cli-test-passphrase",
		"--out", p12Path,
	)
	if err != nil {
		t.Fatalf("[%s] ca export-bundle: %v\noutput: %s", proto.name, err, out)
	}
	if info, statErr := os.Stat(p12Path); statErr != nil || info.Size() == 0 {
		t.Fatalf("[%s] ca export-bundle did not write a non-empty PKCS#12 archive to %s (stat err: %v)", proto.name, p12Path, statErr)
	}

	// --- revoke ----------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "ca", "revoke", "--serial", serial)
	if err != nil {
		t.Fatalf("[%s] ca revoke: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] ca revoke: expected OK, got: %s", proto.name, out)
	}

	// --- list (post-revoke): the record persists, now marked revoked --------
	out, err = runCLIProto(t, node, proto, "--output", "json", "ca", "list")
	if err != nil {
		t.Fatalf("[%s] ca list (post-revoke): %v\noutput: %s", proto.name, err, out)
	}
	var listedAfterRevoke caListCertsCLIJSON
	if jerr := json.Unmarshal([]byte(out), &listedAfterRevoke); jerr != nil {
		t.Fatalf("[%s] decode ca list JSON (post-revoke): %v\noutput: %s", proto.name, jerr, out)
	}
	recAfter, found := findCertRecord(listedAfterRevoke.Certs, serial)
	if !found || !recAfter.Revoked {
		t.Fatalf("[%s] ca list (post-revoke) record for serial %s (found=%v) = %+v, want revoked=true", proto.name, serial, found, recAfter)
	}
}

// findCertRecord returns the record with the given serial.
func findCertRecord(certs []caCertRecordCLIJSON, serial string) (caCertRecordCLIJSON, bool) {
	for _, c := range certs {
		if c.Serial == serial {
			return c, true
		}
	}
	return caCertRecordCLIJSON{}, false
}

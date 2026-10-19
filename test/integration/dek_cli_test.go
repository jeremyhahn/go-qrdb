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

// dek_cli_test.go drives the entire `qrdb dek` command tree (status, rotate,
// retire, encrypt, decrypt, rewrap) through the real client CLI binary
// (cmd/qrdb/dek.go), against every transport (REST, gRPC, QUIC, MCP, Unix)
// using the shared startMultiProtocolNode harness
// (multi_protocol_helpers_test.go). dek_test.go already drives the tenant
// DEK keyring's cluster-replication and restart-survival behavior directly
// over REST; this file closes the CLI-transport gap test/integration/COVERAGE.md
// records for tenant-dek -- a lifecycle identical to
// test/integration/api/cli_dek_test.go's Docker-based
// TestCLIMultiProtocolDEKLifecycle, but against the self-spawned harness.
package integration_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// dekEncryptCLIJSON mirrors transport.TenantDEKEncryptResponse.
type dekEncryptCLIJSON struct {
	Ciphertext []byte `json:"ciphertext"`
	Version    uint64 `json:"version"`
}

// dekRewrapCLIJSON mirrors transport.TenantDEKRewrapResponse.
type dekRewrapCLIJSON struct {
	Ciphertext  []byte `json:"ciphertext"`
	FromVersion uint64 `json:"from_version"`
	ToVersion   uint64 `json:"to_version"`
}

// dekStatusCLIJSON mirrors transport.TenantDEKStatusResponse.
type dekStatusCLIJSON struct {
	TenantID       string `json:"tenant_id"`
	CurrentVersion uint64 `json:"current_version"`
}

// TestDEK_CLI_PerProtocol drives the whole `qrdb dek` keyring lifecycle --
// encrypt, rotate (old ciphertext still decrypts), rewrap, retire the old
// version (its ciphertext stops decrypting, the rewrapped one does not), and
// the three refusals (a mismatched AAD, retiring the current version, and
// naming another tenant) -- through the CLI binary against every transport
// sharing one multiProtoNode.
func TestDEK_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testDEKLifecycleOverProtocol(t, node, proto)
		})
	}
}

func testDEKLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	plaintext := fmt.Sprintf("secret-over-%s-%d", proto.name, time.Now().UnixNano())
	aad := base64.StdEncoding.EncodeToString([]byte("dekcli/" + proto.name))
	wrongAAD := base64.StdEncoding.EncodeToString([]byte("dekcli/other"))

	// --- encrypt ---------------------------------------------------------
	out, err := runCLIProto(t, node, proto, "--output", "json", "dek", "encrypt", "--plaintext", plaintext, "--aad", aad)
	if err != nil {
		t.Fatalf("[%s] dek encrypt: %v\noutput: %s", proto.name, err, out)
	}
	var enc dekEncryptCLIJSON
	if jerr := json.Unmarshal([]byte(out), &enc); jerr != nil {
		t.Fatalf("[%s] decode dek encrypt JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	original := base64.StdEncoding.EncodeToString(enc.Ciphertext)

	// --- rotate ------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "dek", "rotate")
	if err != nil {
		t.Fatalf("[%s] dek rotate: %v\noutput: %s", proto.name, err, out)
	}
	var st dekStatusCLIJSON
	if jerr := json.Unmarshal([]byte(out), &st); jerr != nil {
		t.Fatalf("[%s] decode dek rotate JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if st.CurrentVersion <= enc.Version {
		t.Fatalf("[%s] rotate left current v%d, sealed under v%d", proto.name, st.CurrentVersion, enc.Version)
	}

	// Rotation keeps the old version decrypting.
	out, err = runCLIProto(t, node, proto, "dek", "decrypt", original, "--aad", aad)
	if err != nil {
		t.Fatalf("[%s] dek decrypt (after rotate): %v\noutput: %s", proto.name, err, out)
	}
	if strings.TrimSpace(out) != plaintext {
		t.Fatalf("[%s] dek decrypt (after rotate) = %q, want %q", proto.name, strings.TrimSpace(out), plaintext)
	}

	// --- rewrap ------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "dek", "rewrap", original, "--aad", aad)
	if err != nil {
		t.Fatalf("[%s] dek rewrap: %v\noutput: %s", proto.name, err, out)
	}
	var rw dekRewrapCLIJSON
	if jerr := json.Unmarshal([]byte(out), &rw); jerr != nil {
		t.Fatalf("[%s] decode dek rewrap JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if rw.FromVersion != enc.Version || rw.ToVersion != st.CurrentVersion {
		t.Fatalf("[%s] rewrap v%d -> v%d, want v%d -> v%d", proto.name, rw.FromVersion, rw.ToVersion, enc.Version, st.CurrentVersion)
	}
	rewrapped := base64.StdEncoding.EncodeToString(rw.Ciphertext)

	// --- refusals ------------------------------------------------------------
	if out, err = runCLIProto(t, node, proto, "dek", "decrypt", rewrapped, "--aad", wrongAAD); err == nil {
		t.Fatalf("[%s] dek decrypt with a mismatched AAD should fail, got: %s", proto.name, out)
	}
	if out, err = runCLIProto(t, node, proto, "dek", "retire", strconv.FormatUint(st.CurrentVersion, 10)); err == nil {
		t.Fatalf("[%s] dek retire of the current version should fail, got: %s", proto.name, out)
	}
	if out, err = runCLIProto(t, node, proto, "dek", "status", "--tenant", "dek-cli-foreign-tenant"); err == nil {
		t.Fatalf("[%s] dek status naming another tenant should fail, got: %s", proto.name, out)
	}

	// --- retire --------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "dek", "retire", strconv.FormatUint(enc.Version, 10))
	if err != nil {
		t.Fatalf("[%s] dek retire: %v\noutput: %s", proto.name, err, out)
	}
	if out, err = runCLIProto(t, node, proto, "dek", "decrypt", original, "--aad", aad); err == nil {
		t.Fatalf("[%s] dek decrypt of a retired version should fail, got: %s", proto.name, out)
	}

	out, err = runCLIProto(t, node, proto, "dek", "decrypt", rewrapped, "--aad", aad)
	if err != nil {
		t.Fatalf("[%s] dek decrypt (rewrapped, after retire): %v\noutput: %s", proto.name, err, out)
	}
	if strings.TrimSpace(out) != plaintext {
		t.Fatalf("[%s] dek decrypt (rewrapped, after retire) = %q, want %q", proto.name, strings.TrimSpace(out), plaintext)
	}
}

// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package integration

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Wire-format mirrors of the tenant DEK keyring responses.
// ---------------------------------------------------------------------------

type dekEncryptResponse struct {
	Ciphertext []byte `json:"ciphertext"`
	Version    uint64 `json:"version"`
}

type dekRewrapResponse struct {
	Ciphertext  []byte `json:"ciphertext"`
	FromVersion uint64 `json:"from_version"`
	ToVersion   uint64 `json:"to_version"`
}

type dekStatusResponse struct {
	TenantID       string `json:"tenant_id"`
	CurrentVersion uint64 `json:"current_version"`
	Versions       []struct {
		Version   uint64  `json:"version"`
		RetiredAt *string `json:"retired_at"`
	} `json:"versions"`
}

// dekCLI runs a dek subcommand over proto with JSON output and decodes it.
func dekCLI(t *testing.T, cfg *TestConfig, proto ProtocolType, out any, args ...string) {
	t.Helper()
	full := append([]string{"--output", "json", "dek"}, args...)
	stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, full...)
	if err != nil {
		t.Fatalf("[%s] dek %s failed: %v\nstdout: %s\nstderr: %s", proto, strings.Join(args, " "), err, stdout, stderr)
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), out); err != nil {
		t.Fatalf("[%s] dek %s: bad JSON %q: %v", proto, strings.Join(args, " "), stdout, err)
	}
}

// dekCLIFails asserts a dek subcommand over proto is refused.
func dekCLIFails(t *testing.T, cfg *TestConfig, proto ProtocolType, why string, args ...string) {
	t.Helper()
	full := append([]string{"dek"}, args...)
	if stdout, _, err := execCLIWithProtocol(t, cfg, proto, full...); err == nil {
		t.Errorf("[%s] %s: dek %s succeeded (%s)", proto, why, strings.Join(args, " "), stdout)
	}
}

// TestCLIMultiProtocolDEKLifecycle drives the whole keyring lifecycle over
// every protocol against the live server: encrypt, rotate (old data still
// decrypts), rewrap, retire the old version (its ciphertext stops
// decrypting, the rewrapped one does not), and the three refusals — a
// mismatched AAD, retiring the current version and naming another tenant.
func TestCLIMultiProtocolDEKLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}
			plaintext := fmt.Sprintf("secret-over-%s", proto)
			aad := base64.StdEncoding.EncodeToString([]byte("app/" + string(proto) + "/v1"))
			wrongAAD := base64.StdEncoding.EncodeToString([]byte("app/other"))

			var enc dekEncryptResponse
			dekCLI(t, cfg, proto, &enc, "encrypt", "--plaintext", plaintext, "--aad", aad)
			original := base64.StdEncoding.EncodeToString(enc.Ciphertext)

			var st dekStatusResponse
			dekCLI(t, cfg, proto, &st, "rotate")
			if st.CurrentVersion <= enc.Version {
				t.Fatalf("[%s] rotate left current v%d, sealed under v%d", proto, st.CurrentVersion, enc.Version)
			}

			// Rotation keeps the old version decrypting.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "dek", "decrypt", original, "--aad", aad)
			if err != nil {
				t.Fatalf("[%s] decrypt after rotate: %v (%s)", proto, err, stderr)
			}
			assertEqual(t, plaintext, strings.TrimSpace(stdout), fmt.Sprintf("[%s] old ciphertext after rotate", proto))

			var rw dekRewrapResponse
			dekCLI(t, cfg, proto, &rw, "rewrap", original, "--aad", aad)
			if rw.FromVersion != enc.Version || rw.ToVersion != st.CurrentVersion {
				t.Fatalf("[%s] rewrap v%d -> v%d, want v%d -> v%d", proto, rw.FromVersion, rw.ToVersion, enc.Version, st.CurrentVersion)
			}
			rewrapped := base64.StdEncoding.EncodeToString(rw.Ciphertext)

			dekCLIFails(t, cfg, proto, "mismatched AAD", "decrypt", rewrapped, "--aad", wrongAAD)
			dekCLIFails(t, cfg, proto, "retiring the current version", "retire", strconv.FormatUint(st.CurrentVersion, 10))
			dekCLIFails(t, cfg, proto, "another tenant", "status", "--tenant", "dek-foreign-tenant")

			dekCLI(t, cfg, proto, &st, "retire", strconv.FormatUint(enc.Version, 10))
			dekCLIFails(t, cfg, proto, "a retired version", "decrypt", original, "--aad", aad)

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "dek", "decrypt", rewrapped, "--aad", aad)
			if err != nil {
				t.Fatalf("[%s] decrypt rewrapped after retire: %v (%s)", proto, err, stderr)
			}
			assertEqual(t, plaintext, strings.TrimSpace(stdout), fmt.Sprintf("[%s] rewrapped ciphertext after retire", proto))
		})
	}
}

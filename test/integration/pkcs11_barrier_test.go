// Copyright 2026 Jeremy Hahn
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

//go:build integration && pkcs11 && softhsm

// Package integration_test exercises the PKCS#11 seal strategy against a real
// SoftHSM2 token. The test is skipped automatically when libsofthsm2.so is not
// found at common installation paths.
//
// Prerequisites (handled by devcontainer):
//
//	apt-get install softhsm2
//	softhsm2-util --init-token --free --label qrdb-test --pin 1234 --so-pin 0000
//
// Build: go test -tags "integration pkcs11 softhsm" ./test/integration/...
package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// commonSoftHSMLibPaths are the canonical installation locations for
// libsofthsm2.so across Linux distributions.
var commonSoftHSMLibPaths = []string{
	"/usr/lib/softhsm/libsofthsm2.so",
	"/usr/lib/x86_64-linux-gnu/softhsm/libsofthsm2.so",
	"/usr/lib64/softhsm/libsofthsm2.so",
	"/usr/local/lib/softhsm/libsofthsm2.so",
}

// findSoftHSMLib returns the path to libsofthsm2.so or empty string if not found.
func findSoftHSMLib() string {
	for _, p := range commonSoftHSMLibPaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// initSoftHSMToken initializes a fresh SoftHSM token for testing.
// Returns the slot ID string or skips the test if softhsm2-util is unavailable.
func initSoftHSMToken(t *testing.T, label, userPIN, soPIN string) {
	t.Helper()
	cmd := exec.Command("softhsm2-util",
		"--init-token", "--free",
		"--label", label,
		"--pin", userPIN,
		"--so-pin", soPIN,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("softhsm2-util unavailable (%v); output: %s", err, out)
	}
}

// generateAESKEK creates a 256-bit AES key labelled kekLabel on the token
// using pkcs11-tool. Skips if pkcs11-tool is unavailable.
func generateAESKEK(t *testing.T, libPath, tokenLabel, userPIN, kekLabel string) {
	t.Helper()
	cmd := exec.Command("pkcs11-tool",
		"--module", libPath,
		"--token-label", tokenLabel,
		"--login", "--pin", userPIN,
		"--keygen",
		"--key-type", "AES:32",
		"--label", kekLabel,
		"--extractable", "--sensitive=no",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// pkcs11-tool may not be present; skip gracefully.
		if strings.Contains(string(out), "not found") || strings.Contains(err.Error(), "not found") {
			t.Skipf("pkcs11-tool not found; skipping SoftHSM KEK generation: %s", out)
		}
		t.Skipf("pkcs11-tool failed: %v; output: %s", err, out)
	}
}

// TestPKCS11Barrier_SoftHSM_RoundTrip initializes a SoftHSM token, generates
// an AES-256 KEK, seals a known root key via PKCS11Strategy, unseals it, and
// asserts the round-trip. The test is skipped when SoftHSM is unavailable.
func TestPKCS11Barrier_SoftHSM_RoundTrip(t *testing.T) {
	libPath := findSoftHSMLib()
	if libPath == "" {
		t.Skip("libsofthsm2.so not found at common paths; skipping SoftHSM integration test")
	}

	const (
		tokenLabel = "qrdb-integration-test"
		userPIN    = "1234"
		soPIN      = "0000"
		kekLabel   = "qrdb-kek"
		pinEnvVar  = "QRDB_SOFTHSM_TEST_PIN"
	)

	// Set the user PIN env var so PKCS11Config.UserPIN() can retrieve it.
	t.Setenv(pinEnvVar, userPIN)

	// Initialize a fresh token (skips if softhsm2-util absent).
	initSoftHSMToken(t, tokenLabel, userPIN, soPIN)

	// Create the KEK on the token (skips if pkcs11-tool absent).
	generateAESKEK(t, libPath, tokenLabel, userPIN, kekLabel)

	cfg := &seal.PKCS11Config{
		ModuleLibPath: libPath,
		TokenLabel:    tokenLabel,
		KEKLabel:      kekLabel,
		UserPinEnvVar: pinEnvVar,
	}

	strategy, err := seal.NewPKCS11Strategy(cfg)
	if err != nil {
		t.Fatalf("NewPKCS11Strategy: %v", err)
	}
	defer func() { _ = strategy.Close() }()

	if !strategy.Available() {
		t.Skip("PKCS11Strategy.Available() returned false; token or KEK not accessible")
	}

	// Generate a random root key.
	rootKey := make([]byte, 32)
	if _, err := rand.Read(rootKey); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	sealed, err := strategy.SealRootKey(context.Background(), rootKey, seal.Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}
	if sealed == nil {
		t.Fatal("SealRootKey returned nil")
	}
	if sealed.Strategy != seal.StrategyID("pkcs11") {
		t.Fatalf("sealed.Strategy = %q, want %q", sealed.Strategy, seal.StrategyID("pkcs11"))
	}
	if len(sealed.HardwarePayload) == 0 {
		t.Fatal("HardwarePayload is empty")
	}

	plainKey, err := strategy.UnsealRootKey(context.Background(), sealed, seal.Credentials{})
	if err != nil {
		t.Fatalf("UnsealRootKey: %v", err)
	}

	if !bytes.Equal(rootKey, plainKey) {
		t.Fatal("round-trip failed: unsealed key does not match original")
	}
}

// TestPKCS11Barrier_SoftHSM_MissingLib_Unavailable verifies that a strategy
// configured with a nonexistent library path reports unavailable rather than
// panicking.
func TestPKCS11Barrier_SoftHSM_MissingLib_Unavailable(t *testing.T) {
	cfg := &seal.PKCS11Config{
		ModuleLibPath: "/nonexistent/libpkcs11.so",
		KEKLabel:      "kek",
	}
	_, err := seal.NewPKCS11Strategy(cfg)
	if err == nil {
		t.Fatal("expected error for nonexistent library path, got nil")
	}
}

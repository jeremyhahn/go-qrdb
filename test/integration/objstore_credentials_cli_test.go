// Copyright (c) 2026 Jeremy Hahn
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

package integration_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Fake object-storage credential material: never a real key.
const (
	fakeObjstoreKey    = "FAKEACCESSKEYID0000"
	fakeObjstoreSecret = "fake-objstore-secret-for-integration"
)

// writeObjstoreCredentialFile writes a fake credential to a file with mode.
func writeObjstoreCredentialFile(t *testing.T, dir, name, value string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(value+"\n"), mode); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", name, err)
	}
	return path
}

// writeObjstoreModeConfig writes a standalone configuration whose default
// database is the objstore backend on the local provider; objstore is the
// YAML appended under storage.objstore after its settings.
func writeObjstoreModeConfig(t *testing.T, objstore string) string {
	t.Helper()
	dir := t.TempDir()
	body := fmt.Sprintf("seal:\n  enabled: true\n  strategy: passphrase\n"+
		"databases:\n  - name: \"default\"\n    backend: \"objstore\"\n"+
		"storage:\n  objstore:\n    provider: local\n    settings:\n      path: %q\n%s",
		filepath.Join(dir, "bucket"), objstore)
	path := filepath.Join(dir, "qrdb.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestObjstoreCredentials_RefusesAnInlineCredential proves qrdbd start
// refuses a provider credential written into the configuration file, under
// settings or under credentials, naming the key and never echoing the value.
func TestObjstoreCredentials_RefusesAnInlineCredential(t *testing.T) {
	bin := buildBinary(t)
	cases := map[string]struct {
		objstore string
		names    string
	}{
		"in settings": {
			objstore: "      secret_key: " + fakeObjstoreSecret + "\n",
			names:    "storage.objstore.settings",
		},
		"under credentials": {
			objstore: "    credentials:\n      secret: " + fakeObjstoreSecret + "\n",
			names:    "storage.objstore.credentials.secret",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out := &watchOutputBuf{}
			cmd := spawnNode(t, bin, []string{
				"start",
				"--config", writeObjstoreModeConfig(t, tc.objstore),
				"--mode", "standalone",
				"--data-dir", t.TempDir(),
			}, out)
			if err := awaitNodeExit(t, cmd, 30*time.Second); err == nil {
				t.Fatalf("qrdbd start accepted an inline credential:\n%s", out)
			}
			if strings.Contains(out.String(), fakeObjstoreSecret) {
				t.Fatal("qrdbd start echoed the credential")
			}
			if !strings.Contains(out.String(), tc.names) {
				t.Fatalf("output does not name %q:\n%s", tc.names, out)
			}
		})
	}
}

// TestObjstoreCredentials_IgnoredCredentialFailsTheBoot proves a credential
// reference for a provider that does not read it stops the boot once the
// barrier unseals rather than open the store under the ambient identity: the
// "local" provider reads no credential, the error names the refused file, and
// the secret never reaches the output.
func TestObjstoreCredentials_IgnoredCredentialFailsTheBoot(t *testing.T) {
	bin := buildBinary(t)
	ca := generateTestCA(t)
	secrets := t.TempDir()
	keyFile := writeObjstoreCredentialFile(t, secrets, "access-key", fakeObjstoreKey, 0o600)
	secretFile := writeObjstoreCredentialFile(t, secrets, "secret-key", fakeObjstoreSecret, 0o600)
	cfg := writeObjstoreModeConfig(t, fmt.Sprintf(
		"    credentials:\n      key_file: %q\n      secret_file: %q\n", keyFile, secretFile))

	restPort := allocateTCPPorts(t, 1)[0]
	args := append([]string{
		"start",
		"--config", cfg,
		"--mode", "standalone",
		"--data-dir", t.TempDir(),
		"--admin-port", strconv.Itoa(restPort),
		"--auth-enabled",
		"--enable-rest",
	}, ca.tlsFlags(t)...)
	out := &watchOutputBuf{}
	cmd := spawnNode(t, bin, args, out)

	waitForBarrierStatusReachable(t, ca, restPort, 90*time.Second)
	initAndUnsealBarrier(t, ca, restPort, "objstore-credentials-passphrase")

	if err := awaitNodeExit(t, cmd, 60*time.Second); err == nil {
		t.Fatalf("qrdbd booted with a credential the local provider ignores:\n%s", out)
	}
	if !strings.Contains(out.String(), `provider "local" does not read the access key credential`) ||
		!strings.Contains(out.String(), strconv.Quote(keyFile)) {
		t.Fatalf("output does not refuse the key file %q as ignored:\n%s", keyFile, out)
	}
	if strings.Contains(out.String(), fakeObjstoreSecret) {
		t.Fatal("qrdbd echoed the credential")
	}
}

// randomObjstoreCredential returns credential material generated for this
// test run, so no committed value can mask an echo.
func randomObjstoreCredential(t *testing.T) string {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("generate credential: %v", err)
	}
	return hex.EncodeToString(b)
}

// TestObjstoreCredentials_NeverEchoesTheKeyOrSecret boots qrdbd with key and
// secret files generated at test time and proves neither value reaches the
// node's output, the barrier replies' output, or any file the node writes
// under its data directory or the provider's bucket: the credential crosses
// the seam only as a protected-memory handle.
func TestObjstoreCredentials_NeverEchoesTheKeyOrSecret(t *testing.T) {
	bin := buildBinary(t)
	ca := generateTestCA(t)
	key := randomObjstoreCredential(t)
	secret := randomObjstoreCredential(t)
	secrets := t.TempDir()
	keyFile := writeObjstoreCredentialFile(t, secrets, "access-key", key, 0o600)
	secretFile := writeObjstoreCredentialFile(t, secrets, "secret-key", secret, 0o600)
	cfg := writeObjstoreModeConfig(t, fmt.Sprintf(
		"    credentials:\n      key_file: %q\n      secret_file: %q\n", keyFile, secretFile))
	dataDir := t.TempDir()

	restPort := allocateTCPPorts(t, 1)[0]
	args := append([]string{
		"start",
		"--config", cfg,
		"--mode", "standalone",
		"--data-dir", dataDir,
		"--admin-port", strconv.Itoa(restPort),
		"--auth-enabled",
		"--enable-rest",
	}, ca.tlsFlags(t)...)
	out := &watchOutputBuf{}
	cmd := spawnNode(t, bin, args, out)

	waitForBarrierStatusReachable(t, ca, restPort, 90*time.Second)
	initAndUnsealBarrier(t, ca, restPort, "objstore-credentials-passphrase")
	_ = awaitNodeExit(t, cmd, 60*time.Second)

	for name, value := range map[string]string{"key": key, "secret": secret} {
		if strings.Contains(out.String(), value) {
			t.Fatalf("qrdbd echoed the %s credential", name)
		}
		for _, root := range []string{dataDir, filepath.Dir(cfg)} {
			err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil || !d.Type().IsRegular() {
					return err
				}
				body, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if bytes.Contains(body, []byte(value)) {
					t.Errorf("qrdbd wrote the %s credential to %s", name, path)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("walk %s: %v", root, err)
			}
		}
	}
}

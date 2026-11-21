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
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
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
// YAML appended under databases[0].objstore after its settings.
func writeObjstoreModeConfig(t *testing.T, objstore string) string {
	t.Helper()
	dir := t.TempDir()
	return writeObjstoreProviderConfig(t, dir, fmt.Sprintf(
		"    provider: local\n    settings:\n      path: %q\n", filepath.Join(dir, "bucket")), objstore)
}

// writeObjstoreProviderConfig writes, into dir, a standalone configuration
// whose default database is the objstore backend; provider is the YAML of
// the provider and its settings under databases[0].objstore, and objstore the
// YAML appended after it.
func writeObjstoreProviderConfig(t *testing.T, dir, provider, objstore string) string {
	t.Helper()
	// Config.Load requires databases[].objstore.provider for an objstore
	// backend, so the provider block sits under the database.
	body := "seal:\n  enabled: false\nauth:\n  enabled: false\n" +
		"databases:\n  - name: \"default\"\n    backend: \"objstore\"\n    objstore:\n" +
		indentYAML(provider) + indentYAML(objstore)
	path := filepath.Join(dir, "qrdb.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// indentYAML indents every non-empty line of block by two more spaces.
func indentYAML(block string) string {
	lines := strings.SplitAfter(block, "\n")
	var b strings.Builder
	for _, line := range lines {
		if line != "" && line != "\n" {
			b.WriteString("  ")
		}
		b.WriteString(line)
	}
	return b.String()
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
			names:    "databases[0].objstore.settings",
		},
		"under credentials": {
			objstore: "    credentials:\n      secret: " + fakeObjstoreSecret + "\n",
			names:    "databases[0].objstore.credentials.secret",
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
// node boots rather than open the store under the ambient identity: the
// "local" provider reads no credential, the error names the refused file, and
// the secret never reaches the output.
func TestObjstoreCredentials_IgnoredCredentialFailsTheBoot(t *testing.T) {
	bin := buildBinary(t)
	secrets := t.TempDir()
	keyFile := writeObjstoreCredentialFile(t, secrets, "access-key", fakeObjstoreKey, 0o600)
	secretFile := writeObjstoreCredentialFile(t, secrets, "secret-key", fakeObjstoreSecret, 0o600)
	cfg := writeObjstoreModeConfig(t, fmt.Sprintf(
		"    credentials:\n      key_file: %q\n      secret_file: %q\n", keyFile, secretFile))

	cmd, out := startObjstoreCredentialNode(t, bin, cfg, t.TempDir())

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

// startObjstoreCredentialNode boots qrdbd from bin with cfg and dataDir; the
// boot opens the objstore backend. The configuration disables the seal (and
// so auth, which requires it): a seal-enabled node refuses an objstore
// database outright (database.SealedNodeBackendError).
func startObjstoreCredentialNode(t *testing.T, bin, cfg, dataDir string) (*exec.Cmd, *watchOutputBuf) {
	t.Helper()
	ca := generateTestCA(t)
	restPort := allocateTCPPorts(t, 1)[0]
	args := append([]string{
		"start",
		"--config", cfg,
		"--mode", "standalone",
		"--data-dir", dataDir,
		"--admin-port", strconv.Itoa(restPort),
		"--enable-rest",
	}, ca.tlsFlags(t)...)
	out := &watchOutputBuf{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("node output:\n%s", out)
		}
	})
	return spawnNode(t, bin, args, out), out
}

// assertObjstoreCredentialsNotEchoed fails the test when any credential in
// creds appears in the node's output, in a file under roots, or in a stored
// object body.
func assertObjstoreCredentialsNotEchoed(t *testing.T, creds map[string]string, output string, roots []string, objects map[string][]byte) {
	t.Helper()
	for name, value := range creds {
		if strings.Contains(output, value) {
			t.Errorf("qrdbd echoed the %s credential", name)
		}
		for _, root := range roots {
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
		for key, body := range objects {
			if bytes.Contains(body, []byte(value)) {
				t.Errorf("qrdbd stored the %s credential in object %q", name, key)
			}
		}
	}
}

// TestObjstoreCredentials_LocalRefusesUnusedCredentialFiles boots qrdbd on
// the local provider with key and secret files generated at test time: the
// local provider reads neither, so the boot stops,
// the error names the refused key file, and neither value reaches the
// node's output, its data directory or its configuration directory.
func TestObjstoreCredentials_LocalRefusesUnusedCredentialFiles(t *testing.T) {
	bin := buildBinary(t)
	key := randomObjstoreCredential(t)
	secret := randomObjstoreCredential(t)
	secrets := t.TempDir()
	keyFile := writeObjstoreCredentialFile(t, secrets, "access-key", key, 0o600)
	secretFile := writeObjstoreCredentialFile(t, secrets, "secret-key", secret, 0o600)
	cfg := writeObjstoreModeConfig(t, fmt.Sprintf(
		"    credentials:\n      key_file: %q\n      secret_file: %q\n", keyFile, secretFile))
	dataDir := t.TempDir()

	cmd, out := startObjstoreCredentialNode(t, bin, cfg, dataDir)
	if err := awaitNodeExit(t, cmd, 60*time.Second); err == nil {
		t.Fatalf("qrdbd booted with credential files the local provider ignores:\n%s", out)
	}
	if !strings.Contains(out.String(), `provider "local" does not read the access key credential`) ||
		!strings.Contains(out.String(), strconv.Quote(keyFile)) {
		t.Fatalf("output does not refuse the key file %q as ignored:\n%s", keyFile, out)
	}
	assertObjstoreCredentialsNotEchoed(t, map[string]string{"key": key, "secret": secret},
		out.String(), []string{dataDir, filepath.Dir(cfg)}, nil)
}

// TestObjstoreCredentials_S3ReceivesBothCredentialsAndNeverEchoesThem boots
// a qrdbd built with the S3 provider against an in-test S3 endpoint that
// admits only requests SigV4-signed with the key and secret generated for
// this run: a verified request proves the provider was configured with both
// credentials read from their files. Neither value may then appear in the
// node's output, its data directory, its configuration directory, or any
// object in the bucket.
//
// The bucket is seeded with the objects a formatted, empty store holds (the
// format marker and an empty commit journal), so the engine opens: the s3
// provider answers a missing object with the SDK's NoSuchKey error rather
// than go-objstore's ErrKeyNotFound, and an empty bucket cannot be opened.
// The node then completes its boot, a key is written and read back through
// the qrdb CLI, and the write must have reached the bucket before the node
// is stopped and checked, so the bodies searched include the written ones.
func TestObjstoreCredentials_S3ReceivesBothCredentialsAndNeverEchoesThem(t *testing.T) {
	bin, err := qrdbdharness.BuildBinary(qrdbdharness.DaemonPackage,
		qrdbdharness.WithBuildTags(qrdbdharness.DefaultBuildTags+" awss3"))
	if err != nil {
		t.Fatalf("build qrdbd with awss3: %v", err)
	}
	key := randomObjstoreCredential(t)
	secret := randomObjstoreCredential(t)
	const bucket, region = "qrdb-credentials", "us-east-1"
	stub := newS3Stub(t, bucket, region, key, secret)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("S3 requests: %v", stub.requestList())
		}
	})

	// The configuration loader lowercases settings keys, so the provider
	// never sees forcePathStyle and addresses the bucket virtual-hosted, as
	// <bucket>.<endpoint host>. The endpoint names a host that never
	// resolves, and qrdbd, which inherits this process's environment,
	// reaches it through the stub as its HTTP proxy. This process's own
	// proxy setting is read once and cached, so it is read before the
	// variable is set and none of its later requests go to the stub.
	_, _ = http.ProxyFromEnvironment(&http.Request{URL: &url.URL{Scheme: "http", Host: "s3.invalid"}})
	t.Setenv("HTTP_PROXY", stub.URL())
	t.Setenv("http_proxy", stub.URL())
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	secrets := t.TempDir()
	keyFile := writeObjstoreCredentialFile(t, secrets, "access-key", key, 0o600)
	secretFile := writeObjstoreCredentialFile(t, secrets, "secret-key", secret, 0o600)
	cfgDir := t.TempDir()
	cfg := writeObjstoreProviderConfig(t, cfgDir, fmt.Sprintf(
		"    provider: s3\n    settings:\n      bucket: %q\n      region: %q\n      endpoint: \"http://s3.invalid\"\n",
		bucket, region), fmt.Sprintf(
		"    credentials:\n      key_file: %q\n      secret_file: %q\n", keyFile, secretFile))
	dataDir := t.TempDir()

	// The bucket starts empty, as a new deployment's does: the node formats
	// the store itself, through the stub.
	// The engine formats the store at open, so a signed PUT of the format
	// marker proves the credential reached the provider. Serving the
	// database on objstore is the object-storage-provider row's, not this
	// one's, so the node is stopped once the signed format-marker PUT is
	// stored.
	cmd, out := startObjstoreCredentialNode(t, bin, cfg, dataDir)
	// Whatever the node did before the test ends, on a passing path or a
	// failing one, it left neither credential behind.
	defer func() {
		stopNode(t, cmd, 30*time.Second)
		assertObjstoreCredentialsNotEchoed(t, map[string]string{"key": key, "secret": secret},
			out.String(), []string{dataDir, cfgDir}, stub.objectBodies())
	}()
	stub.awaitStoredObject(t, 60*time.Second, out)
	stopNode(t, cmd, 30*time.Second)

	if stub.verifiedCount() == 0 {
		t.Fatal("no request reached the S3 stub signed with the generated credentials")
	}
	if !slices.ContainsFunc(stub.requestList(), func(r string) bool { return strings.HasPrefix(r, http.MethodPut+" ") }) {
		t.Errorf("no signed PUT reached the S3 stub; requests: %v", stub.requestList())
	}
	if failures := stub.failureList(); len(failures) != 0 {
		t.Errorf("S3 stub refused or could not serve requests:\n%s\nnode output:\n%s", strings.Join(failures, "\n"), out)
	}
}

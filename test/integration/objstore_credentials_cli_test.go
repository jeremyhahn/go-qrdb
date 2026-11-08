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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
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
// YAML appended under storage.objstore after its settings.
func writeObjstoreModeConfig(t *testing.T, objstore string) string {
	t.Helper()
	dir := t.TempDir()
	return writeObjstoreProviderConfig(t, dir, fmt.Sprintf(
		"    provider: local\n    settings:\n      path: %q\n", filepath.Join(dir, "bucket")), objstore)
}

// writeObjstoreProviderConfig writes, into dir, a standalone configuration
// whose default database is the objstore backend; provider is the YAML of
// the provider and its settings under storage.objstore, and objstore the
// YAML appended after it.
func writeObjstoreProviderConfig(t *testing.T, dir, provider, objstore string) string {
	t.Helper()
	body := "seal:\n  enabled: true\n  strategy: passphrase\n" +
		"databases:\n  - name: \"default\"\n    backend: \"objstore\"\n" +
		"storage:\n  objstore:\n" + provider + objstore
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

// startObjstoreCredentialNode boots qrdbd from bin with cfg and dataDir and
// initializes and unseals its barrier, which opens the objstore backend.
func startObjstoreCredentialNode(t *testing.T, bin, cfg, dataDir string) (*exec.Cmd, *watchOutputBuf) {
	t.Helper()
	cmd, out, _ := startObjstoreCredentialNodeConn(t, bin, cfg, dataDir)
	return cmd, out
}

// startObjstoreCredentialNodeConn is startObjstoreCredentialNode that also
// returns the qrdb CLI's connection flags for the node's REST transport, as
// the admin the ceremony bound.
func startObjstoreCredentialNodeConn(t *testing.T, bin, cfg, dataDir string) (*exec.Cmd, *watchOutputBuf, []string) {
	t.Helper()
	ca := generateTestCA(t)
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
	certPath, keyPath := initAndUnsealBarrier(t, ca, restPort, "objstore-credentials-passphrase")
	return cmd, out, restConnArgs(ca, certPath, keyPath, restPort)
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
// local provider reads neither, so the boot stops once the barrier unseals,
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

	// The MVCC store reads its revision and node compaction floor when it
	// opens; both are seeded at revision 0, a fresh database.
	seeded := stub.seedFormattedStore(t, map[string][]byte{
		string(mvcc.RevisionKey()):            mvcc.EncodeRevision(0),
		string(mvcc.NodeCompactionFloorKey()): mvcc.EncodeRevision(0),
		lease.TablePrefix + "seq":             make([]byte, 8),
	})

	cmd, out, conn := startObjstoreCredentialNodeConn(t, bin, cfg, dataDir)
	stub.awaitSignedRequest(t, 60*time.Second, out)
	cli := buildCLIBinary(t)
	const kvKey, kvValue = "objstore-credentials-key", "objstore-credentials-value"
	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, conn...),
		"kv", "put", kvKey, kvValue)...); err != nil {
		t.Fatalf("kv put: %v\nstderr: %s\nS3 requests: %v\nnode output:\n%s", err, stderr, stub.requestList(), out)
	}
	stdout, stderr, err := runCLISplit(t, cli, append(append([]string{}, conn...), "kv", "get", kvKey)...)
	if err != nil {
		t.Fatalf("kv get: %v\nstderr: %s\nnode output:\n%s", err, stderr, out)
	}
	if !strings.Contains(stdout, kvValue) {
		t.Fatalf("kv get %q = %q, want %q", kvKey, stdout, kvValue)
	}
	written := stub.objectBodies()
	for _, name := range seeded {
		delete(written, name)
	}
	if len(written) == 0 {
		t.Fatalf("the kv put stored no object in the bucket beyond the seeded %v; requests: %v\nnode output:\n%s",
			seeded, stub.requestList(), out)
	}
	stopNode(t, cmd, 30*time.Second)

	if stub.verifiedCount() == 0 {
		t.Fatal("no request reached the S3 stub signed with the generated credentials")
	}
	if failures := stub.failureList(); len(failures) != 0 {
		t.Errorf("S3 stub refused or could not serve requests:\n%s\nnode output:\n%s", strings.Join(failures, "\n"), out)
	}
	assertObjstoreCredentialsNotEchoed(t, map[string]string{"key": key, "secret": secret},
		out.String(), []string{dataDir, cfgDir}, stub.objectBodies())
}

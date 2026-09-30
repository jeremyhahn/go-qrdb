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

package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
)

// writeTLSFixtures generates a CA and node certificate, writes them to
// the given directory using standard file names, and returns the bundle.
func writeTLSFixtures(t *testing.T, dir string) *TLSBundle {
	t.Helper()

	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), bundle.CACertPEM, 0o600); err != nil {
		t.Fatalf("WriteFile ca.pem: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca-key.pem"), bundle.CAKeyPEM, 0o600); err != nil {
		t.Fatalf("WriteFile ca-key.pem: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node-cert.pem"), bundle.CertPEM, 0o600); err != nil {
		t.Fatalf("WriteFile node-cert.pem: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node-key.pem"), bundle.KeyPEM, 0o600); err != nil {
		t.Fatalf("WriteFile node-key.pem: %v", err)
	}
	// Cluster CA material with no enrollment role beside it is the state
	// go-qrdb#264 refuses to boot on, so a fixture that stopped here would not
	// be a cached data directory -- it would be that defect, and every test
	// using it would be asserting against a daemon that no longer starts.
	// single-node because these fixtures assert the cached path starts no
	// enrollment listener.
	if err := os.WriteFile(filepath.Join(dir, clusterenroll.RoleFile),
		[]byte(clusterenroll.RoleSingleNode), 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", clusterenroll.RoleFile, err)
	}

	return bundle
}

// ---------------------------------------------------------------------------
// resolveTLS tests
// ---------------------------------------------------------------------------

func TestResolveTLS_ExplicitFiles(t *testing.T) {
	dir := t.TempDir()
	bundle := writeTLSFixtures(t, dir)

	f := &startFlags{
		dataDir:         t.TempDir(),
		discoveryMethod: "static",
		tlsCAFile:       filepath.Join(dir, "ca.pem"),
		tlsCertFile:     filepath.Join(dir, "node-cert.pem"),
		tlsKeyFile:      filepath.Join(dir, "node-key.pem"),
		host:            testBindHost,
	}

	got, servers, err := resolveTLS(f, testLogger())
	if err != nil {
		t.Fatalf("resolveTLS: %v", err)
	}
	if servers != nil {
		t.Fatal("expected nil bootstrapServers for explicit file path")
	}
	if got == nil {
		t.Fatal("expected non-nil TLSBundle")
	}
	if got.TLSConfig == nil {
		t.Fatal("expected non-nil TLSConfig")
	}
	if len(got.CACertPEM) == 0 {
		t.Fatal("expected non-empty CACertPEM")
	}
	// CACertPEM should match the fixture.
	if string(got.CACertPEM) != string(bundle.CACertPEM) {
		t.Error("CACertPEM does not match fixture")
	}
}

func TestResolveTLS_ExplicitFiles_MissingFile(t *testing.T) {
	f := &startFlags{
		dataDir:         t.TempDir(),
		discoveryMethod: "static",
		tlsCAFile:       "/nonexistent/ca.pem",
		tlsCertFile:     "/nonexistent/cert.pem",
		tlsKeyFile:      "/nonexistent/key.pem",
		host:            testBindHost,
	}

	_, _, err := resolveTLS(f, testLogger())
	if err == nil {
		t.Fatal("expected error for missing TLS files")
	}
	if _, ok := err.(*TLSLoadError); !ok {
		t.Fatalf("expected *TLSLoadError, got %T: %v", err, err)
	}
}

func TestResolveTLS_ExistingCerts(t *testing.T) {
	dir := t.TempDir()
	writeTLSFixtures(t, dir)

	f := &startFlags{
		dataDir:         dir,
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001", "2=127.0.0.2:63001"},
		host:            testBindHost,
	}

	got, servers, err := resolveTLS(f, testLogger())
	if err != nil {
		t.Fatalf("resolveTLS: %v", err)
	}
	if servers != nil {
		t.Fatal("expected nil bootstrapServers for existing cert path")
	}
	if got == nil {
		t.Fatal("expected non-nil TLSBundle")
	}
	if got.TLSConfig == nil {
		t.Fatal("expected non-nil TLSConfig")
	}
	if len(got.CAKeyPEM) == 0 {
		t.Fatal("expected CA key to be loaded from ca-key.pem")
	}
}

func TestResolveTLS_SingleNode(t *testing.T) {
	dir := t.TempDir()

	f := &startFlags{
		dataDir:         dir,
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
		host:            testBindHost,
	}

	got, servers, err := resolveTLS(f, testLogger())
	if err != nil {
		t.Fatalf("resolveTLS: %v", err)
	}
	if servers != nil {
		t.Fatal("expected nil bootstrapServers for single-node path")
	}
	if got == nil {
		t.Fatal("expected non-nil TLSBundle")
	}
	if len(got.CACertPEM) == 0 {
		t.Fatal("expected non-empty CACertPEM")
	}
	if len(got.CAKeyPEM) == 0 {
		t.Fatal("expected non-empty CAKeyPEM")
	}

	// Verify files were persisted.
	if !fileExists(filepath.Join(dir, "ca.pem")) {
		t.Error("expected ca.pem to be persisted")
	}
	if !fileExists(filepath.Join(dir, "node-cert.pem")) {
		t.Error("expected node-cert.pem to be persisted")
	}
	if !fileExists(filepath.Join(dir, "node-key.pem")) {
		t.Error("expected node-key.pem to be persisted")
	}
	if !fileExists(filepath.Join(dir, "ca-key.pem")) {
		t.Error("expected ca-key.pem to be persisted")
	}
}

func TestResolveTLS_FirstNode(t *testing.T) {
	dir := t.TempDir()

	f := &startFlags{
		dataDir:         dir,
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001", "2=127.0.0.2:63001"},
		// The Noise listener has no default bind host of its own: it
		// follows --host, which cobra always populates in production but
		// a hand-built startFlags does not.
		host: testBindHost,
	}
	// Path 5 opens the enrollment listener, so this test owns a port rather
	// than taking the fixed default 63006 every concurrent copy wants
	// (go-qrdb#313). See reserveNoisePort in enrollment_resume_test.go.
	reserveNoisePort(t, f)

	got, servers, err := resolveTLS(f, testLogger())
	if err != nil {
		t.Fatalf("resolveTLS: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil TLSBundle")
	}
	if servers == nil {
		t.Fatal("expected non-nil bootstrapServers for first-node path")
	}
	defer servers.Stop()

	if !noiseServed() {
		// FIPS 140-3: the node advertises the approved methods only. It is a
		// first node either way, which is what registers /v1/ca/bootstrap.
		if servers.NoiseEnabled() {
			t.Fatal("NoiseEnabled() = true, want false when the method is unavailable")
		}
		if fileExists(filepath.Join(dir, noiseKeyFile)) {
			t.Errorf("%s must not be persisted when the Noise method is unavailable", noiseKeyFile)
		}
		if !fileExists(filepath.Join(dir, "ca.pem")) {
			t.Error("expected ca.pem to be persisted")
		}
		return
	}

	if servers.Addr() == "" {
		t.Fatal("expected Noise server to be running")
	}
	if len(servers.NoisePublicKey()) == 0 {
		t.Fatal("expected Noise key to be populated")
	}
	if servers.Addr() == "" {
		t.Fatal("expected Noise server to have a listener address")
	}

	// Verify the noise key was persisted.
	keyPath := filepath.Join(dir, noiseKeyFile)
	if !fileExists(keyPath) {
		t.Error("expected noise-key.hex to be persisted")
	}

	// Verify cert files were persisted.
	if !fileExists(filepath.Join(dir, "ca.pem")) {
		t.Error("expected ca.pem to be persisted")
	}
}

// ---------------------------------------------------------------------------
// hasBootstrapFlags tests
// ---------------------------------------------------------------------------

func TestHasBootstrapFlags_NoFlags(t *testing.T) {
	f := &startFlags{}
	if hasBootstrapFlags(f) {
		t.Error("expected false when no bootstrap flags are set")
	}
}

func TestHasBootstrapFlags_NoiseKey(t *testing.T) {
	f := &startFlags{
		bootstrapNoiseKey: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	}
	if !hasBootstrapFlags(f) {
		t.Error("expected true when bootstrapNoiseKey is set")
	}
}

func TestHasBootstrapFlags_NoiseAddr(t *testing.T) {
	f := &startFlags{
		bootstrapNoiseAddr: "10.0.0.1:63006",
	}
	if !hasBootstrapFlags(f) {
		t.Error("expected true when bootstrapNoiseAddr is set")
	}
}

func TestHasBootstrapFlags_SPKIPin(t *testing.T) {
	f := &startFlags{
		bootstrapSPKIPin: "abc123def456",
	}
	if !hasBootstrapFlags(f) {
		t.Error("expected true when bootstrapSPKIPin is set")
	}
}

func TestHasBootstrapFlags_DirectURL(t *testing.T) {
	f := &startFlags{
		bootstrapDirectURL: "https://bootstrap.example.com:8443",
	}
	if !hasBootstrapFlags(f) {
		t.Error("expected true when bootstrapDirectURL is set")
	}
}

func TestHasBootstrapFlags_DANEDNS(t *testing.T) {
	f := &startFlags{
		bootstrapDANEDNS: "8.8.8.8:53",
	}
	if !hasBootstrapFlags(f) {
		t.Error("expected true when bootstrapDANEDNS is set")
	}
}

func TestHasBootstrapFlags_MultipleFlags(t *testing.T) {
	f := &startFlags{
		bootstrapNoiseKey:  "key",
		bootstrapNoiseAddr: "addr",
		bootstrapSPKIPin:   "pin",
	}
	if !hasBootstrapFlags(f) {
		t.Error("expected true when multiple bootstrap flags are set")
	}
}

// ---------------------------------------------------------------------------
// bootstrapServers.Stop tests
// ---------------------------------------------------------------------------

func TestBootstrapServers_Stop_Nil(t *testing.T) {
	// Verify that Stop on a nil receiver does not panic.
	var bs *bootstrapServers
	bs.Stop()
}

func TestBootstrapServers_Stop_NilServer(t *testing.T) {
	// Verify that Stop with nil internal servers does not panic.
	bs := &bootstrapServers{}
	bs.Stop()
}

// ---------------------------------------------------------------------------
// startBootstrapServers tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// logBootstrapInfo tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// bootstrapTLS: joining is refused before any dial or write
// ---------------------------------------------------------------------------

// TestBootstrapTLS_EveryMethodIsRefusedBeforeAnyDialOrWrite: the cluster CA
// private key is no longer distributed, so no bootstrap method can enroll a
// node in this release. Each configured method -- and a mix of them -- is
// refused with *EnrollmentUnavailableError, not a fetch error (the refusal
// precedes the dial; every endpoint here is dead), and the data directory is
// left empty.
func TestBootstrapTLS_EveryMethodIsRefusedBeforeAnyDialOrWrite(t *testing.T) {
	dead := net.JoinHostPort(testBindHost, strconv.Itoa(deadTCPPort(t)))
	for name, set := range map[string]func(f *startFlags){
		"noise": func(f *startFlags) {
			f.bootstrapNoiseKey = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
			f.bootstrapNoiseAddr = dead
		},
		"spki": func(f *startFlags) {
			f.bootstrapSPKIPin = "sha256//AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
			f.bootstrapAddr = dead
		},
		"dane": func(f *startFlags) {
			f.bootstrapDANEDNS = dead
			f.bootstrapAddr = dead
		},
		"direct": func(f *startFlags) {
			f.bootstrapDirectURL = "https://" + dead + "/v1/ca/bootstrap"
		},
		"several": func(f *startFlags) {
			f.bootstrapSPKIPin = "sha256//AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
			f.bootstrapAddr = dead
			f.bootstrapDirectURL = "https://" + dead + "/v1/ca/bootstrap"
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			f := &startFlags{dataDir: dir}
			set(f)
			if !hasBootstrapFlags(f) {
				t.Fatal("the configured method must select the join path")
			}
			bundle, err := bootstrapTLS(f, testLogger())
			if bundle != nil {
				t.Fatal("a refused join must return no TLS material")
			}
			var want *EnrollmentUnavailableError
			if !errors.As(err, &want) {
				t.Fatalf("error = %v (%T), want *EnrollmentUnavailableError", err, err)
			}
			if _, ok := err.(*BootstrapFetchError); ok {
				t.Fatal("the refusal must come before any bootstrap fetch")
			}
			entries, rerr := os.ReadDir(dir)
			if rerr != nil {
				t.Fatalf("ReadDir: %v", rerr)
			}
			if len(entries) != 0 {
				t.Fatalf("a refused join wrote %d entries into the data directory", len(entries))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// resolveTLS error path tests (Phase 2)
// ---------------------------------------------------------------------------

func TestResolveTLS_Path2_LoadError(t *testing.T) {
	// Path 2 triggers when all three files exist in dataDir but one is
	// invalid. Write a valid ca.pem and node-key.pem but an invalid
	// node-cert.pem so that loadTLSFromFiles fails.
	dir := t.TempDir()

	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}

	// Write valid CA and key.
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), bundle.CACertPEM, 0o600); err != nil {
		t.Fatalf("WriteFile ca.pem: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node-key.pem"), bundle.KeyPEM, 0o600); err != nil {
		t.Fatalf("WriteFile node-key.pem: %v", err)
	}

	// Write invalid node-cert.pem (not valid PEM, causes X509KeyPair to fail).
	if err := os.WriteFile(filepath.Join(dir, "node-cert.pem"), []byte("not-a-valid-certificate"), 0o600); err != nil {
		t.Fatalf("WriteFile node-cert.pem: %v", err)
	}

	f := &startFlags{
		dataDir:         dir,
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001", "2=127.0.0.2:63001"},
		host:            testBindHost,
	}

	_, _, err = resolveTLS(f, testLogger())
	if err == nil {
		t.Fatal("expected error for invalid node-cert.pem")
	}
	if _, ok := err.(*TLSLoadError); !ok {
		t.Fatalf("expected *TLSLoadError, got %T: %v", err, err)
	}
}

func TestResolveTLS_Path3_PersistError(t *testing.T) {
	// Single-node: the CA is generated but persisting it fails because
	// dataDir is a regular file, not a directory.
	tmpDir := t.TempDir()
	badDataDir := filepath.Join(tmpDir, "blockfile")
	if err := os.WriteFile(badDataDir, []byte("block"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	f := &startFlags{
		dataDir:         badDataDir,
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001"},
		host:            testBindHost,
	}

	_, _, err := resolveTLS(f, testLogger())
	if err == nil {
		t.Fatal("expected error when dataDir is a file")
	}
	if _, ok := err.(*BootstrapWriteError); !ok {
		t.Fatalf("expected *BootstrapWriteError, got %T: %v", err, err)
	}
}

func TestResolveTLS_Path4_JoiningNode(t *testing.T) {
	// Path 4: bootstrap flags select the join, which this release refuses:
	// the cluster CA private key is no longer distributed. It must not fall
	// through to path 5 and generate a CA of its own.
	dir := t.TempDir()
	f := &startFlags{
		dataDir:            dir,
		discoveryMethod:    "static",
		peers:              []string{"1=127.0.0.1:63001", "2=127.0.0.2:63001"},
		bootstrapDirectURL: "https://" + net.JoinHostPort(testBindHost, strconv.Itoa(deadTCPPort(t))) + "/v1/ca/bootstrap",
		host:               testBindHost,
	}
	bundle, adv, err := resolveTLS(f, testLogger())
	if bundle != nil || adv != nil {
		adv.Stop()
		t.Fatal("a refused join must yield no material and no advertiser")
	}
	var want *EnrollmentUnavailableError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *EnrollmentUnavailableError", err, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "ca-key.pem")); statErr == nil {
		t.Fatal("a refused join generated a local CA")
	}
}

func TestResolveTLS_Path5_PersistError(t *testing.T) {
	// Path 5: multi-node, no bootstrap flags, no cached certs.
	// The persist fails because dataDir is a regular file.
	tmpDir := t.TempDir()
	badDataDir := filepath.Join(tmpDir, "blockfile")
	if err := os.WriteFile(badDataDir, []byte("block"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	f := &startFlags{
		dataDir:         badDataDir,
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001", "2=127.0.0.2:63001"},
		host:            testBindHost,
	}

	_, _, err := resolveTLS(f, testLogger())
	if err == nil {
		t.Fatal("expected error when dataDir is a file")
	}
	if _, ok := err.(*BootstrapWriteError); !ok {
		t.Fatalf("expected *BootstrapWriteError, got %T: %v", err, err)
	}
}

func TestResolveTLS_Path5_BootstrapServerStartError(t *testing.T) {
	// Path 5: multi-node, no bootstrap flags, no cached certs.
	// The CA is generated and persisted, but
	// startBootstrapServers fails because noise-key.hex contains
	// invalid data.
	dir := t.TempDir()

	// Write an invalid noise key file so that startBootstrapServers
	// will attempt to load it and fail.
	keyPath := filepath.Join(dir, noiseKeyFile)
	if err := os.WriteFile(keyPath, []byte("not-valid-hex!!!"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	f := &startFlags{
		dataDir:         dir,
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001", "2=127.0.0.2:63001"},
		host:            testBindHost,
	}
	// Fails on the planted key before the listener opens; reserved anyway,
	// for the reason given in TestStartBootstrapServers_InvalidExistingKey.
	reserveNoisePort(t, f)

	if !noiseServed() {
		// The planted key is never read, so path 5 completes: the node comes
		// up advertising the approved methods. A regression that started
		// reading the key file in FIPS mode would fail here.
		_, servers, err := resolveTLS(f, testLogger())
		if err != nil {
			t.Fatalf("resolveTLS must not read the Noise key file when the method is unavailable: %v", err)
		}
		if servers == nil {
			t.Fatal("expected an advertiser for the first-node path")
		}
		defer servers.Stop()
		if servers.NoiseEnabled() {
			t.Fatal("NoiseEnabled() = true, want false when the method is unavailable")
		}
		return
	}

	_, _, err := resolveTLS(f, testLogger())
	if err == nil {
		t.Fatal("expected error for invalid noise key file")
	}
	if _, ok := err.(*BootstrapServerError); !ok {
		t.Fatalf("expected *BootstrapServerError, got %T: %v", err, err)
	}
}

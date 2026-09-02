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
	"context"
	encoding_hex "encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-truststrap/pkg/noiseproto"
	noiseboot "github.com/jeremyhahn/go-truststrap/pkg/noiseproto/bootstrap"

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
		dataDir:            dir,
		discoveryMethod:    "static",
		peers:              []string{"1=127.0.0.1:63001", "2=127.0.0.2:63001"},
		bootstrapNoisePort: 0, // will use default
		// The Noise listener has no default bind host of its own: it
		// follows --host, which cobra always populates in production but
		// a hand-built startFlags does not.
		host: testBindHost,
	}

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
// resolveBootstrapServerURL tests
// ---------------------------------------------------------------------------

func TestResolveBootstrapServerURL_ExplicitAddr(t *testing.T) {
	f := &startFlags{
		bootstrapAddr: "10.0.0.1:9443",
	}
	got, err := resolveBootstrapServerURL(f)
	if err != nil {
		t.Fatalf("resolveBootstrapServerURL error = %v, want nil", err)
	}
	want := "https://10.0.0.1:9443"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveBootstrapServerURL_StaticPeers(t *testing.T) {
	f := &startFlags{
		discoveryMethod: "static",
		peers:           []string{"1=192.168.1.10:63001", "2=192.168.1.11:63001"},
		adminPort:       8443,
	}
	got, err := resolveBootstrapServerURL(f)
	if err != nil {
		t.Fatalf("resolveBootstrapServerURL error = %v, want nil", err)
	}
	want := "https://192.168.1.10:8443"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestResolveBootstrapServerURL_StaticPeers_MissingAdminPort: with a peer to
// derive the host from but no admin port, there is no URL. The old code
// completed it with 8443 and the enroll client dialed a port this system
// never listens on (go-qrdb#167 item 4).
func TestResolveBootstrapServerURL_StaticPeers_MissingAdminPort(t *testing.T) {
	f := &startFlags{
		discoveryMethod: "static",
		peers:           []string{"1=192.168.1.10:63001"},
		adminPort:       0,
	}
	got, err := resolveBootstrapServerURL(f)
	if got != "" {
		t.Errorf("resolveBootstrapServerURL = %q, want empty", got)
	}
	var missing *clusterenroll.MissingAdminPortError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v (%T), want *clusterenroll.MissingAdminPortError", err, err)
	}
}

func TestResolveBootstrapServerURL_DNSDomain(t *testing.T) {
	f := &startFlags{
		discoveryMethod: "dns",
		dnsDomain:       "cluster.example.com",
		adminPort:       9443,
	}
	got, err := resolveBootstrapServerURL(f)
	if err != nil {
		t.Fatalf("resolveBootstrapServerURL error = %v, want nil", err)
	}
	want := "https://cluster.example.com:9443"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveBootstrapServerURL_DNSDomain_MissingAdminPort(t *testing.T) {
	f := &startFlags{
		discoveryMethod: "dns",
		dnsDomain:       "cluster.example.com",
		adminPort:       0,
	}
	got, err := resolveBootstrapServerURL(f)
	if got != "" {
		t.Errorf("resolveBootstrapServerURL = %q, want empty", got)
	}
	var missing *clusterenroll.MissingAdminPortError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v (%T), want *clusterenroll.MissingAdminPortError", err, err)
	}
}

func TestResolveBootstrapServerURL_Empty(t *testing.T) {
	f := &startFlags{
		discoveryMethod: "multicast",
	}
	got, err := resolveBootstrapServerURL(f)
	if err != nil {
		t.Fatalf("resolveBootstrapServerURL error = %v, want nil", err)
	}
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestResolveBootstrapServerURL_ExplicitAddrTakesPriority(t *testing.T) {
	f := &startFlags{
		bootstrapAddr:   "priority.example.com:9443",
		discoveryMethod: "static",
		peers:           []string{"1=peer.example.com:63001"},
		dnsDomain:       "dns.example.com",
		adminPort:       8443,
	}
	got, err := resolveBootstrapServerURL(f)
	if err != nil {
		t.Fatalf("resolveBootstrapServerURL error = %v, want nil", err)
	}
	want := "https://priority.example.com:9443"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// extractHostFromPeer tests
// ---------------------------------------------------------------------------

func TestExtractHostFromPeer_Valid(t *testing.T) {
	tests := []struct {
		name string
		peer string
		want string
	}{
		{name: "ip_with_port", peer: "1=192.168.1.10:63001", want: "192.168.1.10"},
		{name: "hostname_with_port", peer: "2=node2.cluster.local:63001", want: "node2.cluster.local"},
		{name: "ipv6_with_port", peer: "3=[::1]:63001", want: "::1"},
		{name: "no_port", peer: "4=192.168.1.10", want: "192.168.1.10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractHostFromPeer(tt.peer)
			if got != tt.want {
				t.Errorf("extractHostFromPeer(%q) = %q, want %q", tt.peer, got, tt.want)
			}
		})
	}
}

func TestExtractHostFromPeer_Invalid(t *testing.T) {
	got := extractHostFromPeer("invalid-no-equals")
	if got != "" {
		t.Errorf("expected empty string for invalid peer, got %q", got)
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

func TestStartBootstrapServers_GeneratesKey(t *testing.T) {
	dir := t.TempDir()

	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}

	f := &startFlags{
		dataDir:            dir,
		bootstrapNoisePort: 0, // OS-assigned port
		host:               testBindHost,
	}

	// Use port 0 for OS-assigned port to avoid conflicts.
	servers, err := startBootstrapServers(bundle, f, testLogger())
	if err != nil {
		t.Fatalf("startBootstrapServers: %v", err)
	}
	defer servers.Stop()

	// Verify key was persisted.
	keyPath := filepath.Join(dir, noiseKeyFile)
	if !fileExists(keyPath) {
		t.Fatal("expected noise-key.hex to be persisted")
	}

	// Verify key file contains valid hex.
	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty key file")
	}

	// Verify server is listening.
	if servers.Addr() == "" {
		t.Fatal("expected Noise server to have listener address")
	}
}

func TestStartBootstrapServers_LoadsExistingKey(t *testing.T) {
	dir := t.TempDir()

	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}

	// Pre-generate and persist a key.
	f := &startFlags{
		dataDir:            dir,
		bootstrapNoisePort: 0,
		host:               testBindHost,
	}

	servers1, err := startBootstrapServers(bundle, f, testLogger())
	if err != nil {
		t.Fatalf("startBootstrapServers (first): %v", err)
	}
	originalPubKey := append([]byte{}, servers1.NoisePublicKey()...)
	servers1.Stop()

	// Start again - should load existing key.
	servers2, err := startBootstrapServers(bundle, f, testLogger())
	if err != nil {
		t.Fatalf("startBootstrapServers (second): %v", err)
	}
	defer servers2.Stop()

	// Verify the same key was loaded.
	if string(servers2.NoisePublicKey()) != string(originalPubKey) {
		t.Error("expected same public key after reload")
	}
}

func TestStartBootstrapServers_InvalidExistingKey(t *testing.T) {
	dir := t.TempDir()

	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}

	// Write an invalid key file.
	keyPath := filepath.Join(dir, noiseKeyFile)
	if err := os.WriteFile(keyPath, []byte("not-valid-hex!!!"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	f := &startFlags{
		dataDir:            dir,
		bootstrapNoisePort: 0,
		host:               testBindHost,
	}

	_, err = startBootstrapServers(bundle, f, testLogger())
	if err == nil {
		t.Fatal("expected error for invalid key file")
	}
	if _, ok := err.(*BootstrapServerError); !ok {
		t.Fatalf("expected *BootstrapServerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// logBootstrapInfo tests
// ---------------------------------------------------------------------------

func TestLogBootstrapInfo_NilServers(t *testing.T) {
	// Verify no panic with nil servers.
	logBootstrapInfo(nil, nil, &startFlags{})
}

func TestLogBootstrapInfo_NilKey(t *testing.T) {
	// Verify no panic with nil key.
	logBootstrapInfo(nil, &bootstrapServers{}, &startFlags{})
}

// ---------------------------------------------------------------------------
// startTestBootstrapServer helper
// ---------------------------------------------------------------------------

// testBundleProvider is a test-only noiseboot.BundleProvider that serves the
// cluster CA bundle. After the refactor, clusterBundleProvider lives in
// pkg/clusterenroll (unexported), so package main tests use this local copy.
type testBundleProvider struct{ cert, key []byte }

func (p *testBundleProvider) CABundle() ([]byte, error) {
	bundle := make([]byte, 0, len(p.cert)+len(p.key))
	bundle = append(bundle, p.cert...)
	bundle = append(bundle, p.key...)
	return bundle, nil
}

// startTestBootstrapServer creates and starts a real Noise NK bootstrap server
// backed by the given CA certificate and key PEM. It returns the hex-encoded
// public key and the listen address (host:port). The server is stopped
// automatically via t.Cleanup.
func startTestBootstrapServer(t *testing.T, caCertPEM, caKeyPEM []byte) (pubKeyHex string, listenAddr string) {
	t.Helper()

	provider := &testBundleProvider{cert: caCertPEM, key: caKeyPEM}

	key, err := noiseproto.GenerateStaticKey()
	if err != nil {
		t.Fatalf("GenerateStaticKey: %v", err)
	}

	srv, err := noiseboot.NewServer(&noiseboot.ServerConfig{
		ListenAddr: "127.0.0.1:0",
		StaticKey:  &key.DHKey,
		CABundler:  provider,
		Logger:     testLogger(),
	})
	if err != nil {
		key.Destroy()
		t.Fatalf("noiseboot.NewServer: %v", err)
	}

	if err := srv.Start(); err != nil {
		key.Destroy()
		t.Fatalf("noiseboot.Server.Start: %v", err)
	}

	// Release the key's protected pages only after the server has stopped —
	// it holds a noise.DHKey aliasing them for the life of the listener.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
		key.Destroy()
	})

	return encoding_hex.EncodeToString(key.Public), srv.Addr().String()
}

// ---------------------------------------------------------------------------
// bootstrapTLS tests (Phase 1)
// ---------------------------------------------------------------------------

func TestBootstrapTLS_Noise_Success(t *testing.T) {
	// Start a real Noise NK bootstrap server and verify that bootstrapTLS
	// successfully connects, completes the Noise handshake, and fetches the
	// CA bundle. The truststrap bootstrap protocol intentionally strips
	// non-CERTIFICATE PEM blocks from the response, so the fetch succeeds
	// but generateLeafFromCA fails because the CA private key is not
	// included in the response. This test verifies the fetch + handshake
	// path is exercised and that the resulting error is correctly typed.
	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}

	pubKeyHex, listenAddr := startTestBootstrapServer(t, bundle.CACertPEM, bundle.CAKeyPEM)

	f := &startFlags{
		dataDir:            t.TempDir(),
		bootstrapNoiseKey:  pubKeyHex,
		bootstrapNoiseAddr: listenAddr,
	}

	// The Noise handshake and fetch succeed, but generateLeafFromCA
	// fails because the bootstrap protocol strips non-CERTIFICATE PEM
	// blocks (the CA private key). This produces a BootstrapError
	// wrapping the BootstrapKeyError from generateLeafFromCA.
	_, err = bootstrapTLS(f, testLogger())
	if err == nil {
		t.Fatal("expected error because bootstrap protocol strips CA private key")
	}
	if _, ok := err.(*BootstrapError); !ok {
		t.Fatalf("expected *BootstrapError, got %T: %v", err, err)
	}
}

func TestBootstrapTLS_Noise_FetchError(t *testing.T) {
	f := &startFlags{
		dataDir:            t.TempDir(),
		bootstrapNoiseKey:  "0000000000000000000000000000000000000000000000000000000000000000",
		bootstrapNoiseAddr: "127.0.0.1:1",
	}

	_, err := bootstrapTLS(f, testLogger())
	if err == nil {
		t.Fatal("expected error for unreachable noise server")
	}
	if _, ok := err.(*BootstrapFetchError); !ok {
		t.Fatalf("expected *BootstrapFetchError, got %T: %v", err, err)
	}
}

func TestBootstrapTLS_Noise_PersistError(t *testing.T) {
	// Start a real Noise server and set dataDir to a regular file so
	// persistFullBundle would fail. However, since the bootstrap protocol
	// strips non-CERTIFICATE PEM blocks, generateLeafFromCA fails first
	// (before persist is reached). This test verifies the error is a
	// BootstrapError, confirming the fetch succeeded but downstream
	// processing failed. The bad dataDir ensures that even if
	// generateLeafFromCA were to succeed, persist would still fail.
	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}

	pubKeyHex, listenAddr := startTestBootstrapServer(t, bundle.CACertPEM, bundle.CAKeyPEM)

	// Use a regular file as dataDir so MkdirAll would fail if reached.
	tmpDir := t.TempDir()
	badDataDir := filepath.Join(tmpDir, "blockfile")
	if err := os.WriteFile(badDataDir, []byte("block"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	f := &startFlags{
		dataDir:            badDataDir,
		bootstrapNoiseKey:  pubKeyHex,
		bootstrapNoiseAddr: listenAddr,
	}

	_, err = bootstrapTLS(f, testLogger())
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*BootstrapError); !ok {
		t.Fatalf("expected *BootstrapError, got %T: %v", err, err)
	}
}

func TestBootstrapTLS_SPKI_ConfigBuilt(t *testing.T) {
	f := &startFlags{
		dataDir:          t.TempDir(),
		bootstrapSPKIPin: "sha256//AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		bootstrapAddr:    "127.0.0.1:1",
	}

	_, err := bootstrapTLS(f, testLogger())
	if err == nil {
		t.Fatal("expected error for unreachable SPKI server")
	}
	if _, ok := err.(*BootstrapFetchError); !ok {
		t.Fatalf("expected *BootstrapFetchError, got %T: %v", err, err)
	}
}

func TestBootstrapTLS_DANE_ConfigBuilt(t *testing.T) {
	f := &startFlags{
		dataDir:          t.TempDir(),
		bootstrapDANEDNS: "127.0.0.1:1",
		bootstrapAddr:    "127.0.0.1:1",
	}

	_, err := bootstrapTLS(f, testLogger())
	if err == nil {
		t.Fatal("expected error for unreachable DANE server")
	}
	if _, ok := err.(*BootstrapFetchError); !ok {
		t.Fatalf("expected *BootstrapFetchError, got %T: %v", err, err)
	}
}

func TestBootstrapTLS_DANE_EmptyServerURL(t *testing.T) {
	// DANE DNS is set but no address can be resolved (no bootstrapAddr,
	// no peers, no domain). The DANE config is skipped because
	// resolveBootstrapServerURL returns "". With no other methods
	// configured, AutoFetch should fail with no methods configured error.
	f := &startFlags{
		dataDir:          t.TempDir(),
		bootstrapDANEDNS: "8.8.8.8:53",
		// No bootstrapAddr, no peers, no dnsDomain.
		discoveryMethod: "multicast",
	}

	_, err := bootstrapTLS(f, testLogger())
	if err == nil {
		t.Fatal("expected error when DANE has no server URL and no other methods")
	}
	if _, ok := err.(*BootstrapFetchError); !ok {
		t.Fatalf("expected *BootstrapFetchError, got %T: %v", err, err)
	}
}

func TestBootstrapTLS_Direct_ConfigBuilt(t *testing.T) {
	f := &startFlags{
		dataDir:            t.TempDir(),
		bootstrapDirectURL: "https://127.0.0.1:1/ca-bundle",
	}

	_, err := bootstrapTLS(f, testLogger())
	if err == nil {
		t.Fatal("expected error for unreachable direct URL")
	}
	if _, ok := err.(*BootstrapFetchError); !ok {
		t.Fatalf("expected *BootstrapFetchError, got %T: %v", err, err)
	}
}

func TestBootstrapTLS_MultipleMethods_Fallback(t *testing.T) {
	// Configure both SPKI (unreachable) and Noise (real server). The
	// AutoBootstrapper tries methods in configured order. Noise succeeds
	// at the protocol level (handshake + fetch), demonstrating fallback
	// from the failed SPKI method. As with TestBootstrapTLS_Noise_Success,
	// generateLeafFromCA subsequently fails because the bootstrap protocol
	// strips non-CERTIFICATE PEM blocks. A BootstrapError confirms that
	// the Noise fetch succeeded (BootstrapFetchError would indicate the
	// fetch itself failed).
	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}

	pubKeyHex, listenAddr := startTestBootstrapServer(t, bundle.CACertPEM, bundle.CAKeyPEM)

	f := &startFlags{
		dataDir: t.TempDir(),
		// SPKI points to unreachable server (will fail).
		bootstrapSPKIPin: "sha256//AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		bootstrapAddr:    "127.0.0.1:1",
		// Noise points to real server (handshake + fetch succeed).
		bootstrapNoiseKey:  pubKeyHex,
		bootstrapNoiseAddr: listenAddr,
	}

	_, err = bootstrapTLS(f, testLogger())
	if err == nil {
		t.Fatal("expected error because bootstrap protocol strips CA private key")
	}
	// BootstrapError (not BootstrapFetchError) confirms the Noise fetch
	// succeeded and the failure occurred in generateLeafFromCA.
	if _, ok := err.(*BootstrapError); !ok {
		t.Fatalf("expected *BootstrapError (not BootstrapFetchError), got %T: %v", err, err)
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
	// Single-node: generateCATLS succeeds but persistFullBundle fails
	// because dataDir is a regular file, not a directory.
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
	// Path 4: joining node with bootstrap flags. The resolveTLS function
	// delegates to bootstrapTLS which connects to the Noise server. The
	// handshake and fetch succeed, but generateLeafFromCA fails because
	// the bootstrap protocol strips non-CERTIFICATE PEM blocks. This
	// test verifies that Path 4 is exercised (bootstrap flags detected)
	// and that the error propagates correctly from bootstrapTLS.
	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}

	pubKeyHex, listenAddr := startTestBootstrapServer(t, bundle.CACertPEM, bundle.CAKeyPEM)

	f := &startFlags{
		dataDir:            t.TempDir(),
		discoveryMethod:    "static",
		peers:              []string{"1=127.0.0.1:63001", "2=127.0.0.2:63001"},
		bootstrapNoiseKey:  pubKeyHex,
		bootstrapNoiseAddr: listenAddr,
		host:               testBindHost,
	}

	_, _, err = resolveTLS(f, testLogger())
	if err == nil {
		t.Fatal("expected error because bootstrap protocol strips CA private key")
	}
	// The error should be a BootstrapError from generateLeafFromCA,
	// not a BootstrapFetchError, confirming the Noise fetch succeeded.
	if _, ok := err.(*BootstrapError); !ok {
		t.Fatalf("expected *BootstrapError, got %T: %v", err, err)
	}
}

func TestResolveTLS_Path5_PersistError(t *testing.T) {
	// Path 5: multi-node, no bootstrap flags, no cached certs.
	// persistFullBundle fails because dataDir is a regular file.
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
	// generateCATLS succeeds, persistFullBundle succeeds, but
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
		dataDir:            dir,
		discoveryMethod:    "static",
		peers:              []string{"1=127.0.0.1:63001", "2=127.0.0.2:63001"},
		bootstrapNoisePort: 0,
		host:               testBindHost,
	}

	_, _, err := resolveTLS(f, testLogger())
	if err == nil {
		t.Fatal("expected error for invalid noise key file")
	}
	if _, ok := err.(*BootstrapServerError); !ok {
		t.Fatalf("expected *BootstrapServerError, got %T: %v", err, err)
	}
}

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

// White-box tests for pkg/clusterenroll. The package declaration gives access
// to unexported types (clusterBundleProvider, Advertiser fields) for thorough
// coverage of the internal implementation.
package clusterenroll

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// discardLogger returns an slog.Logger that silently discards all output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------------------------------------------------------------------------
// clusterBundleProvider tests
// ---------------------------------------------------------------------------

func TestClusterBundleProvider_CABundle(t *testing.T) {
	certPEM := []byte("-----BEGIN CERTIFICATE-----\ntest-cert\n-----END CERTIFICATE-----\n")
	keyPEM := []byte("-----BEGIN EC PRIVATE KEY-----\ntest-key\n-----END EC PRIVATE KEY-----\n")

	provider := &clusterBundleProvider{
		caCertPEM: certPEM,
		caKeyPEM:  keyPEM,
	}

	got, err := provider.CABundle()
	if err != nil {
		t.Fatalf("CABundle: %v", err)
	}

	want := append(append([]byte{}, certPEM...), keyPEM...)
	if string(got) != string(want) {
		t.Errorf("CABundle returned unexpected data\ngot:  %q\nwant: %q", got, want)
	}
}

func TestClusterBundleProvider_CABundle_EmptyCert(t *testing.T) {
	provider := &clusterBundleProvider{
		caCertPEM: nil,
		caKeyPEM:  []byte("key-data"),
	}

	got, err := provider.CABundle()
	if err != nil {
		t.Fatalf("CABundle: %v", err)
	}
	if string(got) != "key-data" {
		t.Errorf("expected just key data, got %q", got)
	}
}

func TestClusterBundleProvider_CABundle_EmptyKey(t *testing.T) {
	provider := &clusterBundleProvider{
		caCertPEM: []byte("cert-data"),
		caKeyPEM:  nil,
	}

	got, err := provider.CABundle()
	if err != nil {
		t.Fatalf("CABundle: %v", err)
	}
	if string(got) != "cert-data" {
		t.Errorf("expected just cert data, got %q", got)
	}
}

func TestClusterBundleProvider_CABundle_DoesNotMutateInputs(t *testing.T) {
	certPEM := []byte("cert")
	keyPEM := []byte("key")
	origCert := append([]byte{}, certPEM...)
	origKey := append([]byte{}, keyPEM...)

	provider := &clusterBundleProvider{caCertPEM: certPEM, caKeyPEM: keyPEM}
	if _, err := provider.CABundle(); err != nil {
		t.Fatalf("CABundle: %v", err)
	}

	if string(certPEM) != string(origCert) {
		t.Error("caCertPEM was mutated")
	}
	if string(keyPEM) != string(origKey) {
		t.Error("caKeyPEM was mutated")
	}
}

// ---------------------------------------------------------------------------
// Resolve path 1: explicit files
// ---------------------------------------------------------------------------

func TestResolve_Path1_ExplicitFiles(t *testing.T) {
	dir := t.TempDir()
	mat := writeTestFixtures(t, dir)

	opts := &Options{
		DataDir:     t.TempDir(), // empty data-dir so path 2 is skipped
		TLSCAFile:   filepath.Join(dir, "ca.pem"),
		TLSCertFile: filepath.Join(dir, "node-cert.pem"),
		TLSKeyFile:  filepath.Join(dir, "node-key.pem"),
	}

	got, adv, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if adv != nil {
		t.Fatal("expected nil Advertiser for path 1")
	}
	if got == nil {
		t.Fatal("expected non-nil Material")
	}
	if string(got.CACertPEM) != string(mat.CACertPEM) {
		t.Error("CACertPEM does not match fixture")
	}
}

func TestResolve_Path1_MissingFile(t *testing.T) {
	opts := &Options{
		DataDir:     t.TempDir(),
		TLSCAFile:   "/nonexistent/ca.pem",
		TLSCertFile: "/nonexistent/cert.pem",
		TLSKeyFile:  "/nonexistent/key.pem",
	}

	_, _, err := Resolve(context.Background(), opts, discardLogger())
	if err == nil {
		t.Fatal("expected error for missing files")
	}
	if _, ok := err.(*clustertls.LoadError); !ok {
		t.Fatalf("expected *clustertls.LoadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Resolve path 2: cached certificates
// ---------------------------------------------------------------------------

func TestResolve_Path2_CachedCerts(t *testing.T) {
	dir := t.TempDir()
	fixture := writeTestFixtures(t, dir)

	opts := &Options{DataDir: dir}

	got, adv, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if adv != nil {
		t.Fatal("expected nil Advertiser for path 2")
	}
	if got == nil {
		t.Fatal("expected non-nil Material")
	}
	if string(got.CACertPEM) != string(fixture.CACertPEM) {
		t.Error("CACertPEM does not match fixture")
	}
}

// ---------------------------------------------------------------------------
// Resolve path 3: single node
// ---------------------------------------------------------------------------

func TestResolve_Path3_SingleNode_GeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()

	opts := &Options{
		RaftAddress: "127.0.0.1:63001",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  true,
	}

	got, adv, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if adv != nil {
		t.Fatal("expected nil Advertiser for path 3")
	}
	if got == nil {
		t.Fatal("expected non-nil Material")
	}
	if len(got.CACertPEM) == 0 {
		t.Fatal("expected non-empty CACertPEM")
	}
	if len(got.CAKeyPEM) == 0 {
		t.Fatal("expected non-empty CAKeyPEM")
	}

	// Verify files were persisted.
	for _, name := range []string{"ca.pem", "node-cert.pem", "node-key.pem", "ca-key.pem"} {
		if !fileExists(filepath.Join(dir, name)) {
			t.Errorf("expected %s to be persisted in dataDir", name)
		}
	}
}

func TestResolve_Path3_SingleNode_SPIFFESANPresent(t *testing.T) {
	dir := t.TempDir()

	const trustDomain = "spiffe.test"
	const raftAddr = "10.0.0.1:63001"

	opts := &Options{
		RaftAddress: raftAddr,
		TrustDomain: trustDomain,
		DataDir:     dir,
		SingleNode:  true,
	}

	got, _, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	block, _ := pem.Decode(got.CertPEM)
	if block == nil {
		t.Fatal("no PEM block in CertPEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	wantURI := "spiffe://" + trustDomain + "/node/" + raftAddr
	found := false
	for _, u := range cert.URIs {
		if u.String() == wantURI {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("SPIFFE URI SAN %q not found in certificate; URIs: %v", wantURI, cert.URIs)
	}
}

// ---------------------------------------------------------------------------
// Resolve path 5: first node — advertiser lifecycle
// ---------------------------------------------------------------------------

func TestResolve_Path5_FirstNode_ReturnsAdvertiser(t *testing.T) {
	dir := t.TempDir()

	opts := &Options{
		RaftAddress: "127.0.0.1:63001",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  false,
		Advertise:   AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost}, // 0 would map to DefaultNoisePort and flake when occupied
	}

	got, adv, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil Material")
	}
	if adv == nil {
		t.Fatal("expected non-nil Advertiser for path 5")
	}
	defer adv.Stop()

	if !assertAdvertises(t, adv) {
		// FIPS 140-3: the advertiser is up and serves the approved methods.
		// No Noise identity exists, so none is persisted either.
		if fileExists(filepath.Join(dir, NoiseKeyFile)) {
			t.Errorf("%s must not be persisted when the Noise method is unavailable", NoiseKeyFile)
		}
		return
	}

	if adv.Addr() == "" {
		t.Fatal("expected Advertiser to have a listen address")
	}
	if len(adv.NoisePublicKey()) == 0 {
		t.Fatal("expected Advertiser to have a Noise public key")
	}

	// Verify noise key was persisted.
	if !fileExists(filepath.Join(dir, NoiseKeyFile)) {
		t.Errorf("expected %s to be persisted in dataDir", NoiseKeyFile)
	}
}

func TestResolve_Path5_FirstNode_ReloadsExistingKey(t *testing.T) {
	dir := t.TempDir()

	opts := &Options{
		RaftAddress: "127.0.0.1:63001",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  false,
		Advertise:   AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost},
	}

	// First call: generates key and starts server.
	_, adv1, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("Resolve (first): %v", err)
	}
	if !assertAdvertises(t, adv1) {
		// FIPS 140-3: there is no static key to reload, and the reload branch
		// this test covers is not reached. That the second boot still comes
		// back advertising is asserted by
		// TestResumeAdvertiser_UnderFIPSFirstNodeStillAdvertisesOnRestart.
		adv1.Stop()
		return
	}
	pubKey1 := append([]byte{}, adv1.NoisePublicKey()...)
	adv1.Stop()

	// Remove the persisted PEM files so path 2 is skipped on second call.
	for _, name := range []string{"ca.pem", "node-cert.pem", "node-key.pem", "ca-key.pem"} {
		_ = os.Remove(filepath.Join(dir, name))
	}

	// Second call: should reload the existing Noise key file.
	_, adv2, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("Resolve (second): %v", err)
	}
	defer adv2.Stop()

	if string(adv2.NoisePublicKey()) != string(pubKey1) {
		t.Error("expected same Noise public key on reload")
	}
}

// ---------------------------------------------------------------------------
// Advertiser nil/zero safety
// ---------------------------------------------------------------------------

func TestAdvertiser_Stop_NilReceiver(t *testing.T) {
	var adv *Advertiser
	adv.Stop() // must not panic
}

func TestAdvertiser_Stop_ZeroValue(t *testing.T) {
	adv := &Advertiser{} // noiseSrv is nil
	adv.Stop()           // must not panic
}

func TestAdvertiser_NoisePublicKey_NilReceiver(t *testing.T) {
	var adv *Advertiser
	if adv.NoisePublicKey() != nil {
		t.Error("expected nil from nil receiver")
	}
}

func TestAdvertiser_Addr_NilReceiver(t *testing.T) {
	var adv *Advertiser
	if adv.Addr() != "" {
		t.Error("expected empty string from nil receiver")
	}
}

// ---------------------------------------------------------------------------
// ResolveServerURL
// ---------------------------------------------------------------------------

func TestResolveServerURL_ExplicitAddr(t *testing.T) {
	got, err := ResolveServerURL(BootstrapOptions{ServerAddr: "10.0.0.1:9443"})
	if err != nil {
		t.Fatalf("ResolveServerURL error = %v, want nil", err)
	}
	if got != "https://10.0.0.1:9443" {
		t.Errorf("got %q, want https://10.0.0.1:9443", got)
	}
}

func TestResolveServerURL_StaticPeers(t *testing.T) {
	opts := BootstrapOptions{
		DiscoveryMethod: "static",
		Peers:           []string{"1=192.168.1.10:63001"},
		AdminPort:       8443,
	}
	got, err := ResolveServerURL(opts)
	if err != nil {
		t.Fatalf("ResolveServerURL error = %v, want nil", err)
	}
	if got != "https://192.168.1.10:8443" {
		t.Errorf("got %q, want https://192.168.1.10:8443", got)
	}
}

func TestResolveServerURL_DNSDomain(t *testing.T) {
	opts := BootstrapOptions{
		DNSDomain: "cluster.example.com",
		AdminPort: 9443,
	}
	got, err := ResolveServerURL(opts)
	if err != nil {
		t.Fatalf("ResolveServerURL error = %v, want nil", err)
	}
	if got != "https://cluster.example.com:9443" {
		t.Errorf("got %q, want https://cluster.example.com:9443", got)
	}
}

// TestResolveServerURL_MissingAdminPort_Peers pins the fail-closed contract
// for go-qrdb#167 item 4: an address source is configured but the admin port
// is not, so there is nothing to complete the URL with. The old code
// substituted 8443 -- a literal absent from the defaults layer and in conflict
// with DefaultConfig().Server.AdminPort -- and the enroll client dialed it.
func TestResolveServerURL_MissingAdminPort_Peers(t *testing.T) {
	opts := BootstrapOptions{
		DiscoveryMethod: "static",
		Peers:           []string{"1=10.0.0.1:63001"},
		AdminPort:       0,
	}
	got, err := ResolveServerURL(opts)
	if got != "" {
		t.Errorf("ResolveServerURL = %q, want empty", got)
	}
	var missing *MissingAdminPortError
	if !errors.As(err, &missing) {
		t.Fatalf("ResolveServerURL error = %v (%T), want *MissingAdminPortError", err, err)
	}
	if !strings.Contains(missing.Error(), "1=10.0.0.1:63001") {
		t.Errorf("error %q does not name the address source", missing.Error())
	}
}

func TestResolveServerURL_MissingAdminPort_DNSDomain(t *testing.T) {
	opts := BootstrapOptions{
		DNSDomain: "cluster.example.com",
		AdminPort: 0,
	}
	got, err := ResolveServerURL(opts)
	if got != "" {
		t.Errorf("ResolveServerURL = %q, want empty", got)
	}
	var missing *MissingAdminPortError
	if !errors.As(err, &missing) {
		t.Fatalf("ResolveServerURL error = %v (%T), want *MissingAdminPortError", err, err)
	}
}

// TestResolveServerURL_ExplicitAddrTakesPriority pins the precedence rule:
// an explicit ServerAddr wins over both a static peer and a DNS domain, even
// when all three are configured and an admin port is available to complete
// either of the others. Ported here from cmd/qrdbd's resolveBootstrapServerURL
// wrapper (go-qrdb#243), which was the only place this precedence was covered
// and which had no production caller.
func TestResolveServerURL_ExplicitAddrTakesPriority(t *testing.T) {
	opts := BootstrapOptions{
		ServerAddr:      "priority.example.com:9443",
		DiscoveryMethod: "static",
		Peers:           []string{"1=peer.example.com:63001"},
		DNSDomain:       "dns.example.com",
		AdminPort:       8443,
	}
	got, err := ResolveServerURL(opts)
	if err != nil {
		t.Fatalf("ResolveServerURL error = %v, want nil", err)
	}
	if got != "https://priority.example.com:9443" {
		t.Errorf("got %q, want https://priority.example.com:9443", got)
	}
}

// TestResolveServerURL_Empty covers "no address source at all", which is a
// caller stating it has no bootstrap server rather than a misconfiguration:
// empty URL, nil error.
func TestResolveServerURL_Empty(t *testing.T) {
	got, err := ResolveServerURL(BootstrapOptions{DiscoveryMethod: "multicast"})
	if err != nil {
		t.Fatalf("ResolveServerURL error = %v, want nil", err)
	}
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// ExtractHostFromPeer
// ---------------------------------------------------------------------------

func TestExtractHostFromPeer_Valid(t *testing.T) {
	tests := []struct{ peer, want string }{
		{"1=192.168.1.10:63001", "192.168.1.10"},
		{"2=node2.cluster.local:63001", "node2.cluster.local"},
		{"3=[::1]:63001", "::1"},
		{"4=192.168.1.10", "192.168.1.10"},
	}
	for _, tt := range tests {
		if got := ExtractHostFromPeer(tt.peer); got != tt.want {
			t.Errorf("ExtractHostFromPeer(%q) = %q, want %q", tt.peer, got, tt.want)
		}
	}
}

func TestExtractHostFromPeer_Invalid(t *testing.T) {
	if got := ExtractHostFromPeer("no-equals-sign"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// BootstrapOptions.HasFlags
// ---------------------------------------------------------------------------

func TestBootstrapOptions_HasFlags_ZeroValue(t *testing.T) {
	if (BootstrapOptions{}).HasFlags() {
		t.Error("expected false for zero-value BootstrapOptions")
	}
}

func TestBootstrapOptions_HasFlags_NoiseKey(t *testing.T) {
	if !(BootstrapOptions{NoiseKey: "key"}).HasFlags() {
		t.Error("expected true when NoiseKey is set")
	}
}

func TestBootstrapOptions_HasFlags_NoiseAddr(t *testing.T) {
	if !(BootstrapOptions{NoiseAddr: "addr"}).HasFlags() {
		t.Error("expected true when NoiseAddr is set")
	}
}

func TestBootstrapOptions_HasFlags_SPKIPin(t *testing.T) {
	if !(BootstrapOptions{SPKIPin: "pin"}).HasFlags() {
		t.Error("expected true when SPKIPin is set")
	}
}

func TestBootstrapOptions_HasFlags_DirectURL(t *testing.T) {
	if !(BootstrapOptions{DirectURL: "https://example.com"}).HasFlags() {
		t.Error("expected true when DirectURL is set")
	}
}

func TestBootstrapOptions_HasFlags_DANEDNS(t *testing.T) {
	if !(BootstrapOptions{DANEDNS: "8.8.8.8:53"}).HasFlags() {
		t.Error("expected true when DANEDNS is set")
	}
}

// ---------------------------------------------------------------------------
// StartAdvertiser error paths
// ---------------------------------------------------------------------------

func TestStartAdvertiser_InvalidExistingKey(t *testing.T) {
	dir := t.TempDir()
	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}

	// Write a noise key file with invalid hex content.
	if err := os.WriteFile(filepath.Join(dir, NoiseKeyFile), []byte("not-valid-hex!!!"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	opts := AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost}
	if !noiseAvailable() {
		assertNoiseFaultUnreachable(t, mat, opts, dir)
		return
	}

	_, err = StartAdvertiser(mat, opts, dir, discardLogger())
	if err == nil {
		t.Fatal("expected error for invalid noise key file")
	}
	if _, ok := err.(*ServerError); !ok {
		t.Fatalf("expected *ServerError, got %T: %v", err, err)
	}
}

// TestStartAdvertiser_KeyReadFails covers the branch between the two above:
// the key file passes fileExists and then cannot be read. Reusing an existing
// static key is what keeps a node's Noise identity stable across restarts, so
// a read failure must surface rather than fall through to generating a second
// identity and silently rotating the key joining nodes were told to expect.
//
// Injected through readFileFn because there is no uid-independent filesystem
// shape for it: fileExists rejects a directory, so the ENOTDIR trick used by
// the write test cannot reach this branch, and a mode-0000 file is read
// successfully by root (testing-guidelines.md, Fault Injection).
func TestStartAdvertiser_KeyReadFails(t *testing.T) {
	dir := t.TempDir()
	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}

	keyPath := filepath.Join(dir, NoiseKeyFile)
	if err := os.WriteFile(keyPath, []byte("00"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	opts := AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost}
	if !noiseAvailable() {
		assertNoiseFaultUnreachable(t, mat, opts, dir)
		return
	}

	sentinel := errors.New("read key: injected")
	orig := readFileFn
	t.Cleanup(func() { readFileFn = orig })
	readFileFn = func(name string) ([]byte, error) {
		if name == keyPath {
			return nil, sentinel
		}
		return orig(name)
	}

	_, err = StartAdvertiser(mat, opts, dir, discardLogger())
	if !errors.Is(err, sentinel) {
		t.Fatalf("StartAdvertiser error = %v, want the injected read error", err)
	}
	var serverErr *ServerError
	if !errors.As(err, &serverErr) {
		t.Fatalf("expected *ServerError, got %T: %v", err, err)
	}
	if serverErr.Protocol != "noise" {
		t.Fatalf("ServerError.Protocol = %q, want %q", serverErr.Protocol, "noise")
	}
}

func TestStartAdvertiser_WriteKeyFileFails(t *testing.T) {
	dir := t.TempDir()
	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}

	// Create a directory at the noise-key.hex path so os.WriteFile fails.
	keyFilePath := filepath.Join(dir, NoiseKeyFile)
	if err := os.MkdirAll(keyFilePath, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	opts := AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost}
	if !noiseAvailable() {
		assertNoiseFaultUnreachable(t, mat, opts, dir)
		return
	}

	_, err = StartAdvertiser(mat, opts, dir, discardLogger())
	if err == nil {
		t.Fatal("expected error when noise key path is a directory")
	}
	if _, ok := err.(*ServerError); !ok {
		t.Fatalf("expected *ServerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

func TestFetchError_ErrorAndUnwrap(t *testing.T) {
	inner := textError("connection refused")
	e := &FetchError{Method: "Noise", Err: inner}
	want := "clusterenroll: bootstrap fetch failed via Noise: connection refused"
	if e.Error() != want {
		t.Errorf("got %q, want %q", e.Error(), want)
	}
	if e.Unwrap() != inner {
		t.Error("Unwrap did not return inner error")
	}
}

func TestJoinError_ErrorAndUnwrap(t *testing.T) {
	inner := textError("key parse failed")
	e := &JoinError{Err: inner}
	want := "clusterenroll: TLS bootstrap failed: key parse failed"
	if e.Error() != want {
		t.Errorf("got %q, want %q", e.Error(), want)
	}
	if e.Unwrap() != inner {
		t.Error("Unwrap did not return inner error")
	}
}

func TestServerError_ErrorAndUnwrap(t *testing.T) {
	inner := textError("address in use")
	e := &ServerError{Protocol: "noise", Err: inner}
	want := "clusterenroll: bootstrap server (noise) failed to start: address in use"
	if e.Error() != want {
		t.Errorf("got %q, want %q", e.Error(), want)
	}
	if e.Unwrap() != inner {
		t.Error("Unwrap did not return inner error")
	}
}

// ---------------------------------------------------------------------------
// ResolveCA tests
// ---------------------------------------------------------------------------

// TestResolveCA_SingleNode_GeneratesAndPersistsCA verifies that ResolveCA in
// single-node mode generates a CA, persists ca.pem and ca-key.pem, and returns
// a nil Advertiser.
func TestResolveCA_SingleNode_GeneratesAndPersistsCA(t *testing.T) {
	dir := t.TempDir()

	opts := &Options{
		RaftAddress: "127.0.0.1:63001",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  true,
	}

	caCertPEM, caKeyPEM, adv, err := ResolveCA(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("ResolveCA: %v", err)
	}
	if adv != nil {
		t.Fatal("expected nil Advertiser for single-node path")
	}
	if len(caCertPEM) == 0 {
		t.Error("expected non-empty caCertPEM")
	}
	if len(caKeyPEM) == 0 {
		t.Error("expected non-empty caKeyPEM")
	}

	// Verify CA files were persisted (no node-cert or node-key).
	for _, name := range []string{"ca.pem", "ca-key.pem"} {
		if !fileExists(filepath.Join(dir, name)) {
			t.Errorf("expected %s to be persisted in dataDir", name)
		}
	}
	for _, name := range []string{"node-cert.pem", "node-key.pem"} {
		if fileExists(filepath.Join(dir, name)) {
			t.Errorf("unexpected %s in dataDir for ResolveCA", name)
		}
	}
}

// TestResolveCA_FirstNode_ReturnsAdvertiser verifies that ResolveCA in
// first-node mode (multi-node, no bootstrap flags) generates a CA, persists
// it, starts a Noise Advertiser, and returns a non-nil Advertiser that can be
// stopped cleanly.
func TestResolveCA_FirstNode_ReturnsAdvertiser(t *testing.T) {
	dir := t.TempDir()

	opts := &Options{
		RaftAddress: "127.0.0.1:63001",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  false,
		Advertise:   AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost}, // 0 would map to DefaultNoisePort and flake when occupied
	}

	caCertPEM, caKeyPEM, adv, err := ResolveCA(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("ResolveCA: %v", err)
	}
	if adv == nil {
		t.Fatal("expected non-nil Advertiser for first-node path")
	}

	if assertAdvertises(t, adv) {
		if adv.Addr() == "" {
			t.Error("expected non-empty Advertiser listen address")
		}
		if len(adv.NoisePublicKey()) == 0 {
			t.Error("expected non-empty Advertiser Noise public key")
		}
	}
	if len(caCertPEM) == 0 {
		t.Error("expected non-empty caCertPEM")
	}
	if len(caKeyPEM) == 0 {
		t.Error("expected non-empty caKeyPEM")
	}

	// Stop must not panic or block.
	adv.Stop()
}

// TestResolveCA_CachedPath_ReturnsCachedCA verifies that ResolveCA returns the
// cached CA PEM bytes from dataDir when ca.pem and ca-key.pem already exist,
// without generating a new CA or starting an Advertiser.
func TestResolveCA_CachedPath_ReturnsCachedCA(t *testing.T) {
	dir := t.TempDir()

	// Pre-populate the cache with a known CA.
	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	// The role goes down first, as ResolveCA's own paths write it. Without it
	// this is the pre-role data directory go-qrdb#264 refuses to boot on,
	// not a cache.
	if err := persistRole(dir, RoleSingleNode); err != nil {
		t.Fatalf("persistRole: %v", err)
	}
	if err := clustertls.PersistCA(mat.CACertPEM, mat.CAKeyPEM, dir); err != nil {
		t.Fatalf("PersistCA: %v", err)
	}

	opts := &Options{
		DataDir: dir,
	}

	caCertPEM, caKeyPEM, adv, err := ResolveCA(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("ResolveCA: %v", err)
	}
	if adv != nil {
		t.Fatal("expected nil Advertiser for cached path")
	}
	if string(caCertPEM) != string(mat.CACertPEM) {
		t.Error("caCertPEM does not match cached CA cert")
	}
	if string(caKeyPEM) != string(mat.CAKeyPEM) {
		t.Error("caKeyPEM does not match cached CA key")
	}
}

// TestResolveCA_ExplicitFilesReturnsError verifies that ResolveCA returns an
// *ExplicitFilesError when any TLS*File option is set, because those options
// supply a node cert/key pair rather than a CA signing key.
func TestResolveCA_ExplicitFilesReturnsError(t *testing.T) {
	tests := []struct {
		name string
		opts *Options
	}{
		{
			name: "AllThreeSet",
			opts: &Options{
				DataDir:     t.TempDir(),
				TLSCAFile:   "/some/ca.pem",
				TLSCertFile: "/some/cert.pem",
				TLSKeyFile:  "/some/key.pem",
			},
		},
		{
			name: "OnlyCAFile",
			opts: &Options{
				DataDir:   t.TempDir(),
				TLSCAFile: "/some/ca.pem",
			},
		},
		{
			name: "OnlyCertFile",
			opts: &Options{
				DataDir:     t.TempDir(),
				TLSCertFile: "/some/cert.pem",
			},
		},
		{
			name: "OnlyKeyFile",
			opts: &Options{
				DataDir:    t.TempDir(),
				TLSKeyFile: "/some/key.pem",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, err := ResolveCA(context.Background(), tt.opts, discardLogger())
			if err == nil {
				t.Fatal("expected error for explicit TLS file options, got nil")
			}
			if _, ok := err.(*ExplicitFilesError); !ok {
				t.Errorf("expected *ExplicitFilesError, got %T: %v", err, err)
			}
		})
	}
}

// TestExplicitFilesError_Message verifies that ExplicitFilesError produces a
// helpful error message that identifies the conflicting options.
func TestExplicitFilesError_Message(t *testing.T) {
	e := &ExplicitFilesError{}
	msg := e.Error()
	if msg == "" {
		t.Error("expected non-empty error message")
	}
	// The message must mention ResolveCA and TLS file options so the caller
	// understands which function returned the error and what to fix.
	if !containsAll(msg, "ResolveCA", "TLS") {
		t.Errorf("error message %q should mention ResolveCA and TLS", msg)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// writeTestFixtures generates a CA+node bundle, writes the standard file
// names to dir using PersistMaterial, and returns the generated material.
func writeTestFixtures(t *testing.T, dir string) *clustertls.Material {
	t.Helper()
	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	// The role, before the material, exactly as every path that writes CA
	// material does. A cached data directory with no role file is the state
	// go-qrdb#264 refuses to boot on, so a fixture that omits it is not a
	// cached directory -- it is that defect, and any test using it would be
	// asserting against a node that no longer starts. single-node because
	// these fixtures assert that the cached path starts no advertiser.
	if err := persistRole(dir, RoleSingleNode); err != nil {
		t.Fatalf("persistRole: %v", err)
	}
	if err := clustertls.PersistMaterial(mat, dir); err != nil {
		t.Fatalf("PersistMaterial: %v", err)
	}
	return mat
}

// textError is a minimal error implementation for use in test assertions.
type textError string

func (e textError) Error() string { return string(e) }

// containsAll returns true when s contains every substring in subs.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

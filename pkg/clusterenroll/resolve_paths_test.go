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

// Path-selection tests for Resolve and ResolveCA. Enrollment picks exactly one
// of five paths from the option shape, and picking the wrong one is silent --
// a node that generates its own CA when it should have joined looks healthy
// until it fails mTLS against every peer.

package clusterenroll

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// TestResolveJoinPathNeverFallsThroughToALocalCA covers Resolve's path 4:
// bootstrap flags are set, so it must take the join branch -- refused in this
// release -- rather than fall through to generating its own CA. Falling
// through is the dangerous failure: the node would come up with a CA no peer
// trusts.
func TestResolveJoinPathNeverFallsThroughToALocalCA(t *testing.T) {
	dir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63010",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		Bootstrap:   BootstrapOptions{DirectURL: "https://127.0.0.1:1/bundle"},
	}

	mat, adv, err := Resolve(context.Background(), opts, discardLogger())
	if err == nil {
		t.Fatal("Resolve must refuse a join")
	}
	if mat != nil || adv != nil {
		t.Fatal("a refused join must yield no material and no advertiser")
	}
	var want *EnrollmentUnavailableError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *EnrollmentUnavailableError", err, err)
	}

	// It must NOT have silently generated and persisted its own CA.
	if fileExists(filepath.Join(dir, "ca-key.pem")) {
		t.Error("a failed join generated a local CA; the node would trust nobody")
	}
}

// TestResolveCARejectsExplicitFiles covers the guard at the top of ResolveCA.
// Explicit TLS files supply a node leaf, not a CA signing key, so the request
// is refused rather than partially honoured.
func TestResolveCARejectsExplicitFiles(t *testing.T) {
	tests := []struct {
		name string
		opts *Options
	}{
		{"ca file only", &Options{TLSCAFile: "/nonexistent/ca.pem"}},
		{"cert file only", &Options{TLSCertFile: "/nonexistent/cert.pem"}},
		{"key file only", &Options{TLSKeyFile: "/nonexistent/key.pem"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.DataDir = t.TempDir()
			_, _, _, err := ResolveCA(context.Background(), tc.opts, discardLogger())
			var want *ExplicitFilesError
			if !errors.As(err, &want) {
				t.Fatalf("error = %v (%T), want *ExplicitFilesError", err, err)
			}
		})
	}
}

// TestResolveCAJoinPathNeverFallsThroughToALocalCA is the ResolveCA
// counterpart: its bootstrap branch is refused and generates nothing.
func TestResolveCAJoinPathNeverFallsThroughToALocalCA(t *testing.T) {
	dir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63011",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		Bootstrap:   BootstrapOptions{DirectURL: "https://127.0.0.1:1/bundle"},
	}

	certPEM, keyPEM, adv, err := ResolveCA(context.Background(), opts, discardLogger())
	if err == nil {
		t.Fatal("ResolveCA must refuse a join")
	}
	if certPEM != nil || keyPEM != nil || adv != nil {
		t.Fatal("a refused join must yield nothing")
	}
	var want *EnrollmentUnavailableError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *EnrollmentUnavailableError", err, err)
	}
	if fileExists(filepath.Join(dir, "ca-key.pem")) {
		t.Error("a refused join generated a local CA")
	}
}

// TestResolveCASingleNodeStartsNoAdvertiser covers the single-node branch: a
// CA is generated and persisted, but no bootstrap server is started because
// no peer will ever enroll.
func TestResolveCASingleNodeStartsNoAdvertiser(t *testing.T) {
	dir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63012",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  true,
	}

	certPEM, keyPEM, adv, err := ResolveCA(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("ResolveCA: %v", err)
	}
	if adv != nil {
		adv.Stop()
		t.Fatal("single-node mode must not start an advertiser")
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Fatal("expected CA material")
	}
	for _, name := range []string{"ca.pem", "ca-key.pem"} {
		if !fileExists(filepath.Join(dir, name)) {
			t.Errorf("expected %s to be persisted", name)
		}
	}
}

// TestResolveCAReloadsTheCachedCA covers the cache branch, and pins that the
// second call returns the SAME CA. Regenerating instead would silently
// invalidate every certificate already issued from the first one.
func TestResolveCAReloadsTheCachedCA(t *testing.T) {
	dir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63013",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  true,
	}

	firstCert, firstKey, _, err := ResolveCA(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("first ResolveCA: %v", err)
	}

	secondCert, secondKey, adv, err := ResolveCA(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("second ResolveCA: %v", err)
	}
	if adv != nil {
		adv.Stop()
		t.Fatal("the cached path must not start an advertiser")
	}
	if string(firstCert) != string(secondCert) {
		t.Error("cached ResolveCA returned a different CA certificate")
	}
	if string(firstKey) != string(secondKey) {
		t.Error("cached ResolveCA returned a different CA key")
	}
}

// TestResolveCAPropagatesAPersistFailure covers the error return from
// PersistCA, reached here by making the data directory unwritable.
func TestResolveCAPropagatesAPersistFailure(t *testing.T) {
	// A DataDir whose parent path component is a regular FILE fails every
	// create/write under it with ENOTDIR — for root too. A chmod 0o500
	// directory does not: root ignores permission bits, and CI runners
	// execute as root, where the previous fixture stopped failing.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	dir := filepath.Join(blocker, "denied")

	opts := &Options{
		RaftAddress: "127.0.0.1:63014",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  true,
	}

	if _, _, _, err := ResolveCA(context.Background(), opts, discardLogger()); err == nil {
		t.Fatal("ResolveCA must fail when the CA cannot be persisted")
	}
}

// TestResolveCAFirstNodeStartsAnAdvertiser covers the multi-node branch, which
// both generates the CA and stands up the bootstrap server peers enroll
// against.
func TestResolveCAFirstNodeStartsAnAdvertiser(t *testing.T) {
	dir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63015",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  false,
		Advertise:   AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost},
	}

	certPEM, keyPEM, adv, err := ResolveCA(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("ResolveCA: %v", err)
	}
	if adv == nil {
		t.Fatal("multi-node first node must start an advertiser")
	}
	defer adv.Stop()

	if len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Fatal("expected CA material")
	}
	if !assertAdvertises(t, adv) {
		if fileExists(filepath.Join(dir, NoiseKeyFile)) {
			t.Errorf("%s must not be persisted when the Noise method is unavailable", NoiseKeyFile)
		}
		return
	}
	if len(adv.NoisePublicKey()) == 0 {
		t.Fatal("advertiser must expose a Noise public key")
	}
	if !fileExists(filepath.Join(dir, NoiseKeyFile)) {
		t.Errorf("expected %s to be persisted", NoiseKeyFile)
	}
}

// TestResolvePropagatesAPersistFailure covers Resolve's persist error on the
// single-node path, the counterpart to the ResolveCA case above.
func TestResolvePropagatesAPersistFailure(t *testing.T) {
	// A DataDir whose parent path component is a regular FILE fails every
	// create/write under it with ENOTDIR — for root too. A chmod 0o500
	// directory does not: root ignores permission bits, and CI runners
	// execute as root, where the previous fixture stopped failing.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	dir := filepath.Join(blocker, "denied")

	opts := &Options{
		RaftAddress: "127.0.0.1:63016",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  true,
	}

	if _, _, err := Resolve(context.Background(), opts, discardLogger()); err == nil {
		t.Fatal("Resolve must fail when material cannot be persisted")
	}
}

// TestStartAdvertiserRejectsAnUnusablePort covers the listener error path.
// A node that failed to advertise but returned success would leave every
// joining peer unable to enroll, with nothing in the logs saying why.
func TestStartAdvertiserRejectsAnUnusablePort(t *testing.T) {
	dir := t.TempDir()
	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	mat := &clustertls.Material{CACertPEM: caCertPEM, CAKeyPEM: caKeyPEM}

	// Occupy a TCP port first so the advertiser's own bind (a wildcard TCP
	// listen on the same port) collides with EADDRINUSE. An address conflict
	// fails for every privilege level — a privileged low port does not: root
	// binds port 1 without complaint, and CI runners execute as root, where
	// the previous fixture stopped failing.
	// #nosec G102 -- the wildcard address is the point: this reserves a port that the code under test, which itself binds the wildcard address, must then collide with. A loopback-only reservation does not reproduce the EADDRINUSE this fault injection needs.
	occupied, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	t.Cleanup(func() { _ = occupied.Close() })
	port := occupied.Addr().(*net.TCPAddr).Port

	opts := AdvertiseOptions{NoisePort: port, NoiseHost: testBindHost}
	if !noiseAvailable() {
		// No listener is opened at all, so there is no address to collide
		// with -- and an advertiser that reported a bind failure here would
		// mean the FIPS branch had stopped short-circuiting the listen.
		assertNoiseFaultUnreachable(t, mat, opts, dir)
		return
	}

	_, err = StartAdvertiser(mat, opts, dir, discardLogger())
	if err == nil {
		t.Fatal("expected StartAdvertiser to fail on an already-bound port")
	}
	var want *ServerError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *ServerError", err, err)
	}
}

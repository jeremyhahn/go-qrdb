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

// Successful-enrollment tests driven over the SPKI-pinned HTTPS path.
//
// The SPKI bootstrapper returns the served body verbatim, so a shared-CA-key
// bundle (certificate + private key) survives the round trip and Join can mint
// its own leaf from it. The Noise path cannot be used for this: truststrap's
// Noise handler rebuilds the response from parsed CERTIFICATE blocks only and
// drops the CA key, so a shared-CA-key enrollment over Noise fails with
// "no EC PRIVATE KEY block found". See docs/clusterenroll-noise-gap.md.

package clusterenroll

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// bundleServer serves a shared-CA-key bundle over TLS at the path the SPKI
// bootstrapper fetches, and returns the bootstrap options that reach it
// (including the SPKI pin of its own serving certificate).
func bundleServer(t *testing.T, bundle []byte) BootstrapOptions {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ca/bootstrap", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(bundle)
	})

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	leaf := srv.Certificate()
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)

	return BootstrapOptions{
		SPKIPin:    hex.EncodeToString(sum[:]),
		ServerAddr: strings.TrimPrefix(srv.URL, "https://"),
	}
}

// sharedCABundle returns a CA certificate + CA private key bundle in the
// concatenated form clusterBundleProvider produces, along with the CA cert for
// chain verification.
func sharedCABundle(t *testing.T) (bundle, caCertPEM []byte) {
	t.Helper()
	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	provider := &clusterBundleProvider{caCertPEM: caCertPEM, caKeyPEM: caKeyPEM}
	bundle, err = provider.CABundle()
	if err != nil {
		t.Fatalf("CABundle: %v", err)
	}
	return bundle, caCertPEM
}

// TestJoinMintsALeafFromTheFetchedCA exercises Join end to end: fetch the
// shared-CA-key bundle, mint a node leaf from it, persist the material.
//
// The assertion that matters is the chain check. A leaf that verifies against
// the cluster CA is the entire point of enrollment -- a self-signed leaf would
// satisfy "a certificate was produced" while leaving the node unable to speak
// mTLS with the cluster it just joined.
func TestJoinMintsALeafFromTheFetchedCA(t *testing.T) {
	bundle, caCertPEM := sharedCABundle(t)
	bootstrap := bundleServer(t, bundle)

	joinDir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63020",
		TrustDomain: "test.cluster",
		DataDir:     joinDir,
		Bootstrap:   bootstrap,
	}

	mat, err := Join(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if mat == nil {
		t.Fatal("Join must return material")
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caCertPEM) {
		t.Fatal("could not load the cluster CA")
	}
	leafBlock, _ := pem.Decode(mat.CertPEM)
	if leafBlock == nil {
		t.Fatal("joined material carries no leaf certificate")
	}
	leaf, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("parse joined leaf: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		t.Fatalf("joined leaf does not chain to the cluster CA: %v", err)
	}

	// The leaf must carry this node's own SPIFFE identity; sharing one would
	// make two nodes authenticate as the same peer.
	wantURI := spiffe.NodeURI(opts.TrustDomain, opts.RaftAddress)
	var found bool
	for _, u := range leaf.URIs {
		if u.String() == wantURI {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("leaf SANs %v do not include %s", leaf.URIs, wantURI)
	}

	for _, name := range []string{"ca.pem", "node-cert.pem", "node-key.pem"} {
		if !fileExists(filepath.Join(joinDir, name)) {
			t.Errorf("expected %s to be persisted", name)
		}
	}
}

// TestJoinRejectsAKeylessBundle covers the failure the Noise transport
// produces today: a bundle carrying only the CA certificate. Join cannot mint
// a leaf without the signing key, and must say so rather than emit an
// unusable identity.
func TestJoinRejectsAKeylessBundle(t *testing.T) {
	_, caCertPEM := sharedCABundle(t)
	bootstrap := bundleServer(t, caCertPEM) // certificate only, no key

	opts := &Options{
		RaftAddress: "127.0.0.1:63021",
		TrustDomain: "test.cluster",
		DataDir:     t.TempDir(),
		Bootstrap:   bootstrap,
	}

	_, err := Join(context.Background(), opts, discardLogger())
	if err == nil {
		t.Fatal("Join must reject a bundle with no CA private key")
	}
	var want *JoinError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *JoinError", err, err)
	}
}

// TestResolveCAJoinPathPersistsTheFetchedCA covers ResolveCA's bootstrap
// branch through to a successful split and persist.
func TestResolveCAJoinPathPersistsTheFetchedCA(t *testing.T) {
	bundle, caCertPEM := sharedCABundle(t)
	bootstrap := bundleServer(t, bundle)

	dir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63022",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		Bootstrap:   bootstrap,
	}

	gotCert, gotKey, adv, err := ResolveCA(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("ResolveCA: %v", err)
	}
	if adv != nil {
		adv.Stop()
		t.Fatal("a joining node must not start an advertiser")
	}
	if string(gotCert) != string(caCertPEM) {
		t.Error("ResolveCA returned a different CA certificate than was served")
	}
	if len(gotKey) == 0 {
		t.Fatal("ResolveCA returned no CA key")
	}
	for _, name := range []string{"ca.pem", "ca-key.pem"} {
		if !fileExists(filepath.Join(dir, name)) {
			t.Errorf("expected %s to be persisted", name)
		}
	}
}

// TestResolveCARejectsAKeylessFetchedBundle covers the split-failure branch,
// which must surface as a JoinError rather than persisting half a CA.
func TestResolveCARejectsAKeylessFetchedBundle(t *testing.T) {
	_, caCertPEM := sharedCABundle(t)
	bootstrap := bundleServer(t, caCertPEM)

	dir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63023",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		Bootstrap:   bootstrap,
	}

	_, _, _, err := ResolveCA(context.Background(), opts, discardLogger())
	if err == nil {
		t.Fatal("ResolveCA must reject a keyless bundle")
	}
	var want *JoinError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *JoinError", err, err)
	}
	if fileExists(filepath.Join(dir, "ca-key.pem")) {
		t.Error("a rejected bundle must not leave a persisted CA key")
	}
}

// TestResolveJoinPathEnrollsThroughBootstrap covers Resolve's path 4 success:
// bootstrap flags present, so the node joins rather than generating its own CA.
func TestResolveJoinPathEnrollsThroughBootstrap(t *testing.T) {
	bundle, caCertPEM := sharedCABundle(t)
	bootstrap := bundleServer(t, bundle)

	dir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63024",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		Bootstrap:   bootstrap,
	}

	mat, adv, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if adv != nil {
		adv.Stop()
		t.Fatal("a joining node must not start an advertiser")
	}
	if mat == nil {
		t.Fatal("expected material")
	}
	if string(mat.CACertPEM) != string(caCertPEM) {
		t.Error("the joined node did not adopt the served cluster CA")
	}
}

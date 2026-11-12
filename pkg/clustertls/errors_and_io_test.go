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

// Error-type and file-IO tests for pkg/clustertls.

package clustertls_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// readonlyDir returns a directory that cannot be written to, for exercising
// persist failures.
func readonlyDir(t *testing.T) string {
	t.Helper()
	// A path component that is a regular FILE makes every create/write under
	// it fail with ENOTDIR — for root too. A chmod 0o500 directory does not:
	// root ignores permission bits, and CI runners execute as root, which is
	// exactly where the previous fixture stopped failing and these tests
	// went red.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	return filepath.Join(blocker, "denied")
}

// TestErrorMessagesNameTheirSubject checks each typed error renders the detail
// an operator needs. These surface during node startup, where the process
// usually exits immediately afterwards -- the message is the only artifact
// left to diagnose from.
func TestErrorMessagesNameTheirSubject(t *testing.T) {
	cause := errors.New("underlying")

	tests := []struct {
		name     string
		err      error
		contains []string
	}{
		{
			name:     "generate",
			err:      &clustertls.GenerateError{Err: cause},
			contains: []string{"clustertls", "generation failed", "underlying"},
		},
		{
			name:     "load names the path",
			err:      &clustertls.LoadError{Path: "/etc/qrdb/ca.pem", Err: cause},
			contains: []string{"failed to load", "/etc/qrdb/ca.pem", "underlying"},
		},
		{
			name:     "key names the kind",
			err:      &clustertls.KeyError{Kind: "CA private key", Err: cause},
			contains: []string{"key parse failed", "CA private key", "underlying"},
		},
		{
			name:     "write names the path",
			err:      &clustertls.WriteError{Path: "/data/node-key.pem", Err: cause},
			contains: []string{"failed to write", "/data/node-key.pem", "underlying"},
		},
		{
			name: "partial material lists what is missing",
			err: &clustertls.PartialMaterialError{
				Dir:     "/data",
				Missing: []string{"/data/ca.pem", "/data/node-key.pem"},
			},
			contains: []string{"incomplete TLS material", "/data", "ca.pem", "node-key.pem"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.err.Error()
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("message %q does not contain %q", got, want)
				}
			}
		})
	}
}

// TestUnwrapReachesTheCause covers the errors.Is plumbing on every wrapping
// error type. Startup code distinguishes "file absent" from "file corrupt" by
// matching on the wrapped cause, so a broken chain changes that decision.
func TestUnwrapReachesTheCause(t *testing.T) {
	cause := errors.New("underlying")

	tests := []struct {
		name string
		err  error
	}{
		{"generate", &clustertls.GenerateError{Err: cause}},
		{"load", &clustertls.LoadError{Path: "p", Err: cause}},
		{"key", &clustertls.KeyError{Kind: "k", Err: cause}},
		{"write", &clustertls.WriteError{Path: "p", Err: cause}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.err, cause) {
				t.Fatalf("errors.Is could not reach the cause through %T", tc.err)
			}
		})
	}
}

// TestLoadFromFilesReadsAllThreeComponents covers the success path against
// files this package itself wrote, so the test cannot drift from the layout
// PersistMaterial produces.
func TestLoadFromFilesReadsAllThreeComponents(t *testing.T) {
	dir := t.TempDir()
	mat, err := clustertls.GenerateCABundle("spiffe://test.cluster/node/127.0.0.1:1")
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	if err := clustertls.PersistMaterial(mat, dir); err != nil {
		t.Fatalf("PersistMaterial: %v", err)
	}

	got, err := clustertls.LoadFromFiles(
		filepath.Join(dir, "ca.pem"),
		filepath.Join(dir, "node-cert.pem"),
		filepath.Join(dir, "node-key.pem"),
	)
	if err != nil {
		t.Fatalf("LoadFromFiles: %v", err)
	}
	if string(got.CACertPEM) != string(mat.CACertPEM) {
		t.Error("CA certificate did not round-trip")
	}
	if string(got.CertPEM) != string(mat.CertPEM) {
		t.Error("node certificate did not round-trip")
	}
	if string(got.KeyPEM) != string(mat.KeyPEM) {
		t.Error("node key did not round-trip")
	}
}

// TestLoadFromFilesNamesTheMissingFile covers each of the three read failures
// separately. The path in the error is the whole diagnostic value: an operator
// who typo'd one of three flags needs to know which.
func TestLoadFromFilesNamesTheMissingFile(t *testing.T) {
	dir := t.TempDir()
	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	if err := clustertls.PersistMaterial(mat, dir); err != nil {
		t.Fatalf("PersistMaterial: %v", err)
	}

	ca := filepath.Join(dir, "ca.pem")
	cert := filepath.Join(dir, "node-cert.pem")
	key := filepath.Join(dir, "node-key.pem")
	absent := filepath.Join(dir, "does-not-exist.pem")

	tests := []struct {
		name              string
		ca, cert, key     string
		wantPathSubstring string
	}{
		{"ca missing", absent, cert, key, "does-not-exist.pem"},
		{"cert missing", ca, absent, key, "does-not-exist.pem"},
		{"key missing", ca, cert, absent, "does-not-exist.pem"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := clustertls.LoadFromFiles(tc.ca, tc.cert, tc.key)
			if err == nil {
				t.Fatal("expected a load failure")
			}
			if got != nil {
				t.Error("a failed load must return no material")
			}
			var want *clustertls.LoadError
			if !errors.As(err, &want) {
				t.Fatalf("error = %v (%T), want *clustertls.LoadError", err, err)
			}
			if !strings.Contains(want.Path, tc.wantPathSubstring) {
				t.Errorf("Path = %q, want it to contain %q", want.Path, tc.wantPathSubstring)
			}
			if !errors.Is(err, os.ErrNotExist) {
				t.Errorf("error should wrap os.ErrNotExist, got %v", err)
			}
		})
	}
}

// TestPersistMaterialReportsAWriteFailure covers the atomic-write error path.
// A persist that failed silently would leave the node running on in-memory
// material that vanishes at restart, turning a permissions problem into a
// mysterious loss of identity one reboot later.
func TestPersistMaterialReportsAWriteFailure(t *testing.T) {
	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}

	err = clustertls.PersistMaterial(mat, readonlyDir(t))
	if err == nil {
		t.Fatal("expected PersistMaterial to fail on an unwritable directory")
	}
	var want *clustertls.WriteError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *clustertls.WriteError", err, err)
	}
}

// TestPersistCAReportsAWriteFailure is the CA-only counterpart.
func TestPersistCAReportsAWriteFailure(t *testing.T) {
	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	err = clustertls.PersistCA(caCertPEM, caKeyPEM, readonlyDir(t))
	if err == nil {
		t.Fatal("expected PersistCA to fail on an unwritable directory")
	}
	var want *clustertls.WriteError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *clustertls.WriteError", err, err)
	}
}

// blockCAKeyWrite makes every write of dataDir/ca-key.pem fail by occupying
// the path with a directory, which an atomic rename cannot replace.
func blockCAKeyWrite(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "ca-key.pem"), 0o700); err != nil {
		t.Fatalf("mkdir ca-key.pem: %v", err)
	}
	return dir
}

// TestPersistMaterialWritesTheCAKeyBeforeTheCertificate pins the write order
// a reader of the directory relies on: ca.pem is the file that announces the
// material, so it must never be visible while a CA key that belongs with it is
// not yet durable. A failed key write therefore leaves no ca.pem, and
// LoadFromDir reports no cached material instead of a CA that cannot sign.
func TestPersistMaterialWritesTheCAKeyBeforeTheCertificate(t *testing.T) {
	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	dir := blockCAKeyWrite(t)

	if err := clustertls.PersistMaterial(mat, dir); err == nil {
		t.Fatal("expected PersistMaterial to fail when ca-key.pem cannot be written")
	}
	if _, err := os.Stat(filepath.Join(dir, "ca.pem")); !os.IsNotExist(err) {
		t.Fatalf("ca.pem stat err = %v, want not-exist: the certificate became visible before its key was durable", err)
	}
	got, err := clustertls.LoadFromDir(dir)
	if err != nil || got != nil {
		t.Fatalf("LoadFromDir = %v, %v; want nil, nil", got, err)
	}
}

// TestIssueLeafForKeyRejectsMalformedCAMaterial covers the parse-failure
// branches. Each input is well-formed PEM of the wrong kind, so the failure is
// the semantic check rather than base64 decoding.
func TestIssueLeafForKeyRejectsMalformedCAMaterial(t *testing.T) {
	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tests := []struct {
		name         string
		certPEM      []byte
		keyPEM       []byte
		wantKindPart string
	}{
		{"empty CA certificate", nil, caKeyPEM, "CA certificate"},
		{"CA certificate slot holds a key", caKeyPEM, caKeyPEM, "CA certificate"},
		{"empty CA key", caCertPEM, nil, "CA private key"},
		{"CA key slot holds a certificate", caCertPEM, caCertPEM, "CA private key"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			certPEM, err := clustertls.IssueLeafForKey(tc.certPEM, tc.keyPEM, &key.PublicKey)
			if err == nil {
				t.Fatal("expected a rejection")
			}
			if certPEM != nil {
				t.Error("a rejected request must issue no certificate")
			}
			var want *clustertls.KeyError
			if !errors.As(err, &want) {
				t.Fatalf("error = %v (%T), want *clustertls.KeyError", err, err)
			}
			if !strings.Contains(want.Kind, tc.wantKindPart) {
				t.Errorf("Kind = %q, want it to contain %q", want.Kind, tc.wantKindPart)
			}
		})
	}
}

// TestLocalSANsAlwaysIncludesLoopback pins the invariant the doc comment
// promises. A certificate missing loopback SANs breaks every in-process and
// same-host client, which is how most of the test suite talks to a node.
func TestLocalSANsAlwaysIncludesLoopback(t *testing.T) {
	dnsNames, ips := clustertls.LocalSANs()

	var hasLocalhost bool
	for _, n := range dnsNames {
		if n == "localhost" {
			hasLocalhost = true
			break
		}
	}
	if !hasLocalhost {
		t.Errorf("DNS SANs %v do not include localhost", dnsNames)
	}

	want := map[string]bool{"127.0.0.1": false, "::1": false}
	for _, ip := range ips {
		if _, ok := want[ip.String()]; ok {
			want[ip.String()] = true
		}
	}
	for addr, found := range want {
		if !found {
			t.Errorf("IP SANs %v do not include %s", ips, addr)
		}
	}
}

// TestLocalSANs_ExcludesUnspecified pins the OTHER half of the doc comment.
//
// Until #304 the inclusion half was tested here and the exclusion half was only
// asserted in prose, so a copy of this function that kept 0.0.0.0 and :: could
// live in sdk/go/testutil/qrdbdharness for months without any test disagreeing
// with it -- and an assertion written against that copy was then pasted into
// cmd/qrdbd, where it reddened every container gate for three weeks (#297).
//
// The addresses are excluded because nothing dials them. They are BIND
// addresses; a TLS client verifies the name it dialed, and pkg/cluster refuses
// to advertise them to peers for the same reason (AdvertisableHost). Listing
// them widens the certificate's stated identity and verifies nothing.
func TestLocalSANs_ExcludesUnspecified(t *testing.T) {
	_, ips := clustertls.LocalSANs()

	for _, ip := range ips {
		if ip.IsUnspecified() {
			t.Errorf("IP SANs %v include the unspecified address %s; "+
				"LocalSANs excludes 0.0.0.0 and :: deliberately", ips, ip)
		}
	}
}

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

// Load-failure tests: unreadable files and well-formed PEM wrapping malformed
// DER. Both shapes reach code that a missing-file test cannot.

package clustertls_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// persistedMaterial writes a full set of cluster TLS files (including
// ca-key.pem) into a fresh directory and returns it.
func persistedMaterial(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mat, err := clustertls.GenerateCABundle("spiffe://test.cluster/node/127.0.0.1:1")
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	if err := clustertls.PersistMaterial(mat, dir); err != nil {
		t.Fatalf("PersistMaterial: %v", err)
	}
	if len(mat.CAKeyPEM) == 0 {
		t.Fatal("expected GenerateCABundle to produce a CA key")
	}
	return dir
}

// makeUnreadable strips all permissions from path so a read fails while the
// file still exists -- the case that separates "absent" from "unreadable".
func makeUnreadable(t *testing.T, path string) {
	t.Helper()
	// Replace the file with a DIRECTORY of the same name: os.ReadFile then
	// fails with EISDIR for every caller — including root, which ignores a
	// chmod 0o000 (CI runners execute as root, where the previous fixture
	// stopped failing). The path still EXISTS, preserving the property under
	// test: unreadable is not absent, and must fail the load closed.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
}

// TestLoadFromDirLoadsTheOptionalCAKey covers the branch that picks up
// ca-key.pem when present. A first node needs the CA key back after restart to
// keep issuing certificates; dropping it silently would break enrollment only
// for the NEXT node to join.
func TestLoadFromDirLoadsTheOptionalCAKey(t *testing.T) {
	dir := persistedMaterial(t)

	mat, err := clustertls.LoadFromDir(dir)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if mat == nil {
		t.Fatal("expected material")
	}
	if len(mat.CAKeyPEM) == 0 {
		t.Fatal("ca-key.pem was present but CAKeyPEM is empty")
	}
}

// TestLoadFromDirReportsUnreadableFiles covers each read-failure branch. An
// unreadable file is NOT the same as an absent one: absent means "no cache,
// generate", while unreadable must fail closed, or a permissions slip would
// silently fork cluster trust by generating a fresh CA.
func TestLoadFromDirReportsUnreadableFiles(t *testing.T) {
	for _, name := range []string{"ca.pem", "node-cert.pem", "node-key.pem", "ca-key.pem"} {
		t.Run(name, func(t *testing.T) {
			dir := persistedMaterial(t)
			path := filepath.Join(dir, name)
			makeUnreadable(t, path)

			mat, err := clustertls.LoadFromDir(dir)
			if err == nil {
				t.Fatal("expected an unreadable file to fail the load")
			}
			if mat != nil {
				t.Error("a failed load must return no material")
			}
			var want *clustertls.LoadError
			if !errors.As(err, &want) {
				t.Fatalf("error = %v (%T), want *clustertls.LoadError", err, err)
			}
			if want.Path != path {
				t.Errorf("Path = %q, want %q", want.Path, path)
			}
		})
	}
}

// TestLoadCAFromDirReportsUnreadableFiles is the CA-only counterpart.
func TestLoadCAFromDirReportsUnreadableFiles(t *testing.T) {
	for _, name := range []string{"ca.pem", "ca-key.pem"} {
		t.Run(name, func(t *testing.T) {
			dir := persistedMaterial(t)
			path := filepath.Join(dir, name)
			makeUnreadable(t, path)

			certPEM, keyPEM, err := clustertls.LoadCAFromDir(dir)
			if err == nil {
				t.Fatal("expected an unreadable file to fail the load")
			}
			if certPEM != nil || keyPEM != nil {
				t.Error("a failed load must return no material")
			}
			var want *clustertls.LoadError
			if !errors.As(err, &want) {
				t.Fatalf("error = %v (%T), want *clustertls.LoadError", err, err)
			}
			if want.Path != path {
				t.Errorf("Path = %q, want %q", want.Path, path)
			}
		})
	}
}

// TestLoadFromDirRejectsPartialMaterial covers the fail-closed guard for an
// interrupted write. Treating a half-written directory as "no cache" is the
// dangerous behaviour: the node would generate a new CA and stop trusting
// every peer it had already enrolled with.
func TestLoadFromDirRejectsPartialMaterial(t *testing.T) {
	for _, removed := range []string{"ca.pem", "node-cert.pem", "node-key.pem"} {
		t.Run("missing "+removed, func(t *testing.T) {
			dir := persistedMaterial(t)
			path := filepath.Join(dir, removed)
			if err := os.Remove(path); err != nil {
				t.Fatalf("remove: %v", err)
			}

			mat, err := clustertls.LoadFromDir(dir)
			if err == nil {
				t.Fatal("expected partial material to be rejected")
			}
			if mat != nil {
				t.Error("partial material must yield no Material")
			}
			var want *clustertls.PartialMaterialError
			if !errors.As(err, &want) {
				t.Fatalf("error = %v (%T), want *clustertls.PartialMaterialError", err, err)
			}
			var listed bool
			for _, m := range want.Missing {
				if m == path {
					listed = true
					break
				}
			}
			if !listed {
				t.Errorf("Missing %v does not name %s", want.Missing, path)
			}
		})
	}
}

// TestLoadFromDirReportsNoCacheWhenEmpty pins the other side of that guard:
// a directory with none of the files present is a clean first boot.
func TestLoadFromDirReportsNoCacheWhenEmpty(t *testing.T) {
	mat, err := clustertls.LoadFromDir(t.TempDir())
	if err != nil {
		t.Fatalf("an empty directory must not be an error: %v", err)
	}
	if mat != nil {
		t.Fatal("an empty directory must yield no material")
	}
}

// TestIssueLeafForKeyRejectsWellFormedPEMWithBadDER covers the DER-parse
// branches, which a wrong-PEM-type test cannot reach: the block types are
// correct here and only the contents are garbage, so the failure happens
// inside x509 rather than at the PEM type check.
func TestIssueLeafForKeyRejectsWellFormedPEMWithBadDER(t *testing.T) {
	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	badCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not der")})
	badKey := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("not der")})

	tests := []struct {
		name    string
		certPEM []byte
		keyPEM  []byte
		kind    string
	}{
		{"unparseable CA certificate", badCert, caKeyPEM, "CA certificate"},
		{"unparseable CA key", caCertPEM, badKey, "CA private key"},
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
			if want.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", want.Kind, tc.kind)
			}
		})
	}
}

// TestGenerateLeafFromCARejectsWellFormedPEMWithBadDER is the bundle-shaped
// counterpart, covering the same two parse failures via the join path.
func TestGenerateLeafFromCARejectsWellFormedPEMWithBadDER(t *testing.T) {
	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	badCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not der")})
	badKey := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("not der")})

	tests := []struct {
		name   string
		bundle []byte
		kind   string
	}{
		{"unparseable certificate", append(append([]byte{}, badCert...), caKeyPEM...), "CA certificate"},
		{"unparseable key", append(append([]byte{}, caCertPEM...), badKey...), "CA private key"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mat, err := clustertls.GenerateLeafFromCA(tc.bundle)
			if err == nil {
				t.Fatal("expected a rejection")
			}
			if mat != nil {
				t.Error("a rejected bundle must yield no material")
			}
			var want *clustertls.KeyError
			if !errors.As(err, &want) {
				t.Fatalf("error = %v (%T), want *clustertls.KeyError", err, err)
			}
			if want.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", want.Kind, tc.kind)
			}
		})
	}
}

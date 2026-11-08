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

//go:build qrdb_embedded && !integration

package main

import (
	"bytes"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// writePEMFiles (certs.go; replaced writeCertBundle when server-side keygen
// was retired -- see cmd/qrdb/certs.go newCertsIssueCmd)
// ---------------------------------------------------------------------------

func TestWritePEMFiles_Success(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "certs")
	files := map[string][]byte{
		"cert.pem": []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"),
		"key.pem":  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("test")}),
	}
	if err := writePEMFiles(dir, files); err != nil {
		t.Fatalf("writePEMFiles: %v", err)
	}

	for name, want := range files {
		path := filepath.Join(dir, name)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s content = %q, want %q", name, got, want)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s mode = %o, want 0600", name, perm)
		}
	}
}

func TestWritePEMFiles_DirectoryCreateError(t *testing.T) {
	// Use a path under /dev/null to force MkdirAll to fail.
	err := writePEMFiles("/dev/null/impossible", map[string][]byte{"cert.pem": []byte("x")})
	if err == nil {
		t.Fatal("expected error when directory cannot be created")
	}
	var target *DirectoryCreateError
	if !errors.As(err, &target) {
		t.Fatalf("expected *DirectoryCreateError, got %T: %v", err, err)
	}
}

func TestWritePEMFiles_WriteError(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "certs")
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Place a directory where cert.pem would be written - WriteFile will fail
	// because the path is a directory (works even as root, unlike chmod).
	certPath := filepath.Join(targetDir, "cert.pem")
	if err := os.MkdirAll(certPath, 0700); err != nil {
		t.Fatalf("mkdir cert.pem: %v", err)
	}

	err := writePEMFiles(targetDir, map[string][]byte{"cert.pem": []byte("cert")})
	if err == nil {
		t.Fatal("expected error when cert.pem cannot be written")
	}
	var target *FileWriteError
	if !errors.As(err, &target) {
		t.Fatalf("expected *FileWriteError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Batch execute commands
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Transaction execute commands
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// certRecordRow
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// printEnrolledCert (certs.go; replaced printCertBundle when server-side
// keygen was retired -- there is no key to print any more)
// ---------------------------------------------------------------------------

func TestPrintEnrolledCert_YAMLFormat(t *testing.T) {
	origWriter := outputWriter
	origFormat := outputFormat
	t.Cleanup(func() {
		outputWriter = origWriter
		outputFormat = origFormat
	})

	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "yaml"

	e := &transport.EnrolledCert{
		Username: "alice",
		Serial:   "YAML-SERIAL",
		CertPEM:  "yaml-cert",
	}
	if err := printEnrolledCert(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "YAML-SERIAL") {
		t.Fatalf("expected serial in YAML output, got: %q", output)
	}
}

// ---------------------------------------------------------------------------
// printList
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// printValue and printOK
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// printTable
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// printJSON and printYAML
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// EmbeddedServerBinding: startup/shutdown lifecycle
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// resolveStorageSM additional tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// validateOutputFormat
// ---------------------------------------------------------------------------

//go:build !integration

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

// init_officers_file_test.go verifies `qrdb init --officers-file`
// (parseOfficersFile and its wiring into runInit / CeremonyInitializeRequest),
// the rich-holder alternative to --officers/--custodians (go-qrdb#4578
// review item 1).
package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// writeOfficersFile JSON-encodes doc to a fresh temp file and returns its
// path.
func writeOfficersFile(t *testing.T, doc officersFileDocument) string {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal officers file: %v", err)
	}
	path := filepath.Join(t.TempDir(), "officers.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("writing officers file: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// parseOfficersFile -- direct unit tests
// ---------------------------------------------------------------------------

func TestParseOfficersFile_ValidEntry_FullShape(t *testing.T) {
	csrPath := writeOfficerTempFile(t, "alice.csr", []byte("ALICE-CSR-PEM"))
	encKeyPath := writeOfficerTempFile(t, "alice.enc.pub", []byte("ALICE-ENC-PUB"))
	path := writeOfficersFile(t, officersFileDocument{Officers: []officersFileEntry{{
		Username:          "alice",
		Role:              "so",
		CSRPEMPath:        csrPath,
		EncryptionKEM:     "dhkem-p256",
		EncryptionKeyPath: encKeyPath,
		Designated:        "fido2",
	}}})

	got, err := parseOfficersFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d officers, want 1", len(got))
	}
	o := got[0]
	if o.Username != "alice" || o.Role != "so" || o.CSRPEM != "ALICE-CSR-PEM" ||
		o.EncryptionKEM != "dhkem-p256" || string(o.EncryptionKey) != "ALICE-ENC-PUB" || o.Designated != "fido2" {
		t.Errorf("unexpected officer: %+v", o)
	}
}

func TestParseOfficersFile_FactorsSurviveTheRoundTrip(t *testing.T) {
	csrPath := writeOfficerTempFile(t, "carol.csr", []byte("CAROL-CSR-PEM"))
	path := writeOfficersFile(t, officersFileDocument{Officers: []officersFileEntry{{
		Username:   "carol",
		Role:       "custodian",
		CSRPEMPath: csrPath,
		Factors: []transport.HolderFactorEnrolment{
			{Name: "fido2", Params: map[string]any{"attestation": "blob"}},
		},
		Designated: "fido2",
	}}})

	got, err := parseOfficersFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0].Factors) != 1 || got[0].Factors[0].Name != "fido2" {
		t.Fatalf("factors did not survive: %+v", got)
	}
}

func TestParseOfficersFile_Empty(t *testing.T) {
	path := writeOfficersFile(t, officersFileDocument{})
	if _, err := parseOfficersFile(path); err == nil {
		t.Fatal("expected an error for an officers file naming no holders")
	}
}

func TestParseOfficersFile_MissingUsername(t *testing.T) {
	csrPath := writeOfficerTempFile(t, "x.csr", []byte("X"))
	path := writeOfficersFile(t, officersFileDocument{Officers: []officersFileEntry{{CSRPEMPath: csrPath}}})
	if _, err := parseOfficersFile(path); err == nil {
		t.Fatal("expected an error for a holder with no username")
	}
}

func TestParseOfficersFile_MissingCSRPath(t *testing.T) {
	path := writeOfficersFile(t, officersFileDocument{Officers: []officersFileEntry{{Username: "alice"}}})
	if _, err := parseOfficersFile(path); err == nil {
		t.Fatal("expected an error for a holder with no csr_pem")
	}
}

func TestParseOfficersFile_MissingCSRFile(t *testing.T) {
	path := writeOfficersFile(t, officersFileDocument{Officers: []officersFileEntry{{
		Username:   "alice",
		CSRPEMPath: filepath.Join(t.TempDir(), "missing.csr"),
	}}})
	_, err := parseOfficersFile(path)
	if err == nil {
		t.Fatal("expected an error for a missing CSR file")
	}
	var fileErr *FileReadError
	if !errors.As(err, &fileErr) {
		t.Errorf("expected *FileReadError, got: %v (%T)", err, err)
	}
}

func TestParseOfficersFile_MissingEncryptionKeyFile(t *testing.T) {
	csrPath := writeOfficerTempFile(t, "alice.csr", []byte("X"))
	path := writeOfficersFile(t, officersFileDocument{Officers: []officersFileEntry{{
		Username:          "alice",
		CSRPEMPath:        csrPath,
		EncryptionKEM:     "dhkem-p256",
		EncryptionKeyPath: filepath.Join(t.TempDir(), "missing.pub"),
	}}})
	_, err := parseOfficersFile(path)
	if err == nil {
		t.Fatal("expected an error for a missing encryption key file")
	}
	var fileErr *FileReadError
	if !errors.As(err, &fileErr) {
		t.Errorf("expected *FileReadError, got: %v (%T)", err, err)
	}
}

func TestParseOfficersFile_NotJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "officers.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseOfficersFile(path); err == nil {
		t.Fatal("expected an error for a malformed officers file")
	}
}

func TestParseOfficersFile_MissingFile(t *testing.T) {
	_, err := parseOfficersFile(filepath.Join(t.TempDir(), "missing.json"))
	var fileErr *FileReadError
	if !errors.As(err, &fileErr) {
		t.Errorf("expected *FileReadError, got: %v (%T)", err, err)
	}
}

// ---------------------------------------------------------------------------
// qrdb init --officers-file -- end-to-end (in-process) success case
// ---------------------------------------------------------------------------

// TestInitCmd_ShamirWithOfficersFile_Success proves --officers-file carries
// the rich holder shape (separate envelope key, factors, designated factor)
// all the way through to CeremonyInitializeOptions, the same destination
// --officers/--custodians reach.
func TestInitCmd_ShamirWithOfficersFile_Success(t *testing.T) {
	alicePath := writeOfficerTempFile(t, "alice.csr", []byte("ALICE-CSR-PEM"))
	bobPath := writeOfficerTempFile(t, "bob.csr", []byte("BOB-CSR-PEM"))
	bobEncPath := writeOfficerTempFile(t, "bob.enc.pub", []byte("BOB-ENC-PUB"))
	officersPath := writeOfficersFile(t, officersFileDocument{Officers: []officersFileEntry{
		{Username: "alice", Role: "so", CSRPEMPath: alicePath},
		{
			Username: "bob", Role: "custodian", CSRPEMPath: bobPath,
			EncryptionKEM: "dhkem-p256", EncryptionKeyPath: bobEncPath,
			Designated: "fido2",
		},
	}})

	svc := &capturingCeremonyService{}
	root := newTestRootCmdInitOfficers(svc)

	out, err := executeCommand(root, "init",
		"--so-pin", "sopin", "--user-pin", "userpin",
		"--barrier-strategy", "shamir", "--shares", "2", "--threshold", "2",
		"--admin-generate",
		"--officers-file", officersPath,
	)
	if err != nil {
		t.Fatalf("init --officers-file: %v\noutput: %s", err, out)
	}

	if svc.gotOpts == nil {
		t.Fatal("CeremonyInitialize was not called")
	}
	if len(svc.gotOpts.Officers) != 2 {
		t.Fatalf("expected 2 officers forwarded, got %d: %+v", len(svc.gotOpts.Officers), svc.gotOpts.Officers)
	}
	if o := svc.gotOpts.Officers[0]; o.Username != "alice" || o.CSRPEM != "ALICE-CSR-PEM" || o.Role != seal.RoleSecurityOfficer {
		t.Errorf("holder[0] mismatch: %+v", o)
	}
	if o := svc.gotOpts.Officers[1]; o.Username != "bob" || o.CSRPEM != "BOB-CSR-PEM" || o.Role != seal.RoleCustodian ||
		o.EncryptionKEM != "dhkem-p256" || string(o.EncryptionKey) != "BOB-ENC-PUB" || o.Designated != "fido2" {
		t.Errorf("holder[1] mismatch: %+v", o)
	}
}

// TestInitCmd_OfficersFileMalformed_LocalError proves a malformed
// --officers-file fails before any call, exactly like a malformed
// --officers/--custodians entry does.
func TestInitCmd_OfficersFileMalformed_LocalError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "officers.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := &capturingCeremonyService{}
	root := newTestRootCmdInitOfficers(svc)

	_, err := executeCommand(root, "init",
		"--so-pin", "sopin", "--user-pin", "userpin",
		"--barrier-strategy", "none",
		"--admin-generate",
		"--officers-file", path,
	)
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) || invalid.Arg != "--officers-file" {
		t.Fatalf("err = %v, want an InvalidArgError on --officers-file", err)
	}
	if svc.gotOpts != nil {
		t.Error("CeremonyInitialize should not have been called for a local validation failure")
	}
}

// TestInitCmd_OfficersFileAndOfficersFlag_MutuallyExclusive proves
// --officers-file cannot be combined with --officers/--custodians: a holder
// is named one way or the other, never both, so there is exactly one
// resolved list.
func TestInitCmd_OfficersFileAndOfficersFlag_MutuallyExclusive(t *testing.T) {
	alicePath := writeOfficerTempFile(t, "alice.csr", []byte("ALICE-CSR-PEM"))
	officersPath := writeOfficersFile(t, officersFileDocument{Officers: []officersFileEntry{
		{Username: "alice", CSRPEMPath: alicePath},
	}})

	svc := &capturingCeremonyService{}
	root := newTestRootCmdInitOfficers(svc)

	_, err := executeCommand(root, "init",
		"--so-pin", "sopin", "--user-pin", "userpin",
		"--barrier-strategy", "none",
		"--admin-generate",
		"--officers-file", officersPath,
		"--officers", "alice="+alicePath,
	)
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) || invalid.Arg != "--officers-file" {
		t.Fatalf("err = %v, want an InvalidArgError on --officers-file", err)
	}
	if svc.gotOpts != nil {
		t.Error("CeremonyInitialize should not have been called for a local validation failure")
	}
}

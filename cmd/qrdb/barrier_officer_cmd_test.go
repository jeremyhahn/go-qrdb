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

//go:build !integration

// barrier_officer_cmd_test.go verifies the Security Officer CLI verbs:
// barrier officer {register,list,claim-status,claim-begin,claim-complete},
// barrier shamir-seal-vote, and
// barrier recovery officer {register,claim-begin,claim-complete}.
package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

var errOfficerCmdTest = errors.New("officer cmd test failure")

// writeOfficerTempFile writes content to a file in a per-test temp dir and
// returns its path.
func writeOfficerTempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}

// officerTestStatuses returns a deterministic two-officer fixture covering
// both the claimed and unclaimed rendering branches.
func officerTestStatuses() []*service.BarrierOfficerStatus {
	return []*service.BarrierOfficerStatus{
		{
			Username:     "alice",
			ShareIndex:   1,
			AuthFactor:   "software",
			EnrolledAt:   time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
			SharePending: true,
		},
		{
			Username:     "bob",
			ShareIndex:   2,
			AuthFactor:   "fido2",
			EnrolledAt:   time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC),
			ClaimedAt:    time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC),
			SharePending: false,
		},
	}
}

// ---------------------------------------------------------------------------
// barrier officer register
// ---------------------------------------------------------------------------

// TestBarrierOfficerRegisterCmd_TextOutput verifies the register verb reads
// the CSR file, forwards it to the service, and renders the share index and
// signed certificate.
func TestBarrierOfficerRegisterCmd_TextOutput(t *testing.T) {
	csrPath := writeOfficerTempFile(t, "officer.csr", []byte("CSR-PEM-DATA"))

	var gotReq *service.BarrierOfficerRegisterRequest
	svc := &mockBarrierService{
		officerRegisterFn: func(_ context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
			gotReq = req
			return &service.BarrierOfficerRegisterResult{CertPEM: []byte("CERT-PEM"), ShareIndex: 3}, nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "barrier", "officer", "register",
		"--username", "alice",
		"--csr-file", csrPath,
		"--authenticator-type", "software",
		"--authenticator-ref", "slot-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotReq == nil {
		t.Fatal("service was not called")
	}
	if gotReq.Username != "alice" {
		t.Errorf("expected username alice, got %q", gotReq.Username)
	}
	if !bytes.Equal(gotReq.CSRPEM, []byte("CSR-PEM-DATA")) {
		t.Errorf("expected CSR bytes forwarded, got %q", gotReq.CSRPEM)
	}
	if gotReq.AuthenticatorType != "software" || gotReq.AuthenticatorRef != "slot-1" {
		t.Errorf("authenticator fields not forwarded: %+v", gotReq)
	}
	if !strings.Contains(out, "ShareIndex:  3") || !strings.Contains(out, "CERT-PEM") {
		t.Errorf("unexpected output: %q", out)
	}
}

// TestBarrierOfficerRegisterCmd_JSONOutput verifies the JSON renderer.
func TestBarrierOfficerRegisterCmd_JSONOutput(t *testing.T) {
	csrPath := writeOfficerTempFile(t, "officer.csr", []byte("CSR"))
	svc := &mockBarrierService{
		officerRegisterFn: func(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
			return &service.BarrierOfficerRegisterResult{CertPEM: []byte("CERT"), ShareIndex: 7}, nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "--output", "json", "barrier", "officer", "register",
		"--username", "alice", "--csr-file", csrPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "\"share_index\"") && !strings.Contains(out, "7") {
		t.Errorf("unexpected JSON output: %q", out)
	}
}

// TestBarrierOfficerRegisterCmd_MissingCSRFile verifies a typed read error
// when the CSR file does not exist.
func TestBarrierOfficerRegisterCmd_MissingCSRFile(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "officer", "register",
		"--username", "alice", "--csr-file", filepath.Join(t.TempDir(), "missing.csr"))
	if err == nil {
		t.Fatal("expected error for missing CSR file")
	}
	if !strings.Contains(err.Error(), "reading CSR file") {
		t.Errorf("expected CSR read error, got: %v", err)
	}
}

// TestBarrierOfficerRegisterCmd_ServiceError verifies service errors
// propagate to the caller.
func TestBarrierOfficerRegisterCmd_ServiceError(t *testing.T) {
	csrPath := writeOfficerTempFile(t, "officer.csr", []byte("CSR"))
	svc := &mockBarrierService{
		officerRegisterFn: func(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
			return nil, errOfficerCmdTest
		},
	}
	root := newBarrierTestRoot(t, svc)
	_, err := executeCommand(root, "barrier", "officer", "register",
		"--username", "alice", "--csr-file", csrPath)
	if err == nil {
		t.Fatal("expected service error")
	}
}

// ---------------------------------------------------------------------------
// barrier officer list
// ---------------------------------------------------------------------------

// TestBarrierOfficerListCmd_TextOutput verifies the table renderer prints
// every officer with the claimed/unclaimed branches.
func TestBarrierOfficerListCmd_TextOutput(t *testing.T) {
	svc := &mockBarrierService{
		officerListFn: func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
			return officerTestStatuses(), nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "barrier", "officer", "list")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"alice", "bob", "software", "fido2", "ClaimedAt"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got: %q", want, out)
		}
	}
}

// TestBarrierOfficerListCmd_JSONOutput verifies the JSON renderer.
func TestBarrierOfficerListCmd_JSONOutput(t *testing.T) {
	svc := &mockBarrierService{
		officerListFn: func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
			return officerTestStatuses(), nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "--output", "json", "barrier", "officer", "list")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "alice") || !strings.Contains(out, "bob") {
		t.Errorf("unexpected JSON output: %q", out)
	}
}

// TestBarrierOfficerListCmd_ServiceError verifies service errors propagate.
func TestBarrierOfficerListCmd_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		officerListFn: func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
			return nil, errOfficerCmdTest
		},
	}
	root := newBarrierTestRoot(t, svc)
	if _, err := executeCommand(root, "barrier", "officer", "list"); err == nil {
		t.Fatal("expected service error")
	}
}

// ---------------------------------------------------------------------------
// barrier officer claim-status
// ---------------------------------------------------------------------------

// TestBarrierOfficerClaimStatusCmd_TextOutput verifies the compact claim
// status renderer covers both claimed and unclaimed officers.
func TestBarrierOfficerClaimStatusCmd_TextOutput(t *testing.T) {
	svc := &mockBarrierService{
		officerClaimStatusFn: func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
			return officerTestStatuses(), nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "barrier", "officer", "claim-status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "claimed=no") {
		t.Errorf("expected unclaimed officer marked claimed=no, got: %q", out)
	}
	if !strings.Contains(out, "claimed=2026-06-02T09:30:00Z") {
		t.Errorf("expected claimed timestamp, got: %q", out)
	}
}

// TestBarrierOfficerClaimStatusCmd_YAMLOutput verifies the YAML renderer.
func TestBarrierOfficerClaimStatusCmd_YAMLOutput(t *testing.T) {
	svc := &mockBarrierService{
		officerClaimStatusFn: func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
			return officerTestStatuses(), nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "--output", "yaml", "barrier", "officer", "claim-status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("unexpected YAML output: %q", out)
	}
}

// TestBarrierOfficerClaimStatusCmd_ServiceError verifies service errors
// propagate.
func TestBarrierOfficerClaimStatusCmd_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		officerClaimStatusFn: func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
			return nil, errOfficerCmdTest
		},
	}
	root := newBarrierTestRoot(t, svc)
	if _, err := executeCommand(root, "barrier", "officer", "claim-status"); err == nil {
		t.Fatal("expected service error")
	}
}

// ---------------------------------------------------------------------------
// barrier officer claim-begin
// ---------------------------------------------------------------------------

// TestBarrierOfficerClaimBeginCmd_TextOutput verifies the challenge nonce
// and expiry render and the username is forwarded.
func TestBarrierOfficerClaimBeginCmd_TextOutput(t *testing.T) {
	var gotUsername string
	svc := &mockBarrierService{
		shareClaimBeginFn: func(_ context.Context, username, _ string) (*service.BarrierShareClaimChallenge, error) {
			gotUsername = username
			return &service.BarrierShareClaimChallenge{
				NonceHex:  "abcd1234",
				ExpiresAt: time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC),
			}, nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "barrier", "officer", "claim-begin", "--username", "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotUsername != "alice" {
		t.Errorf("expected username alice forwarded, got %q", gotUsername)
	}
	if !strings.Contains(out, "abcd1234") || !strings.Contains(out, "2026-06-11T12:00:00Z") {
		t.Errorf("unexpected output: %q", out)
	}
}

// TestBarrierOfficerClaimBeginCmd_JSONOutput verifies the JSON renderer.
func TestBarrierOfficerClaimBeginCmd_JSONOutput(t *testing.T) {
	svc := &mockBarrierService{
		shareClaimBeginFn: func(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
			return &service.BarrierShareClaimChallenge{NonceHex: "ff00", ExpiresAt: time.Now()}, nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "--output", "json", "barrier", "officer", "claim-begin", "--username", "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "ff00") {
		t.Errorf("unexpected JSON output: %q", out)
	}
}

// TestBarrierOfficerClaimBeginCmd_ServiceError verifies service errors
// propagate.
func TestBarrierOfficerClaimBeginCmd_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		shareClaimBeginFn: func(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
			return nil, errOfficerCmdTest
		},
	}
	root := newBarrierTestRoot(t, svc)
	if _, err := executeCommand(root, "barrier", "officer", "claim-begin", "--username", "alice"); err == nil {
		t.Fatal("expected service error")
	}
}

// ---------------------------------------------------------------------------
// barrier officer claim-complete
// ---------------------------------------------------------------------------

// TestBarrierOfficerClaimCompleteCmd_TextOutput verifies the signature file
// is read, all parameters are forwarded, and the share renders in hex.
func TestBarrierOfficerClaimCompleteCmd_TextOutput(t *testing.T) {
	sigPath := writeOfficerTempFile(t, "claim.sig", []byte{0xAA, 0xBB})

	var gotUsername, gotNonce string
	var gotSig []byte
	svc := &mockBarrierService{
		shareClaimCompleteFn: func(_ context.Context, username, nonceHex string, signature []byte, _ string) (*service.BarrierShareClaimResult, error) {
			gotUsername, gotNonce, gotSig = username, nonceHex, signature
			return &service.BarrierShareClaimResult{ShareIndex: 2, ShareBytes: []byte{0x01, 0x02}}, nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "barrier", "officer", "claim-complete",
		"--username", "alice", "--nonce-hex", "abcd", "--sig-file", sigPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotUsername != "alice" || gotNonce != "abcd" {
		t.Errorf("parameters not forwarded: username=%q nonce=%q", gotUsername, gotNonce)
	}
	if !bytes.Equal(gotSig, []byte{0xAA, 0xBB}) {
		t.Errorf("signature bytes not forwarded: %x", gotSig)
	}
	if !strings.Contains(out, "ShareIndex:  2") || !strings.Contains(out, "0102") {
		t.Errorf("unexpected output: %q", out)
	}
}

// TestBarrierOfficerClaimCompleteCmd_MissingSigFile verifies a typed read
// error when the signature file does not exist.
func TestBarrierOfficerClaimCompleteCmd_MissingSigFile(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "officer", "claim-complete",
		"--username", "alice", "--nonce-hex", "abcd",
		"--sig-file", filepath.Join(t.TempDir(), "missing.sig"))
	if err == nil {
		t.Fatal("expected error for missing signature file")
	}
	if !strings.Contains(err.Error(), "reading signature file") {
		t.Errorf("expected signature read error, got: %v", err)
	}
}

// TestBarrierOfficerClaimCompleteCmd_ServiceError verifies service errors
// propagate.
func TestBarrierOfficerClaimCompleteCmd_ServiceError(t *testing.T) {
	sigPath := writeOfficerTempFile(t, "claim.sig", []byte{0x01})
	svc := &mockBarrierService{
		shareClaimCompleteFn: func(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
			return nil, errOfficerCmdTest
		},
	}
	root := newBarrierTestRoot(t, svc)
	_, err := executeCommand(root, "barrier", "officer", "claim-complete",
		"--username", "alice", "--nonce-hex", "abcd", "--sig-file", sigPath)
	if err == nil {
		t.Fatal("expected service error")
	}
}

// TestBarrierOfficerClaimCompleteCmd_JSONOutput verifies the JSON renderer
// and confirms ShareBytes is excluded from JSON (json:"-").
func TestBarrierOfficerClaimCompleteCmd_JSONOutput(t *testing.T) {
	sigPath := writeOfficerTempFile(t, "claim.sig", []byte{0x01})
	svc := &mockBarrierService{
		shareClaimCompleteFn: func(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
			return &service.BarrierShareClaimResult{ShareIndex: 4, ShareBytes: []byte{0xCA, 0xFE}}, nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "--output", "json", "barrier", "officer", "claim-complete",
		"--username", "alice", "--nonce-hex", "abcd", "--sig-file", sigPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "4") {
		t.Errorf("unexpected JSON output: %q", out)
	}
}

// ---------------------------------------------------------------------------
// barrier shamir-seal-vote
// ---------------------------------------------------------------------------

// TestBarrierShamirSealVoteCmd_TextOutput verifies the share is forwarded
// and quorum progress renders.
func TestBarrierShamirSealVoteCmd_TextOutput(t *testing.T) {
	var gotShare string
	svc := &mockBarrierService{
		shamirSealVoteFn: func(_ context.Context, share string) (*service.UnsealShareProgress, error) {
			gotShare = share
			return &service.UnsealShareProgress{Required: 3, Submitted: 2, Complete: false}, nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "barrier", "shamir-seal-vote", "--share", "share-abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotShare != "share-abc" {
		t.Errorf("expected share forwarded, got %q", gotShare)
	}
	for _, want := range []string{"Required:   3", "Submitted:  2", "Complete:   false"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got: %q", want, out)
		}
	}
}

// TestBarrierShamirSealVoteCmd_JSONOutput verifies the JSON renderer.
func TestBarrierShamirSealVoteCmd_JSONOutput(t *testing.T) {
	svc := &mockBarrierService{
		shamirSealVoteFn: func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
			return &service.UnsealShareProgress{Required: 2, Submitted: 2, Complete: true}, nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "--output", "json", "barrier", "shamir-seal-vote", "--share", "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "true") {
		t.Errorf("unexpected JSON output: %q", out)
	}
}

// TestBarrierShamirSealVoteCmd_ServiceError verifies service errors
// propagate.
func TestBarrierShamirSealVoteCmd_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		shamirSealVoteFn: func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
			return nil, errOfficerCmdTest
		},
	}
	root := newBarrierTestRoot(t, svc)
	if _, err := executeCommand(root, "barrier", "shamir-seal-vote", "--share", "s1"); err == nil {
		t.Fatal("expected service error")
	}
}

// ---------------------------------------------------------------------------
// barrier recovery officer register
// ---------------------------------------------------------------------------

// TestBarrierRecoveryOfficerRegisterCmd_TextOutput verifies the recovery
// register verb reads the CSR and routes to the recovery service method.
func TestBarrierRecoveryOfficerRegisterCmd_TextOutput(t *testing.T) {
	csrPath := writeOfficerTempFile(t, "recovery.csr", []byte("REC-CSR"))

	var gotReq *service.BarrierOfficerRegisterRequest
	svc := &mockBarrierService{
		recoveryOfficerRegisterFn: func(_ context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
			gotReq = req
			return &service.BarrierOfficerRegisterResult{CertPEM: []byte("REC-CERT"), ShareIndex: 5}, nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "barrier", "recovery", "officer", "register",
		"--username", "carol", "--csr-file", csrPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotReq == nil || gotReq.Username != "carol" || !bytes.Equal(gotReq.CSRPEM, []byte("REC-CSR")) {
		t.Errorf("recovery register request not forwarded: %+v", gotReq)
	}
	if !strings.Contains(out, "ShareIndex:  5") || !strings.Contains(out, "REC-CERT") {
		t.Errorf("unexpected output: %q", out)
	}
}

// TestBarrierRecoveryOfficerRegisterCmd_MissingCSRFile verifies the typed
// read error path.
func TestBarrierRecoveryOfficerRegisterCmd_MissingCSRFile(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "recovery", "officer", "register",
		"--username", "carol", "--csr-file", filepath.Join(t.TempDir(), "missing.csr"))
	if err == nil {
		t.Fatal("expected error for missing CSR file")
	}
	if !strings.Contains(err.Error(), "reading CSR file") {
		t.Errorf("expected CSR read error, got: %v", err)
	}
}

// TestBarrierRecoveryOfficerRegisterCmd_ServiceError verifies service errors
// propagate.
func TestBarrierRecoveryOfficerRegisterCmd_ServiceError(t *testing.T) {
	csrPath := writeOfficerTempFile(t, "recovery.csr", []byte("CSR"))
	svc := &mockBarrierService{
		recoveryOfficerRegisterFn: func(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
			return nil, errOfficerCmdTest
		},
	}
	root := newBarrierTestRoot(t, svc)
	_, err := executeCommand(root, "barrier", "recovery", "officer", "register",
		"--username", "carol", "--csr-file", csrPath)
	if err == nil {
		t.Fatal("expected service error")
	}
}

// TestBarrierRecoveryOfficerRegisterCmd_JSONOutput verifies the JSON renderer.
func TestBarrierRecoveryOfficerRegisterCmd_JSONOutput(t *testing.T) {
	csrPath := writeOfficerTempFile(t, "recovery.csr", []byte("CSR"))
	svc := &mockBarrierService{
		recoveryOfficerRegisterFn: func(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
			return &service.BarrierOfficerRegisterResult{CertPEM: []byte("CERT"), ShareIndex: 9}, nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "--output", "json", "barrier", "recovery", "officer", "register",
		"--username", "carol", "--csr-file", csrPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "9") {
		t.Errorf("unexpected JSON output: %q", out)
	}
}

// ---------------------------------------------------------------------------
// barrier recovery officer claim-begin
// ---------------------------------------------------------------------------

// TestBarrierRecoveryOfficerClaimBeginCmd_TextOutput verifies the recovery
// claim-begin verb routes to the recovery service method.
func TestBarrierRecoveryOfficerClaimBeginCmd_TextOutput(t *testing.T) {
	var gotUsername string
	svc := &mockBarrierService{
		recoveryClaimBeginFn: func(_ context.Context, username, _ string) (*service.BarrierShareClaimChallenge, error) {
			gotUsername = username
			return &service.BarrierShareClaimChallenge{
				NonceHex:  "beef",
				ExpiresAt: time.Date(2026, 6, 11, 13, 0, 0, 0, time.UTC),
			}, nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "barrier", "recovery", "officer", "claim-begin", "--username", "carol")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotUsername != "carol" {
		t.Errorf("expected username carol forwarded, got %q", gotUsername)
	}
	if !strings.Contains(out, "beef") || !strings.Contains(out, "2026-06-11T13:00:00Z") {
		t.Errorf("unexpected output: %q", out)
	}
}

// TestBarrierRecoveryOfficerClaimBeginCmd_ServiceError verifies service
// errors propagate.
func TestBarrierRecoveryOfficerClaimBeginCmd_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		recoveryClaimBeginFn: func(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
			return nil, errOfficerCmdTest
		},
	}
	root := newBarrierTestRoot(t, svc)
	if _, err := executeCommand(root, "barrier", "recovery", "officer", "claim-begin", "--username", "carol"); err == nil {
		t.Fatal("expected service error")
	}
}

// TestBarrierRecoveryOfficerClaimBeginCmd_JSONOutput verifies the JSON
// renderer.
func TestBarrierRecoveryOfficerClaimBeginCmd_JSONOutput(t *testing.T) {
	svc := &mockBarrierService{
		recoveryClaimBeginFn: func(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
			return &service.BarrierShareClaimChallenge{NonceHex: "0011", ExpiresAt: time.Now()}, nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "--output", "json", "barrier", "recovery", "officer", "claim-begin", "--username", "carol")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "0011") {
		t.Errorf("unexpected JSON output: %q", out)
	}
}

// ---------------------------------------------------------------------------
// barrier recovery officer claim-complete
// ---------------------------------------------------------------------------

// TestBarrierRecoveryOfficerClaimCompleteCmd_TextOutput verifies the
// recovery claim-complete verb reads the signature file and renders the
// recovered share.
func TestBarrierRecoveryOfficerClaimCompleteCmd_TextOutput(t *testing.T) {
	sigPath := writeOfficerTempFile(t, "recovery.sig", []byte{0x10, 0x20})

	var gotUsername, gotNonce string
	var gotSig []byte
	svc := &mockBarrierService{
		recoveryClaimCompleteFn: func(_ context.Context, username, nonceHex string, signature []byte, _ string) (*service.BarrierShareClaimResult, error) {
			gotUsername, gotNonce, gotSig = username, nonceHex, signature
			return &service.BarrierShareClaimResult{ShareIndex: 6, ShareBytes: []byte{0x42}}, nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "barrier", "recovery", "officer", "claim-complete",
		"--username", "carol", "--nonce-hex", "beef", "--sig-file", sigPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotUsername != "carol" || gotNonce != "beef" || !bytes.Equal(gotSig, []byte{0x10, 0x20}) {
		t.Errorf("parameters not forwarded: username=%q nonce=%q sig=%x", gotUsername, gotNonce, gotSig)
	}
	if !strings.Contains(out, "ShareIndex:  6") || !strings.Contains(out, "42") {
		t.Errorf("unexpected output: %q", out)
	}
}

// TestBarrierRecoveryOfficerClaimCompleteCmd_MissingSigFile verifies the
// typed read error path.
func TestBarrierRecoveryOfficerClaimCompleteCmd_MissingSigFile(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "recovery", "officer", "claim-complete",
		"--username", "carol", "--nonce-hex", "beef",
		"--sig-file", filepath.Join(t.TempDir(), "missing.sig"))
	if err == nil {
		t.Fatal("expected error for missing signature file")
	}
	if !strings.Contains(err.Error(), "reading signature file") {
		t.Errorf("expected signature read error, got: %v", err)
	}
}

// TestBarrierRecoveryOfficerClaimCompleteCmd_ServiceError verifies service
// errors propagate.
func TestBarrierRecoveryOfficerClaimCompleteCmd_ServiceError(t *testing.T) {
	sigPath := writeOfficerTempFile(t, "recovery.sig", []byte{0x01})
	svc := &mockBarrierService{
		recoveryClaimCompleteFn: func(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
			return nil, errOfficerCmdTest
		},
	}
	root := newBarrierTestRoot(t, svc)
	_, err := executeCommand(root, "barrier", "recovery", "officer", "claim-complete",
		"--username", "carol", "--nonce-hex", "beef", "--sig-file", sigPath)
	if err == nil {
		t.Fatal("expected service error")
	}
}

// ---------------------------------------------------------------------------
// Officer verb-tree wiring
// ---------------------------------------------------------------------------

// officerBarrierVerbs enumerates the Security Officer verb tree exposed by
// qrdb: officer enrollment/claim, quorum seal votes, and the recovery
// officer mirror. These verbs are part of the qrdb surface but are NOT yet
// part of the four-CLI canonical table in canonicalBarrierVerbs.
var officerBarrierVerbs = []struct {
	path  []string
	flags []string
}{
	{
		path:  []string{"barrier", "officer", "register"},
		flags: []string{"username", "csr-file", "authenticator-type", "authenticator-ref"},
	},
	{path: []string{"barrier", "officer", "list"}, flags: nil},
	{path: []string{"barrier", "officer", "claim-status"}, flags: nil},
	{
		path:  []string{"barrier", "officer", "claim-begin"},
		flags: []string{"username"},
	},
	{
		path:  []string{"barrier", "officer", "claim-complete"},
		flags: []string{"username", "nonce-hex", "sig-file"},
	},
	{
		path:  []string{"barrier", "shamir-seal-vote"},
		flags: []string{"share"},
	},
	{
		path:  []string{"barrier", "recovery", "officer", "register"},
		flags: []string{"username", "csr-file", "authenticator-type", "authenticator-ref"},
	},
	{
		path:  []string{"barrier", "recovery", "officer", "claim-begin"},
		flags: []string{"username"},
	},
	{
		path:  []string{"barrier", "recovery", "officer", "claim-complete"},
		flags: []string{"username", "nonce-hex", "sig-file"},
	},
}

// TestOfficerBarrierVerbTree pins the Security Officer verb tree and every
// flag per verb so accidental removals or renames fail fast.
func TestOfficerBarrierVerbTree(t *testing.T) {
	root := newRootCmd()
	for _, tc := range officerBarrierVerbs {
		name := strings.Join(tc.path, "/")
		t.Run(name, func(t *testing.T) {
			cmd, _, err := root.Find(tc.path)
			if err != nil {
				t.Fatalf("verb %s not registered: %v", name, err)
			}
			for _, flagName := range tc.flags {
				if cmd.Flags().Lookup(flagName) == nil {
					t.Errorf("verb %s missing flag --%s", name, flagName)
				}
			}
		})
	}
}

// TestBarrierRecoveryOfficerClaimCompleteCmd_YAMLOutput verifies the YAML
// renderer.
func TestBarrierRecoveryOfficerClaimCompleteCmd_YAMLOutput(t *testing.T) {
	sigPath := writeOfficerTempFile(t, "recovery.sig", []byte{0x01})
	svc := &mockBarrierService{
		recoveryClaimCompleteFn: func(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
			return &service.BarrierShareClaimResult{ShareIndex: 8, ShareBytes: []byte{0x99}}, nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "--output", "yaml", "barrier", "recovery", "officer", "claim-complete",
		"--username", "carol", "--nonce-hex", "beef", "--sig-file", sigPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "8") {
		t.Errorf("unexpected YAML output: %q", out)
	}
}

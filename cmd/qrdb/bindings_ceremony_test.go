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

// bindings_ceremony_test.go verifies CeremonyBinding, in particular that a
// generated bootstrap admin key is saved to a local file and never appears
// in the value returned to the page (go-qrdb#4578 review item 2).
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// newCeremonyTestBinding wires a CeremonyBinding to an embedded transport
// backed by svc, mirroring how the desktop GUI connects.
func newCeremonyTestBinding(t *testing.T, svc service.CeremonyServicer) *CeremonyBinding {
	t.Helper()
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService(),
		embedded.WithCeremonyService(svc), embedded.WithCeremonyCA(&mockCeremonyCA{}))
	return NewCeremonyBinding(testLogger(t), conn)
}

// errCeremonyTest is a sentinel error returned by mock ceremony functions.
var errCeremonyTest = errors.New("ceremony test failure")

// ---------------------------------------------------------------------------
// NewCeremonyBinding constructor
// ---------------------------------------------------------------------------

func TestNewCeremonyBinding_Constructor(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewCeremonyBinding(log, conn)
	if b == nil {
		t.Fatal("expected non-nil CeremonyBinding")
	}
	if b.logger != log {
		t.Error("expected logger to be set")
	}
	if b.conn != conn {
		t.Error("expected connection binding to be set")
	}
}

func TestNewCeremonyBinding_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewCeremonyBinding(log, conn)
	if _, err := b.Initialize(&transport.CeremonyInitializeRequest{}); err == nil {
		t.Fatal("expected an error when no client is loaded")
	}
}

// ---------------------------------------------------------------------------
// CeremonyBinding.Initialize
// ---------------------------------------------------------------------------

func TestCeremonyBinding_Initialize_CSRPath_NoKeySaved(t *testing.T) {
	svc := &capturingCeremonyService{}
	b := newCeremonyTestBinding(t, svc)

	result, err := b.Initialize(&transport.CeremonyInitializeRequest{
		AdminCSRPEM: "-----BEGIN CERTIFICATE REQUEST-----\nadmin\n-----END CERTIFICATE REQUEST-----",
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if svc.gotOpts == nil {
		t.Fatal("CeremonyInitialize was not called")
	}
	if result.AdminKeyPath != "" {
		t.Errorf("CSR path must never produce a saved key path, got %q", result.AdminKeyPath)
	}
}

func TestCeremonyBinding_Initialize_GeneratePath_SavesKeyLocally(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	svc := &capturingGeneratingCeremonyService{adminKeyPEM: []byte("-----BEGIN PRIVATE KEY-----\nmock\n-----END PRIVATE KEY-----")}
	b := newCeremonyTestBinding(t, svc)

	result, err := b.Initialize(&transport.CeremonyInitializeRequest{AdminGenerate: true})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if result.AdminKeyPath == "" {
		t.Fatal("expected AdminKeyPath to be set on the generate path")
	}
	if !strings.HasPrefix(result.AdminKeyPath, filepath.Join(configDir, "qrdb")) {
		t.Errorf("AdminKeyPath = %q, want under %q", result.AdminKeyPath, filepath.Join(configDir, "qrdb"))
	}
	saved, err := os.ReadFile(result.AdminKeyPath)
	if err != nil {
		t.Fatalf("reading saved key file: %v", err)
	}
	if string(saved) != "-----BEGIN PRIVATE KEY-----\nmock\n-----END PRIVATE KEY-----" {
		t.Errorf("saved key content mismatch: %q", saved)
	}
	if result.AdminCredentialSource != string(ceremony.AdminCredentialGenerated) {
		t.Errorf("AdminCredentialSource = %q, want %q", result.AdminCredentialSource, ceremony.AdminCredentialGenerated)
	}
}

// TestCeremonyBinding_Initialize_GeneratePath_RefusesLocallyBeforeCallingServerWhenUnsavable
// is the regression test for go-qrdb#560 review: when the local admin-key
// output path cannot be written, Initialize must refuse BEFORE asking the
// server to mint the credential at all. Discovering the save failure only
// AFTER the server had already issued the credential and recorded the
// pending-admin-cert hand-off would leave the bootstrap admin eventually
// bound to a key nobody holds.
func TestCeremonyBinding_Initialize_GeneratePath_RefusesLocallyBeforeCallingServerWhenUnsavable(t *testing.T) {
	configDir := t.TempDir()
	// A regular file sits exactly where the "qrdb" subdirectory needs to
	// be created, so os.MkdirAll fails regardless of process privilege
	// (unlike a permission-bit probe, which a root-run test would ignore).
	if err := os.WriteFile(filepath.Join(configDir, "qrdb"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seeding blocker file: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", configDir)

	svc := &capturingCeremonyService{}
	b := newCeremonyTestBinding(t, svc)

	_, err := b.Initialize(&transport.CeremonyInitializeRequest{AdminGenerate: true})
	if err == nil {
		t.Fatal("expected Initialize to refuse when the local admin-key output path is not writable")
	}
	if svc.gotOpts != nil {
		t.Fatal("the server must never be asked to mint the credential when the local save path is not writable")
	}
}

func TestCeremonyBinding_Initialize_HoldersMapped(t *testing.T) {
	svc := &capturingCeremonyService{}
	b := newCeremonyTestBinding(t, svc)

	result, err := b.Initialize(&transport.CeremonyInitializeRequest{
		Threshold:   2,
		AdminCSRPEM: "-----BEGIN CERTIFICATE REQUEST-----\nadmin\n-----END CERTIFICATE REQUEST-----",
		Officers: []transport.CeremonyOfficerRequest{
			{Username: "alice", Role: "so", CSRPEM: "csr-alice"},
			{Username: "bob", Role: "custodian", CSRPEM: "csr-bob"},
		},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if len(result.Holders) != 2 {
		t.Fatalf("expected 2 holders, got %d: %+v", len(result.Holders), result.Holders)
	}
	if result.Holders[0].Username != "alice" || result.Holders[1].Username != "bob" {
		t.Errorf("unexpected holder order/content: %+v", result.Holders)
	}
}

func TestCeremonyBinding_Initialize_ServiceError(t *testing.T) {
	svc := &capturingCeremonyService{err: errCeremonyTest}
	b := newCeremonyTestBinding(t, svc)

	if _, err := b.Initialize(&transport.CeremonyInitializeRequest{}); !errors.Is(err, errCeremonyTest) {
		t.Errorf("expected errCeremonyTest wrapped/propagated, got: %v", err)
	}
}

func TestCeremonyBinding_Initialize_SecretIsClonedThenBothWiped(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	svc := &capturingGeneratingCeremonyService{adminKeyPEM: []byte("-----BEGIN PRIVATE KEY-----\nmock\n-----END PRIVATE KEY-----")}
	b := newCeremonyTestBinding(t, svc)

	req := &transport.CeremonyInitializeRequest{Secret: []byte("barrier-secret"), AdminGenerate: true}
	if _, err := b.Initialize(req); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// go-qrdb#560 review: the caller's own req.Secret must be wiped too,
	// not only Initialize's own clone -- nothing in this process needs it
	// once the call has gone out, and it carries the same sensitive
	// secret the clone does.
	if !allZero(req.Secret) {
		t.Errorf("caller's req.Secret was not wiped: %q", req.Secret)
	}
}

// allZero reports whether every byte of b is zero -- what secure.Wipe
// leaves behind, as opposed to a shortened or untouched slice.
func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return len(b) > 0
}

// ---------------------------------------------------------------------------
// CeremonyBinding.Status
// ---------------------------------------------------------------------------

func TestCeremonyBinding_Status_Success(t *testing.T) {
	svc := &stubCeremonyServicer{}
	b := newCeremonyTestBinding(t, svc)

	status, err := b.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status == nil || status.State != "operational" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestCeremonyBinding_Status_ServiceError(t *testing.T) {
	svc := &stubCeremonyServicer{statusErr: errCeremonyTest}
	b := newCeremonyTestBinding(t, svc)

	if _, err := b.Status(); !errors.Is(err, errCeremonyTest) {
		t.Fatalf("expected errCeremonyTest wrapped/propagated, got: %v", err)
	}
}

func TestCeremonyBinding_Status_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewCeremonyBinding(log, conn)
	if _, err := b.Status(); err == nil {
		t.Fatal("expected an error when no client is loaded")
	}
}

// ---------------------------------------------------------------------------
// CeremonyBinding.SignCSR
// ---------------------------------------------------------------------------

func TestCeremonyBinding_SignCSR_Success(t *testing.T) {
	svc := &stubCeremonyServicer{}
	b := newCeremonyTestBinding(t, svc)

	resp, err := b.SignCSR(&transport.SignCSRInitRequest{Username: "alice", SOPin: "so-pin", CSRPEM: "csr", Role: "so"})
	if err != nil {
		t.Fatalf("SignCSR: %v", err)
	}
	if resp == nil || resp.CertPEM == "" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestCeremonyBinding_SignCSR_ServiceError(t *testing.T) {
	svc := &stubCeremonyServicer{csrErr: errCeremonyTest}
	b := newCeremonyTestBinding(t, svc)

	if _, err := b.SignCSR(&transport.SignCSRInitRequest{}); !errors.Is(err, errCeremonyTest) {
		t.Fatalf("expected errCeremonyTest wrapped/propagated, got: %v", err)
	}
}

func TestCeremonyBinding_SignCSR_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewCeremonyBinding(log, conn)
	if _, err := b.SignCSR(&transport.SignCSRInitRequest{}); err == nil {
		t.Fatal("expected an error when no client is loaded")
	}
}

// ---------------------------------------------------------------------------
// saveAdminKeyLocally
// ---------------------------------------------------------------------------

func TestSaveAdminKeyLocally_WritesUnderConfigDir(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	path, err := saveAdminKeyLocally([]byte("key-material"))
	if err != nil {
		t.Fatalf("saveAdminKeyLocally: %v", err)
	}
	if !strings.HasPrefix(path, filepath.Join(configDir, "qrdb")) {
		t.Errorf("path = %q, want under %q", path, filepath.Join(configDir, "qrdb"))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat saved key file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("saved key file mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestSaveAdminKeyLocally_UniquePathsPerCall(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	path1, err := saveAdminKeyLocally([]byte("key-1"))
	if err != nil {
		t.Fatalf("saveAdminKeyLocally: %v", err)
	}
	path2, err := saveAdminKeyLocally([]byte("key-2"))
	if err != nil {
		t.Fatalf("saveAdminKeyLocally: %v", err)
	}
	if path1 == path2 {
		t.Errorf("expected distinct paths across calls, got the same: %q", path1)
	}
}

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// stubCeremonyServicer is a minimal service.CeremonyServicer implementing
// CeremonyStatus and CeremonySignCSR directly: capturingCeremonyService
// (init_officers_cmd_test.go) overrides only CeremonyInitialize and embeds a
// nil service.CeremonyServicer, which panics if either of the other two
// methods is ever reached through it.
type stubCeremonyServicer struct {
	service.CeremonyServicer
	statusErr error
	csrErr    error
}

func (s *stubCeremonyServicer) CeremonyStatus(context.Context) (*service.CeremonyStatusInfo, error) {
	if s.statusErr != nil {
		return nil, s.statusErr
	}
	return &service.CeremonyStatusInfo{State: "operational", SPKIPin: "pin"}, nil
}

func (s *stubCeremonyServicer) CeremonySignCSR(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) {
	if s.csrErr != nil {
		return nil, s.csrErr
	}
	return &service.CeremonySignCSRResult{CertPEM: []byte("-----BEGIN CERTIFICATE-----\ncert\n-----END CERTIFICATE-----")}, nil
}

// capturingGeneratingCeremonyService is a service.CeremonyServicer whose
// CeremonyInitialize always reports the admin-generate path with a fixed
// key, for exercising CeremonyBinding's local-save behavior deterministically.
type capturingGeneratingCeremonyService struct {
	service.CeremonyServicer
	adminKeyPEM []byte
}

func (m *capturingGeneratingCeremonyService) CeremonyInitialize(_ context.Context, _ ceremony.CeremonyCA, _ *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	return &service.CeremonyInitResult{
		State:                 "operational",
		AdminCertPEM:          []byte("-----BEGIN CERTIFICATE-----\nadmin\n-----END CERTIFICATE-----"),
		AdminKeyPEM:           m.adminKeyPEM,
		AdminCredentialSource: string(ceremony.AdminCredentialGenerated),
	}, nil
}

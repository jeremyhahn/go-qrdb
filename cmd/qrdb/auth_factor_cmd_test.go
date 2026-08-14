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

// auth_factor_cmd_test.go covers the user-side AuthFactor CLI surface
// (`qrdb auth login` and `qrdb auth factor list/add/remove/rotate/
// relabel`) wired against the M14 UserAuthFactorClient interface.
package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Mock UserAuthFactorClient
// ---------------------------------------------------------------------------

// mockUserAuthFactorTransport embeds the standard mock auth transport
// and satisfies transport.UserAuthFactorClient with a process-local,
// in-memory factor store. It implements the M14 surface (Add / List /
// Get / Remove / Rotate / Relabel / Login).
type mockUserAuthFactorTransport struct {
	*embedded.Transport
	mu      sync.Mutex
	nextID  int
	factors map[string]*transport.AuthFactorRecord
}

// newMockUserAuthFactorTransport constructs a mock transport with an
// empty factor store.
func newMockUserAuthFactorTransport() (*mockUserAuthFactorTransport, error) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	base, err := embedded.New(kvSvc, adminSvc)
	if err != nil {
		return nil, err
	}
	return &mockUserAuthFactorTransport{
		Transport: base,
		factors:   make(map[string]*transport.AuthFactorRecord),
	}, nil
}

// AddAuthFactor stores a new factor record.
func (m *mockUserAuthFactorTransport) AddAuthFactor(_ context.Context, req *transport.AuthFactorAddRequest) (*transport.AuthFactorAddResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	id := "factor-mock-" + intToString(m.nextID)
	rec := &transport.AuthFactorRecord{
		ID:           id,
		UserID:       req.UserID,
		Type:         req.Type,
		Label:        req.Label,
		Credential:   append([]byte(nil), req.Credential...),
		Metadata:     copyMetadata(req.Metadata),
		RegisteredAt: time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
		CreatedBy:    req.CreatedBy,
	}
	m.factors[id] = rec
	return &transport.AuthFactorAddResponse{Factor: *rec}, nil
}

// ListAuthFactors returns every factor for a user.
func (m *mockUserAuthFactorTransport) ListAuthFactors(_ context.Context, req *transport.AuthFactorListRequest) (*transport.AuthFactorListResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]transport.AuthFactorRecord, 0)
	for _, f := range m.factors {
		if f.UserID == req.UserID {
			out = append(out, *f)
		}
	}
	return &transport.AuthFactorListResponse{Factors: out, Total: len(out)}, nil
}

// GetAuthFactor returns a factor by id.
func (m *mockUserAuthFactorTransport) GetAuthFactor(_ context.Context, req *transport.AuthFactorGetRequest) (*transport.AuthFactorGetResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.factors[req.FactorID]
	if !ok {
		return nil, errors.New("factor not found: " + req.FactorID)
	}
	return &transport.AuthFactorGetResponse{Factor: *rec}, nil
}

// RemoveAuthFactor soft-deletes a factor.
func (m *mockUserAuthFactorTransport) RemoveAuthFactor(_ context.Context, req *transport.AuthFactorRemoveRequest) (*transport.AuthFactorRemoveResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.factors[req.FactorID]
	if !ok {
		return nil, errors.New("factor not found: " + req.FactorID)
	}
	now := time.Date(2026, 4, 25, 1, 0, 0, 0, time.UTC)
	rec.RevokedAt = &now
	return &transport.AuthFactorRemoveResponse{Status: "revoked"}, nil
}

// RotateAuthFactor revokes the old factor and adds a new one.
func (m *mockUserAuthFactorTransport) RotateAuthFactor(_ context.Context, req *transport.AuthFactorRotateRequest) (*transport.AuthFactorRotateResponse, error) {
	m.mu.Lock()
	old, ok := m.factors[req.OldFactorID]
	if !ok {
		m.mu.Unlock()
		return nil, errors.New("factor not found: " + req.OldFactorID)
	}
	now := time.Date(2026, 4, 25, 2, 0, 0, 0, time.UTC)
	old.RevokedAt = &now
	userID := req.NewFactor.UserID
	if userID == 0 {
		userID = old.UserID
	}
	m.nextID++
	id := "factor-mock-" + intToString(m.nextID)
	rec := &transport.AuthFactorRecord{
		ID:           id,
		UserID:       userID,
		Type:         req.NewFactor.Type,
		Label:        req.NewFactor.Label,
		Credential:   append([]byte(nil), req.NewFactor.Credential...),
		Metadata:     copyMetadata(req.NewFactor.Metadata),
		RegisteredAt: time.Date(2026, 4, 25, 2, 0, 0, 0, time.UTC),
		CreatedBy:    req.NewFactor.CreatedBy,
	}
	m.factors[id] = rec
	m.mu.Unlock()
	return &transport.AuthFactorRotateResponse{NewFactor: *rec}, nil
}

// RelabelAuthFactor renames a factor.
func (m *mockUserAuthFactorTransport) RelabelAuthFactor(_ context.Context, req *transport.AuthFactorRelabelRequest) (*transport.AuthFactorRelabelResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.factors[req.FactorID]
	if !ok {
		return nil, errors.New("factor not found: " + req.FactorID)
	}
	rec.Label = req.Label
	return &transport.AuthFactorRelabelResponse{Factor: *rec}, nil
}

// LoginAuthFactor resolves a (Type, LookupKey) to a user record. The
// mock matches the credential bytes verbatim.
func (m *mockUserAuthFactorTransport) LoginAuthFactor(_ context.Context, req *transport.AuthFactorLoginRequest) (*transport.AuthFactorLoginResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.factors {
		if f.Type != req.Type {
			continue
		}
		if string(f.Credential) != string(req.LookupKey) {
			continue
		}
		if f.RevokedAt != nil {
			return nil, errors.New("factor revoked")
		}
		return &transport.AuthFactorLoginResponse{
			UserID:   f.UserID,
			Username: "user-" + intToString(int(f.UserID)),
			Active:   true,
			Factor:   *f,
		}, nil
	}
	return nil, errors.New("factor not found for lookup key")
}

// copyMetadata returns a defensive copy of a string-string map.
func copyMetadata(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// intToString returns a base-10 representation of an int. Avoids
// pulling strconv just for the mock id minting.
func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ---------------------------------------------------------------------------
// Test root command wiring
// ---------------------------------------------------------------------------

// newTestRootCmdAuthFactor creates a root command wired to a mock
// transport implementing the M14 UserAuthFactorClient surface.
func newTestRootCmdAuthFactor(t *testing.T) (*cobra.Command, *mockUserAuthFactorTransport) {
	t.Helper()
	rootCmd := newRootCmd()

	mock, err := newMockUserAuthFactorTransport()
	if err != nil {
		t.Fatalf("newMockUserAuthFactorTransport: %v", err)
	}

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Name() == "version" || cmd.Name() == "help" {
			return nil
		}
		format, _ := cmd.Root().PersistentFlags().GetString("output")
		if err := validateOutputFormat(format); err != nil {
			return err
		}
		outputFormat = format

		c, err := sdk.NewClient(mock)
		if err != nil {
			return err
		}
		if err := c.Connect(cmd.Context()); err != nil {
			return err
		}
		client = c
		return nil
	}
	return rootCmd, mock
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestAuthFactorAdd_success exercises the canonical add flow against
// the M14 service.
func TestAuthFactorAdd_success(t *testing.T) {
	root, _ := newTestRootCmdAuthFactor(t)

	out, err := executeCommand(root,
		"auth", "factor", "add",
		"--user", "42",
		"--type", "fido2",
		"--label", "yubikey-work",
		"--credential-hex", "deadbeef",
	)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(out, "yubikey-work") {
		t.Fatalf("expected label in output, got: %q", out)
	}
	if !strings.Contains(out, "fido2") {
		t.Fatalf("expected type in output, got: %q", out)
	}
}

// TestAuthFactorAdd_invalidUserID asserts non-numeric --user fails
// fast with InvalidUserIDError.
func TestAuthFactorAdd_invalidUserID(t *testing.T) {
	root, _ := newTestRootCmdAuthFactor(t)

	_, err := executeCommand(root,
		"auth", "factor", "add",
		"--user", "alice",
		"--type", "fido2",
		"--label", "x",
		"--credential-hex", "00",
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var iue *InvalidUserIDError
	if !errors.As(err, &iue) {
		t.Fatalf("want *InvalidUserIDError, got %T: %v", err, err)
	}
}

// TestAuthFactorList_success exercises the canonical list flow.
func TestAuthFactorList_success(t *testing.T) {
	root, mock := newTestRootCmdAuthFactor(t)

	mock.factors["seed-1"] = &transport.AuthFactorRecord{
		ID:           "seed-1",
		UserID:       7,
		Type:         "mtls",
		Label:        "alice-mtls",
		Credential:   []byte("creda"),
		RegisteredAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	out, err := executeCommand(root, "auth", "factor", "list", "--user", "7")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "seed-1") {
		t.Fatalf("expected factor id in output, got: %q", out)
	}
	if !strings.Contains(out, "alice-mtls") {
		t.Fatalf("expected label in output, got: %q", out)
	}
}

// TestAuthFactorList_missingUser asserts list fails fast when --user
// is not supplied.
func TestAuthFactorList_missingUser(t *testing.T) {
	root, _ := newTestRootCmdAuthFactor(t)

	_, err := executeCommand(root, "auth", "factor", "list")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var mu *MissingUserError
	if !errors.As(err, &mu) {
		t.Fatalf("want *MissingUserError, got %T: %v", err, err)
	}
}

// TestAuthFactorRelabel_success is the headline test: the M14
// RelabelAuthFactor RPC must be invoked and succeed against a real
// factor record.
func TestAuthFactorRelabel_success(t *testing.T) {
	root, mock := newTestRootCmdAuthFactor(t)

	mock.factors["abc"] = &transport.AuthFactorRecord{
		ID:           "abc",
		UserID:       1,
		Type:         "mtls",
		Label:        "old-label",
		Credential:   []byte("k"),
		RegisteredAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	out, err := executeCommand(root, "auth", "factor", "relabel", "abc", "--label", "new-label")
	if err != nil {
		t.Fatalf("relabel: %v", err)
	}
	if !strings.Contains(out, "new-label") {
		t.Fatalf("expected new-label in output, got: %q", out)
	}
	if got := mock.factors["abc"].Label; got != "new-label" {
		t.Fatalf("mock factor label = %q, want new-label", got)
	}
}

// TestAuthFactorRelabel_missingFactorID asserts the handler rejects a
// missing positional argument.
func TestAuthFactorRelabel_missingFactorID(t *testing.T) {
	root, _ := newTestRootCmdAuthFactor(t)

	_, err := executeCommand(root, "auth", "factor", "relabel", "--label", "x")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var mfid *MissingFactorIDError
	if !errors.As(err, &mfid) {
		t.Fatalf("want *MissingFactorIDError, got %T: %v", err, err)
	}
}

// TestAuthFactorRelabel_missingLabel asserts the handler rejects a
// missing --label flag.
func TestAuthFactorRelabel_missingLabel(t *testing.T) {
	root, _ := newTestRootCmdAuthFactor(t)

	_, err := executeCommand(root, "auth", "factor", "relabel", "abc")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ml *MissingLabelError
	if !errors.As(err, &ml) {
		t.Fatalf("want *MissingLabelError, got %T: %v", err, err)
	}
}

// TestAuthFactorRemove_success exercises the canonical remove flow.
func TestAuthFactorRemove_success(t *testing.T) {
	root, mock := newTestRootCmdAuthFactor(t)

	mock.factors["rm-1"] = &transport.AuthFactorRecord{
		ID:           "rm-1",
		UserID:       1,
		Type:         "mtls",
		Credential:   []byte("k"),
		RegisteredAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	if _, err := executeCommand(root, "auth", "factor", "remove", "rm-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if mock.factors["rm-1"].RevokedAt == nil {
		t.Fatal("expected RevokedAt to be set")
	}
}

// TestAuthFactorRotate_success exercises the canonical rotate flow.
func TestAuthFactorRotate_success(t *testing.T) {
	root, mock := newTestRootCmdAuthFactor(t)

	mock.factors["old"] = &transport.AuthFactorRecord{
		ID:           "old",
		UserID:       9,
		Type:         "fido2",
		Credential:   []byte("oldcred"),
		RegisteredAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	out, err := executeCommand(root,
		"auth", "factor", "rotate", "old",
		"--type", "fido2",
		"--label", "rotated",
		"--credential-hex", "cafebabe",
	)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if !strings.Contains(out, "Old factor revoked") {
		t.Fatalf("expected rotation banner, got: %q", out)
	}
	if mock.factors["old"].RevokedAt == nil {
		t.Fatal("expected old factor RevokedAt to be set")
	}
	// New factor should be present and inherit user 9.
	found := false
	for id, f := range mock.factors {
		if id == "old" {
			continue
		}
		if f.UserID == 9 && f.Label == "rotated" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected new factor with label rotated and userID 9")
	}
}

// TestAuthLogin_fido2_success exercises the M14 LoginAuthFactor RPC
// for fido2 with a hex-encoded credential lookup key.
func TestAuthLogin_fido2_success(t *testing.T) {
	root, mock := newTestRootCmdAuthFactor(t)

	mock.factors["login-1"] = &transport.AuthFactorRecord{
		ID:           "login-1",
		UserID:       77,
		Type:         "fido2",
		Label:        "yk-home",
		Credential:   []byte{0xde, 0xad, 0xbe, 0xef},
		RegisteredAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	out, err := executeCommand(root,
		"auth", "login",
		"--method", "fido2",
		"--credential-hex", "deadbeef",
	)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(out, "User ID:   77") {
		t.Fatalf("expected resolved user id 77 in output, got: %q", out)
	}
	if !strings.Contains(out, "yk-home") {
		t.Fatalf("expected factor label in output, got: %q", out)
	}
}

// TestAuthLogin_invalidMethod asserts the handler rejects a method
// outside the canonical set.
func TestAuthLogin_invalidMethod(t *testing.T) {
	root, _ := newTestRootCmdAuthFactor(t)

	_, err := executeCommand(root,
		"auth", "login",
		"--method", "smartcard",
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var iam *InvalidAuthMethodError
	if !errors.As(err, &iam) {
		t.Fatalf("want *InvalidAuthMethodError, got %T: %v", err, err)
	}
}

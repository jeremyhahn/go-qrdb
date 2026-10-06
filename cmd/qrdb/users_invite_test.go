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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Fake invitation service
// ---------------------------------------------------------------------------

// fakeInvitationService implements service.InvitationServicer with
// configurable function fields, mirroring embedded's own
// transport_invitations_test.go fakeInvitations (unexported there, so "qrdb
// users invite" and "qrdb enroll" each need their own copy).
type fakeInvitationService struct {
	inviteFn func(ctx context.Context, req *service.InviteUserRequest) (*service.Invitation, error)
	redeemFn func(ctx context.Context, req *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error)
}

func (f *fakeInvitationService) InviteUser(ctx context.Context, req *service.InviteUserRequest) (*service.Invitation, error) {
	return f.inviteFn(ctx, req)
}

func (f *fakeInvitationService) RedeemInvitation(ctx context.Context, req *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
	return f.redeemFn(ctx, req)
}

var _ service.InvitationServicer = (*fakeInvitationService)(nil)

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

// executeUsersCmd builds an embedded transport wired with the given user and
// invitation services, installs it as the package-level client, and executes
// the "users" command tree with the given args. It captures output written
// to outputWriter, mirroring executeAdminCmd (admin_test.go).
func executeUsersCmd(t *testing.T, userSvc *mockUserService, invSvc service.InvitationServicer, args ...string) (string, error) {
	t.Helper()

	opts := []embedded.Option{embedded.WithUserService(userSvc)}
	if invSvc != nil {
		opts = append(opts, embedded.WithInvitations(invSvc))
	}

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(), opts...)
	if err != nil {
		t.Fatalf("embedded.New() error: %v", err)
	}

	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient() error: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	oldWriter := outputWriter
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
	})

	client = sdkClient
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newUsersCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(context.Background())
	return buf.String(), execErr
}

// ---------------------------------------------------------------------------
// users invite tests
// ---------------------------------------------------------------------------

func TestUsersInvite_Table(t *testing.T) {
	expires := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	var gotReq *service.InviteUserRequest
	invSvc := &fakeInvitationService{
		inviteFn: func(_ context.Context, req *service.InviteUserRequest) (*service.Invitation, error) {
			gotReq = req
			return &service.Invitation{
				ID:        42,
				Token:     "one-time-token",
				ExpiresAt: expires,
				UserID:    7,
				Username:  req.Username,
				TenantID:  req.TenantID,
				Created:   true,
			}, nil
		},
	}

	out, err := executeUsersCmd(t, &mockUserService{}, invSvc,
		"invite", "--username", "bob", "--email", "bob@example.com", "--roles", "role-1, role-2",
	)
	if err != nil {
		t.Fatalf("users invite error: %v", err)
	}

	if gotReq.Username != "bob" || gotReq.Email != "bob@example.com" {
		t.Errorf("request = %+v, want username=bob email=bob@example.com", gotReq)
	}
	if len(gotReq.RoleIDs) != 2 || gotReq.RoleIDs[0] != "role-1" || gotReq.RoleIDs[1] != "role-2" {
		t.Errorf("RoleIDs = %v, want [role-1 role-2] (comma-split and trimmed)", gotReq.RoleIDs)
	}

	for _, want := range []string{
		"ID:        42",
		"Token:     one-time-token",
		"ExpiresAt: " + expires.Format(time.RFC3339),
		"UserID:    7",
		"Username:  bob",
		"Created:   true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q, got: %q", want, out)
		}
	}
	if strings.Contains(out, "KeyPinFingerprint:") {
		t.Errorf("output should omit KeyPinFingerprint when unset, got: %q", out)
	}
}

func TestUsersInvite_KeyPinFingerprint(t *testing.T) {
	invSvc := &fakeInvitationService{
		inviteFn: func(_ context.Context, req *service.InviteUserRequest) (*service.Invitation, error) {
			return &service.Invitation{
				ID:                1,
				Token:             "tok",
				Username:          req.Username,
				KeyPinFingerprint: req.KeyPinFingerprint,
			}, nil
		},
	}

	out, err := executeUsersCmd(t, &mockUserService{}, invSvc,
		"invite", "--username", "carol", "--key-pin-fingerprint", "abc123fingerprint",
	)
	if err != nil {
		t.Fatalf("users invite error: %v", err)
	}
	if !strings.Contains(out, "KeyPinFingerprint: abc123fingerprint") {
		t.Errorf("output missing KeyPinFingerprint line, got: %q", out)
	}
}

func TestUsersInvite_JSON(t *testing.T) {
	invSvc := &fakeInvitationService{
		inviteFn: func(_ context.Context, req *service.InviteUserRequest) (*service.Invitation, error) {
			return &service.Invitation{ID: 9, Token: "jsontok", Username: req.Username}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	out, err := executeUsersCmd(t, &mockUserService{}, invSvc, "invite", "--username", "dave")
	if err != nil {
		t.Fatalf("users invite json error: %v", err)
	}

	var inv transport.Invitation
	if jsonErr := json.Unmarshal([]byte(out), &inv); jsonErr != nil {
		t.Fatalf("JSON unmarshal error: %v, output: %q", jsonErr, out)
	}
	if inv.Token != "jsontok" || inv.ID != 9 {
		t.Errorf("invitation = %+v, want token=jsontok id=9", inv)
	}
}

func TestUsersInvite_YAML(t *testing.T) {
	invSvc := &fakeInvitationService{
		inviteFn: func(_ context.Context, req *service.InviteUserRequest) (*service.Invitation, error) {
			return &service.Invitation{ID: 3, Token: "yamltok", Username: req.Username}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "yaml"
	t.Cleanup(func() { outputFormat = oldFormat })

	out, err := executeUsersCmd(t, &mockUserService{}, invSvc, "invite", "--username", "erin")
	if err != nil {
		t.Fatalf("users invite yaml error: %v", err)
	}
	if !strings.Contains(out, "yamltok") {
		t.Errorf("output missing yamltok, got: %q", out)
	}
}

func TestUsersInvite_MissingUsername(t *testing.T) {
	_, err := executeUsersCmd(t, &mockUserService{}, &fakeInvitationService{
		inviteFn: func(context.Context, *service.InviteUserRequest) (*service.Invitation, error) {
			t.Fatal("InviteUser should not be called without --username")
			return nil, nil
		},
	}, "invite")
	if err == nil {
		t.Fatal("expected error for missing --username")
	}
	if !strings.Contains(err.Error(), "username") {
		t.Errorf("error should mention username, got: %v", err)
	}
}

func TestUsersInvite_ServiceError(t *testing.T) {
	invSvc := &fakeInvitationService{
		inviteFn: func(context.Context, *service.InviteUserRequest) (*service.Invitation, error) {
			return nil, errors.New("invitation ttl exceeded")
		},
	}

	_, err := executeUsersCmd(t, &mockUserService{}, invSvc, "invite", "--username", "frank")
	if err == nil {
		t.Fatal("expected service error to propagate")
	}
	if !strings.Contains(err.Error(), "invitation ttl exceeded") {
		t.Errorf("error = %v", err)
	}
}

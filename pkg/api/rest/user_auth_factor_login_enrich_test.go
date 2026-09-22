// Copyright 2026 Jeremy Hahn
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

package rest

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestResolveUserAuthFactorLogin_Enriches verifies the REST login resolver
// populates username, tenant, roles, and enabled state from the user service.
func TestResolveUserAuthFactorLogin_Enriches(t *testing.T) {
	factorSvc := &mockUserAuthFactorService{
		lookupByCredentialFn: func(_ context.Context, ty auth.AuthFactorType, _ []byte) (*auth.AuthFactor, error) {
			return &auth.AuthFactor{ID: "factor-1", UserID: 42, Type: ty}, nil
		},
	}
	var gotID uint64
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			gotID = id
			return &user.User{
				ID:        42,
				Username:  "alice",
				TenantID:  "acme",
				RoleIDs:   []string{"admin", "editor"},
				Lifecycle: user.LifecycleActive,
			}, nil
		},
	}
	s := &Server{userAuthFactorSvc: factorSvc, userSvc: userSvc}

	resp, err := s.resolveUserAuthFactorLogin(context.Background(), &transport.AuthFactorLoginRequest{
		Type:      "totp",
		LookupKey: []byte("key"),
	})
	if err != nil {
		t.Fatalf("resolveUserAuthFactorLogin: %v", err)
	}
	if resp.UserID != 42 {
		t.Errorf("UserID: got %d, want 42", resp.UserID)
	}
	if resp.Username != "alice" {
		t.Errorf("Username: got %q, want %q", resp.Username, "alice")
	}
	if resp.TenantID != "acme" {
		t.Errorf("TenantID: got %q, want %q", resp.TenantID, "acme")
	}
	if len(resp.Roles) != 2 || resp.Roles[0] != "admin" {
		t.Errorf("Roles: got %v, want [admin editor]", resp.Roles)
	}
	if !resp.Active {
		t.Error("Active: got false, want true")
	}
	if gotID != 42 {
		t.Errorf("GetUser called with id %d, want 42", gotID)
	}
}

// TestResolveUserAuthFactorLogin_NoUserService verifies the resolver returns
// the base identifiers when no user service is wired.
func TestResolveUserAuthFactorLogin_NoUserService(t *testing.T) {
	factorSvc := &mockUserAuthFactorService{
		lookupByCredentialFn: func(_ context.Context, ty auth.AuthFactorType, _ []byte) (*auth.AuthFactor, error) {
			return &auth.AuthFactor{ID: "factor-1", UserID: 7, Type: ty}, nil
		},
	}
	s := &Server{userAuthFactorSvc: factorSvc}

	resp, err := s.resolveUserAuthFactorLogin(context.Background(), &transport.AuthFactorLoginRequest{
		Type:      "totp",
		LookupKey: []byte("key"),
	})
	if err != nil {
		t.Fatalf("resolveUserAuthFactorLogin: %v", err)
	}
	if resp.UserID != 7 {
		t.Errorf("UserID: got %d, want 7", resp.UserID)
	}
	if resp.Username != "" {
		t.Errorf("Username: got %q, want empty", resp.Username)
	}
}

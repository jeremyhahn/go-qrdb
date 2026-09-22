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

package quic

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// loginFactorSvc is a UserAuthFactorServicer whose lookup returns a real
// factor so the login enrichment path can be exercised.
type loginFactorSvc struct{}

var _ service.UserAuthFactorServicer = (*loginFactorSvc)(nil)

func (loginFactorSvc) Add(_ context.Context, _ uint64, _ *auth.AuthFactor) error { return nil }
func (loginFactorSvc) List(_ context.Context, _ uint64) ([]*auth.AuthFactor, error) {
	return nil, nil
}
func (loginFactorSvc) Get(_ context.Context, _ string) (*auth.AuthFactor, error) { return nil, nil }
func (loginFactorSvc) LookupByCredentialKey(_ context.Context, ty auth.AuthFactorType, _ []byte) (*auth.AuthFactor, error) {
	return &auth.AuthFactor{ID: "factor-1", UserID: 42, Type: ty}, nil
}
func (loginFactorSvc) Remove(_ context.Context, _ string, _ bool) error { return nil }
func (loginFactorSvc) Rotate(_ context.Context, _ string, _ *auth.AuthFactor) error {
	return nil
}
func (loginFactorSvc) Relabel(_ context.Context, _ string, _ string) error     { return nil }
func (loginFactorSvc) MarkUsed(_ context.Context, _ string, _ time.Time) error { return nil }

func TestQUICUserAuthFactorLogin_Enriches(t *testing.T) {
	userSvc := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			if id != 42 {
				t.Fatalf("GetUser id = %d, want 42", id)
			}
			return &user.User{
				ID:        42,
				Username:  "alice",
				TenantID:  "acme",
				RoleIDs:   []string{"admin"},
				Lifecycle: user.LifecycleActive,
			}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0",
		WithUserAuthFactorService(loginFactorSvc{}),
		WithUserService(userSvc),
	)

	payload, _ := json.Marshal(quicUserFactorLoginRequest{Type: "totp", LookupKey: []byte("key")})
	out, err := srv.handleUserAuthFactorLogin(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleUserAuthFactorLogin: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["username"] != "alice" {
		t.Errorf("username: got %v, want alice", resp["username"])
	}
	if resp["tenant_id"] != "acme" {
		t.Errorf("tenant_id: got %v, want acme", resp["tenant_id"])
	}
	roles, ok := resp["roles"].([]any)
	if !ok || len(roles) != 1 || roles[0] != "admin" {
		t.Errorf("roles: got %v, want [admin]", resp["roles"])
	}
	if active, _ := resp["active"].(bool); !active {
		t.Errorf("active: got %v, want true", resp["active"])
	}
}

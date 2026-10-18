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

package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// recordingGuard records the account and tenant it was asked about, and
// answers with err.
type recordingGuard struct {
	tenantID string
	target   *user.User
	err      error
}

func (g *recordingGuard) AuthorizeCertTarget(_ context.Context, tenantID string, target *user.User) error {
	g.tenantID, g.target = tenantID, target
	return g.err
}

var _ CertTargetGuard = (*recordingGuard)(nil)

func bindingMW(g CertTargetGuard) *AuthMiddleware {
	opts := []Option{}
	if g != nil {
		opts = append(opts, WithCertTargetGuard(g))
	}
	return newTestMiddleware(rbac.New(&grantingRoleServicer{}), opts...)
}

func TestAuthorizeCertBindingTarget_AsksTheGuardAboutTheRecordedAccount(t *testing.T) {
	g := &recordingGuard{}
	users := &userTenantFixture{u: &user.User{ID: 7, Username: "sam", TenantID: "acme"}}
	if err := bindingMW(g).AuthorizeCertBindingTarget(context.Background(), users, 7); err != nil {
		t.Fatalf("admitted target: %v", err)
	}
	if g.tenantID != "acme" || g.target == nil || g.target.Username != "sam" {
		t.Fatalf("guard asked about %+v in %q; want sam in acme, the tenant read from the record", g.target, g.tenantID)
	}
	refused := errors.New("ceremony-only")
	g.err = refused
	if err := bindingMW(g).AuthorizeCertBindingTarget(context.Background(), users, 7); !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the guard's refusal", err)
	}
}

func TestAuthorizeCertBindingTarget_FailsClosed(t *testing.T) {
	users := &userTenantFixture{u: &user.User{ID: 7, Username: "sam", TenantID: "acme"}}
	var required *CertTargetGuardRequiredError
	if err := bindingMW(nil).AuthorizeCertBindingTarget(context.Background(), users, 7); !errors.As(err, &required) {
		t.Fatalf("no guard: %v", err)
	}
	if required.Error() == "" {
		t.Fatal("empty message")
	}
	boom := errors.New("store down")
	if err := bindingMW(&recordingGuard{}).AuthorizeCertBindingTarget(context.Background(), &userTenantFixture{err: boom}, 7); !errors.Is(err, boom) {
		t.Fatalf("GetUser error: %v", err)
	}
	var notFound *user.UserNotFoundError
	if err := bindingMW(&recordingGuard{}).AuthorizeCertBindingTarget(context.Background(), &userTenantFixture{}, 7); !errors.As(err, &notFound) {
		t.Fatalf("nil record: %v", err)
	}
	var nilMW *AuthMiddleware
	if err := nilMW.AuthorizeCertBindingTarget(context.Background(), users, 7); err != nil {
		t.Fatalf("nil middleware: %v", err)
	}
}

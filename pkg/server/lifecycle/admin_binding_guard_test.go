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

package lifecycle_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"log/slog"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

func spkiThumbprint(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "admin"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return user.ComputeSPKIThumbprint(c)
}

func buildWithPolicy(t *testing.T, policy string) (*lifecycle.Node, error) {
	t.Helper()
	host := newLocalHost()
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host: host, WatchHub: hub, DataDir: t.TempDir(), MTLSBindingPolicy: policy,
	})
	if node != nil {
		t.Cleanup(func() { _ = node.Stop(context.Background()) })
	}
	return node, err
}

// withdrawAdminsOnlyBinding enrols one binding on the bootstrap admin through
// the user service BuildServices wired, then withdraws it.
func withdrawAdminsOnlyBinding(t *testing.T, node *lifecycle.Node) error {
	t.Helper()
	ctx := context.Background()
	admin, err := node.User.CreateUser(ctx, &user.CreateUserRequest{
		Username: user.BootstrapAdminUsername, TenantID: tenantid.System, WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	tp := spkiThumbprint(t)
	if _, err := node.User.AddCertBinding(ctx, admin.ID, tp, 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	_, err = node.User.RemoveCertBinding(ctx, admin.ID, tp)
	return err
}

// TestBuildServices_RequiredPolicyGuardsTheAdminsLastBinding (go-qrdb#517
// review N1): the policy reaches the user service every transport uses.
func TestBuildServices_RequiredPolicyGuardsTheAdminsLastBinding(t *testing.T) {
	node, err := buildWithPolicy(t, auth.MTLSBindingRequired.String())
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	var refused *user.BootstrapAdminLockoutError
	if err := withdrawAdminsOnlyBinding(t, node); !errors.As(err, &refused) || refused.Reason != user.AdminLockoutNoLiveBinding {
		t.Fatalf("withdrawal = %v, want *user.BootstrapAdminLockoutError(no_live_binding)", err)
	}
	// The same service refuses the other ways to lock the admin out.
	ctx := context.Background()
	admin, err := node.User.GetUserByUsername(ctx, tenantid.System, user.BootstrapAdminUsername)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if err := node.User.DeleteUser(ctx, admin.ID); !errors.As(err, &refused) || refused.Reason != user.AdminLockoutDelete {
		t.Fatalf("DeleteUser(admin) = %v, want *user.BootstrapAdminLockoutError(delete)", err)
	}
}

func TestBuildServices_EnrolledPolicyLeavesWithdrawalOpen(t *testing.T) {
	node, err := buildWithPolicy(t, auth.MTLSBindingEnrolled.String())
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	if err := withdrawAdminsOnlyBinding(t, node); err != nil {
		t.Fatalf("withdrawal under the enrolled opt-out = %v, want nil", err)
	}
}

func TestBuildServices_UnknownBindingPolicyFails(t *testing.T) {
	_, err := buildWithPolicy(t, "requierd")
	var inv *auth.InvalidMTLSBindingPolicyError
	if !errors.As(err, &inv) {
		t.Fatalf("BuildServices = %v, want *auth.InvalidMTLSBindingPolicyError", err)
	}
}

func TestUserStoreOptions(t *testing.T) {
	// Every policy sets the store's logger; "required" adds the admin guard.
	// The empty string resolves to "required" (go-qrdb#548's zero value), so
	// it carries the guard too.
	for policy, want := range map[string]int{"": 2, "enrolled": 1, "required": 2} {
		opts, err := lifecycle.UserStoreOptions(policy, slog.Default())
		if err != nil || len(opts) != want {
			t.Fatalf("UserStoreOptions(%q) = (%d options, %v), want %d", policy, len(opts), err, want)
		}
	}
	if _, err := lifecycle.UserStoreOptions("strict", slog.Default()); err == nil {
		t.Fatal("an unknown policy produced options")
	}
}

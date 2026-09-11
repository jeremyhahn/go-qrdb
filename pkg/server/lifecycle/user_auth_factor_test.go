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

package lifecycle_test

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestBuildServices_UserAuthFactorIsTenantRouted asserts the node lifecycle
// wires a TENANT-ROUTED user auth-factor service, exercised through the
// boundary rather than through its type: a factor enrolled under one tenant
// must not resolve a lookup made under another (go-qrdb#393).
//
// This runs the seal-DISABLED branch, which is the one that used to leave the
// factor service on the raw shared store. Keyspace isolation is not a property
// of encryption: a node with the seal subsystem off still has tenants.
func TestBuildServices_UserAuthFactorIsTenantRouted(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.StoreResolver == nil {
		t.Fatal("StoreResolver is nil; IAM is not routed on this node")
	}
	svc := node.UserAuthFactor
	if svc == nil {
		t.Fatal("UserAuthFactor is nil while StoreResolver is not; the login surface lost its store")
	}

	ctx := context.Background()
	if _, err := node.Tenant.CreateTenant(ctx, "acme", "acme", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant(acme): %v", err)
	}
	if _, err := node.Tenant.CreateTenant(ctx, "globex", "globex", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant(globex): %v", err)
	}

	cred := []byte("lifecycle-credential")
	acme := tenant.WithTenantID(ctx, "acme")
	globex := tenant.WithTenantID(ctx, "globex")
	if err := svc.Add(acme, 1, &auth.AuthFactor{
		ID: "lifecycle-factor", UserID: 1, Type: auth.AuthFactorMTLS, Credential: cred,
	}); err != nil {
		t.Fatalf("Add under acme: %v", err)
	}
	if _, err := svc.LookupByCredentialKey(globex, auth.AuthFactorMTLS, cred); err == nil {
		t.Fatal("globex resolved a factor enrolled under acme")
	}
	got, err := svc.LookupByCredentialKey(acme, auth.AuthFactorMTLS, cred)
	if err != nil {
		t.Fatalf("acme cannot resolve its own factor: %v", err)
	}
	if got.ID != "lifecycle-factor" {
		t.Fatalf("resolved %q", got.ID)
	}
}

// TestBuildServices_UserAuthFactorServesEveryRoutedNode asserts the service is
// wired wherever the resolver is, so no composition gets IAM routing with an
// unrouted login surface beside it.
func TestBuildServices_UserAuthFactorServesEveryRoutedNode(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if (node.StoreResolver == nil) != (node.UserAuthFactor == nil) {
		t.Fatalf("StoreResolver and UserAuthFactor disagree: resolver=%v factors=%v",
			node.StoreResolver != nil, node.UserAuthFactor != nil)
	}
}

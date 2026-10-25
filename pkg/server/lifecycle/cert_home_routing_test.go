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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// strictCATenant and inheritCATenant are raft tenants, one strict and one on
// the cluster's operator-access floor; sharedCATenant is a prefix tenant.
const (
	strictCATenant  = "strict-ca"
	inheritCATenant = "inherit-ca"
	sharedCATenant  = "shared-ca"
)

// buildStrictCANode builds a node whose tenants may take raft isolation and
// creates the strict, the inheriting and the shared tenant on it.
func buildStrictCANode(t *testing.T) *lifecycle.Node {
	t.Helper()
	host := newLocalHost()
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	barrier, _, credSvc := newTestBarrier(t)
	registry := newPooledRegistry(t, barrier)

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:               host,
		WatchHub:           hub,
		DataDir:            t.TempDir(),
		SealBarrier:        barrier,
		BarrierRegistry:    registry,
		CredentialCoreSvc:  credSvc,
		ClusterTrustDomain: "cluster.example.com",
		DefaultTenantEntitlements: &quota.Entitlements{
			AllowedIsolationModes: []string{quota.IsolationModePrefix, quota.IsolationModeRaft},
			AllowDedicatedRaft:    true,
			AllowedVolumePools:    []string{"default"},
		},
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	t.Cleanup(func() { _ = node.Stop(context.Background()) })
	if node.CertProv == nil {
		t.Fatal("CertProv is nil")
	}

	ctx := context.Background()
	for id, opts := range map[string][]tenant.CreateTenantOption{
		strictCATenant:  {tenant.WithIsolationPolicy(tenant.TenantIsolationPolicyStrict)},
		inheritCATenant: nil,
	} {
		if _, err := node.Tenant.CreateTenant(ctx, id, id, tenant.IsolationRaft, opts...); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
		if err := registry.InitializeTenant(ctx, id, seal.Credentials{}); err != nil {
			t.Fatalf("InitializeTenant(%s): %v", id, err)
		}
	}
	if _, err := node.Tenant.CreateTenant(ctx, sharedCATenant, sharedCATenant, tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant(%s): %v", sharedCATenant, err)
	}
	return node
}

// issueOnNode asks the node's certificate provider to sign a client
// certificate for tenantID.
func issueOnNode(t *testing.T, prov cert.CertProvider, tenantID string) error {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, err = prov.IssueClientCert(tenant.WithTenantID(context.Background(), tenantID), &cert.IssueRequest{
		Username:  "alice",
		TenantID:  tenantID,
		PublicKey: &key.PublicKey,
		TTL:       time.Hour,
	})
	return err
}

// downHomeSource is a tenant.HomeStoreSource whose home store cannot be
// reached.
type downHomeSource struct{ err error }

func (d downHomeSource) EnsureHome(context.Context, string) error { return nil }

func (d downHomeSource) HomeStore(context.Context, string) (kvstore.KVStore, error) {
	return nil, d.err
}

var _ tenant.HomeStoreSource = downHomeSource{}

// TestBuildServices_TenantCARoutesThroughTheStoreResolver: the node's
// certificate provider routes by the same resolver the node's IAM does. An
// isolated tenant's CA lives in its home store whatever its policy, so with no
// home attached the provider refuses to sign for it rather than mint a CA in
// the system store, while a shared tenant signs from the system store.
func TestBuildServices_TenantCARoutesThroughTheStoreResolver(t *testing.T) {
	node := buildStrictCANode(t)

	for _, id := range []string{strictCATenant, inheritCATenant} {
		if err := issueOnNode(t, node.CertProv, id); !errors.Is(err, tenant.ErrTenantSealed) {
			t.Fatalf("issue for isolated tenant %s with no home: err = %v; want ErrTenantSealed", id, err)
		}
	}
	if err := issueOnNode(t, node.CertProv, sharedCATenant); err != nil {
		t.Fatalf("issue for a shared tenant: %v", err)
	}
}

// TestBuildServices_TenantCAFollowsTheAttachedHome: the provider holds the
// resolver, not a snapshot of it, so a home source attached after
// BuildServices returns -- as every composer attaches it -- is the one the
// provider signs through.
func TestBuildServices_TenantCAFollowsTheAttachedHome(t *testing.T) {
	node := buildStrictCANode(t)
	router, ok := node.StoreResolver.(tenant.HomeRouter)
	if !ok {
		t.Fatalf("StoreResolver %T cannot route home stores", node.StoreResolver)
	}
	down := errors.New("home group unreachable")
	router.SetHomeStoreSource(downHomeSource{err: down})

	if err := issueOnNode(t, node.CertProv, strictCATenant); !errors.Is(err, down) {
		t.Fatalf("issue for a strict tenant: err = %v; want the attached home's error", err)
	}
}

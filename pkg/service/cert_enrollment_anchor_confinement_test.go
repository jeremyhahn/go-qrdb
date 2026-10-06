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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// anchorCaller is the context the auth middleware builds for subject of
// tenantID: the identity, and the tenant bound from it.
func anchorCaller(tenantID, subject string) context.Context {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: subject, TenantID: tenantID})
	return tenant.WithTenantID(ctx, tenantID)
}

func caPEM(t *testing.T, cn string) []byte {
	t.Helper()
	p, err := certtest.NewCAPEM(cn)
	if err != nil {
		t.Fatalf("NewCAPEM: %v", err)
	}
	return p
}

// TestCertEnrollmentService_TrustAnchorsAreConfinedToTheCallersTenant: an
// anchor decides which certificates log in to its tenant, so the service --
// the one place every transport reaches -- refuses a caller acting on another
// tenant's anchors, whatever gate the transport did or did not apply first.
func TestCertEnrollmentService_TrustAnchorsAreConfinedToTheCallersTenant(t *testing.T) {
	anchors := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	svc := NewCertEnrollmentService(nil, nil, anchors)
	seeded, err := anchors.Add(context.Background(), "globex", caPEM(t, "globex-root"))
	if err != nil {
		t.Fatalf("seed globex: %v", err)
	}

	for _, caller := range []context.Context{anchorCaller("acme", "acme-admin"), anchorCaller(tenantid.System, "platform-op")} {
		var denied *tenant.CrossTenantDeniedError
		if _, err := svc.AddTrustAnchors(caller, "globex", caPEM(t, "rogue")); !errors.As(err, &denied) {
			t.Fatalf("add to another tenant: err = %v, want *tenant.CrossTenantDeniedError", err)
		}
		if _, err := svc.ListTrustAnchors(caller, "globex"); !errors.As(err, &denied) {
			t.Fatalf("list another tenant: err = %v, want *tenant.CrossTenantDeniedError", err)
		}
		if err := svc.RemoveTrustAnchor(caller, "globex", seeded[0].Fingerprint); !errors.As(err, &denied) {
			t.Fatalf("remove from another tenant: err = %v, want *tenant.CrossTenantDeniedError", err)
		}
	}
	left, err := anchors.List(context.Background(), "globex")
	if err != nil || len(left) != 1 || left[0].Fingerprint != seeded[0].Fingerprint {
		t.Fatalf("globex's anchors after the refusals = %v, %v; want only the seeded one", left, err)
	}
}

func TestCertEnrollmentService_TrustAnchorsActOnTheCallersOwnTenant(t *testing.T) {
	anchors := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	svc := NewCertEnrollmentService(nil, nil, anchors)
	acme := anchorCaller("acme", "acme-admin")

	// Naming the own tenant and naming none are the same request.
	added, err := svc.AddTrustAnchors(acme, "acme", caPEM(t, "acme-root"))
	if err != nil || len(added) != 1 {
		t.Fatalf("add own: %v, %v", added, err)
	}
	if _, err := svc.AddTrustAnchors(acme, "", caPEM(t, "acme-second")); err != nil {
		t.Fatalf("add with no tenant named: %v", err)
	}
	listed, err := svc.ListTrustAnchors(acme, "")
	if err != nil || len(listed) != 2 {
		t.Fatalf("list own = %v, %v; want 2", listed, err)
	}
	if err := svc.RemoveTrustAnchor(acme, "acme", added[0].Fingerprint); err != nil {
		t.Fatalf("remove own: %v", err)
	}
	if listed, _ := svc.ListTrustAnchors(acme, "acme"); len(listed) != 1 {
		t.Fatalf("after remove: %v", listed)
	}
}

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

package user

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// Scenario (e): the cert-SPKI index is tenant-scoped. Two users in different
// tenants binding the SAME SPKI thumbprint must NOT collide — each resolves to
// its own identity under its own tenant, and a lookup in tenant A never
// resolves the user in tenant B.
func TestCertThumbprintIndex_TenantScoped_NoCollision(t *testing.T) {
	svc := newTestStore(t)

	ctxA := tenant.WithTenantID(t.Context(), "tenant-a")
	ctxB := tenant.WithTenantID(t.Context(), "tenant-b")

	userA, err := svc.CreateUser(ctxA, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice", TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("create userA: %v", err)
	}
	userB, err := svc.CreateUser(ctxB, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice", TenantID: "tenant-b"})
	if err != nil {
		t.Fatalf("create userB: %v", err)
	}

	// Same physical certificate / thumbprint bound in both tenants.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cert := makeCertForKey(t, key, "alice", 1)
	tp := ComputeSPKIThumbprint(cert)

	if _, err := svc.AddCertBinding(ctxA, userA.ID, tp, 0); err != nil {
		t.Fatalf("bind A: %v", err)
	}
	if _, err := svc.AddCertBinding(ctxB, userB.ID, tp, 0); err != nil {
		t.Fatalf("bind B: %v", err)
	}

	// Lookup in tenant A resolves userA.
	gotA, err := svc.UserByCertThumbprint(ctxA, tp)
	if err != nil {
		t.Fatalf("lookup A: %v", err)
	}
	if gotA.ID != userA.ID {
		t.Fatalf("tenant A lookup resolved %d, want %d", gotA.ID, userA.ID)
	}

	// Lookup in tenant B resolves userB — proving no global collision.
	gotB, err := svc.UserByCertThumbprint(ctxB, tp)
	if err != nil {
		t.Fatalf("lookup B: %v", err)
	}
	if gotB.ID != userB.ID {
		t.Fatalf("tenant B lookup resolved %d, want %d", gotB.ID, userB.ID)
	}

	if gotA.ID == gotB.ID {
		t.Fatal("tenant A and B resolved the same identity — index is not tenant-scoped")
	}
}

// A lookup in a tenant with no binding for the thumbprint must miss, even when
// another tenant has the binding.
func TestCertThumbprintIndex_WrongTenant_Misses(t *testing.T) {
	svc := newTestStore(t)
	ctxA := tenant.WithTenantID(t.Context(), "tenant-a")
	ctxC := tenant.WithTenantID(t.Context(), "tenant-c")

	userA, err := svc.CreateUser(ctxA, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice", TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cert := makeCertForKey(t, key, "alice", 1)
	tp := ComputeSPKIThumbprint(cert)
	if _, err := svc.AddCertBinding(ctxA, userA.ID, tp, 0); err != nil {
		t.Fatalf("bind: %v", err)
	}

	if _, err := svc.UserByCertThumbprint(ctxC, tp); err == nil {
		t.Fatal("expected miss in tenant-c (no binding), got resolution")
	}
}

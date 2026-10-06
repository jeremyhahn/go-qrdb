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

package embedded

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestTrustAnchors_AnotherTenantIsRefusedOverTheRealService: over the real
// enrolment service, a tenant acting on another tenant's anchors is refused
// for add, list and remove, and the other tenant's trust store is unchanged.
func TestTrustAnchors_AnotherTenantIsRefusedOverTheRealService(t *testing.T) {
	anchors := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	tr := newAuthTransport(t, WithCertEnrollment(service.NewCertEnrollmentService(nil, nil, anchors)))
	globexPEM, err := certtest.NewCAPEM("globex-root")
	if err != nil {
		t.Fatal(err)
	}
	seeded, err := anchors.Add(context.Background(), "globex", globexPEM)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	rogue, err := certtest.NewCAPEM("rogue")
	if err != nil {
		t.Fatal(err)
	}
	acme := callerCtx("acme")

	_, err = tr.AddTrustAnchors(acme, &transport.AddTrustAnchorsRequest{TenantID: "globex", CertsPEM: string(rogue)})
	assertCrossTenantDenied(t, "add to globex", err)
	_, err = tr.ListTrustAnchors(acme, "globex")
	assertCrossTenantDenied(t, "list globex", err)
	err = tr.RemoveTrustAnchor(acme, "globex", seeded[0].Fingerprint)
	assertCrossTenantDenied(t, "remove from globex", err)

	left, err := anchors.List(context.Background(), "globex")
	if err != nil || len(left) != 1 || left[0].Fingerprint != seeded[0].Fingerprint {
		t.Fatalf("globex's anchors = %v, %v; want only the seeded one", left, err)
	}
	if _, err := tr.AddTrustAnchors(acme, &transport.AddTrustAnchorsRequest{CertsPEM: string(rogue)}); err != nil {
		t.Fatalf("acme adding to its own tenant: %v", err)
	}
}

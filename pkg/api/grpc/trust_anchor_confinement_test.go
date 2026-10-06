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

package grpc

import (
	"context"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// TestGRPCTrustAnchors_AnotherTenantIsRefusedWithOrWithoutTheHandlerGate:
// add, list and remove on another tenant's anchors are refused by the
// handler's tenancy gate and, with no middleware at all, by the service.
func TestGRPCTrustAnchors_AnotherTenantIsRefusedWithOrWithoutTheHandlerGate(t *testing.T) {
	for _, withMW := range []bool{true, false} {
		f := newCertScopeGRPCFixture(t)
		if !withMW {
			f.svc.authMW = nil
		}
		seeded, err := f.anchors.Add(context.Background(), certScopeTenantB, newTestCAPEM(t, "globex-root"))
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		ctx := f.as(certScopeTenantA, "a-admin")

		_, err = f.svc.TrustAnchorAdd(ctx, &pb.TrustAnchorAddRequest{TenantId: certScopeTenantB, CertsPem: string(newTestCAPEM(t, "rogue"))})
		assertPermissionDenied(t, err, "add to tenant B")
		_, err = f.svc.TrustAnchorList(ctx, &pb.TrustAnchorListRequest{TenantId: certScopeTenantB})
		assertPermissionDenied(t, err, "list tenant B")
		_, err = f.svc.TrustAnchorRemove(ctx, &pb.TrustAnchorRemoveRequest{TenantId: certScopeTenantB, Fingerprint: seeded[0].Fingerprint})
		assertPermissionDenied(t, err, "remove from tenant B")

		left, err := f.anchors.List(context.Background(), certScopeTenantB)
		if err != nil || len(left) != 1 || left[0].Fingerprint != seeded[0].Fingerprint {
			t.Fatalf("middleware=%v: tenant B's anchors = %v, %v; want only the seeded one", withMW, left, err)
		}
	}
}

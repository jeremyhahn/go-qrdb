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

package quic

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

const barrierDelegatedPrincipal = "quic-barrier-deleg-test"

func init() {
	permission.RegisterRoleGrant(barrierDelegatedPrincipal, permission.TenantBarrierSeal, permission.TenantBarrierOfficerList)
}

// newDelegatedBarrierServer returns a QUIC server whose gate verifies a #873
// delegation for acme and audits every delegated decision, and a context
// bound to that delegation exactly as an mTLS stream presenting it would be.
func newDelegatedBarrierServer(t *testing.T) (*Server, context.Context) {
	t.Helper()
	d := authtest.NewDelegation(t, delegation.Grant{Tenant: "acme", Principal: barrierDelegatedPrincipal,
		Scope: []string{string(permission.TenantBarrierSeal), string(permission.TenantBarrierOfficerList)}})
	roles := authtest.BuiltInRoles(t, "acme", "bob-corp")
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
		authmw.WithDelegationVerifier(d.Authenticator),
		authmw.WithAuditLogger(audit.NewDecisionLogger(audit.NewRingBuffer(8))))
	ctx, _, err := mw.AuthenticateDelegation(t.Context(), []*x509.Certificate{d.Certificate})
	if err != nil {
		t.Fatalf("AuthenticateDelegation: %v", err)
	}
	return newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(&mockTenantService{}), WithAuthMiddleware(mw)), ctx
}

// delegatedBarrierCalls runs the seal and officer_list handlers for tenantID
// and returns each one's error by operation name.
func delegatedBarrierCalls(t *testing.T, srv *Server, ctx context.Context, tenantID string) map[string]error {
	t.Helper()
	seal, _ := json.Marshal(quicTenantBarrierSealRequest{TenantID: tenantID})
	list, err := proto.Marshal(&qrdbv1.TenantBarrierOfficerListRequest{TenantId: tenantID})
	if err != nil {
		t.Fatalf("marshal officer_list: %v", err)
	}
	_, sealErr := srv.handleTenantBarrierSeal(ctx, seal)
	_, listErr := srv.handleTenantBarrierOfficerList(ctx, list)
	return map[string]error{"seal": sealErr, "officer_list": listErr}
}

// TestTenantBarrierGate_Delegated_SealAndOfficerList proves a #873
// delegation for acme passes the seal and officer_list gates in acme.
func TestTenantBarrierGate_Delegated_SealAndOfficerList(t *testing.T) {
	srv, ctx := newDelegatedBarrierServer(t)
	for op, err := range delegatedBarrierCalls(t, srv, ctx, "acme") {
		if err != nil {
			t.Errorf("delegated %s in acme: %v; want the gate to pass", op, err)
		}
	}
}

// TestTenantBarrierGate_Delegated_CrossTenantDenied proves the same
// delegation is denied on another tenant's barrier.
func TestTenantBarrierGate_Delegated_CrossTenantDenied(t *testing.T) {
	srv, ctx := newDelegatedBarrierServer(t)
	for op, err := range delegatedBarrierCalls(t, srv, ctx, "bob-corp") {
		if pd := (*rbac.PermissionDeniedError)(nil); !errors.As(err, &pd) {
			t.Errorf("delegated %s in bob-corp: %v (%T); want permission denied", op, err, err)
		}
	}
}

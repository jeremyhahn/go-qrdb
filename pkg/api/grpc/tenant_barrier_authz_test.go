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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package grpc

import (
	"context"
	"crypto"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof/initprooftest"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// barrierGateRoleSvc is a minimal role.RoleServicer that resolves a fixed
// permission set for any role id. Only ResolvePermissions is exercised by the
// RBAC authorizer; the remaining methods panic if reached.
type barrierGateRoleSvc struct {
	perms []permission.Permission
}

func (s *barrierGateRoleSvc) ResolvePermissions(_ context.Context, _ []string) ([]permission.Permission, error) {
	return s.perms, nil
}
func (s *barrierGateRoleSvc) CreateRole(context.Context, *role.CreateRoleRequest) (*role.Role, error) {
	panic("unused")
}
func (s *barrierGateRoleSvc) GetRole(context.Context, string) (*role.Role, error) { panic("unused") }
func (s *barrierGateRoleSvc) GetRoleByName(context.Context, string, string) (*role.Role, error) {
	panic("unused")
}
func (s *barrierGateRoleSvc) UpdateRole(context.Context, *role.UpdateRoleRequest) (*role.Role, error) {
	panic("unused")
}
func (s *barrierGateRoleSvc) DeleteRole(context.Context, string) error { panic("unused") }
func (s *barrierGateRoleSvc) ListRoles(context.Context, *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	panic("unused")
}

// newBarrierGateServer wires a tenantBarrierServer with a real RBAC-backed
// auth middleware whose admin role carries the three tenant_barrier:*
// permissions. The barrier servicer itself is a no-op mock so the test
// isolates the authorization gate.
func newBarrierGateServer(t testing.TB, opts ...authmw.Option) *tenantBarrierServer {
	roleSvc := &barrierGateRoleSvc{perms: []permission.Permission{
		permission.TenantBarrierInit,
		permission.TenantBarrierUnseal,
		permission.TenantBarrierSeal,
	}}
	authz := rbac.New(roleSvc)
	mw := authmw.New(authz, append([]authmw.Option{authmw.WithRoleResolver(authtest.BuiltInRoles(t))}, opts...)...)
	return &tenantBarrierServer{svc: newMockTenantService(), authMW: mw}
}

// brokenAuditSink returns a decision logger over a REAL ChainFileLogger whose
// file has been closed, so the append fails at os.File.Write and the sink
// returns a real *audit.WriteError -- the same fault a full audit disk
// produces, and the reason the middleware refuses the decision.
func brokenAuditSink(t *testing.T) audit.Logger {
	t.Helper()
	chain, err := audit.NewFileLogger(filepath.Join(t.TempDir(), "audit-events.ndjson"))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	if err := chain.Close(); err != nil {
		t.Fatalf("close (the injected fault): %v", err)
	}
	return audit.NewDecisionLogger(chain)
}

// ctxWithIdentity binds an authenticated identity to the context the way the
// auth middleware would after authentication.
func ctxWithIdentity(tenantID string) context.Context {
	return auth.WithIdentity(context.Background(), &auth.Identity{
		Subject:  tenantID + "-admin",
		TenantID: tenantID,
		Roles:    []string{"admin"},
	})
}

// ctxOnConn binds tenantID's admin identity and the server end of conn to the
// context, the way the gRPC auth interceptor does for a call on conn.
func ctxOnConn(tenantID string, conn *initprooftest.Conn) context.Context {
	return auth.WithTLSState(ctxWithIdentity(tenantID), &conn.Server)
}

// TestTenantBarrierGate_TenantAdmin_OwnTenant_Allowed proves a tenant admin
// may init their OWN tenant's barrier through the handler gate: InitBegin
// issues a nonce on the connection and InitComplete accepts it signed, with
// that connection's exporter, by the key of the certificate it presented.
func TestTenantBarrierGate_TenantAdmin_OwnTenant_Allowed(t *testing.T) {
	srv := newBarrierGateServer(t)
	cert := initprooftest.Certificate(t, "acme-admin")
	conn := initprooftest.Dial(t, cert)
	ctx := ctxOnConn("acme", conn)

	begin, err := srv.InitBegin(ctx, &pb.TenantBarrierInitBeginRequest{TenantId: "acme"})
	if err != nil {
		t.Fatalf("InitBegin: %v", err)
	}
	if len(begin.GetNonce()) == 0 || begin.GetExpires() == 0 {
		t.Fatalf("InitBegin = %+v; want a nonce and its expiry", begin)
	}
	proof := conn.Prove(t, cert.PrivateKey.(crypto.Signer), "acme", begin.GetNonce())
	req := &pb.TenantBarrierInitRequest{
		TenantId: "acme", Passphrase: []byte("pw"),
		Nonce: proof.Nonce, PublicKey: proof.PublicKey, Signature: proof.Signature,
	}
	if _, err := srv.InitComplete(ctx, req); err != nil {
		t.Fatalf("tenant admin init own barrier: %v", err)
	}

	// The nonce was spent: the same request again is a replay.
	_, err = srv.InitComplete(ctx, req)
	requireCode(t, err, codes.Unauthenticated)
}

// TestTenantBarrierGate_InitCompleteWithoutProof_Unauthenticated proves an
// RBAC grant admits the caller to the gate but never past the proof: a
// complete carrying none is refused as unauthenticated.
func TestTenantBarrierGate_InitCompleteWithoutProof_Unauthenticated(t *testing.T) {
	srv := newBarrierGateServer(t)
	conn := initprooftest.Dial(t, initprooftest.Certificate(t, "acme-admin"))
	_, err := srv.InitComplete(ctxOnConn("acme", conn), &pb.TenantBarrierInitRequest{TenantId: "acme", Passphrase: []byte("pw")})
	requireCode(t, err, codes.Unauthenticated)
}

// TestTenantBarrierGate_InitCompleteOnAnotherConnection_Unauthenticated
// proves a proof made on one connection is refused on another: the exporter
// it signed is not the one the node derives there.
func TestTenantBarrierGate_InitCompleteOnAnotherConnection_Unauthenticated(t *testing.T) {
	srv := newBarrierGateServer(t)
	cert := initprooftest.Certificate(t, "acme-admin")
	first := initprooftest.Dial(t, cert)
	begin, err := srv.InitBegin(ctxOnConn("acme", first), &pb.TenantBarrierInitBeginRequest{TenantId: "acme"})
	if err != nil {
		t.Fatalf("InitBegin: %v", err)
	}
	proof := first.Prove(t, cert.PrivateKey.(crypto.Signer), "acme", begin.GetNonce())
	second := initprooftest.Dial(t, cert)
	_, err = srv.InitComplete(ctxOnConn("acme", second), &pb.TenantBarrierInitRequest{
		TenantId: "acme", Passphrase: []byte("pw"),
		Nonce: proof.Nonce, PublicKey: proof.PublicKey, Signature: proof.Signature,
	})
	requireCode(t, err, codes.Unauthenticated)
}

// TestTenantBarrierGate_InitBeginWithoutTLS_Unauthenticated proves begin
// refuses a connection with no TLS 1.3 exporter: no complete could succeed
// on it.
func TestTenantBarrierGate_InitBeginWithoutTLS_Unauthenticated(t *testing.T) {
	srv := newBarrierGateServer(t)
	_, err := srv.InitBegin(ctxWithIdentity("acme"), &pb.TenantBarrierInitBeginRequest{TenantId: "acme"})
	requireCode(t, err, codes.Unauthenticated)
}

// TestTenantBarrierGate_InitBeginCrossTenant_Denied proves begin admits only
// whom complete would: a tenant admin of acme gets no nonce for bob-corp.
func TestTenantBarrierGate_InitBeginCrossTenant_Denied(t *testing.T) {
	srv := newBarrierGateServer(t)
	conn := initprooftest.Dial(t, initprooftest.Certificate(t, "acme-admin"))
	_, err := srv.InitBegin(ctxOnConn("acme", conn), &pb.TenantBarrierInitBeginRequest{TenantId: "bob-corp"})
	requireCode(t, err, codes.PermissionDenied)
}

// TestTenantBarrierGate_TenantAdmin_CrossTenant_Denied proves the gate denies
// a tenant admin of acme initializing bob-corp's barrier.
func TestTenantBarrierGate_TenantAdmin_CrossTenant_Denied(t *testing.T) {
	srv := newBarrierGateServer(t)
	ctx := ctxWithIdentity("acme")
	req := &pb.TenantBarrierInitRequest{TenantId: "bob-corp", Passphrase: []byte("pw")}
	_, err := srv.InitComplete(ctx, req)
	if err == nil {
		t.Fatal("cross-tenant init must be denied")
	}
	if st, _ := status.FromError(err); st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %s", st.Code())
	}
}

// TestTenantBarrierGate_SystemAdmin_Denied proves the gate denies a
// system-tenant admin on any tenant barrier, even though the role grants the
// permission.
func TestTenantBarrierGate_SystemAdmin_Denied(t *testing.T) {
	srv := newBarrierGateServer(t)
	ctx := ctxWithIdentity(tenantid.System)
	for _, target := range []string{"acme", tenantid.System} {
		req := &pb.TenantBarrierInitRequest{TenantId: target, Passphrase: []byte("pw")}
		_, err := srv.InitComplete(ctx, req)
		if err == nil {
			t.Fatalf("system admin init on %q must be denied", target)
		}
		if st, _ := status.FromError(err); st.Code() != codes.PermissionDenied {
			t.Fatalf("target %q: expected PermissionDenied, got %s", target, st.Code())
		}
	}
}

// ReconcileBuiltInRoles satisfies role.RoleServicer; this double never drifts.
func (s *barrierGateRoleSvc) ReconcileBuiltInRoles(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
	return &role.ReconcileReport{TenantID: tenantID}, nil
}

// ---------------------------------------------------------------------------
// go-qrdb#335: an audit-write failure is UNAVAILABLE, never PermissionDenied
// ---------------------------------------------------------------------------

// TestTenantBarrierGate_AuditFailure_IsUnavailableNotPermissionDenied is the
// regression test for the mis-mapping that made this surface dangerous.
//
// Every RPC here hard-coded codes.PermissionDenied for whatever the gate
// returned. Once the middleware began refusing decisions it could not record,
// an operator recovering a node with a full audit disk was told by the UNSEAL
// endpoint that they were not permitted -- and PermissionDenied is terminal in
// every gRPC client library, so nothing retries it.
//
// Init and Unseal are both exercised because they take different routes:
// Init through authorizeTenantBarrierInit's trust-on-first-init fallback,
// Unseal through authorizeTenantBarrierSealedOp's sealed-window fallback. Both
// fallbacks call auditDecision again, which fails for the same reason, which
// is exactly why *AuditError escapes this surface reliably rather than rarely.
func TestTenantBarrierGate_AuditFailure_IsUnavailableNotPermissionDenied(t *testing.T) {
	srv := newBarrierGateServer(t, authmw.WithAuditLogger(brokenAuditSink(t)))
	ctx := ctxWithIdentity("acme")

	t.Run("Init", func(t *testing.T) {
		_, err := srv.InitComplete(ctx, &pb.TenantBarrierInitRequest{TenantId: "acme", Passphrase: []byte("pw")})
		requireUnavailable(t, err)
	})
	t.Run("Unseal", func(t *testing.T) {
		_, err := srv.Unseal(ctx, &pb.TenantBarrierUnsealRequest{TenantId: "acme", Key: []byte("k")})
		requireUnavailable(t, err)
	})
	t.Run("Status", func(t *testing.T) {
		_, err := srv.Status(ctx, &pb.TenantBarrierStatusRequest{TenantId: "acme"})
		requireUnavailable(t, err)
	})
	t.Run("Seal", func(t *testing.T) {
		_, err := srv.Seal(ctx, &pb.TenantBarrierSealRequest{TenantId: "acme"})
		requireUnavailable(t, err)
	})
}

// requireUnavailable asserts the RPC failed with codes.Unavailable and that the
// message says why, so an operator reading a client log learns it was the
// audit plane and not their permissions.
func requireUnavailable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("an unrecordable decision must be refused")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected a gRPC status, got %T", err)
	}
	if st.Code() != codes.Unavailable {
		t.Fatalf("code = %s, want Unavailable (PermissionDenied is terminal: no client retries it)", st.Code())
	}
	if !strings.Contains(st.Message(), "audit log failed") {
		t.Errorf("message must name the cause, got %q", st.Message())
	}
}

// TestTenantBarrierGate_OrdinaryDenialIsStillPermissionDenied is the control.
// Routing the gate through the classifier must not turn a real denial into
// something a client will retry forever.
func TestTenantBarrierGate_OrdinaryDenialIsStillPermissionDenied(t *testing.T) {
	srv := newBarrierGateServer(t)
	ctx := ctxWithIdentity("acme")
	_, err := srv.InitComplete(ctx, &pb.TenantBarrierInitRequest{TenantId: "bob-corp", Passphrase: []byte("pw")})
	if err == nil {
		t.Fatal("cross-tenant init must be denied")
	}
	if st, _ := status.FromError(err); st.Code() != codes.PermissionDenied {
		t.Fatalf("code = %s, want PermissionDenied", st.Code())
	}
}

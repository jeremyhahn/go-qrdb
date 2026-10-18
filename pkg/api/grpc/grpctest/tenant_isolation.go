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

// Package grpctest holds the tenant-isolation conformance suite for the
// transports that serve the QRDBService protobuf API -- gRPC over TCP and
// gRPC over the Unix socket. Both serve the same handlers behind the same
// interceptor chain, so one client-side suite pins both, and they cannot
// drift.
//
// The suite drives a real client connection end to end: the transport's real
// authentication and authorization chain, a real rbac.RBACAuthorizer over the
// real built-in roles, and a real in-memory tenant service. The only double is
// the authenticator (authtest.FixedAuthenticator), standing in for the mTLS
// handshake.
//
// It is imported by tests only; a production import of it is a defect.
package grpctest

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// StartFunc starts the transport under test with mw as its auth middleware,
// tenants as BOTH its tenant service and its tenant-barrier service, and
// onboarding as the service behind TenantCreate, and returns a client
// connection to it. It registers its own cleanup.
type StartFunc func(t *testing.T, mw *authmw.AuthMiddleware, tenants tenant.TenantServicer, onboarding service.TenantOnboardingServicer) *grpc.ClientConn

// fakeOnboarding adapts tenant.TenantServicer.CreateTenant to
// service.TenantOnboardingServicer for this suite, which exercises tenant
// SCOPE permission wiring on TenantCreate -- not the onboarding business
// rules (first-admin credential issuance), which pkg/service and
// pkg/bootstrap test directly against the real TenantOnboardingService. It
// ignores the admin fields entirely, so a request naming none, as every
// scenario here does, still reaches tenants.CreateTenant and the same
// ID/name validation the production handler always delegated to it.
type fakeOnboarding struct {
	tenants tenant.TenantServicer
}

func (f *fakeOnboarding) OnboardTenant(ctx context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
	info, err := f.tenants.CreateTenant(ctx, req.ID, req.Name, req.IsolationType)
	if err != nil {
		return nil, err
	}
	return &service.OnboardTenantResult{Tenant: info}, nil
}

// fixture is one started transport holding the system tenant, acme and
// globex, with an acme Admin as the caller until as() rebinds it.
type fixture struct {
	tenants *authtest.Tenants
	caller  *authtest.FixedAuthenticator
	client  pb.QRDBServiceClient
	barrier pb.TenantBarrierServiceClient
}

func newFixture(t *testing.T, start StartFunc, opts ...authmw.Option) *fixture {
	t.Helper()
	tenants := authtest.NewTenants(t, tenantid.System, "acme", "globex")
	caller := authtest.NewFixedAuthenticator(authtest.AdminIdentity("acme", "acme-admin"))
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	mw := authmw.New(rbac.New(roles), append([]authmw.Option{authmw.WithRoleResolver(roles), authmw.WithAuthenticator(caller)}, opts...)...)
	conn := start(t, mw, tenants, &fakeOnboarding{tenants: tenants})
	return &fixture{
		tenants: tenants,
		caller:  caller,
		client:  pb.NewQRDBServiceClient(conn),
		barrier: pb.NewTenantBarrierServiceClient(conn),
	}
}

func (f *fixture) as(id *auth.Identity) { f.caller.Set(id) }

func (f *fixture) operator() { f.as(authtest.AdminIdentity(tenantid.System, "root")) }

func callCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func wantCode(t *testing.T, what string, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("%s = %v (%v), want %v", what, got, err, want)
	}
}

// byID is one tenant-by-id RPC against id.
type byID struct {
	name string
	call func(ctx context.Context, c pb.QRDBServiceClient, id string) error
}

func byIDCalls() []byID {
	return []byID{
		{"get", func(ctx context.Context, c pb.QRDBServiceClient, id string) error {
			_, err := c.TenantGet(ctx, &pb.TenantGetRequest{Id: id})
			return err
		}},
		{"update", func(ctx context.Context, c pb.QRDBServiceClient, id string) error {
			_, err := c.TenantUpdate(ctx, &pb.TenantUpdateRequest{Id: id, Name: "pwned"})
			return err
		}},
		{"quotas", func(ctx context.Context, c pb.QRDBServiceClient, id string) error {
			_, err := c.TenantUpdateQuotas(ctx, &pb.TenantUpdateQuotasRequest{Id: id, Quotas: &pb.TenantQuotas{MaxDatabases: 1000}})
			return err
		}},
		{"entitlements", func(ctx context.Context, c pb.QRDBServiceClient, id string) error {
			_, err := c.TenantUpdateEntitlements(ctx, &pb.TenantUpdateEntitlementsRequest{Id: id, Entitlements: &pb.TenantEntitlements{AllowDedicatedWal: true}})
			return err
		}},
		{"delete", func(ctx context.Context, c pb.QRDBServiceClient, id string) error {
			_, err := c.TenantDelete(ctx, &pb.TenantDeleteRequest{Id: id})
			return err
		}},
	}
}

// unchanged fails unless id still exists with its original name and no plan.
func (f *fixture) unchanged(t *testing.T, id string) {
	t.Helper()
	got, err := f.tenants.GetTenant(context.Background(), id)
	if err != nil {
		t.Fatalf("%s after a refused call: %v", id, err)
	}
	if got.Name != id || got.Quotas != nil || got.Entitlements != nil || got.IsolationType != tenant.IsolationPrefix {
		t.Fatalf("a refused call changed %s: %+v", id, got)
	}
}

// TenantIsolation runs the whole suite against the transport start builds.
func TenantIsolation(t *testing.T, start StartFunc) {
	t.Run("TenantAdminCannotActOnAnotherTenant", func(t *testing.T) {
		for _, c := range byIDCalls() {
			t.Run(c.name, func(t *testing.T) {
				f := newFixture(t, start)
				wantCode(t, "acme admin "+c.name+" globex", c.call(callCtx(t), f.client, "globex"), codes.PermissionDenied)
				f.unchanged(t, "globex")
			})
		}
	})

	t.Run("TenantAdminCannotChangeItsOwnPlan", func(t *testing.T) {
		plan := map[string]func(ctx context.Context, c pb.QRDBServiceClient) error{
			"quotas": func(ctx context.Context, c pb.QRDBServiceClient) error {
				_, err := c.TenantUpdateQuotas(ctx, &pb.TenantUpdateQuotasRequest{Id: "acme", Quotas: &pb.TenantQuotas{MaxDatabases: 1000}})
				return err
			},
			"entitlements": func(ctx context.Context, c pb.QRDBServiceClient) error {
				_, err := c.TenantUpdateEntitlements(ctx, &pb.TenantUpdateEntitlementsRequest{Id: "acme", Entitlements: &pb.TenantEntitlements{AllowDedicatedWal: true}})
				return err
			},
			"isolation": func(ctx context.Context, c pb.QRDBServiceClient) error {
				_, err := c.TenantUpdate(ctx, &pb.TenantUpdateRequest{Id: "acme", IsolationType: string(tenant.IsolationBarrier)})
				return err
			},
		}
		for name, call := range plan {
			t.Run(name, func(t *testing.T) {
				f := newFixture(t, start)
				wantCode(t, "acme admin setting acme's "+name, call(callCtx(t), f.client), codes.PermissionDenied)
				f.unchanged(t, "acme")
			})
		}
	})

	t.Run("TenantAdminServedOnItsOwnTenant", func(t *testing.T) {
		f := newFixture(t, start)
		if _, err := f.client.TenantGet(callCtx(t), &pb.TenantGetRequest{Id: "acme"}); err != nil {
			t.Fatalf("acme admin get acme = %v, want nil", err)
		}
		if _, err := f.client.TenantUpdate(callCtx(t), &pb.TenantUpdateRequest{Id: "acme", Name: "Acme Corp"}); err != nil {
			t.Fatalf("acme admin renaming acme = %v, want nil", err)
		}
	})

	t.Run("PlatformOperatorSetsAPlanAndCreates", func(t *testing.T) {
		f := newFixture(t, start)
		f.operator()
		ctx := callCtx(t)
		if _, err := f.client.TenantUpdateQuotas(ctx, &pb.TenantUpdateQuotasRequest{Id: "acme", Quotas: &pb.TenantQuotas{MaxDatabases: 1000}}); err != nil {
			t.Fatalf("operator setting acme's quotas = %v, want nil", err)
		}
		if _, err := f.client.TenantUpdateEntitlements(ctx, &pb.TenantUpdateEntitlementsRequest{Id: "acme", Entitlements: &pb.TenantEntitlements{AllowDedicatedWal: true}}); err != nil {
			t.Fatalf("operator granting acme entitlements = %v, want nil", err)
		}
		if _, err := f.client.TenantCreate(ctx, &pb.TenantCreateRequest{Id: "initech", Name: "initech"}); err != nil {
			t.Fatalf("operator creating a tenant = %v, want nil", err)
		}
		got, err := f.tenants.GetTenant(context.Background(), "acme")
		if err != nil {
			t.Fatalf("GetTenant: %v", err)
		}
		if got.Quotas == nil || got.Quotas.MaxDatabases != 1000 || got.Entitlements == nil || !got.Entitlements.AllowDedicatedWAL {
			t.Fatalf("the operator's plan did not reach acme: %+v", got)
		}
	})

	t.Run("IsolationChangeRefusedWhileTenantHoldsData", func(t *testing.T) {
		f := newFixture(t, start)
		f.operator()
		req := &pb.TenantUpdateRequest{Id: "acme", IsolationType: string(tenant.IsolationVolume)}
		f.tenants.HoldsData(t, true)
		_, err := f.client.TenantUpdate(callCtx(t), req)
		wantCode(t, "operator changing a data-bearing tenant's isolation", err, codes.FailedPrecondition)
		f.unchanged(t, "acme")
		f.tenants.HoldsData(t, false)
		if _, err := f.client.TenantUpdate(callCtx(t), req); err != nil {
			t.Fatalf("operator changing an empty tenant's isolation = %v, want nil", err)
		}
	})

	t.Run("TenantAdminCannotCreateATenant", func(t *testing.T) {
		f := newFixture(t, start)
		_, err := f.client.TenantCreate(callCtx(t), &pb.TenantCreateRequest{Id: "initech", Name: "initech"})
		wantCode(t, "acme admin create", err, codes.PermissionDenied)
		if _, err := f.tenants.GetTenant(context.Background(), "initech"); err == nil {
			t.Fatal("a refused create left a tenant behind")
		}
	})

	t.Run("ListShowsATenantAdminOnlyItsOwnTenant", func(t *testing.T) {
		f := newFixture(t, start)
		resp, err := f.client.TenantList(callCtx(t), &emptypb.Empty{})
		if err != nil {
			t.Fatalf("acme admin list = %v", err)
		}
		if got := resp.GetTenants(); len(got) != 1 || got[0].GetId() != "acme" {
			t.Fatalf("acme admin listed %v, want [acme]", got)
		}
		f.operator()
		resp, err = f.client.TenantList(callCtx(t), &emptypb.Empty{})
		if err != nil {
			t.Fatalf("operator list = %v", err)
		}
		if got := resp.GetTenants(); len(got) != 3 {
			t.Fatalf("operator listed %d tenants, want 3", len(got))
		}
	})

	t.Run("SlashInTenantIDIsInvalidArgument", func(t *testing.T) {
		f := newFixture(t, start)
		f.operator()
		for _, c := range byIDCalls() {
			wantCode(t, c.name+" acme/evil", c.call(callCtx(t), f.client, "acme/evil"), codes.InvalidArgument)
		}
		_, err := f.client.TenantCreate(callCtx(t), &pb.TenantCreateRequest{Id: "acme/evil", Name: "x"})
		wantCode(t, "create acme/evil", err, codes.InvalidArgument)
	})

	t.Run("SealedWindowHonoursTheUnsealRateLimit", func(t *testing.T) {
		limiter := ratelimit.NewTokenBucketLimiter()
		t.Cleanup(func() { _ = limiter.Close() })
		f := newFixture(t, start,
			authmw.WithRateLimiter(limiter),
			authmw.WithRateLimitPolicies(authtest.OneToken(permission.TenantBarrierUnseal)))
		f.as(authtest.SealedIdentity("acme", "acme-admin"))

		req := &pb.TenantBarrierUnsealRequest{TenantId: "acme", Key: []byte("key")}
		if _, err := f.barrier.Unseal(callCtx(t), req); err != nil {
			t.Fatalf("first unseal = %v, want nil", err)
		}
		_, err := f.barrier.Unseal(callCtx(t), req)
		wantCode(t, "second unseal", err, codes.ResourceExhausted)
		if n := f.tenants.Unseals(); n != 1 {
			t.Fatalf("unseals reaching the service = %d, want 1", n)
		}
	})
}

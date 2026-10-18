package middleware

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// A credential issued before SuspendUser keeps validating: the authenticator
// below returns the same identity on every request, as a still-unexpired
// token or session does. The refusal therefore has to come from the
// per-request lifecycle check in finalizeIdentity, which every transport's
// entry point reaches, and it must lift again on UnsuspendUser.
func TestFinalizeIdentity_RefusesASuspendedUserWithAPreSuspensionCredential(t *testing.T) {
	users := newSystemConfinedUserStore(t)
	u := createUser(t, users, tenantid.System, "sam", []string{"operator"})

	id := &auth.Identity{Subject: "sam", TenantID: tenantid.System}
	m := newTestMiddleware(&mockAuthorizer{},
		WithAuthenticator(&mockAuthenticator{name: "token", identity: id}),
		WithUserService(users))

	if _, err := m.AuthenticateQUIC(context.Background()); err != nil {
		t.Fatalf("active user refused: %v", err)
	}

	if err := users.SuspendUser(context.Background(), u.ID); err != nil {
		t.Fatalf("SuspendUser: %v", err)
	}
	_, err := m.AuthenticateQUIC(context.Background())
	var ude *UserDisabledError
	if !errors.As(err, &ude) {
		t.Fatalf("suspended user with a pre-suspension credential: got %v, want *UserDisabledError", err)
	}
	if ude.Lifecycle != string(user.LifecycleSuspended) {
		t.Fatalf("refusal names lifecycle %q, want suspended", ude.Lifecycle)
	}

	if err := users.UnsuspendUser(context.Background(), u.ID); err != nil {
		t.Fatalf("UnsuspendUser: %v", err)
	}
	if _, err := m.AuthenticateQUIC(context.Background()); err != nil {
		t.Fatalf("unsuspended user still refused: %v", err)
	}
}

// The same refusal holds on the gRPC entry point: the interceptor never
// reaches the handler for a suspended caller.
func TestUnaryInterceptor_RefusesASuspendedUser(t *testing.T) {
	users := newSystemConfinedUserStore(t)
	u := createUser(t, users, tenantid.System, "tess", []string{"operator"})
	if err := users.SuspendUser(context.Background(), u.ID); err != nil {
		t.Fatalf("SuspendUser: %v", err)
	}

	id := &auth.Identity{Subject: "tess", TenantID: tenantid.System}
	m := newTestMiddleware(&mockAuthorizer{},
		WithAuthenticator(&mockAuthenticator{name: "token", identity: id}),
		WithUserService(users))

	called := false
	_, err := m.UnaryInterceptor()(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/Get"},
		func(context.Context, any) (any, error) { called = true; return nil, nil })
	if err == nil || called {
		t.Fatalf("suspended user reached the handler (err=%v, called=%v)", err, called)
	}
}

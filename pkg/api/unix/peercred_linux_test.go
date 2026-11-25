//go:build linux

package unix

import (
	"context"
	"net"
	"os"
	"slices"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// peerCredCapture is an authenticator that records the peer credential and
// peer address the request context carries, then refuses: the test asks
// only what reached authentication.
type peerCredCapture struct {
	mu   sync.Mutex
	cred auth.PeerCredential
	ok   bool
	addr net.Addr
}

func (c *peerCredCapture) Name() string { return "capture" }

func (c *peerCredCapture) Authenticate(ctx context.Context) (*auth.Identity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cred, c.ok = auth.PeerCredentialFromContext(ctx)
	if p, ok := peer.FromContext(ctx); ok {
		c.addr = p.Addr
	}
	return nil, auth.ErrUnauthenticated
}

// startPeerCredServer starts the socket with mw and returns a certificate-less
// TLS client dialled to it.
func startPeerCredServer(t *testing.T, mw *authmw.AuthMiddleware) pb.QRDBServiceClient {
	t.Helper()
	tlsCfg := selfSignedTLSConfig(t)
	socket := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socket, WithAuthMiddleware(mw), WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	conn := dialUnixTLS(t, socket, tlsCfg)
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewQRDBServiceClient(conn)
}

func TestUnixPeerCredential_HandshakeCarriesProcessUID(t *testing.T) {
	capture := &peerCredCapture{}
	mw := authmw.New(allowAllAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(capture))
	client := startPeerCredServer(t, mw)
	_, err := client.Get(context.Background(), &pb.GetRequest{Key: "k"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("Get: %v, want Unauthenticated from the capturing authenticator", err)
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if !capture.ok {
		t.Fatal("request context carries no peer credential")
	}
	if capture.cred.UID != uint32(os.Getuid()) || capture.cred.GID != uint32(os.Getgid()) {
		t.Fatalf("peer credential uid/gid = %d/%d, want %d/%d", capture.cred.UID, capture.cred.GID, os.Getuid(), os.Getgid())
	}
	if capture.cred.PID != int32(os.Getpid()) {
		t.Fatalf("peer credential pid = %d, want %d", capture.cred.PID, os.Getpid())
	}
}

func TestUnixPeerCredential_AddrStringUnchanged(t *testing.T) {
	inner := &net.UnixAddr{Name: "/run/qrdb.sock", Net: "unix"}
	a := &peerCredAddr{Addr: inner, cred: auth.PeerCredential{UID: 1}}
	if a.String() != inner.String() || a.Network() != inner.Network() {
		t.Fatalf("wrapped address %s/%s, want %s/%s", a.Network(), a.String(), inner.Network(), inner.String())
	}

	capture := &peerCredCapture{}
	mw := authmw.New(allowAllAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(capture))
	client := startPeerCredServer(t, mw)
	_, _ = client.Get(context.Background(), &pb.GetRequest{Key: "k"})
	capture.mu.Lock()
	defer capture.mu.Unlock()
	got, ok := capture.addr.(*peerCredAddr)
	if !ok {
		t.Fatalf("peer address %T, want *peerCredAddr", capture.addr)
	}
	if got.String() != got.Addr.String() || got.Network() != got.Addr.Network() {
		t.Fatalf("socket peer address %s/%s differs from the accepted conn's %s/%s", got.Network(), got.String(), got.Addr.Network(), got.Addr.String())
	}
}

func TestPeerCredCredentials_CloneKeepsWrapper(t *testing.T) {
	creds := newPeerCredCredentials(credentials.NewTLS(selfSignedTLSConfig(t)))
	clone := creds.Clone()
	if _, ok := clone.(*peerCredCredentials); !ok {
		t.Fatalf("Clone returned %T, want *peerCredCredentials", clone)
	}
	if clone == creds {
		t.Fatal("Clone returned the same credentials value")
	}
}

func TestPeerCredAddr_NilAddr(t *testing.T) {
	a := &peerCredAddr{cred: auth.PeerCredential{UID: 1}}
	if a.Network() != "unix" {
		t.Fatalf("Network() = %q, want unix", a.Network())
	}
	if a.String() != "" {
		t.Fatalf("String() = %q, want empty", a.String())
	}
}

type ctxServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *ctxServerStream) Context() context.Context { return s.ctx }

func TestPeerCredStreamInterceptor(t *testing.T) {
	cred := auth.PeerCredential{UID: 42}
	withCred := peer.NewContext(context.Background(), &peer.Peer{Addr: &peerCredAddr{cred: cred}})
	plain := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.UnixAddr{Name: "/run/qrdb.sock", Net: "unix"}})

	tests := []struct {
		name     string
		ctx      context.Context
		wantWrap bool
	}{
		{name: "no peer", ctx: context.Background()},
		{name: "plain address", ctx: plain},
		{name: "peer credential", ctx: withCred, wantWrap: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ss := &ctxServerStream{ctx: tc.ctx}
			var got grpc.ServerStream
			err := peerCredStreamInterceptor(nil, ss, nil, func(_ any, s grpc.ServerStream) error {
				got = s
				return nil
			})
			if err != nil {
				t.Fatalf("interceptor: %v", err)
			}
			if !tc.wantWrap {
				if got != ss {
					t.Fatalf("handler got %T, want the original stream", got)
				}
				return
			}
			if got == ss {
				t.Fatal("handler got the original stream, want a wrapped one")
			}
			bound, ok := auth.PeerCredentialFromContext(got.Context())
			if !ok || bound.UID != cred.UID {
				t.Fatalf("stream context credential = %+v (ok=%v), want UID %d", bound, ok, cred.UID)
			}
		})
	}
}

// peerCredUsers is a user store holding one account.
type peerCredUsers struct {
	user.UserServicer
	u *user.User
}

func (s *peerCredUsers) GetUserByUsername(_ context.Context, tenantID, username string) (*user.User, error) {
	if s.u != nil && s.u.Username == username && s.u.TenantID == tenantID {
		return s.u, nil
	}
	return nil, &user.UserNotFoundError{Lookup: username}
}

func (s *peerCredUsers) GetUser(_ context.Context, id uint64) (*user.User, error) {
	if s.u != nil && s.u.ID == id {
		return s.u, nil
	}
	return nil, &user.UserNotFoundError{ID: id}
}

// roleAuthorizer permits exactly the identities holding role.
type roleAuthorizer struct{ role string }

func (a roleAuthorizer) Authorize(_ context.Context, id *auth.Identity, _, _ string) error {
	if id != nil && slices.Contains(id.Roles, a.role) {
		return nil
	}
	return auth.ErrUnauthorized
}

const peerCredTenant = "acme"

func peerCredMiddleware(t *testing.T, uid uint32) *authmw.AuthMiddleware {
	t.Helper()
	users := &peerCredUsers{u: &user.User{ID: 7, Username: "pc-reader", TenantID: peerCredTenant, RoleIDs: []string{"role-reader"}, Lifecycle: user.LifecycleActive}}
	authn := auth.NewPeerCredentialAuthenticator([]auth.PeerCredentialMapping{{UID: &uid, TenantID: peerCredTenant, Username: "pc-reader"}})
	return authmw.New(roleAuthorizer{role: "role-reader"}, authmw.WithRoleResolver(authtest.BuiltInRoles(t, peerCredTenant)),
		authmw.WithAuthenticator(authn), authmw.WithUserService(users))
}

func TestUnixPeerCredential_MappedUIDAuthorizedByBoundUser(t *testing.T) {
	client := startPeerCredServer(t, peerCredMiddleware(t, uint32(os.Getuid())))
	_, err := client.Get(context.Background(), &pb.GetRequest{Key: "k"})
	if c := status.Code(err); c == codes.Unauthenticated || c == codes.PermissionDenied {
		t.Fatalf("Get by the mapped uid: %v, want it admitted with the bound user's role", err)
	}
}

func TestUnixPeerCredential_UnmappedUIDRefused(t *testing.T) {
	client := startPeerCredServer(t, peerCredMiddleware(t, uint32(os.Getuid())+1))
	_, err := client.Get(context.Background(), &pb.GetRequest{Key: "k"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("Get by an unmapped uid: %v, want Unauthenticated", err)
	}
}

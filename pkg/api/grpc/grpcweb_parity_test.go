package grpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	connectrpc "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/grpcweb"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// parityCA is a test certificate authority issuing the server's leaf and
// the clients' certificates, so both transports verify the same chain.
type parityCA struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
	pool *x509.CertPool
}

func newParityCA(t *testing.T) *parityCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "parity test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(crand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &parityCA{key: key, cert: cert, pool: pool}
}

// issue signs a leaf for subject. A client leaf carries the tenant as its
// first Subject OU, which is where MTLSAuthenticator derives it from.
func (ca *parityCA) issue(t *testing.T, serial int64, subject pkix.Name, usage x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      subject,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	if usage == x509.ExtKeyUsageServerAuth {
		tmpl.DNSNames = []string{"localhost"}
		tmpl.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	}
	der, err := x509.CreateCertificate(crand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// bearerAuthenticator is the embedder's token validator the
// WithHTTPAuthorization seam exists for: it reads the Authorization value
// the transport put in the context and knows nothing about how it got there.
type bearerAuthenticator map[string]*auth.Identity

func (b bearerAuthenticator) Authenticate(ctx context.Context) (*auth.Identity, error) {
	id, ok := b[auth.HTTPAuthorizationFromContext(ctx)]
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	cp := *id
	return &cp, nil
}

func (bearerAuthenticator) Name() string { return "test-bearer" }

// enrolledUsers is the binding fixture's user store with client
// certificates enrolled on its accounts, as a certificate login requires.
type enrolledUsers struct {
	*authtest.BindingUsers
	thumbprints map[string]string // username -> SPKI thumbprint
}

func enroll(t *testing.T, certs map[string]tls.Certificate) enrolledUsers {
	t.Helper()
	u := enrolledUsers{BindingUsers: authtest.NewBindingUsers(), thumbprints: make(map[string]string, len(certs))}
	for username, c := range certs {
		leaf, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			t.Fatalf("parse %s certificate: %v", username, err)
		}
		u.thumbprints[username] = user.ComputeSPKIThumbprint(leaf)
	}
	return u
}

func (u enrolledUsers) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	rec, err := u.BindingUsers.GetUserByUsername(ctx, tenantID, username)
	if tp, ok := u.thumbprints[username]; ok && err == nil {
		rec.CertBindings = append(rec.CertBindings, user.CertBinding{Thumbprint: tp, Kind: user.CertBindingSPKIThumbprint})
	}
	return rec, err
}

// parityCaller is one caller's credentials, presented identically over
// native gRPC and gRPC-Web.
type parityCaller struct {
	cert   *tls.Certificate
	bearer string
}

// parityServer is one gRPC server reached two ways: its own listener and
// gRPC-Web over its ServeHTTP.
type parityServer struct {
	srv   *Server
	web   *httptest.Server
	pool  *x509.CertPool
	probe *fakeSealProbe
}

func startParityServer(t *testing.T, ca *parityCA, users enrolledUsers) *parityServer {
	t.Helper()
	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{ca.issue(t, 2, pkix.Name{CommonName: "localhost"}, x509.ExtKeyUsageServerAuth)},
		ClientCAs:    ca.pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		MinVersion:   tls.VersionTLS13,
	}
	roles := authtest.BindingRoles(t)
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithUserService(users),
		authmw.WithAuthenticator(auth.NewMTLSAuthenticator()),
		authmw.WithAuthenticator(bearerAuthenticator{"Bearer acme-admin-token": authtest.BindingTenantAdmin()}))
	probe := newProbe(false)
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithTLS(serverTLS), WithUserService(users), WithRoleService(roles),
		WithAuthMiddleware(mw), WithSealedWindowProbe(probe))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	h, err := grpcweb.NewHandler(grpcweb.Config{Enabled: true}, srv)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(grpcweb.PathPrefix, h)
	web := httptest.NewUnstartedServer(mux)
	web.TLS = serverTLS.Clone()
	web.StartTLS()
	t.Cleanup(web.Close)
	return &parityServer{srv: srv, web: web, pool: ca.pool, probe: probe}
}

func (p *parityServer) clientTLS(c parityCaller) *tls.Config {
	cfg := &tls.Config{RootCAs: p.pool, ServerName: "localhost", MinVersion: tls.VersionTLS13}
	if c.cert != nil {
		cfg.Certificates = []tls.Certificate{*c.cert}
	}
	return cfg
}

// native calls ListUsers on the gRPC listener and returns its status code.
func (p *parityServer) native(t *testing.T, c parityCaller, req *pb.ListUsersRequest) codes.Code {
	t.Helper()
	conn, err := grpc.NewClient(p.srv.Addr(), grpc.WithTransportCredentials(credentials.NewTLS(p.clientTLS(c))))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if c.bearer != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", c.bearer)
	}
	_, err = pb.NewQRDBServiceClient(conn).ListUsers(ctx, req)
	return status.Code(err)
}

// grpcWeb calls ListUsers over gRPC-Web and returns its status code.
func (p *parityServer) grpcWeb(t *testing.T, c parityCaller, req *pb.ListUsersRequest) codes.Code {
	t.Helper()
	tr := &http.Transport{TLSClientConfig: p.clientTLS(c)}
	defer tr.CloseIdleConnections()
	client := connectrpc.NewClient(connecthttp.NewTransport(&http.Client{Transport: tr}, p.web.URL+"/grpc-web", connecthttp.WithGRPCWeb()))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx, info := connectrpc.NewClientContext(ctx)
	if c.bearer != "" {
		info.RequestHeader().Set("Authorization", c.bearer)
	}
	spec := connectrpc.Spec{StreamType: connectrpc.StreamTypeUnary, Procedure: pb.QRDBService_ListUsers_FullMethodName}
	if err := client.CallUnary(ctx, spec, req, &pb.ListUsersResponse{}); err != nil {
		return codes.Code(connectrpc.CodeOf(err))
	}
	return codes.OK
}

// TestGRPCWeb_InterceptorParity drives one method through the server's own
// interceptor chain twice per caller, over native gRPC and over gRPC-Web,
// and requires the same answer from both. The admitted certificate proves a
// client certificate reaches the chain as credentials.TLSInfo; the admitted
// bearer proves the Authorization header reaches it as metadata.
func TestGRPCWeb_InterceptorParity(t *testing.T) {
	ca := newParityCA(t)
	admin := ca.issue(t, 10, pkix.Name{CommonName: authtest.BindingTenantAdminName, OrganizationalUnit: []string{authtest.BindingTenant}}, x509.ExtKeyUsageClientAuth)
	alice := ca.issue(t, 11, pkix.Name{CommonName: "alice", OrganizationalUnit: []string{authtest.BindingTenant}}, x509.ExtKeyUsageClientAuth)
	ownTenant := &pb.ListUsersRequest{TenantId: authtest.BindingTenant}
	p := startParityServer(t, ca, enroll(t, map[string]tls.Certificate{authtest.BindingTenantAdminName: admin, "alice": alice}))

	for _, tc := range []struct {
		name   string
		caller parityCaller
		req    *pb.ListUsersRequest
		sealed bool
		want   codes.Code
	}{
		{name: "client certificate admitted", caller: parityCaller{cert: &admin}, req: ownTenant, want: codes.OK},
		{name: "bearer admitted", caller: parityCaller{bearer: "Bearer acme-admin-token"}, req: ownTenant, want: codes.OK},
		{name: "unauthenticated refused", req: ownTenant, want: codes.Unauthenticated},
		{name: "unknown bearer refused", caller: parityCaller{bearer: "Bearer forged"}, req: ownTenant, want: codes.Unauthenticated},
		{name: "wrong tenant refused", caller: parityCaller{cert: &admin}, req: &pb.ListUsersRequest{TenantId: authtest.OtherTenant}, want: codes.PermissionDenied},
		{name: "missing permission denied", caller: parityCaller{cert: &alice}, req: ownTenant, want: codes.PermissionDenied},
		{name: "sealed window refuses", caller: parityCaller{cert: &admin}, req: ownTenant, sealed: true, want: codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p.probe.sealed.Store(tc.sealed)
			native, web := p.native(t, tc.caller, tc.req), p.grpcWeb(t, tc.caller, tc.req)
			if native != tc.want || web != tc.want {
				t.Fatalf("native gRPC = %v, gRPC-Web = %v, want %v from both", native, web, tc.want)
			}
		})
	}
}

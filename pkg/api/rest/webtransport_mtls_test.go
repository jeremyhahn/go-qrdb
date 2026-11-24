package rest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"github.com/jeremyhahn/go-qrdb/internal/webtransport"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// wtMTLS is a test PKI for client-certificate WebTransport sessions: one CA
// issues the server's localhost certificate and every client certificate the
// server trusts.
type wtMTLS struct {
	caCert    *x509.Certificate
	caKey     *ecdsa.PrivateKey
	pool      *x509.CertPool
	serverCfg *tls.Config
	serial    int64
}

// wtMTLSFixture generates the CA and a server tls.Config that asks for a
// client certificate and verifies one against the CA when it is given.
func wtMTLSFixture(t *testing.T) *wtMTLS {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "wt-mtls-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	f := &wtMTLS{caCert: caCert, caKey: caKey, pool: pool, serial: 10}

	srvTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	srvCert := f.issue(t, srvTmpl)
	f.serverCfg = &tls.Config{
		Certificates: []tls.Certificate{srvCert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS13,
	}
	return f
}

// issue signs tmpl with the fixture's CA under a fresh key.
func (f *wtMTLS) issue(t *testing.T, tmpl *x509.Certificate) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, f.caCert, &key.PublicKey, f.caKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// issueWTClientCert issues a client certificate for name in tenant, carrying
// the tenant the way the mTLS authenticator reads it (Subject OU and the
// workload SPIFFE URI SAN).
func issueWTClientCert(t *testing.T, f *wtMTLS, tenant, name string) tls.Certificate {
	t.Helper()
	f.serial++
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(f.serial),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	authtest.SetClientTenant(t, tmpl, tenant, name)
	return f.issue(t, tmpl)
}

// wtMTLSMiddleware is the REST auth chain with the mTLS authenticator. There
// is no user store, so its binding policy is "enrolled" (an unenrolled
// certificate authenticates as itself) unless opts set another.
func wtMTLSMiddleware(t *testing.T, opts ...authmw.Option) *authmw.AuthMiddleware {
	t.Helper()
	opts = append([]authmw.Option{
		authmw.WithRoleResolver(authtest.BuiltInRoles(t)),
		authmw.WithAuthenticator(auth.NewMTLSAuthenticatorWithTrustDomain(authtest.ClientTrustDomain)),
		authmw.WithMTLSBindingPolicy(auth.MTLSBindingEnrolled),
	}, opts...)
	return authmw.New(allowAllAuthorizer{}, opts...)
}

// startWTMTLSServer starts a REST server whose TLS config asks for client
// certificates, with mw as its auth chain.
func startWTMTLSServer(t *testing.T, f *wtMTLS, mw *authmw.AuthMiddleware, opts ...ServerOption) *Server {
	t.Helper()
	opts = append([]ServerOption{WithTLS(f.serverCfg), WithAuthMiddleware(mw)}, opts...)
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), dualStackAddrOn(t, "127.0.0.1"), opts...)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	return srv
}

// wtClientCertTLS is the HTTP/3 client config presenting cert.
func wtClientCertTLS(pool *x509.CertPool, cert *tls.Certificate) *tls.Config {
	cfg := wtClientTLS(pool)
	cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		return cert, nil
	}
	return cfg
}

// dialWTCert dials a WebTransport session presenting cert, like dialWTHeader.
func dialWTCert(t *testing.T, srv *Server, pool *x509.CertPool, path string, cert *tls.Certificate, hdr http.Header) (int, *webtransport.Session, error) {
	t.Helper()
	// The CONNECT request's context is the session's: cancelling it on return
	// would cancel the session stream, so it lives until the test ends.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	d := &webtransport.Transport{TLSClientConfig: wtClientCertTLS(pool, cert)}
	t.Cleanup(func() { _ = d.Close() })
	resp, sess, err := d.Dial(ctx, "https://"+srv.Addr()+path, hdr)
	status := 0
	if resp != nil {
		status = resp.StatusCode
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	if sess != nil {
		t.Cleanup(func() { _ = sess.CloseWithError(0, "") })
	}
	return status, sess, err
}

// restStatusCert sends an ordinary REST request over HTTP/3 presenting cert
// and returns its status.
func restStatusCert(t *testing.T, srv *Server, pool *x509.CertPool, method, path string, cert *tls.Certificate) int {
	t.Helper()
	tr := &http3.Transport{TLSClientConfig: wtClientCertTLS(pool, cert)}
	t.Cleanup(func() { _ = tr.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "https://"+srv.Addr()+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatalf("REST %s %s over HTTP/3: %v", method, path, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// awaitWTSession waits for the handler's call, failing after 10 s.
func awaitWTSession(t *testing.T, h *wtRecorder) wtSession {
	t.Helper()
	select {
	case got := <-h.sessions:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("handler was not called within 10 s")
		return wtSession{}
	}
}

// TestWebTransport_MTLSSessionReachesTheHandler: a caller presenting a client
// certificate and no Authorization header opens a session, and the handler
// receives the subject and tenant the certificate names.
func TestWebTransport_MTLSSessionReachesTheHandler(t *testing.T) {
	f := wtMTLSFixture(t)
	h := newWTRecorder()
	srv := startWTMTLSServer(t, f, wtMTLSMiddleware(t), WithWebTransportHandler(wtTestPath, h.handle))
	cert := issueWTClientCert(t, f, "t1", "alice")

	status, sess, err := dialWTCert(t, srv, f.pool, wtTestPath, &cert, nil)
	if err != nil {
		t.Fatalf("dial: status %d: %v", status, err)
	}
	if status != http.StatusOK || sess == nil {
		t.Fatalf("status = %d, session = %v; want 200 and a session", status, sess)
	}
	got := awaitWTSession(t, h)
	// MTLSAuthenticator takes the Subject from the certificate's CN and the
	// tenant from its SPIFFE URI SAN / Subject OU.
	if got.subject != "alice" || got.tenantID != "t1" {
		t.Fatalf("handler saw subject %q tenant %q; want alice, t1", got.subject, got.tenantID)
	}
}

// TestWebTransport_RefusedCertificateStatusMatchesREST: a certificate the REST
// chain refuses is refused on the CONNECT with the status a REST route gives,
// and a certificate from an untrusted CA never completes the handshake.
func TestWebTransport_RefusedCertificateStatusMatchesREST(t *testing.T) {
	f := wtMTLSFixture(t)
	h := newWTRecorder()
	// Binding required and no user store: no certificate is enrolled.
	mw := wtMTLSMiddleware(t, authmw.WithMTLSBindingPolicy(auth.MTLSBindingRequired))
	srv := startWTMTLSServer(t, f, mw, WithWebTransportHandler(wtTestPath, h.handle))

	t.Run("unenrolled certificate", func(t *testing.T) {
		cert := issueWTClientCert(t, f, "t1", "alice")
		status, sess, _ := dialWTCert(t, srv, f.pool, wtTestPath, &cert, nil)
		rest := restStatusCert(t, srv, f.pool, http.MethodGet, "/kv/some-key", &cert)
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Fatalf("CONNECT status = %d; want 401 or 403", status)
		}
		if status != rest {
			t.Fatalf("CONNECT status = %d, REST status = %d; want equal", status, rest)
		}
		if sess != nil {
			t.Fatal("a refused CONNECT returned a session")
		}
		if n := h.calls.Load(); n != 0 {
			t.Fatalf("handler called %d times; want 0", n)
		}
	})

	t.Run("untrusted CA", func(t *testing.T) {
		cert := selfSignedClientCert(t)
		status, sess, err := dialWTCert(t, srv, f.pool, wtTestPath, &cert, nil)
		if err == nil || status != 0 || sess != nil {
			t.Fatalf("dial = status %d, session %v, err %v; want a handshake failure", status, sess, err)
		}
		if n := h.calls.Load(); n != 0 {
			t.Fatalf("handler called %d times; want 0", n)
		}
	})
}

// TestWebTransport_TenantIsTheIdentitysNotAHeader: the handler's tenant is the
// authenticated identity's; a client X-QRDB-Tenant header naming another
// tenant is ignored.
func TestWebTransport_TenantIsTheIdentitysNotAHeader(t *testing.T) {
	f := wtMTLSFixture(t)
	h := newWTRecorder()
	srv := startWTMTLSServer(t, f, wtMTLSMiddleware(t), WithWebTransportHandler(wtTestPath, h.handle))
	cert := issueWTClientCert(t, f, "t1", "alice")

	hdr := http.Header{}
	hdr.Set("X-QRDB-Tenant", "t2")
	status, sess, err := dialWTCert(t, srv, f.pool, wtTestPath, &cert, hdr)
	if err != nil || status != http.StatusOK || sess == nil {
		t.Fatalf("dial = status %d, session %v, err %v; want 200 and a session", status, sess, err)
	}
	if got := awaitWTSession(t, h); got.tenantID != "t1" {
		t.Fatalf("handler tenant = %q; want t1 (the certificate's), not the header's", got.tenantID)
	}
}

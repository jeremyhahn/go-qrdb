package rest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// settingsWTEnabled is SETTINGS_WT_ENABLED, draft-ietf-webtrans-http3-16
// section 9.2.
const settingsWTEnabled = 0x2c7cf000

const wtTestPath = "/wt"

// wtSession is what the test handler saw of one session.
type wtSession struct {
	subject, tenantID string
}

// wtRecorder is a WebTransportHandler that counts and records its sessions.
type wtRecorder struct {
	calls    atomic.Int32
	sessions chan wtSession
}

func newWTRecorder() *wtRecorder {
	return &wtRecorder{sessions: make(chan wtSession, 4)}
}

func (h *wtRecorder) handle(sess *WebTransportSession, caller *auth.Identity, tenantID string) {
	h.calls.Add(1)
	h.sessions <- wtSession{subject: caller.Subject, tenantID: tenantID}
	_ = sess.CloseWithError(0, "")
}

// startWTServer starts a REST server on loopback with the bearer test
// authenticator in its chain, and opts on top.
func startWTServer(t *testing.T, opts ...ServerOption) (*Server, *x509.CertPool) {
	t.Helper()
	tlsCfg, pool := generateTestTLSConfig(t)
	mw := authmw.New(allowAllAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(bearerAuthenticator{}))
	opts = append([]ServerOption{WithTLS(tlsCfg), WithAuthMiddleware(mw)}, opts...)
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), dualStackAddrOn(t, "127.0.0.1"), opts...)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	return srv, pool
}

func wtClientTLS(pool *x509.CertPool) *tls.Config {
	return &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13, NextProtos: []string{http3.NextProtoH3}}
}

// dialWT opens a WebTransport session on path, with authorization as the
// Authorization header when it is not empty.
func dialWT(t *testing.T, srv *Server, pool *x509.CertPool, path, authorization string) (*http.Response, *webtransport.Session, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d := &webtransport.Transport{TLSClientConfig: wtClientTLS(pool)}
	t.Cleanup(func() { _ = d.Close() })
	hdr := http.Header{}
	if authorization != "" {
		hdr.Set("Authorization", authorization)
	}
	resp, sess, err := d.Dial(ctx, "https://"+srv.Addr()+path, hdr)
	if sess != nil {
		t.Cleanup(func() { _ = sess.CloseWithError(0, "") })
	}
	return resp, sess, err
}

func TestWebTransport_AuthenticatedSessionReachesTheHandler(t *testing.T) {
	h := newWTRecorder()
	srv, pool := startWTServer(t, WithWebTransportHandler(wtTestPath, h.handle))
	resp, _, err := dialWT(t, srv, pool, wtTestPath, "Bearer good")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	select {
	case got := <-h.sessions:
		if got.subject != "alice" || got.tenantID != "t1" {
			t.Fatalf("handler saw %+v, want alice in t1", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the session never reached the handler")
	}
}

func TestWebTransport_UnauthenticatedSessionRefusedBeforeTheHandler(t *testing.T) {
	h := newWTRecorder()
	srv, pool := startWTServer(t, WithWebTransportHandler(wtTestPath, h.handle))
	for name, authorization := range map[string]string{"no credentials": "", "bad credentials": "Bearer bad"} {
		t.Run(name, func(t *testing.T) {
			resp, sess, err := dialWT(t, srv, pool, wtTestPath, authorization)
			if err == nil || sess != nil {
				t.Fatal("an unauthenticated session was established")
			}
			if resp == nil || resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("response %v, want REST's 401", resp)
			}
		})
	}
	if n := h.calls.Load(); n != 0 {
		t.Fatalf("handler ran %d times for unauthenticated sessions", n)
	}
}

func TestWebTransport_OtherPathNotUpgraded(t *testing.T) {
	h := newWTRecorder()
	srv, pool := startWTServer(t, WithWebTransportHandler(wtTestPath, h.handle))
	resp, sess, err := dialWT(t, srv, pool, "/not-wt", "Bearer good")
	if err == nil || sess != nil {
		t.Fatal("a CONNECT to another path was upgraded")
	}
	if resp == nil || resp.StatusCode/100 == 2 {
		t.Fatalf("response %v, want a refusal", resp)
	}
	if n := h.calls.Load(); n != 0 {
		t.Fatalf("handler ran %d times", n)
	}
}

// serverSettings dials srv's HTTP/3 listener and returns the HTTP/3 SETTINGS
// and QUIC transport state the server advertised.
func serverSettings(t *testing.T, srv *Server, pool *x509.CertPool) (*http3.Settings, quic.ConnectionState) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, srv.Addr(), wtClientTLS(pool), &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true})
	if err != nil {
		t.Fatalf("DialAddr: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseWithError(0, "") })
	cc := (&http3.Transport{EnableDatagrams: true}).NewClientConn(conn)
	select {
	case <-cc.ReceivedSettings():
	case <-ctx.Done():
		t.Fatal("no SETTINGS from the server")
	}
	return cc.Settings(), conn.ConnectionState()
}

func TestWebTransport_SettingsOnlyWithAHandler(t *testing.T) {
	h := newWTRecorder()
	srv, pool := startWTServer(t, WithWebTransportHandler(wtTestPath, h.handle))
	settings, state := serverSettings(t, srv, pool)
	if !settings.EnableExtendedConnect || !settings.EnableDatagrams || settings.Other[settingsWTEnabled] != 1 {
		t.Errorf("SETTINGS %+v, want SETTINGS_ENABLE_CONNECT_PROTOCOL, SETTINGS_H3_DATAGRAM and SETTINGS_WT_ENABLED=1", settings)
	}
	if !state.SupportsDatagrams.Remote || !state.SupportsStreamResetPartialDelivery.Remote {
		t.Errorf("transport parameters %+v, want max_datagram_frame_size and reset_stream_at", state)
	}

	plain, plainPool := startWTServer(t)
	settings, state = serverSettings(t, plain, plainPool)
	// SETTINGS_ENABLE_CONNECT_PROTOCOL is not among them: quic-go's
	// http3.Server sends it on every connection and has no switch for it.
	if settings.EnableDatagrams || len(settings.Other) != 0 {
		t.Errorf("SETTINGS %+v without a handler, want none of WebTransport's", settings)
	}
	if state.SupportsDatagrams.Remote || state.SupportsStreamResetPartialDelivery.Remote {
		t.Errorf("transport parameters %+v without a handler, want neither", state)
	}
}

func TestWebTransport_NoHandlerPathIsAnUnknownRoute(t *testing.T) {
	srv, pool := startWTServer(t)
	rt := &http3.Transport{TLSClientConfig: wtClientTLS(pool)}
	t.Cleanup(func() { _ = rt.Close() })
	status := func(path string) int {
		req, err := http.NewRequest(http.MethodGet, "https://"+srv.Addr()+path, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Authorization", "Bearer good")
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip %s: %v", path, err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got, unknown := status(wtTestPath), status("/no-such-route"); got != unknown || got != http.StatusNotFound {
		t.Fatalf("%s answered %d, an unknown route %d; want both 404", wtTestPath, got, unknown)
	}
}

func TestWithWebTransportHandler_InvalidRefusedAtStart(t *testing.T) {
	h := newWTRecorder()
	tlsCfg, _ := generateTestTLSConfig(t)
	for name, tc := range map[string]struct {
		path string
		h    WebTransportHandler
	}{
		"nil handler":   {path: wtTestPath},
		"relative path": {path: "wt", h: h.handle},
		"empty path":    {path: "", h: h.handle},
	} {
		t.Run(name, func(t *testing.T) {
			srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTLS(tlsCfg), WithWebTransportHandler(tc.path, tc.h))
			err := srv.Start()
			var target *InvalidWebTransportHandlerError
			if !errors.As(err, &target) {
				srv.Stop()
				t.Fatalf("Start = %v, want *InvalidWebTransportHandlerError", err)
			}
			if target.Path != tc.path || target.NilHandler != (tc.h == nil) || !strings.Contains(err.Error(), "WebTransport") {
				t.Fatalf("error %+v (%q) does not describe the option", target, err)
			}
		})
	}
}

func TestServeWebTransport_RefusalsWithoutUpgrade(t *testing.T) {
	h := newWTRecorder()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWebTransportHandler(wtTestPath, h.handle))
	bound := func(r *http.Request) *http.Request {
		ctx := tenant.WithTenantID(auth.WithIdentity(r.Context(), &auth.Identity{Subject: "alice", TenantID: "t1"}), "t1")
		return r.WithContext(ctx)
	}
	for name, tc := range map[string]struct {
		req  *http.Request
		want int
	}{
		"no identity":               {req: httptest.NewRequest(http.MethodConnect, wtTestPath, nil), want: http.StatusUnauthorized},
		"identity, not over HTTP/3": {req: httptest.NewRequest(http.MethodConnect, wtTestPath, nil).WithContext(auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice"})), want: http.StatusBadRequest},
		"bound, not over HTTP/3":    {req: bound(httptest.NewRequest(http.MethodConnect, wtTestPath, nil)), want: http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.serveWebTransport(rec, tc.req)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d", rec.Code, tc.want)
			}
		})
	}
	if n := h.calls.Load(); n != 0 {
		t.Fatalf("handler ran %d times", n)
	}
}

func TestWebTransportErrors_Messages(t *testing.T) {
	cause := &WebTransportNotHTTP3Error{}
	up := &WebTransportUpgradeError{Err: cause}
	if !errors.Is(up, cause) || !strings.Contains(up.Error(), cause.Error()) {
		t.Fatalf("upgrade error %q does not carry its cause", up)
	}
	for _, err := range []error{&WebTransportUnauthenticatedError{}, cause, up, &InvalidWebTransportHandlerError{Path: "x"}, &InvalidWebTransportHandlerError{Path: "/x", NilHandler: true}} {
		if !strings.Contains(err.Error(), "WebTransport") {
			t.Errorf("%T message %q does not name WebTransport", err, err)
		}
	}
}

// acceptErrListener is a QUIC listener whose Accept fails with err.
type acceptErrListener struct{ err error }

func (l acceptErrListener) Accept(context.Context) (*quic.Conn, error) { return nil, l.err }
func (l acceptErrListener) Addr() net.Addr                             { return &net.UDPAddr{} }
func (l acceptErrListener) Close() error                               { return nil }

func TestWebTransportServer_ServeListenerEndsOnAcceptError(t *testing.T) {
	wt := newWebTransportServer(&http3.Server{})
	t.Cleanup(func() { _ = wt.close() })
	if err := wt.serveListener(acceptErrListener{err: quic.ErrServerClosed}); !errors.Is(err, http.ErrServerClosed) {
		t.Errorf("closed listener: %v, want http.ErrServerClosed", err)
	}
	if err := wt.serveListener(acceptErrListener{err: net.ErrClosed}); !errors.Is(err, net.ErrClosed) {
		t.Errorf("failed accept: %v, want the accept error", err)
	}
}

package rest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/qlog"
	"github.com/quic-go/quic-go/qlogwriter"
	"github.com/quic-go/quic-go/quicvarint"
	"github.com/quic-go/quic-go/testutils/simnet"

	"github.com/jeremyhahn/go-qrdb/internal/webtransport"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

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
func dialWT(t *testing.T, srv *Server, pool *x509.CertPool, path, authorization string) (int, *webtransport.Session, error) {
	t.Helper()
	hdr := http.Header{}
	if authorization != "" {
		hdr.Set("Authorization", authorization)
	}
	return dialWTHeader(t, srv, pool, path, hdr)
}

// dialWTHeader opens a WebTransport session on path with hdr as the CONNECT
// request's headers.
func dialWTHeader(t *testing.T, srv *Server, pool *x509.CertPool, path string, hdr http.Header) (int, *webtransport.Session, error) {
	t.Helper()
	// The CONNECT request's context is the session's: cancelling it on return
	// would cancel the session stream, so it lives until the test ends.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	d := &webtransport.Transport{TLSClientConfig: wtClientTLS(pool)}
	t.Cleanup(func() { _ = d.Close() })
	resp, sess, err := d.Dial(ctx, "https://"+srv.Addr()+path, hdr)
	status := 0
	if resp != nil {
		// The body of a CONNECT response is the session stream: closing it
		// now would end the session, so it closes when the test does.
		status = resp.StatusCode
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	if sess != nil {
		t.Cleanup(func() { _ = sess.CloseWithError(0, "") })
	}
	return status, sess, err
}

func TestWebTransport_AuthenticatedSessionReachesTheHandler(t *testing.T) {
	h := newWTRecorder()
	srv, pool := startWTServer(t, WithWebTransportHandler(wtTestPath, h.handle))
	status, _, err := dialWT(t, srv, pool, wtTestPath, "Bearer good")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
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

// TestWebTransport_DatagramsBothWaysIsolatedBetweenPooledSessions pools two
// authenticated sessions on one QUIC connection of the REST listener and
// exchanges datagrams on each: the handler echoes every datagram it receives,
// and each session gets only its own echoes.
func TestWebTransport_DatagramsBothWaysIsolatedBetweenPooledSessions(t *testing.T) {
	var handled atomic.Int32
	echo := func(sess *WebTransportSession, _ *auth.Identity, _ string) {
		handled.Add(1)
		for {
			d, err := sess.ReceiveDatagram(sess.Context())
			if err != nil {
				return
			}
			if err := sess.SendDatagram(append([]byte("echo:"), d...)); err != nil {
				return
			}
		}
	}
	srv, pool := startWTServer(t, WithWebTransportHandler(wtTestPath, echo))

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, srv.Addr(), wtClientTLS(pool), &quic.Config{
		EnableDatagrams:                  true,
		EnableStreamResetPartialDelivery: true,
	})
	if err != nil {
		t.Fatalf("DialAddr: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseWithError(0, "") })
	// Flow control on both sides lets the second session share the connection.
	tr := &webtransport.Transport{Config: &webtransport.Config{MaxIncomingStreams: 1, MaxIncomingUniStreams: 1, MaxIncomingData: 1 << 20}}
	t.Cleanup(func() { _ = tr.Close() })
	cc, err := tr.NewClientConn(conn)
	if err != nil {
		t.Fatalf("NewClientConn: %v", err)
	}
	dial := func() (*webtransport.Session, <-chan []byte) {
		rsp, sess, err := cc.Dial(ctx, "https://"+srv.Addr()+wtTestPath, http.Header{"Authorization": {"Bearer good"}})
		if rsp != nil {
			// The body is the session's CONNECT stream: close it only when the test ends.
			t.Cleanup(func() { _ = rsp.Body.Close() })
		}
		if err != nil || rsp.StatusCode != http.StatusOK {
			t.Fatalf("Dial: %v, %v; want 200", rsp, err)
		}
		t.Cleanup(func() { _ = sess.CloseWithError(0, "") })
		got := make(chan []byte, 64)
		go func() {
			for {
				d, err := sess.ReceiveDatagram(ctx)
				if err != nil {
					return
				}
				select {
				case got <- d:
				case <-ctx.Done():
					return
				}
			}
		}()
		return sess, got
	}
	a, aGot := dial()
	b, bGot := dial()

	// Datagrams may be lost: resend until the echo arrives.
	exchange := func(sess *webtransport.Session, got <-chan []byte, payload string) {
		t.Helper()
		resend := time.NewTicker(50 * time.Millisecond)
		defer resend.Stop()
		for {
			if err := sess.SendDatagram([]byte(payload)); err != nil {
				t.Fatalf("SendDatagram(%q): %v", payload, err)
			}
			select {
			case d := <-got:
				if string(d) != "echo:"+payload {
					t.Fatalf("session that sent %q received %q", payload, d)
				}
				return
			case <-resend.C:
			case <-ctx.Done():
				t.Fatalf("no echo of %q", payload)
			}
		}
	}
	exchange(a, aGot, "a")
	exchange(b, bGot, "b")
	exchange(a, aGot, "a")

	for _, s := range []struct {
		got  <-chan []byte
		want string
	}{{aGot, "echo:a"}, {bGot, "echo:b"}} {
	drain:
		for {
			select {
			case d := <-s.got:
				if string(d) != s.want {
					t.Fatalf("session received %q, want only %q", d, s.want)
				}
			default:
				break drain
			}
		}
	}
	if n := handled.Load(); n != 2 {
		t.Fatalf("handler ran %d times, want once per session", n)
	}
}

// TestWebTransport_SessionAndStreamUnderReplicaForwarding runs the session in
// the configuration a clustered node runs: replica forwarding is on, so the
// router is reached through the forwarding middleware. The session must still
// upgrade, and a bidirectional stream must carry bytes both ways.
func TestWebTransport_SessionAndStreamUnderReplicaForwarding(t *testing.T) {
	echoed := make(chan error, 1)
	echo := func(sess *WebTransportSession, caller *auth.Identity, tenantID string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		str, err := sess.AcceptStream(ctx)
		if err != nil {
			echoed <- err
			return
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(str, buf); err != nil {
			echoed <- err
			return
		}
		_, err = str.Write(append([]byte(caller.Subject+":"), buf...))
		_ = str.Close()
		echoed <- err
		// The session ends when the handler returns, so hold it until the
		// client has read the answer and closed it.
		<-sess.Context().Done()
	}
	fwd := replicaforward.Config{
		Locator: replicaforward.NewLocator(replicaforwardtest.Members{}, replicaforward.NewLeaderCache(), "raft-a", nil),
		RootCAs: x509.NewCertPool(),
	}
	srv, pool := startWTServer(t, WithReplicaForwarding(fwd), WithWebTransportHandler(wtTestPath, echo))
	if srv.replicaForwarder == nil {
		t.Fatal("replica forwarding is not installed; the test would not cover the clustered wrapper")
	}
	status, sess, err := dialWT(t, srv, pool, wtTestPath, "Bearer good")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	str, err := sess.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("OpenStreamSync: %v", err)
	}
	if _, err := str.Write([]byte("ping")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := str.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := io.ReadAll(str)
	if err != nil {
		t.Fatalf("ReadAll: %v (session: %v)", err, context.Cause(sess.Context()))
	}
	if string(got) != "alice:ping" {
		t.Fatalf("stream answered %q, want %q", got, "alice:ping")
	}
	if err := <-echoed; err != nil {
		t.Fatalf("handler: %v", err)
	}
}

func TestWebTransport_UnauthenticatedSessionRefusedBeforeTheHandler(t *testing.T) {
	h := newWTRecorder()
	srv, pool := startWTServer(t, WithWebTransportHandler(wtTestPath, h.handle))
	for name, authorization := range map[string]string{"no credentials": "", "bad credentials": "Bearer bad"} {
		t.Run(name, func(t *testing.T) {
			status, sess, err := dialWT(t, srv, pool, wtTestPath, authorization)
			if err == nil || sess != nil {
				t.Fatal("an unauthenticated session was established")
			}
			if status != http.StatusUnauthorized {
				t.Fatalf("status %d, want REST's 401", status)
			}
		})
	}
	if n := h.calls.Load(); n != 0 {
		t.Fatalf("handler ran %d times for unauthenticated sessions", n)
	}
}

// TestWebTransport_RESTLayersRefuseBeforeTheHandler proves the session's
// extended CONNECT passes every other REST layer, not only authentication:
// an authenticated CONNECT over real QUIC is refused with the status REST
// gives while the sealed window is closed, while the admin credential is
// awaited and once the client is over its limit, and the handler never runs.
func TestWebTransport_RESTLayersRefuseBeforeTheHandler(t *testing.T) {
	awaiting := &stubAwaitingProbe{}
	awaiting.awaiting.Store(true)
	for name, tc := range map[string]struct {
		opt      ServerOption
		admitted int
		want     int
	}{
		"sealed window":             {opt: WithSealedWindowProbe(newFakeSealed(true)), want: http.StatusServiceUnavailable},
		"awaiting admin credential": {opt: WithAwaitingAdminCredentialProbe(awaiting), want: http.StatusServiceUnavailable},
		"client limit":              {opt: WithClientLimiter(newRESTClientLimiter(t, 1, nil)), admitted: 1, want: http.StatusTooManyRequests},
	} {
		t.Run(name, func(t *testing.T) {
			h := newWTRecorder()
			srv, pool := startWTServer(t, WithWebTransportHandler(wtTestPath, h.handle), tc.opt)
			for i := range tc.admitted {
				status, sess, err := dialWT(t, srv, pool, wtTestPath, "Bearer good")
				if err != nil || sess == nil || status != http.StatusOK {
					t.Fatalf("admitted dial %d: status %d, err %v, want an established session", i, status, err)
				}
				<-h.sessions
			}
			status, sess, err := dialWT(t, srv, pool, wtTestPath, "Bearer good")
			if err == nil || sess != nil {
				t.Fatal("a session was established through the refusing layer")
			}
			if status != tc.want {
				t.Fatalf("status %d, want REST's %d", status, tc.want)
			}
			if n := h.calls.Load(); n != int32(tc.admitted) {
				t.Fatalf("handler ran %d times, want %d", n, tc.admitted)
			}
		})
	}
}

// TestWebTransport_TCPListenersDoNotSpeakWebTransport proves the HTTP/1.1
// and HTTP/2 listeners of a started server refuse an authenticated CONNECT
// on the session route without running the handler: WebTransport is served
// over HTTP/3 only.
func TestWebTransport_TCPListenersDoNotSpeakWebTransport(t *testing.T) {
	h := newWTRecorder()
	srv, pool := startWTServer(t, WithWebTransportHandler(wtTestPath, h.handle))
	clientTLS := &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13}
	tcp := func(set func(*http.Protocols)) http.RoundTripper {
		p := new(http.Protocols)
		set(p)
		return &http.Transport{TLSClientConfig: clientTLS.Clone(), Protocols: p}
	}
	for name, tc := range map[string]struct {
		rt    http.RoundTripper
		major int
		want  int
	}{
		// HTTP/1.1 carries the path, so the CONNECT reaches the route and
		// serveWebTransport refuses it as not HTTP/3.
		"HTTP/1.1": {rt: tcp(func(p *http.Protocols) { p.SetHTTP1(true) }), major: 1, want: http.StatusBadRequest},
		// net/http's HTTP/2 client refuses a :protocol pseudo-header, so it
		// cannot send an extended CONNECT (RFC 8441): HTTP/2 sends a plain
		// CONNECT, which has no :path and matches no route.
		"HTTP/2": {rt: tcp(func(p *http.Protocols) { p.SetHTTP2(true) }), major: 2, want: http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodConnect, "https://"+srv.Addr()+wtTestPath, nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Authorization", "Bearer good")
			resp, err := (&http.Client{Transport: tc.rt}).Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			_ = resp.Body.Close()
			if resp.ProtoMajor != tc.major {
				t.Fatalf("served over HTTP/%d, want HTTP/%d", resp.ProtoMajor, tc.major)
			}
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
	if n := h.calls.Load(); n != 0 {
		t.Fatalf("handler ran %d times for CONNECTs on the TCP listeners", n)
	}
}

func TestWebTransport_OtherPathNotUpgraded(t *testing.T) {
	h := newWTRecorder()
	srv, pool := startWTServer(t, WithWebTransportHandler(wtTestPath, h.handle))
	status, sess, err := dialWT(t, srv, pool, "/not-wt", "Bearer good")
	if err == nil || sess != nil {
		t.Fatal("a CONNECT to another path was upgraded")
	}
	if status == 0 || status/100 == 2 {
		t.Fatalf("status %d, want a refusal", status)
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
		path   string
		h      WebTransportHandler
		public bool
	}{
		"nil handler":   {path: wtTestPath},
		"relative path": {path: "wt", h: h.handle},
		"empty path":    {path: "", h: h.handle},
		// The middleware exempts these from authentication for every
		// method, so a CONNECT there could never carry a caller.
		"public /v1/ca/bootstrap":    {path: "/v1/ca/bootstrap", h: h.handle, public: true},
		"public /v1/cluster/nodekey": {path: "/v1/cluster/nodekey", h: h.handle, public: true},
	} {
		t.Run(name, func(t *testing.T) {
			srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTLS(tlsCfg), WithWebTransportHandler(tc.path, tc.h))
			err := srv.Start()
			var target *InvalidWebTransportHandlerError
			if !errors.As(err, &target) {
				srv.Stop()
				t.Fatalf("Start = %v, want *InvalidWebTransportHandlerError", err)
			}
			if target.Path != tc.path || target.NilHandler != (tc.h == nil) || target.PublicPath != tc.public || !strings.Contains(err.Error(), "WebTransport") {
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
	for _, err := range []error{&WebTransportUnauthenticatedError{}, cause, up, &InvalidWebTransportHandlerError{Path: "x"}, &InvalidWebTransportHandlerError{Path: "/x", NilHandler: true}, &InvalidWebTransportHandlerError{Path: "/health", PublicPath: true}, &WebTransportClientRequirementError{Requirement: "SETTINGS_WT_ENABLED"}} {
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

// TestWebTransportClientRequirementsMet_NotHTTP3 covers the guard's refusal of
// a request that did not come from the WebTransport listener: its writer is
// not an HTTP/3 one and its context carries no QUIC connection.
func TestWebTransportClientRequirementsMet_NotHTTP3(t *testing.T) {
	err := webTransportClientRequirementsMet(httptest.NewRecorder(), httptest.NewRequest(http.MethodConnect, wtTestPath, nil), nil)
	var target *WebTransportNotHTTP3Error
	if !errors.As(err, &target) {
		t.Fatalf("err %v, want *WebTransportNotHTTP3Error", err)
	}
}

// withWebTransportSettingsDeadline replaces the handshake timer of the
// client-SETTINGS wait with c, so a test fires the deadline itself.
func withWebTransportSettingsDeadline(c <-chan time.Time) ServerOption {
	return func(s *Server) { s.webTransportSettingsDeadline = c }
}

// TestNewWebTransportServer_UpgradeSharesTheHandshakeTimeout pins Upgrade's
// own SETTINGS wait to the constant that bounds ours.
func TestNewWebTransportServer_UpgradeSharesTheHandshakeTimeout(t *testing.T) {
	if got := newWebTransportServer(&http3.Server{}).wt.ReorderingTimeout; got != webTransportHandshakeTimeout {
		t.Fatalf("ReorderingTimeout %v, want webTransportHandshakeTimeout %v", got, webTransportHandshakeTimeout)
	}
}

// qpackString appends s as a QPACK string literal without Huffman coding,
// its length an integer with the given prefix (RFC 9204 section 4.1.2).
func qpackString(b []byte, flags byte, prefix uint, s string) []byte {
	b = qpackInt(b, flags, prefix, uint64(len(s)))
	return append(b, s...)
}

// qpackInt appends v as a QPACK prefixed integer (RFC 9204 section 4.1.1).
func qpackInt(b []byte, flags byte, prefix uint, v uint64) []byte {
	limit := uint64(1)<<prefix - 1
	if v < limit {
		return append(b, flags|byte(v))
	}
	b = append(b, flags|byte(limit))
	for v -= limit; v >= 0x80; v >>= 7 {
		b = append(b, byte(v)|0x80)
	}
	return append(b, byte(v))
}

// TestWebTransport_ConnectWithoutSettingsRejectedAtTheDeadline sends an
// authenticated draft-16 CONNECT on a connection that never opens its
// control stream, so the server never receives the client's SETTINGS. When
// the handshake deadline fires, the request stream is reset with
// H3_REQUEST_REJECTED and no session handler runs.
func TestWebTransport_ConnectWithoutSettingsRejectedAtTheDeadline(t *testing.T) {
	h := newWTRecorder()
	deadline := make(chan time.Time)
	srv, pool := startWTServer(t, WithWebTransportHandler(wtTestPath, h.handle), withWebTransportSettingsDeadline(deadline))

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, srv.Addr(), wtClientTLS(pool), &quic.Config{
		EnableDatagrams:                  true,
		EnableStreamResetPartialDelivery: true,
	})
	if err != nil {
		t.Fatalf("DialAddr: %v", err)
	}
	defer func() { _ = conn.CloseWithError(0, "") }()
	str, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("OpenStreamSync: %v", err)
	}

	// The field section (RFC 9204 section 4.5): a zero Required Insert Count
	// and Base, then static-table references (:method CONNECT 15, :scheme
	// https 23, :authority 0, :path 1, authorization 84) and :protocol by
	// literal name.
	fields := []byte{0x00, 0x00, 0xc0 | 15, 0xc0 | 23}
	fields = qpackString(qpackInt(fields, 0x50, 4, 0), 0, 7, srv.Addr())
	fields = qpackString(qpackInt(fields, 0x50, 4, 1), 0, 7, wtTestPath)
	fields = qpackString(qpackString(fields, 0x20, 3, ":protocol"), 0, 7, validWTHandshake.protocol)
	fields = qpackString(qpackInt(fields, 0x50, 4, 84), 0, 7, "Bearer good")
	frame := quicvarint.Append(quicvarint.Append(nil, 0x01), uint64(len(fields)))
	if _, err := str.Write(append(frame, fields...)); err != nil {
		t.Fatalf("write HEADERS: %v", err)
	}

	close(deadline)
	str.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, err = io.ReadAll(str)
	var streamErr *quic.StreamError
	if !errors.As(err, &streamErr) || streamErr.ErrorCode != quic.StreamErrorCode(http3.ErrCodeRequestRejected) || !streamErr.Remote {
		t.Fatalf("read: %v; want the server's H3_REQUEST_REJECTED stream reset", err)
	}
	if n := h.calls.Load(); n != 0 {
		t.Fatalf("handler ran %d times, want 0", n)
	}
}

// stopSendingTrace is a client-side qlog trace that records the error code of
// every STOP_SENDING frame the server sends, by stream.
type stopSendingTrace struct {
	mu    sync.Mutex
	codes map[quic.StreamID]quic.StreamErrorCode
}

func (s *stopSendingTrace) AddProducer() qlogwriter.Recorder { return s }
func (s *stopSendingTrace) Close() error                     { return nil }

func (s *stopSendingTrace) RecordEvent(ev qlogwriter.Event) {
	p, ok := ev.(qlog.PacketReceived)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range p.Frames {
		switch frame := f.Frame.(type) {
		case *qlog.StopSendingFrame:
			s.codes[frame.StreamID] = frame.ErrorCode
		case qlog.StopSendingFrame:
			s.codes[frame.StreamID] = frame.ErrorCode
		}
	}
}

// rejected returns the number of streams the server stopped, failing on any
// code other than WT_BUFFERED_STREAM_REJECTED.
func (s *stopSendingTrace) rejected(t *testing.T) int {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, code := range s.codes {
		if code != webtransport.WTBufferedStreamRejectedErrorCode {
			t.Fatalf("stream %d stopped with %#x, want WT_BUFFERED_STREAM_REJECTED", id, code)
		}
	}
	return len(s.codes)
}

// TestWebTransport_PreSessionStreamFloodIsBounded floods WebTransport uni
// streams (header and FIN) for a session that was never requested: the
// server buffers at most a fixed number per session, rejects the rest with
// WT_BUFFERED_STREAM_REJECTED (draft-ietf-webtrans-http3-16 section 4.6), and
// drops the buffered ones at the deadline the first stream set.
func TestWebTransport_PreSessionStreamFloodIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// maxBufferedStreamsPerSession in internal/webtransport.
		const perSessionCap = 16
		const excess = 8
		const settle = 10 * time.Millisecond

		n := &simnet.Simnet{Router: &simnet.PerfectRouter{}}
		link := simnet.NodeBiDiLinkSettings{Latency: time.Millisecond}
		clientPC := n.NewEndpoint(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9001}, link)
		serverPC := n.NewEndpoint(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9002}, link)
		if err := n.Start(); err != nil {
			t.Fatalf("simnet: %v", err)
		}
		defer func() {
			_ = clientPC.Close()
			_ = serverPC.Close()
			_ = n.Close()
		}()

		tlsCfg, pool := generateTestTLSConfig(t)
		ln, err := quic.ListenEarly(serverPC, http3.ConfigureTLSConfig(tlsCfg), http3QUICConfig(true))
		if err != nil {
			t.Fatalf("ListenEarly: %v", err)
		}
		wt := newWebTransportServer(&http3.Server{Handler: http.NotFoundHandler(), QUICConfig: http3QUICConfig(true)})
		go func() { _ = wt.serveListener(ln) }()
		defer func() {
			_ = wt.wt.Close()
			_ = ln.Close()
		}()

		trace := &stopSendingTrace{codes: map[quic.StreamID]quic.StreamErrorCode{}}
		// The fake clock runs past the handshake deadline, so no timeout here.
		ctx := t.Context()
		conn, err := quic.Dial(ctx, clientPC, serverPC.LocalAddr(), wtClientTLS(pool), &quic.Config{
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
			Tracer: func(context.Context, bool, quic.ConnectionID) qlogwriter.Trace {
				return trace
			},
		})
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		defer func() { _ = conn.CloseWithError(0, "") }()

		// WT_STREAM (0x54) for session 4, a client bidi stream never opened,
		// and one payload byte: a stream read up to its FIN is complete and
		// gets no STOP_SENDING, so the unread byte makes rejection visible.
		data := append(quicvarint.Append(quicvarint.Append(nil, 0x54), 4), 0)
		flood := func(count int) {
			t.Helper()
			for range count {
				str, err := conn.OpenUniStreamSync(ctx)
				if err != nil {
					t.Fatalf("OpenUniStreamSync: %v", err)
				}
				if _, err := str.Write(data); err != nil {
					t.Fatalf("write: %v", err)
				}
				if err := str.Close(); err != nil {
					t.Fatalf("close: %v", err)
				}
			}
			time.Sleep(settle)
			synctest.Wait()
		}

		start := time.Now()
		flood(1)
		time.Sleep(webTransportHandshakeTimeout / 2)
		flood(perSessionCap - 1)
		if got := trace.rejected(t); got != 0 {
			t.Fatalf("%d streams rejected below the cap, want 0", got)
		}

		flood(excess)
		if got := trace.rejected(t); got != excess {
			t.Fatalf("%d streams rejected past the cap, want %d", got, excess)
		}

		// The buffered streams expire at the deadline the first one set; the
		// later ones do not extend it.
		time.Sleep(time.Until(start.Add(webTransportHandshakeTimeout - settle)))
		synctest.Wait()
		if got := trace.rejected(t); got != excess {
			t.Fatalf("%d streams rejected before the deadline, want %d", got, excess)
		}
		time.Sleep(2 * settle)
		synctest.Wait()
		if got := trace.rejected(t); got != perSessionCap+excess {
			t.Fatalf("%d streams rejected after the deadline, want %d", got, perSessionCap+excess)
		}
	})
}

// TestWebTransport_SessionStreamLimitsConfigured holds the REST integration
// to a WebTransport Config with incoming stream and data limits: they are
// advertised as SETTINGS_WT_INITIAL_MAX_* (draft-ietf-webtrans-http3-16
// section 5) and bound each session's streams waiting to be accepted, also
// when the peer does not enable flow control.
func TestWebTransport_SessionStreamLimitsConfigured(t *testing.T) {
	wt := newWebTransportServer(&http3.Server{})
	cfg := wt.wt.Config
	if cfg == nil {
		t.Fatal("webtransport.Server.Config is nil; sessions take streams without limit")
	}
	if cfg.MaxIncomingStreams <= 0 || cfg.MaxIncomingUniStreams <= 0 || cfg.MaxIncomingData <= 0 {
		t.Fatalf("Config = %+v; want positive MaxIncomingStreams, MaxIncomingUniStreams and MaxIncomingData", *cfg)
	}
}

// TestNewWebTransportServer_ChainsConnContext pins that the connection is
// added on top of the ConnContext the server already had, which still runs.
func TestNewWebTransportServer_ChainsConnContext(t *testing.T) {
	type prevKey struct{}
	for name, tc := range map[string]struct {
		h3      *http3.Server
		hadPrev bool
	}{
		"no previous": {h3: &http3.Server{}},
		"previous": {h3: &http3.Server{ConnContext: func(ctx context.Context, _ *quic.Conn) context.Context {
			return context.WithValue(ctx, prevKey{}, true)
		}}, hadPrev: true},
	} {
		t.Run(name, func(t *testing.T) {
			h3, hadPrev := tc.h3, tc.hadPrev
			newWebTransportServer(h3)
			conn := new(quic.Conn)
			ctx := h3.ConnContext(context.Background(), conn)
			if got, _ := ctx.Value(quicConnContextKey{}).(*quic.Conn); got != conn {
				t.Fatal("the connection is not in the context")
			}
			if ran, _ := ctx.Value(prevKey{}).(bool); ran != hadPrev {
				t.Fatalf("previous ConnContext ran = %v, want %v", ran, hadPrev)
			}
		})
	}
}

// TestWebTransport_PathBesidePublicOneServesSessions pins that the public-path
// refusal is the middleware's exact match, not a prefix: a path under /health
// is authenticated, so it upgrades.
func TestWebTransport_PathBesidePublicOneServesSessions(t *testing.T) {
	h := newWTRecorder()
	srv, pool := startWTServer(t, WithWebTransportHandler("/health/wt", h.handle))
	status, _, err := dialWT(t, srv, pool, "/health/wt", "Bearer good")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	select {
	case got := <-h.sessions:
		if got.subject != "alice" {
			t.Fatalf("handler saw %+v, want alice", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the session never reached the handler")
	}
}

// wtHandshake is what a raw client sends of draft-ietf-webtrans-http3-16
// section 3.1's client requirements.
type wtHandshake struct {
	protocol      string // the extended CONNECT :protocol
	wtEnabled     bool   // SETTINGS_WT_ENABLED = 1, the draft's version codepoint
	h3Datagram    bool   // SETTINGS_H3_DATAGRAM = 1
	quicDatagram  bool   // the max_datagram_frame_size transport parameter
	resetStreamAt bool   // the reset_stream_at transport parameter
}

// validWTHandshake meets every client requirement of draft-16 section 3.1.
var validWTHandshake = wtHandshake{protocol: "webtransport-h3", wtEnabled: true, h3Datagram: true, quicDatagram: true, resetStreamAt: true}

// rawWTConnect sends one authenticated extended CONNECT to wtTestPath over a
// bare HTTP/3 client connection that advertises what hs says, and returns the
// status with the body of a refusal, or the error that ended the request
// before a status arrived.
func rawWTConnect(t *testing.T, srv *Server, pool *x509.CertPool, hs wtHandshake) (int, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, srv.Addr(), wtClientTLS(pool), &quic.Config{
		EnableDatagrams:                  hs.quicDatagram,
		EnableStreamResetPartialDelivery: hs.resetStreamAt,
	})
	if err != nil {
		return 0, "", err
	}
	t.Cleanup(func() { _ = conn.CloseWithError(0, "") })
	settings := map[uint64]uint64{}
	if hs.wtEnabled {
		settings[settingsWTEnabled] = 1
	}
	cc := (&http3.Transport{EnableDatagrams: hs.h3Datagram, AdditionalSettings: settings}).NewClientConn(conn)
	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, "https://"+srv.Addr()+wtTestPath, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Proto = hs.protocol
	req.Header.Set("Authorization", "Bearer good")
	resp, err := cc.RoundTrip(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		// The body of an accepted CONNECT is the session stream.
		return resp.StatusCode, "", nil
	}
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), err
}

// TestWebTransport_NegotiationFailureOpensNoSession starts from a handshake
// that meets every client requirement of draft-ietf-webtrans-http3-16 section
// 3.1 and breaks exactly one per case: the :protocol, the version it names,
// SETTINGS_WT_ENABLED, SETTINGS_H3_DATAGRAM, and the max_datagram_frame_size
// and reset_stream_at transport parameters. None opens a session. A wrong
// :protocol token is Upgrade's 400; a missing SETTINGS_WT_ENABLED,
// SETTINGS_H3_DATAGRAM or reset_stream_at is a malformed CONNECT whose stream
// is reset with H3_MESSAGE_ERROR; SETTINGS_H3_DATAGRAM without
// max_datagram_frame_size closes the connection with H3_SETTINGS_ERROR.
func TestWebTransport_NegotiationFailureOpensNoSession(t *testing.T) {
	h := newWTRecorder()
	srv, pool := startWTServer(t, WithWebTransportHandler(wtTestPath, h.handle))

	// The baseline: the valid handshake opens a session, so each case below
	// fails for the one requirement it breaks.
	if status, body, err := rawWTConnect(t, srv, pool, validWTHandshake); err != nil || status != http.StatusOK {
		t.Fatalf("valid draft-16 handshake: status %d (%s), err %v; want 200", status, body, err)
	}
	select {
	case <-h.sessions:
	case <-time.After(10 * time.Second):
		t.Fatal("the valid handshake's session never reached the handler")
	}

	broken := func(f func(*wtHandshake)) wtHandshake {
		hs := validWTHandshake
		f(&hs)
		return hs
	}
	// refusal is REST's 400 whose body names the cause.
	refusal := func(cause string) func(*testing.T, int, string, error) {
		return func(t *testing.T, status int, body string, err error) {
			if err != nil || status != http.StatusBadRequest || !strings.Contains(body, cause) {
				t.Fatalf("status %d (%s), err %v; want 400 naming %q", status, body, err, cause)
			}
		}
	}
	// malformed is the CONNECT stream reset with H3_MESSAGE_ERROR
	// (draft-ietf-webtrans-http3-16 section 3.1, RFC 9114 section 4.1.2).
	malformed := func(t *testing.T, status int, body string, err error) {
		var streamErr *quic.StreamError
		if !errors.As(err, &streamErr) || streamErr.ErrorCode != quic.StreamErrorCode(http3.ErrCodeMessageError) || !streamErr.Remote {
			t.Fatalf("status %d (%s), err %v; want the server's H3_MESSAGE_ERROR stream reset", status, body, err)
		}
	}
	for name, tc := range map[string]struct {
		hs   wtHandshake
		want func(*testing.T, int, string, error)
	}{
		"other protocol":      {broken(func(hs *wtHandshake) { hs.protocol = "connect-udp" }), refusal("unexpected protocol")},
		"unsupported version": {broken(func(hs *wtHandshake) { hs.protocol = "webtransport-h4" }), refusal("unexpected protocol")},
		"legacy protocol":     {broken(func(hs *wtHandshake) { hs.protocol = "webtransport" }), refusal("unexpected protocol")},
		"no WT_ENABLED":       {broken(func(hs *wtHandshake) { hs.wtEnabled = false }), malformed},
		"no H3_DATAGRAM":      {broken(func(hs *wtHandshake) { hs.h3Datagram = false }), malformed},
		"no reset_stream_at":  {broken(func(hs *wtHandshake) { hs.resetStreamAt = false }), malformed},
		// quic-go's http3.Server closes the connection: SETTINGS_H3_DATAGRAM
		// without the max_datagram_frame_size transport parameter is
		// H3_SETTINGS_ERROR (RFC 9297 section 2.1.1).
		"no QUIC datagrams": {broken(func(hs *wtHandshake) { hs.quicDatagram = false }), func(t *testing.T, status int, body string, err error) {
			var appErr *quic.ApplicationError
			if !errors.As(err, &appErr) || appErr.ErrorCode != quic.ApplicationErrorCode(http3.ErrCodeSettingsError) || !appErr.Remote {
				t.Fatalf("status %d (%s), err %v; want the server's H3_SETTINGS_ERROR", status, body, err)
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			status, body, err := rawWTConnect(t, srv, pool, tc.hs)
			tc.want(t, status, body, err)
		})
	}
	if n := h.calls.Load(); n != 1 {
		t.Fatalf("handler ran %d times, want once, for the valid handshake alone", n)
	}
}

// TestWebTransportClientRequirements_EachIsRequired checks that a client
// meeting every draft-ietf-webtrans-http3-16 section 3.1 requirement passes
// each entry of webTransportClientRequirements, and that dropping one fails
// exactly the entry that names it.
func TestWebTransportClientRequirements_EachIsRequired(t *testing.T) {
	full := func() (*http3.Settings, quic.ConnectionState) {
		var cs quic.ConnectionState
		cs.SupportsStreamResetPartialDelivery.Remote = true
		return &http3.Settings{EnableDatagrams: true, Other: map[uint64]uint64{settingsWTEnabled: 1}}, cs
	}
	s, cs := full()
	for _, req := range webTransportClientRequirements {
		if !req.met(s, cs) {
			t.Fatalf("%s not met by a client that sends every requirement", req.name)
		}
	}
	for name, tc := range map[string]struct {
		breakIt func(*http3.Settings, *quic.ConnectionState)
		fails   string
	}{
		"WT_ENABLED absent": {func(s *http3.Settings, _ *quic.ConnectionState) { delete(s.Other, settingsWTEnabled) }, "SETTINGS_WT_ENABLED"},
		"WT_ENABLED=2":      {func(s *http3.Settings, _ *quic.ConnectionState) { s.Other[settingsWTEnabled] = 2 }, "SETTINGS_WT_ENABLED"},
		"no H3_DATAGRAM":    {func(s *http3.Settings, _ *quic.ConnectionState) { s.EnableDatagrams = false }, "SETTINGS_H3_DATAGRAM"},
		"no reset_stream_at": {func(_ *http3.Settings, cs *quic.ConnectionState) {
			cs.SupportsStreamResetPartialDelivery.Remote = false
		}, "the reset_stream_at transport parameter"},
	} {
		t.Run(name, func(t *testing.T) {
			s, cs := full()
			tc.breakIt(s, &cs)
			var failed []string
			for _, req := range webTransportClientRequirements {
				if !req.met(s, cs) {
					failed = append(failed, req.name)
				}
			}
			if len(failed) != 1 || failed[0] != tc.fails {
				t.Fatalf("failed requirements %q, want exactly [%q]", failed, tc.fails)
			}
		})
	}
}

// panicLog is a slog handler that records whether http3's handler panic
// recovery logged "http3: panic serving".
type panicLog struct{ panicked atomic.Bool }

func (h *panicLog) Enabled(context.Context, slog.Level) bool { return true }
func (h *panicLog) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *panicLog) WithGroup(string) slog.Handler            { return h }
func (h *panicLog) Handle(_ context.Context, r slog.Record) error {
	if strings.Contains(r.Message, "panic serving") {
		h.panicked.Store(true)
	}
	return nil
}

// TestWebTransport_SecondSessionWithoutFlowControlRejected sends a second
// CONNECT on a connection whose first session runs without WebTransport flow
// control. Upgrade takes the second request stream over and resets it with
// H3_REQUEST_REJECTED; the handler must then write nothing (WriteHeader after
// HTTPStream panics), and the first session keeps working.
func TestWebTransport_SecondSessionWithoutFlowControlRejected(t *testing.T) {
	// http3.Server logs a recovered handler panic to slog.Default when it has
	// no Logger, as the REST server's has not.
	logs := &panicLog{}
	prev := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(prev) })

	var calls atomic.Int32
	started := make(chan struct{}, 2)
	echoed := make(chan error, 1)
	echo := func(sess *WebTransportSession, _ *auth.Identity, _ string) {
		calls.Add(1)
		started <- struct{}{}
		ctx, cancel := context.WithTimeout(sess.Context(), 10*time.Second)
		defer cancel()
		str, err := sess.AcceptStream(ctx)
		if err != nil {
			echoed <- err
			return
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(str, buf); err != nil {
			echoed <- err
			return
		}
		_, err = str.Write(buf)
		_ = str.Close()
		echoed <- err
		<-sess.Context().Done()
	}
	srv, pool := startWTServer(t, WithWebTransportHandler(wtTestPath, echo))

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, srv.Addr(), wtClientTLS(pool), &quic.Config{
		EnableDatagrams:                  true,
		EnableStreamResetPartialDelivery: true,
	})
	if err != nil {
		t.Fatalf("DialAddr: %v", err)
	}
	defer func() { _ = conn.CloseWithError(0, "") }()
	// No SETTINGS_WT_INITIAL_MAX_*: the sessions run without flow control.
	cc := (&http3.Transport{EnableDatagrams: true, AdditionalSettings: map[uint64]uint64{settingsWTEnabled: 1}}).NewClientConn(conn)
	connect := func() (*http3.RequestStream, *http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodConnect, "https://"+srv.Addr()+wtTestPath, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Proto = validWTHandshake.protocol
		req.Header.Set("Authorization", "Bearer good")
		str, err := cc.OpenRequestStream(ctx)
		if err != nil {
			t.Fatalf("OpenRequestStream: %v", err)
		}
		if err := str.SendRequestHeader(req); err != nil {
			t.Fatalf("SendRequestHeader: %v", err)
		}
		rsp, err := str.ReadResponse()
		return str, rsp, err
	}

	first, firstRsp, err := connect()
	if err != nil || firstRsp.StatusCode != http.StatusOK {
		t.Fatalf("first CONNECT: %v, %v; want 200", firstRsp, err)
	}
	// The body is the session's CONNECT stream: close it only when the test ends.
	defer func() { _ = firstRsp.Body.Close() }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("the first session never reached the handler")
	}

	_, rsp, err := connect()
	if rsp != nil {
		_ = rsp.Body.Close()
	}
	var streamErr *quic.StreamError
	if !errors.As(err, &streamErr) || streamErr.ErrorCode != quic.StreamErrorCode(http3.ErrCodeRequestRejected) || !streamErr.Remote {
		t.Fatalf("second CONNECT: %v, %v; want the server's H3_REQUEST_REJECTED stream reset", rsp, err)
	}

	// The first session still carries a bidirectional stream: WT_STREAM
	// (0x41) and its session ID, the first CONNECT's stream ID.
	str, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("OpenStreamSync: %v", err)
	}
	hdr := quicvarint.Append(quicvarint.Append(nil, 0x41), uint64(first.StreamID()))
	if _, err := str.Write(append(hdr, "ping"...)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := str.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	str.SetReadDeadline(time.Now().Add(10 * time.Second))
	got, err := io.ReadAll(str)
	if err != nil || string(got) != "ping" {
		t.Fatalf("first session's stream answered %q, %v; want %q", got, err, "ping")
	}
	if err := <-echoed; err != nil {
		t.Fatalf("handler: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("handler ran %d times, want once", n)
	}
	if logs.panicked.Load() {
		t.Fatal("the handler panicked after the refusal took the stream over")
	}
}

// TestWebTransport_CrossOriginRefused covers the origin check: the server sets
// no CheckOrigin, so webtransport-go's same-origin default applies, and an
// authenticated CONNECT from another origin is refused before any session.
func TestWebTransport_CrossOriginRefused(t *testing.T) {
	h := newWTRecorder()
	srv, pool := startWTServer(t, WithWebTransportHandler(wtTestPath, h.handle))
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer good")
	hdr.Set("Origin", "https://attacker.example")
	status, sess, err := dialWTHeader(t, srv, pool, wtTestPath, hdr)
	if err == nil || sess != nil {
		t.Fatal("a cross-origin session was established")
	}
	if status != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", status)
	}
	if n := h.calls.Load(); n != 0 {
		t.Fatalf("handler ran %d times for a cross-origin session", n)
	}
	hdr.Set("Origin", "https://"+srv.Addr())
	if status, _, err := dialWTHeader(t, srv, pool, wtTestPath, hdr); err != nil || status != http.StatusOK {
		t.Fatalf("same-origin Dial = %d, %v; want 200", status, err)
	}
}

// TestWebTransport_StopClosesActiveSessionAndStream stops the server while a
// session has an open stream: Stop returns, both ends see the session and the
// stream close, and the handler goroutine returns rather than leaking. The
// module carries no goroutine-leak detector, so the handler's return, which
// is the goroutine this package starts per session, is observed directly.
func TestWebTransport_StopClosesActiveSessionAndStream(t *testing.T) {
	accepted := make(chan struct{})
	failed := make(chan error, 1)
	ended := make(chan error, 1)
	h := func(sess *WebTransportSession, _ *auth.Identity, _ string) {
		ctx, cancel := context.WithTimeout(sess.Context(), 10*time.Second)
		defer cancel()
		str, err := sess.AcceptStream(ctx)
		if err != nil {
			failed <- err
			return
		}
		buf := make([]byte, 1)
		if _, err := io.ReadFull(str, buf); err != nil {
			failed <- err
			return
		}
		close(accepted)
		<-sess.Context().Done()
		_, err = str.Read(buf)
		ended <- err
	}
	srv, pool := startWTServer(t, WithWebTransportHandler(wtTestPath, h))
	status, sess, err := dialWT(t, srv, pool, wtTestPath, "Bearer good")
	if err != nil || status != http.StatusOK {
		t.Fatalf("Dial = %d, %v; want 200", status, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	str, err := sess.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("OpenStreamSync: %v", err)
	}
	if _, err := str.Write([]byte{1}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	select {
	case <-accepted:
	case err := <-failed:
		t.Fatalf("handler: %v", err)
	case <-ctx.Done():
		t.Fatal("the handler never accepted the stream")
	}

	stopped := make(chan struct{})
	go func() {
		srv.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("Stop did not return with a session open")
	}
	select {
	case <-sess.Context().Done():
	case <-ctx.Done():
		t.Fatal("the client never saw the session close")
	}
	if _, err := str.Read(make([]byte, 1)); err == nil {
		t.Fatal("the client's stream is still readable after Stop")
	}
	select {
	case err := <-ended:
		if err == nil {
			t.Fatal("the handler's stream is still readable after Stop")
		}
	case <-ctx.Done():
		t.Fatal("the handler goroutine never returned after Stop")
	}
}

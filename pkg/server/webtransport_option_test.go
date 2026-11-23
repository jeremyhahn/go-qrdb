package server

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"github.com/jeremyhahn/go-qrdb/internal/webtransport"
	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// wtBearer accepts the one bearer token "Bearer good" as alice in tenant t1.
type wtBearer struct{}

func (wtBearer) Authenticate(ctx context.Context) (*auth.Identity, error) {
	if auth.HTTPAuthorizationFromContext(ctx) == "Bearer good" {
		return &auth.Identity{Subject: "alice", TenantID: "t1"}, nil
	}
	return nil, auth.ErrUnauthenticated
}

func (wtBearer) Name() string { return "bearer" }

// wtSeen is what the handler saw of one session.
type wtSeen struct{ subject, tenantID string }

// wtEchoHandler records each session's caller, echoes one bidirectional
// stream, and holds the session until it ends.
type wtEchoHandler struct {
	calls    atomic.Int32
	sessions chan wtSeen
}

func (h *wtEchoHandler) handle(sess *rest.WebTransportSession, caller *auth.Identity, tenantID string) {
	h.calls.Add(1)
	h.sessions <- wtSeen{subject: caller.Subject, tenantID: tenantID}
	str, err := sess.AcceptStream(sess.Context())
	if err != nil {
		return
	}
	if _, err := io.Copy(str, str); err == nil {
		_ = str.Close()
	}
	<-sess.Context().Done()
}

// dialServerWT opens a WebTransport session to path on srv's REST HTTP/3
// listener, sending authorization when it is not empty.
func dialServerWT(t *testing.T, ctx context.Context, srv *Server, path, authorization string) (int, *webtransport.Session, error) {
	t.Helper()
	d := &webtransport.Transport{TLSClientConfig: &tls.Config{
		RootCAs:    testServerCAPool(t),
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{http3.NextProtoH3},
	}}
	t.Cleanup(func() { _ = d.Close() })
	hdr := http.Header{}
	if authorization != "" {
		hdr.Set("Authorization", authorization)
	}
	resp, sess, err := d.Dial(ctx, "https://"+srv.RESTAddr()+path, hdr)
	status := 0
	if resp != nil {
		// The body of an accepted CONNECT is the session stream.
		status = resp.StatusCode
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	if sess != nil {
		t.Cleanup(func() { _ = sess.CloseWithError(0, "") })
	}
	return status, sess, err
}

// TestWithWebTransportHandler_ServedSessionReachesTheHandler proves the
// pkg/server wiring end to end: a started Server built with the option serves
// an authenticated session on its config-driven REST HTTP/3 listener, refuses
// a CONNECT without credentials before the handler, and Stop ends an open
// session.
func TestWithWebTransportHandler_ServedSessionReachesTheHandler(t *testing.T) {
	h := &wtEchoHandler{sessions: make(chan wtSeen, 4)}
	mw := authmw.New(allowAll{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(wtBearer{}))
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA), WithAuthMiddleware(mw), WithWebTransportHandler("/wt", h.handle))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(srv.Stop) }
	t.Cleanup(stop)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	status, sess, err := dialServerWT(t, ctx, srv, "/wt", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("CONNECT without a token: status %d (err %v), want 401", status, err)
	}
	if sess != nil && err == nil {
		t.Fatal("a session opened without a token")
	}

	status, sess, err = dialServerWT(t, ctx, srv, "/wt", "Bearer good")
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
	case <-ctx.Done():
		t.Fatal("the session never reached the handler")
	}

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
	echo, err := io.ReadAll(str)
	if err != nil || string(echo) != "ping" {
		t.Fatalf("echo = %q, %v; want ping", echo, err)
	}

	stop()
	if _, err := sess.AcceptStream(ctx); err == nil {
		t.Fatal("AcceptStream succeeded after Stop")
	}
	if ctx.Err() != nil {
		t.Fatal("the session did not end before the deadline after Stop")
	}
	if n := h.calls.Load(); n != 1 {
		t.Fatalf("handler ran %d times, want 1 (the unauthenticated CONNECT must not reach it)", n)
	}
}

// TestWithWebTransportHandler_EmptyPathNilHandlerRefusedAtStart pins that an
// explicitly supplied ("", nil) reaches REST's validation: Start refuses it
// rather than starting with WebTransport off.
func TestWithWebTransportHandler_EmptyPathNilHandlerRefusedAtStart(t *testing.T) {
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA), WithWebTransportHandler("", nil))
	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("Start accepted WithWebTransportHandler(\"\", nil)")
	}
	var e *rest.InvalidWebTransportHandlerError
	if !errors.As(err, &e) {
		t.Fatalf("Start = %v, want *rest.InvalidWebTransportHandlerError", err)
	}
	if e.Path != "" || !e.NilHandler {
		t.Fatalf("refusal = %+v, want the empty path and the nil handler", e)
	}
}

// TestWithWebTransportHandler_PublicPathRefusedAtStart pins that a path the
// authentication middleware exempts as public is refused through the server
// option as it is through REST's, and that a path beside it still starts.
func TestWithWebTransportHandler_PublicPathRefusedAtStart(t *testing.T) {
	h := func(*rest.WebTransportSession, *auth.Identity, string) {}
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA), WithWebTransportHandler("/v1/ca/bootstrap", h))
	err := srv.Start()
	var e *rest.InvalidWebTransportHandlerError
	if !errors.As(err, &e) {
		if err == nil {
			srv.Stop()
		}
		t.Fatalf("Start = %v, want *rest.InvalidWebTransportHandlerError", err)
	}
	if e.Path != "/v1/ca/bootstrap" || !e.PublicPath || e.NilHandler {
		t.Fatalf("refusal = %+v, want the public /v1/ca/bootstrap path", e)
	}

	srv = New(testConfig(t, true, true), newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA), WithWebTransportHandler("/health/wt", h))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start with a non-public path = %v", err)
	}
	srv.Stop()
}

// TestWithWebTransportHandler_OmittedLeavesWebTransportOff pins that without
// the option Start succeeds and the REST HTTP/3 listener offers no
// WebTransport: a client's dial is refused.
func TestWithWebTransportHandler_OmittedLeavesWebTransportOff(t *testing.T) {
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	if srv.opts.webTransportSet {
		t.Fatal("webTransportSet is true without the option")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d := &webtransport.Transport{TLSClientConfig: &tls.Config{
		RootCAs:    testServerCAPool(t),
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"h3"},
	}}
	t.Cleanup(func() { _ = d.Close() })
	resp, sess, err := d.Dial(ctx, "https://"+srv.RESTAddr()+"/wt", http.Header{})
	if sess != nil {
		_ = sess.CloseWithError(0, "")
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("a WebTransport session opened with the option omitted")
	}
}

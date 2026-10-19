package rest

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"github.com/jeremyhahn/go-qrdb/pkg/api/grpcweb"
	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// grpcBackend is a native gRPC http.Handler double: it records the request
// the gRPC-Web handler hands it and answers as (*grpc.Server).ServeHTTP
// does, with a message and an OK status trailer.
type grpcBackend struct {
	calls chan *http.Request
}

func newGRPCBackend() *grpcBackend {
	return &grpcBackend{calls: make(chan *http.Request, 1)}
}

func (b *grpcBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.calls <- r
	w.Header().Set("Content-Type", "application/grpc")
	w.Header().Add("Trailer", "Grpc-Status")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte{0, 0, 0, 0, 0})
	w.Header().Set("Grpc-Status", "0")
}

func grpcWebRequest(t *testing.T, base string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+grpcweb.PathPrefix+"qrdb.v1.QRDBService/Get", bytes.NewReader([]byte{0, 0, 0, 0, 0}))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/grpc-web+proto")
	return req
}

func TestGRPCWeb_OffByDefault(t *testing.T) {
	for name, options := range map[string]func(http.Handler) []ServerOption{
		"no option":   func(http.Handler) []ServerOption { return nil },
		"zero config": func(b http.Handler) []ServerOption { return []ServerOption{WithGRPCWeb(grpcweb.Config{}, b)} },
		"enabled unset": func(b http.Handler) []ServerOption {
			return []ServerOption{WithGRPCWeb(grpcweb.Config{AllowedOrigins: []string{"*"}}, b)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			backend := newGRPCBackend()
			opts := options(backend)
			srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", opts...)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, grpcWebRequest(t, ""))
			if len(backend.calls) != 0 {
				t.Fatal("a disabled gRPC-Web reached the gRPC backend")
			}
			if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/grpc-web") {
				t.Fatalf("a disabled gRPC-Web answered as gRPC-Web: %v", rec.Header())
			}
		})
	}
}

func TestGRPCWeb_EnabledDispatchesToTheBackend(t *testing.T) {
	backend := newGRPCBackend()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithGRPCWeb(grpcweb.Config{Enabled: true}, backend))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, grpcWebRequest(t, ""))
	if len(backend.calls) != 1 {
		t.Fatalf("status %d: the call did not reach the gRPC backend", rec.Code)
	}
	if got := (<-backend.calls).URL.Path; got != "/qrdb.v1.QRDBService/Get" {
		t.Errorf("backend path = %q, want the gRPC method path", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/grpc-web+proto" {
		t.Errorf("Content-Type = %q, want application/grpc-web+proto", got)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if len(backend.calls) != 0 || rec.Code != http.StatusOK {
		t.Errorf("/health = %d, backend calls %d: REST routes must still be served", rec.Code, len(backend.calls))
	}
}

// TestGRPCWeb_EveryHTTPVersion carries one call over each of the server's
// listeners: HTTP/1.1 and HTTP/2 on TCP, HTTP/3 on QUIC.
func TestGRPCWeb_EveryHTTPVersion(t *testing.T) {
	tlsCfg, pool := generateTestTLSConfig(t)
	backend := newGRPCBackend()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), dualStackAddrOn(t, "127.0.0.1"), WithTLS(tlsCfg), WithGRPCWeb(grpcweb.Config{Enabled: true}, backend))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	clientTLS := &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13}
	tcp := func(set func(*http.Protocols)) http.RoundTripper {
		p := new(http.Protocols)
		set(p)
		return &http.Transport{TLSClientConfig: clientTLS.Clone(), Protocols: p}
	}
	h3 := &http3.Transport{TLSClientConfig: clientTLS.Clone()}
	t.Cleanup(func() { _ = h3.Close() })

	for name, tc := range map[string]struct {
		rt    http.RoundTripper
		major int
	}{
		"HTTP/1.1": {rt: tcp(func(p *http.Protocols) { p.SetHTTP1(true) }), major: 1},
		"HTTP/2":   {rt: tcp(func(p *http.Protocols) { p.SetHTTP2(true) }), major: 2},
		"HTTP/3":   {rt: h3, major: 3},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp, err := (&http.Client{Transport: tc.rt}).Do(grpcWebRequest(t, "https://"+srv.Addr()).WithContext(ctx))
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if resp.ProtoMajor != tc.major {
				t.Fatalf("served over HTTP/%d, want HTTP/%d", resp.ProtoMajor, tc.major)
			}
			select {
			case r := <-backend.calls:
				if r.TLS == nil {
					t.Error("the backend saw no TLS connection state")
				}
			default:
				t.Fatal("the call did not reach the gRPC backend")
			}
			if resp.Header.Get("Content-Type") != "application/grpc-web+proto" || !bytes.HasSuffix(body, []byte("grpc-status: 0\r\n")) {
				t.Errorf("response %q %q, want a gRPC-Web body ending in the status trailer frame", resp.Header.Get("Content-Type"), body)
			}
		})
	}
}

func TestGRPCWeb_ModuleRouteUnderThePrefixRefused(t *testing.T) {
	mod := &httpModule{name: "squatter", register: func(r *apimodule.HTTPRegistrar) {
		r.Handle(http.MethodPost, "/grpc-web/{svc}/{method}", permission.KVGet, noopHandler)
	}}
	recovered := func() (got any) {
		defer func() { got = recover() }()
		NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
			WithServiceModules([]apimodule.ServiceModule{mod}),
			WithGRPCWeb(grpcweb.Config{Enabled: true}, newGRPCBackend()))
		return nil
	}()
	collision, ok := recovered.(*GRPCWebRouteCollisionError)
	if !ok {
		t.Fatalf("construction panicked with %#v, want *GRPCWebRouteCollisionError", recovered)
	}
	if collision.Method != http.MethodPost || collision.Pattern != "/grpc-web/{svc}/{method}" || collision.Holder != "module squatter" {
		t.Errorf("collision = %+v", collision)
	}

	// The same module is fine while gRPC-Web is off: the prefix is not claimed.
	NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithServiceModules([]apimodule.ServiceModule{mod}))
}

func TestGRPCWeb_InvalidConfigRefused(t *testing.T) {
	recovered := func() (got any) {
		defer func() { got = recover() }()
		NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
			WithGRPCWeb(grpcweb.Config{Enabled: true, AllowedOrigins: []string{"*"}, AllowCredentials: true}, newGRPCBackend()))
		return nil
	}()
	err, _ := recovered.(error)
	var wildcard *grpcweb.WildcardCredentialsError
	if !errors.As(err, &wildcard) {
		t.Fatalf("construction panicked with %#v, want *grpcweb.WildcardCredentialsError", recovered)
	}
}

func TestCollidesWithGRPCWeb(t *testing.T) {
	for pattern, want := range map[string]bool{
		"/grpc-web":              true,
		"/grpc-web/":             true,
		"/grpc-web/{svc}/{m}":    true,
		"/*":                     true,
		"/grpc*":                 true,
		"/{seg}/{rest}":          true,
		"/grpc-{x}/y":            true,
		"/{seg}":                 false,
		"/grpc-webx":             false,
		"/grpc-webx/{a}":         false,
		"/v1/grpc-web/{a}":       false,
		"/health":                false,
		"/x{seg}/{rest}":         false,
		"/v1/kv/{key}":           false,
		"/grpc-web-extra/{a}/*":  false,
		"/grpc-web-{suffix}/x/y": false,
	} {
		if got := collidesWithGRPCWeb(pattern); got != want {
			t.Errorf("collidesWithGRPCWeb(%q) = %v, want %v", pattern, got, want)
		}
	}
}

func TestGRPCWebRouteCollisionError_Message(t *testing.T) {
	for holder, want := range map[string]string{
		"module m": "rest: module m registered POST /grpc-web/*, which collides with the gRPC-Web prefix /grpc-web/",
		"":         "rest: a direct registration registered POST /grpc-web/*, which collides with the gRPC-Web prefix /grpc-web/",
	} {
		err := &GRPCWebRouteCollisionError{Method: http.MethodPost, Pattern: "/grpc-web/*", Holder: holder}
		if err.Error() != want {
			t.Errorf("Error() = %q, want %q", err.Error(), want)
		}
	}
}

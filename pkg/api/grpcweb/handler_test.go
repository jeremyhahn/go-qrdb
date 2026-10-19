package grpcweb

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	connect "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// deniedService is the health service name the test interceptor refuses.
const deniedService = "denied"

// checkSpec and watchSpec are grpc.health.v1's two methods.
var (
	checkSpec = connect.Spec{StreamType: connect.StreamTypeUnary, Procedure: healthpb.Health_Check_FullMethodName}
	watchSpec = connect.Spec{StreamType: connect.StreamTypeServer, Procedure: healthpb.Health_Watch_FullMethodName}
)

// observed is what the backend's interceptors saw of the last call.
type observed struct {
	mu       sync.Mutex
	md       metadata.MD
	authInfo credentials.AuthInfo
	method   string
}

func (o *observed) record(ctx context.Context, method string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.md, _ = metadata.FromIncomingContext(ctx)
	o.method = method
	o.authInfo = nil
	if p, ok := peer.FromContext(ctx); ok {
		o.authInfo = p.AuthInfo
	}
}

func (o *observed) snapshot() (metadata.MD, credentials.AuthInfo, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.md, o.authInfo, o.method
}

// newBackend returns a real *grpc.Server serving grpc.health.v1 behind a
// unary and a stream interceptor that record what they see, set a trailer,
// and refuse the deniedService check.
func newBackend(t *testing.T) (*grpc.Server, *observed) {
	t.Helper()
	obs := &observed{}
	unary := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		obs.record(ctx, info.FullMethod)
		if err := grpc.SetTrailer(ctx, metadata.Pairs("x-test-trailer", "seen")); err != nil {
			return nil, err
		}
		if r, ok := req.(*healthpb.HealthCheckRequest); ok && r.GetService() == deniedService {
			return nil, status.Error(codes.PermissionDenied, "denied: missing permission")
		}
		return handler(ctx, req)
	}
	stream := func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		obs.record(ss.Context(), info.FullMethod)
		return handler(srv, ss)
	}
	gs := grpc.NewServer(grpc.UnaryInterceptor(unary), grpc.StreamInterceptor(stream))
	hs := health.NewServer()
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(gs, hs)
	t.Cleanup(gs.Stop)
	return gs, obs
}

// newClientCert returns a self-signed client certificate with common name cn.
func newClientCert(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// newWebServer serves h, under PathPrefix, over TLS that requests a client
// certificate, on HTTP/2 when http2 is set and HTTP/1.1 otherwise. The
// returned client presents clientCert when it has one.
func newWebServer(t *testing.T, h http.Handler, http2 bool, clientCert *tls.Certificate) (*httptest.Server, *http.Client) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(PathPrefix, h)
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = http2
	srv.TLS = &tls.Config{ClientAuth: tls.RequestClientCert, MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	client := srv.Client()
	transport := client.Transport.(*http.Transport)
	if clientCert != nil {
		transport.TLSClientConfig.Certificates = []tls.Certificate{*clientCert}
	}
	return srv, client
}

func newConnectClient(httpClient *http.Client, baseURL string) *connect.Client {
	return connect.NewClient(connecthttp.NewTransport(httpClient, baseURL+"/grpc-web", connecthttp.WithGRPCWeb()))
}

func mustHandler(t *testing.T, cfg Config, backend http.Handler) *Handler {
	t.Helper()
	h, err := NewHandler(cfg, backend)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func TestNewHandler_RefusesInvalidConfig(t *testing.T) {
	_, err := NewHandler(Config{Enabled: true, AllowedOrigins: []string{"*"}, AllowCredentials: true}, http.NotFoundHandler())
	var target *WildcardCredentialsError
	if !errors.As(err, &target) {
		t.Fatalf("NewHandler error = %v, want *WildcardCredentialsError", err)
	}
}

func TestNewHandler_IndexesOrigins(t *testing.T) {
	h := mustHandler(t, Config{AllowedOrigins: []string{"https://app.example.com", "*"}}, http.NotFoundHandler())
	if !h.anyOrigin {
		t.Error("anyOrigin = false, want true")
	}
	if _, ok := h.origins["https://app.example.com"]; !ok || len(h.origins) != 1 {
		t.Errorf("origins = %v, want only https://app.example.com", h.origins)
	}
}

// TestHandler_UnaryCarriesIdentity is the identity contract on HTTP/1.1 and
// HTTP/2: the client certificate reaches the gRPC peer as
// credentials.TLSInfo and the bearer header reaches the interceptors as
// incoming metadata.
func TestHandler_UnaryCarriesIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		http2 bool
	}{{"http1.1", false}, {"http2", true}} {
		t.Run(tc.name, func(t *testing.T) {
			gs, obs := newBackend(t)
			cert := newClientCert(t, "caller-1")
			srv, httpClient := newWebServer(t, mustHandler(t, Config{Enabled: true}, gs), tc.http2, &cert)
			client := newConnectClient(httpClient, srv.URL)

			ctx, info := connect.NewClientContext(context.Background())
			info.RequestHeader().Set("Authorization", "Bearer token-1")
			res := &healthpb.HealthCheckResponse{}
			if err := client.CallUnary(ctx, checkSpec, &healthpb.HealthCheckRequest{}, res); err != nil {
				t.Fatalf("CallUnary: %v", err)
			}
			if res.GetStatus() != healthpb.HealthCheckResponse_SERVING {
				t.Errorf("status = %v, want SERVING", res.GetStatus())
			}
			if got := info.ResponseTrailer().Get("X-Test-Trailer"); got != "seen" {
				t.Errorf("trailer x-test-trailer = %q, want seen", got)
			}

			md, authInfo, method := obs.snapshot()
			if method != healthpb.Health_Check_FullMethodName {
				t.Errorf("method = %q, want %q", method, healthpb.Health_Check_FullMethodName)
			}
			if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer token-1" {
				t.Errorf("authorization metadata = %v, want [Bearer token-1]", got)
			}
			tlsInfo, ok := authInfo.(credentials.TLSInfo)
			if !ok {
				t.Fatalf("peer AuthInfo = %T, want credentials.TLSInfo", authInfo)
			}
			if certs := tlsInfo.State.PeerCertificates; len(certs) != 1 || certs[0].Subject.CommonName != "caller-1" {
				t.Errorf("peer certificates = %v, want caller-1", certs)
			}
		})
	}
}

func TestHandler_UnaryWithoutClientCertHasNoPeerCertificate(t *testing.T) {
	gs, obs := newBackend(t)
	srv, httpClient := newWebServer(t, mustHandler(t, Config{Enabled: true}, gs), false, nil)
	client := newConnectClient(httpClient, srv.URL)
	if err := client.CallUnary(context.Background(), checkSpec, &healthpb.HealthCheckRequest{}, &healthpb.HealthCheckResponse{}); err != nil {
		t.Fatalf("CallUnary: %v", err)
	}
	md, authInfo, _ := obs.snapshot()
	if got := md.Get("authorization"); len(got) != 0 {
		t.Errorf("authorization metadata = %v, want none", got)
	}
	tlsInfo, ok := authInfo.(credentials.TLSInfo)
	if !ok {
		t.Fatalf("peer AuthInfo = %T, want credentials.TLSInfo", authInfo)
	}
	if n := len(tlsInfo.State.PeerCertificates); n != 0 {
		t.Errorf("peer certificates = %d, want 0", n)
	}
}

func TestHandler_StatusErrorReachesClient(t *testing.T) {
	gs, _ := newBackend(t)
	srv, httpClient := newWebServer(t, mustHandler(t, Config{Enabled: true}, gs), true, nil)
	client := newConnectClient(httpClient, srv.URL)
	err := client.CallUnary(context.Background(), checkSpec, &healthpb.HealthCheckRequest{Service: deniedService}, &healthpb.HealthCheckResponse{})
	if code := connect.CodeOf(err); code != connect.CodePermissionDenied {
		t.Fatalf("code = %v (%v), want PermissionDenied", code, err)
	}
	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Message() != "denied: missing permission" {
		t.Errorf("error = %v, want message %q", err, "denied: missing permission")
	}
}

func TestHandler_UnknownMethodIsUnimplemented(t *testing.T) {
	gs, _ := newBackend(t)
	srv, httpClient := newWebServer(t, mustHandler(t, Config{Enabled: true}, gs), false, nil)
	client := newConnectClient(httpClient, srv.URL)
	spec := connect.Spec{StreamType: connect.StreamTypeUnary, Procedure: "/qrdb.test.Missing/Call"}
	err := client.CallUnary(context.Background(), spec, &healthpb.HealthCheckRequest{}, &healthpb.HealthCheckResponse{})
	if code := connect.CodeOf(err); code != connect.CodeUnimplemented {
		t.Fatalf("code = %v (%v), want Unimplemented", code, err)
	}
}

func TestHandler_ServerStream(t *testing.T) {
	gs, obs := newBackend(t)
	srv, httpClient := newWebServer(t, mustHandler(t, Config{Enabled: true}, gs), false, nil)
	client := newConnectClient(httpClient, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.CallServerStream(ctx, watchSpec, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("CallServerStream: %v", err)
	}
	defer stream.Close()
	res := &healthpb.HealthCheckResponse{}
	if err := stream.Receive(res); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if res.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("status = %v, want SERVING", res.GetStatus())
	}
	if _, _, method := obs.snapshot(); method != healthpb.Health_Watch_FullMethodName {
		t.Errorf("method = %q, want %q", method, healthpb.Health_Watch_FullMethodName)
	}
}

// grpcFrame returns msg as one uncompressed gRPC data frame.
func grpcFrame(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	body, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 5, 5+len(body))
	binary.BigEndian.PutUint32(frame[1:], uint32(len(body)))
	return append(frame, body...)
}

// readFrames splits a gRPC-Web response body into its frames.
func readFrames(t *testing.T, body []byte) (data [][]byte, trailer string) {
	t.Helper()
	for len(body) > 0 {
		if len(body) < 5 {
			t.Fatalf("truncated frame header: %x", body)
		}
		n := int(binary.BigEndian.Uint32(body[1:5]))
		if len(body) < 5+n {
			t.Fatalf("truncated frame: want %d bytes, have %d", n, len(body)-5)
		}
		payload := body[5 : 5+n]
		if body[0]&trailerFlag != 0 {
			trailer += string(payload)
		} else {
			data = append(data, payload)
		}
		body = body[5+n:]
	}
	return data, trailer
}

// decodeTextBody decodes a grpc-web-text body, which is a concatenation of
// independently padded base64 chunks.
func decodeTextBody(t *testing.T, body string) []byte {
	t.Helper()
	var out []byte
	for body != "" {
		end := strings.Index(body, "=")
		if end < 0 {
			end = len(body)
		} else {
			for end < len(body) && body[end] == '=' {
				end++
			}
		}
		chunk, err := base64.StdEncoding.DecodeString(body[:end])
		if err != nil {
			t.Fatalf("decode %q: %v", body[:end], err)
		}
		out = append(out, chunk...)
		body = body[end:]
	}
	return out
}

func postWeb(t *testing.T, client *http.Client, url, contentType, accept string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Grpc-Web", "1")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestHandler_TextEncoding(t *testing.T) {
	gs, _ := newBackend(t)
	srv, httpClient := newWebServer(t, mustHandler(t, Config{Enabled: true}, gs), false, nil)
	reqBody := base64.StdEncoding.EncodeToString(grpcFrame(t, &healthpb.HealthCheckRequest{}))
	resp := postWeb(t, httpClient, srv.URL+"/grpc-web"+healthpb.Health_Check_FullMethodName,
		"application/grpc-web-text", "application/grpc-web-text", []byte(reqBody))
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "application/grpc-web-text+proto" {
		t.Fatalf("content type = %q, want application/grpc-web-text+proto", got)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	data, trailer := readFrames(t, decodeTextBody(t, string(raw)))
	if len(data) != 1 {
		t.Fatalf("data frames = %d, want 1", len(data))
	}
	res := &healthpb.HealthCheckResponse{}
	if err := proto.Unmarshal(data[0], res); err != nil || res.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("response = %v (%v), want SERVING", res, err)
	}
	if !strings.Contains(trailer, "grpc-status: 0\r\n") || !strings.Contains(trailer, "x-test-trailer: seen\r\n") {
		t.Errorf("trailer = %q, want grpc-status 0 and x-test-trailer", trailer)
	}
}

func TestHandler_BinaryTrailerFrameIsLowercase(t *testing.T) {
	gs, _ := newBackend(t)
	srv, httpClient := newWebServer(t, mustHandler(t, Config{Enabled: true}, gs), false, nil)
	resp := postWeb(t, httpClient, srv.URL+"/grpc-web"+healthpb.Health_Check_FullMethodName,
		"application/grpc-web+proto", "", grpcFrame(t, &healthpb.HealthCheckRequest{Service: deniedService}))
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "application/grpc-web+proto" {
		t.Fatalf("content type = %q, want application/grpc-web+proto", got)
	}
	if resp.Header.Get("Trailer") != "" {
		t.Errorf("Trailer header = %q, want none", resp.Header.Get("Trailer"))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	data, trailer := readFrames(t, raw)
	if len(data) != 0 {
		t.Errorf("data frames = %d, want 0", len(data))
	}
	want := "grpc-message: denied: missing permission\r\ngrpc-status: 7\r\n"
	if !strings.HasPrefix(trailer, want) {
		t.Errorf("trailer = %q, want prefix %q", trailer, want)
	}
	if len(resp.Trailer) != 0 {
		t.Errorf("HTTP trailers = %v, want none", resp.Trailer)
	}
}

func TestHandler_RefusesNonPOST(t *testing.T) {
	h := mustHandler(t, Config{Enabled: true}, http.NotFoundHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PathPrefix+"a.B/C", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Errorf("GET = %d Allow %q, want 405 Allow POST", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestHandler_RefusesNonWebContentType(t *testing.T) {
	for _, ct := range []string{"", "application/grpc", "application/json", "application/grpc-webx", "text/plain;;"} {
		t.Run(ct, func(t *testing.T) {
			called := false
			h := mustHandler(t, Config{Enabled: true}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			req := httptest.NewRequest(http.MethodPost, PathPrefix+"a.B/C", nil)
			req.Header.Set("Content-Type", ct)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnsupportedMediaType || called {
				t.Errorf("status = %d backend called %v, want 415 and no call", rec.Code, called)
			}
		})
	}
}

// TestHandler_TranslatesRequest pins the request the backend receives: the
// prefix stripped, HTTP/2 framing semantics and the gRPC media type.
func TestHandler_TranslatesRequest(t *testing.T) {
	var got *http.Request
	h := mustHandler(t, Config{Enabled: true}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r }))
	req := httptest.NewRequest(http.MethodPost, PathPrefix+"qrdb.v1.QRDBService/Get", strings.NewReader("x"))
	req.Header.Set("Content-Type", "application/grpc-web+json; charset=utf-8")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got == nil {
		t.Fatal("backend not called")
	}
	if got.URL.Path != "/qrdb.v1.QRDBService/Get" || got.ProtoMajor != 2 {
		t.Errorf("path %q proto %d, want /qrdb.v1.QRDBService/Get on 2", got.URL.Path, got.ProtoMajor)
	}
	if ct := got.Header.Get("Content-Type"); ct != "application/grpc+json" {
		t.Errorf("content type = %q, want application/grpc+json", ct)
	}
	if req.URL.Path != PathPrefix+"qrdb.v1.QRDBService/Get" {
		t.Errorf("caller's request mutated: %q", req.URL.Path)
	}
}

func TestHandler_PassesNonGRPCResponseThrough(t *testing.T) {
	h := mustHandler(t, Config{Enabled: true}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Trailer", "Grpc-Status")
		http.Error(w, "backend refused", http.StatusTeapot)
		w.Header().Set("Grpc-Status", "0")
	}))
	req := httptest.NewRequest(http.MethodPost, PathPrefix+"a.B/C", nil)
	req.Header.Set("Content-Type", "application/grpc-web-text")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot || rec.Body.String() != "backend refused\n" {
		t.Errorf("response = %d %q, want 418 and the plain body", rec.Code, rec.Body.String())
	}
}

func TestHandler_TrailersOnlyPassesHeaders(t *testing.T) {
	h := mustHandler(t, Config{Enabled: true}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Grpc-Status", "14")
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, PathPrefix+"a.B/C", nil)
	req.Header.Set("Content-Type", "application/grpc-web")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Grpc-Status") != "14" || rec.Body.Len() != 0 {
		t.Errorf("headers %v body %q, want Grpc-Status 14 and no body", rec.Header(), rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/grpc-web+proto" {
		t.Errorf("content type = %q, want application/grpc-web+proto", ct)
	}
}

func TestHandler_BackendWritingNothing(t *testing.T) {
	h := mustHandler(t, Config{Enabled: true}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodPost, PathPrefix+"a.B/C", nil)
	req.Header.Set("Content-Type", "application/grpc-web")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}

func TestParseWebContentType(t *testing.T) {
	for _, tc := range []struct {
		in      string
		subtype string
		text    bool
		ok      bool
	}{
		{"application/grpc-web", "proto", false, true},
		{"application/grpc-web+proto", "proto", false, true},
		{"Application/GRPC-Web+JSON; charset=utf-8", "json", false, true},
		{"application/grpc-web-text", "proto", true, true},
		{"application/grpc-web-text+proto", "proto", true, true},
		{"application/grpc", "", false, false},
		{"application/grpc-web-texts", "", false, false},
		{"", "", false, false},
	} {
		subtype, text, ok := parseWebContentType(tc.in)
		if subtype != tc.subtype || text != tc.text || ok != tc.ok {
			t.Errorf("parseWebContentType(%q) = %q %v %v, want %q %v %v", tc.in, subtype, text, ok, tc.subtype, tc.text, tc.ok)
		}
	}
}

func TestAcceptsText(t *testing.T) {
	for _, tc := range []struct {
		accept []string
		want   bool
	}{
		{nil, false},
		{[]string{"application/grpc-web"}, false},
		{[]string{"application/grpc-web-text"}, true},
		{[]string{"text/html, application/grpc-web-text+proto;q=0.9"}, true},
		{[]string{";;", "application/grpc-web-text"}, true},
		{[]string{";;"}, false},
	} {
		if got := acceptsText(tc.accept); got != tc.want {
			t.Errorf("acceptsText(%q) = %v, want %v", tc.accept, got, tc.want)
		}
	}
}

// fakeResponseWriter fails every body write, the shape of a client that
// went away.
type fakeResponseWriter struct {
	header http.Header
}

func (f *fakeResponseWriter) Header() http.Header       { return f.header }
func (f *fakeResponseWriter) WriteHeader(int)           {}
func (f *fakeResponseWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestResponseWriter_TextWriteError(t *testing.T) {
	f := &fakeResponseWriter{header: make(http.Header)}
	rw := &responseWriter{w: f, rc: http.NewResponseController(f), header: http.Header{"Content-Type": {"application/grpc"}}, text: true}
	if _, err := rw.Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("Write error = %v, want io.ErrClosedPipe", err)
	}
	rw.header.Add("Trailer", "Grpc-Status")
	rw.trailers = []string{"Grpc-Status"}
	rw.header.Set("Grpc-Status", "0")
	rw.finish()
}

func TestResponseWriter_FlushBeforeWriteSendsHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriter{w: rec, rc: http.NewResponseController(rec), header: http.Header{"Content-Type": {"application/grpc"}}, contentType: "application/grpc-web+proto"}
	rw.Flush()
	if !rec.Flushed || rec.Header().Get("Content-Type") != "application/grpc-web+proto" {
		t.Errorf("flushed %v content type %q, want flushed grpc-web", rec.Flushed, rec.Header().Get("Content-Type"))
	}
	rw.WriteHeader(http.StatusTeapot)
	if rec.Code != http.StatusOK {
		t.Errorf("second WriteHeader changed status to %d", rec.Code)
	}
}

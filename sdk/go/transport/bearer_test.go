package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// recordingRoundTripper records the Authorization header of every request
// it is handed and answers 200.
type recordingRoundTripper struct {
	seen []string
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.seen = append(r.seen, req.Header.Get(AuthorizationHeader))
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
}

// countingSource yields "tok-1", "tok-2", ... on successive calls.
type countingSource struct {
	n atomic.Int64
}

func (s *countingSource) Token(context.Context) (string, error) {
	return fmt.Sprintf("tok-%d", s.n.Add(1)), nil
}

type failingSource struct{ err error }

func (s failingSource) Token(context.Context) (string, error) { return "", s.err }

func mustRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return req
}

func TestBearerRoundTripper_SetsHeaderOnConfiguredHost(t *testing.T) {
	base := &recordingRoundTripper{}
	rt := BearerRoundTripper(base, "rest", ConfiguredHost("https://db.test:8443"), StaticTokenSource("abc"))
	req := mustRequest(t, "https://db.test:8443/v1/status")
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if len(base.seen) != 1 || base.seen[0] != "Bearer abc" {
		t.Fatalf("Authorization = %q, want [Bearer abc]", base.seen)
	}
	if got := req.Header.Get(AuthorizationHeader); got != "" {
		t.Fatalf("caller's request mutated: Authorization = %q", got)
	}
}

func TestBearerRoundTripper_NilSourceReturnsBase(t *testing.T) {
	base := &recordingRoundTripper{}
	if rt := BearerRoundTripper(base, "rest", "db.test", nil); rt != http.RoundTripper(base) {
		t.Fatalf("nil source wrapped base: %T", rt)
	}
}

func TestBearerRoundTripper_RefusesPlaintext(t *testing.T) {
	base := &recordingRoundTripper{}
	rt := BearerRoundTripper(base, "rest", ConfiguredHost("http://db.test:8080"), StaticTokenSource("abc"))
	_, err := rt.RoundTrip(mustRequest(t, "http://db.test:8080/v1/status"))
	var tlsErr *BearerRequiresTLSError
	if !errors.As(err, &tlsErr) || tlsErr.Transport != "rest" {
		t.Fatalf("err = %v, want *BearerRequiresTLSError{rest}", err)
	}
	if len(base.seen) != 0 {
		t.Fatalf("plaintext request was sent")
	}
	if strings.Contains(err.Error(), "abc") {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestBearerRoundTripper_RefusesForeignHostRedirect(t *testing.T) {
	base := &recordingRoundTripper{}
	rt := BearerRoundTripper(base, "mcp", ConfiguredHost("https://db.test:8443"), StaticTokenSource("abc"))
	redirected := mustRequest(t, "https://evil.test/collect")
	redirected.Response = &http.Response{StatusCode: http.StatusTemporaryRedirect}
	_, err := rt.RoundTrip(redirected)
	var hostErr *BearerForeignHostError
	if !errors.As(err, &hostErr) || hostErr.Host != "evil.test" || hostErr.Transport != "mcp" {
		t.Fatalf("err = %v, want *BearerForeignHostError{mcp, evil.test}", err)
	}
	if len(base.seen) != 0 {
		t.Fatalf("foreign-host request was sent")
	}
	if strings.Contains(err.Error(), "abc") {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func invokeUnary(ctx context.Context, ic grpc.UnaryClientInterceptor) (metadata.MD, bool, error) {
	var md metadata.MD
	called := false
	err := ic(ctx, "/svc/M", nil, nil, nil, func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		called = true
		md, _ = metadata.FromOutgoingContext(ctx)
		return nil
	})
	return md, called, err
}

func TestBearerInterceptor_SetsMetadata(t *testing.T) {
	ctx := metadata.AppendToOutgoingContext(context.Background(), AuthorizationMetadataKey, "Bearer stale", "x-other", "kept")
	md, called, err := invokeUnary(ctx, BearerUnaryClientInterceptor("grpc", true, StaticTokenSource("abc")))
	if err != nil || !called {
		t.Fatalf("interceptor: called=%v err=%v", called, err)
	}
	if got := md.Get(AuthorizationMetadataKey); len(got) != 1 || got[0] != "Bearer abc" {
		t.Fatalf("authorization = %q, want [Bearer abc] (replaced, not appended)", got)
	}
	if got := md.Get("x-other"); len(got) != 1 || got[0] != "kept" {
		t.Fatalf("x-other = %q, want [kept]", got)
	}

	var streamMD metadata.MD
	_, err = BearerStreamClientInterceptor("grpc", true, StaticTokenSource("abc"))(context.Background(), &grpc.StreamDesc{}, nil, "/svc/S",
		func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
			streamMD, _ = metadata.FromOutgoingContext(ctx)
			return nil, nil
		})
	if err != nil {
		t.Fatalf("stream interceptor: %v", err)
	}
	if got := streamMD.Get(AuthorizationMetadataKey); len(got) != 1 || got[0] != "Bearer abc" {
		t.Fatalf("stream authorization = %q, want [Bearer abc]", got)
	}
}

func TestBearerInterceptor_NilSourcePassesThrough(t *testing.T) {
	md, called, err := invokeUnary(context.Background(), BearerUnaryClientInterceptor("unix", false, nil))
	if err != nil || !called || len(md.Get(AuthorizationMetadataKey)) != 0 {
		t.Fatalf("nil source: called=%v err=%v md=%v", called, err, md)
	}
}

func TestBearerInterceptor_RefusesPlaintext(t *testing.T) {
	src := &countingSource{}
	_, called, err := invokeUnary(context.Background(), BearerUnaryClientInterceptor("unix", false, src))
	var tlsErr *BearerRequiresTLSError
	if !errors.As(err, &tlsErr) || tlsErr.Transport != "unix" {
		t.Fatalf("err = %v, want *BearerRequiresTLSError{unix}", err)
	}
	if called {
		t.Fatalf("plaintext RPC was invoked")
	}
	if src.n.Load() != 0 {
		t.Fatalf("token source consulted on a plaintext connection")
	}
}

func TestTokenSource_CalledPerRequest(t *testing.T) {
	src := &countingSource{}
	base := &recordingRoundTripper{}
	rt := BearerRoundTripper(base, "rest", "db.test", src)
	for i := 0; i < 3; i++ {
		resp, err := rt.RoundTrip(mustRequest(t, "https://db.test/v1/status"))
		if err != nil {
			t.Fatalf("RoundTrip %d: %v", i, err)
		}
		_ = resp.Body.Close()
	}
	want := []string{"Bearer tok-1", "Bearer tok-2", "Bearer tok-3"}
	if strings.Join(base.seen, ",") != strings.Join(want, ",") {
		t.Fatalf("headers = %q, want %q", base.seen, want)
	}

	ic := BearerUnaryClientInterceptor("grpc", true, src)
	for _, want := range []string{"Bearer tok-4", "Bearer tok-5"} {
		md, _, err := invokeUnary(context.Background(), ic)
		if err != nil {
			t.Fatalf("interceptor: %v", err)
		}
		if got := md.Get(AuthorizationMetadataKey); len(got) != 1 || got[0] != want {
			t.Fatalf("authorization = %q, want %q", got, want)
		}
	}
}

func TestTokenSource_ErrorNeverLeaksToken(t *testing.T) {
	cause := errors.New("refresh failed")
	rt := BearerRoundTripper(&recordingRoundTripper{}, "rest", "db.test", failingSource{err: cause})
	_, err := rt.RoundTrip(mustRequest(t, "https://db.test/v1/status"))
	var srcErr *TokenSourceError
	if !errors.As(err, &srcErr) || !errors.Is(err, cause) {
		t.Fatalf("err = %v, want *TokenSourceError wrapping the cause", err)
	}

	const secret = "s3cret-token"
	for _, tok := range []string{secret + "\r\nX-Injected: 1", secret + "\n", "\x00" + secret} {
		_, _, err := invokeUnary(context.Background(), BearerUnaryClientInterceptor("grpc", true, StaticTokenSource(tok)))
		if !errors.As(err, &srcErr) {
			t.Fatalf("token %q: err = %v, want *TokenSourceError", tok, err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks the token: %v", err)
		}
	}
}

func TestTokenSource_ErrorOmitsSourceErrorText(t *testing.T) {
	cause := errors.New("refresh failed: access_token=s3cret-token")
	src := failingSource{err: cause}
	rt := BearerRoundTripper(&recordingRoundTripper{}, "rest", "db.test", src)
	_, restErr := rt.RoundTrip(mustRequest(t, "https://db.test/v1/status"))
	_, _, grpcErr := invokeUnary(context.Background(), BearerUnaryClientInterceptor("grpc", true, src))
	for name, err := range map[string]error{"rest": restErr, "grpc": grpcErr} {
		var srcErr *TokenSourceError
		if !errors.As(err, &srcErr) || !errors.Is(err, cause) {
			t.Fatalf("%s: err = %v, want *TokenSourceError wrapping the cause", name, err)
		}
		if msg := err.Error(); strings.Contains(msg, "s3cret-token") || strings.Contains(msg, "refresh failed") {
			t.Fatalf("%s: error carries the source's text: %q", name, msg)
		}
	}
}

func TestWithBearerToken_RefusesControlBytes(t *testing.T) {
	for _, tok := range []string{"", "a\rb", "a\nb", "a\tb", "a\x7fb"} {
		cfg := ApplyOptions(WithBearerToken(tok))
		if cfg.TokenSource == nil {
			t.Fatalf("WithBearerToken(%q) left TokenSource nil", tok)
		}
		base := &recordingRoundTripper{}
		_, err := BearerRoundTripper(base, "rest", "db.test", cfg.TokenSource).RoundTrip(mustRequest(t, "https://db.test/"))
		var srcErr *TokenSourceError
		if !errors.As(err, &srcErr) {
			t.Fatalf("WithBearerToken(%q): err = %v, want *TokenSourceError", tok, err)
		}
		if len(base.seen) != 0 {
			t.Fatalf("WithBearerToken(%q): request was sent", tok)
		}
	}
	cfg := ApplyOptions(WithBearerToken("good"))
	if tok, err := cfg.TokenSource.Token(context.Background()); err != nil || tok != "good" {
		t.Fatalf("Token = %q, %v", tok, err)
	}
}

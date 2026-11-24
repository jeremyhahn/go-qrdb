package transport

import (
	"context"
	"net/http"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// AuthorizationHeader is the HTTP header, and AuthorizationMetadataKey the
// gRPC metadata key, a bearer token travels in.
const (
	AuthorizationHeader      = "Authorization"
	AuthorizationMetadataKey = "authorization"
	bearerScheme             = "Bearer "
)

// TokenSource supplies the bearer token a client transport sends in the
// Authorization header. Token is called once per request or RPC, so a source
// that refreshes (an OAuth 2.0 client, a workload identity file) hands out the
// current token every time; caching and refresh live in the source. Token
// must be safe for concurrent use.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticTokenSource returns a TokenSource that always yields token.
func StaticTokenSource(token string) TokenSource {
	return staticTokenSource(token)
}

type staticTokenSource string

// Token implements TokenSource.
func (s staticTokenSource) Token(context.Context) (string, error) {
	return string(s), nil
}

// BearerRequiresTLSError is returned when a call configured with a bearer
// token would travel over a connection that is not TLS: a plaintext gRPC or
// Unix socket connection, or an http:// hop on REST or MCP. The call is
// refused before the token source is consulted, so the token never crosses a
// plaintext connection.
type BearerRequiresTLSError struct {
	// Transport names the SDK transport that refused ("rest", "grpc", ...).
	Transport string
}

// Error returns a description that never contains the token.
func (e *BearerRequiresTLSError) Error() string {
	return "transport: " + e.Transport + ": a bearer token is sent only over TLS; this connection is not TLS"
}

// BearerForeignHostError is returned when a REST or MCP request configured
// with a bearer token is addressed, by a redirect or otherwise, to a host
// other than the one the transport was configured with. The request is
// refused before it is sent.
type BearerForeignHostError struct {
	// Transport names the SDK transport that refused.
	Transport string
	// Host is the host the refused hop named.
	Host string
}

// Error returns a description that never contains the token.
func (e *BearerForeignHostError) Error() string {
	return "transport: " + e.Transport + ": refusing to send the bearer token to " + e.Host + ", which is not the configured host"
}

// TokenSourceError is returned when the configured TokenSource fails, or
// yields an empty token or one containing control bytes (CR, LF, ...). The
// call is refused before it is sent. The source's error is reachable only
// through Unwrap, errors.Is and errors.As; Error never includes its text.
type TokenSourceError struct {
	// Transport names the SDK transport that refused.
	Transport string
	// Reason describes a refused token; empty when Err is set.
	Reason string
	// Err is the token source's error, if it failed.
	Err error
}

// Error returns a fixed description that never contains the token or the
// token source's error text.
func (e *TokenSourceError) Error() string {
	if e.Err != nil {
		return "transport: " + e.Transport + ": token source failed"
	}
	return "transport: " + e.Transport + ": token source: " + e.Reason
}

// Unwrap returns the token source's error.
func (e *TokenSourceError) Unwrap() error { return e.Err }

// bearerValue asks src for the current token and returns the Authorization
// value "Bearer <token>", refusing an empty token or one with control bytes.
func bearerValue(ctx context.Context, transportName string, src TokenSource) (string, error) {
	tok, err := src.Token(ctx)
	if err != nil {
		return "", &TokenSourceError{Transport: transportName, Err: err}
	}
	if tok == "" {
		return "", &TokenSourceError{Transport: transportName, Reason: "empty token"}
	}
	for i := 0; i < len(tok); i++ {
		if c := tok[i]; c < 0x20 || c == 0x7f {
			return "", &TokenSourceError{Transport: transportName, Reason: "token contains a control byte"}
		}
	}
	return bearerScheme + tok, nil
}

// BearerRoundTripper wraps base so that every request carries
// "Authorization: Bearer <token>" from src -- but only on a hop that is https
// AND addressed to configuredHost. Every hop is checked, because an
// http.Client calls RoundTrip again for each redirect it follows: a plaintext
// hop is refused with *BearerRequiresTLSError and a hop to another host with
// *BearerForeignHostError, before src is consulted. src is called once per
// hop. A nil src returns base unchanged.
func BearerRoundTripper(base http.RoundTripper, transportName, configuredHost string, src TokenSource) http.RoundTripper {
	if src == nil {
		return base
	}
	return &bearerRoundTripper{base: base, transport: transportName, host: configuredHost, src: src}
}

type bearerRoundTripper struct {
	base      http.RoundTripper
	transport string
	host      string
	src       TokenSource
}

// RoundTrip implements http.RoundTripper.
func (rt *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return nil, &BearerRequiresTLSError{Transport: rt.transport}
	}
	if req.URL.Host != rt.host {
		return nil, &BearerForeignHostError{Transport: rt.transport, Host: req.URL.Host}
	}
	value, err := bearerValue(req.Context(), rt.transport, rt.src)
	if err != nil {
		return nil, err
	}
	// Clone before mutating headers, per the RoundTripper contract.
	clone := req.Clone(req.Context())
	clone.Header.Set(AuthorizationHeader, value)
	return rt.base.RoundTrip(clone)
}

// BearerUnaryClientInterceptor sets the outgoing metadata "authorization" to
// "Bearer <token>" from src on every unary RPC, replacing any value already
// set. secure reports whether the connection is TLS; when it is not, a call
// is refused with *BearerRequiresTLSError before src is consulted. A nil src
// passes every call through untouched.
func BearerUnaryClientInterceptor(transportName string, secure bool, src TokenSource) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx, err := withBearerMetadata(ctx, transportName, secure, src)
		if err != nil {
			return err
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// BearerStreamClientInterceptor is BearerUnaryClientInterceptor for
// streaming RPCs.
func BearerStreamClientInterceptor(transportName string, secure bool, src TokenSource) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		ctx, err := withBearerMetadata(ctx, transportName, secure, src)
		if err != nil {
			return nil, err
		}
		return streamer(ctx, desc, cc, method, opts...)
	}
}

func withBearerMetadata(ctx context.Context, transportName string, tlsConn bool, src TokenSource) (context.Context, error) {
	if src == nil {
		return ctx, nil
	}
	if !tlsConn {
		return ctx, &BearerRequiresTLSError{Transport: transportName}
	}
	value, err := bearerValue(ctx, transportName, src)
	if err != nil {
		return ctx, err
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	md.Set(AuthorizationMetadataKey, value)
	return metadata.NewOutgoingContext(ctx, md), nil
}

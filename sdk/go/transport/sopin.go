// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package transport

import (
	"context"
	"net/http"
	"net/url"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// SOPINHeader is the HTTP header the REST and MCP transports send the SO PIN
// in. It is the server's middleware.SOPINHeader.
const SOPINHeader = "X-QRDB-SO-PIN"

// SOPINMetadataKey is the gRPC metadata key the gRPC and Unix socket
// transports send the SO PIN in. It is the server's
// middleware.SOPINMetadataKey.
const SOPINMetadataKey = "x-qrdb-so-pin"

// soPINKey is the context key WithSOPIN binds under.
type soPINKey struct{}

// soPIN holds the SDK's own copy of an SO PIN for the calls made with one
// context, sealed in protected memory. The release function WithSOPIN returns
// destroys it, and it is never logged: it has no String method and its field
// is unexported. mu orders release against a transport opening the PIN, so a
// call racing release either reads the whole PIN or sees none bound.
type soPIN struct {
	mu  sync.RWMutex
	pin *secure.Secret
}

// WithSOPIN returns a context that presents pin as the SO dual-factor PIN on
// every call made with it, on every transport:
//
//   - REST and MCP send it in the X-QRDB-SO-PIN header;
//   - gRPC and the Unix socket send it in the x-qrdb-so-pin metadata entry;
//   - QUIC, whose frames have no headers, sends it in the request message's
//     so_pin field of the SO-gated operations. A request that already carries
//     a different SOPin is refused with *SOPINConflictError rather than
//     sending either;
//   - the embedded transport runs in process with no SO gate and ignores it.
//
// The server reads it only on the operations its SO gate covers, and only
// over TLS. WithSOPIN seals a copy of pin into protected memory, so the
// caller may wipe its own buffer at once; release destroys the SDK's copy and
// must be called when the calls are done (defer it). After release the
// context presents no PIN. An empty pin binds nothing. The error is the
// protected-memory failure that kept the PIN from being sealed, in which case
// ctx is returned unchanged and release is a no-op. Scope the context to the
// SO operation: a PIN bound to a long-lived context rides every request made
// with it.
func WithSOPIN(ctx context.Context, pin []byte) (context.Context, func(), error) {
	if len(pin) == 0 {
		return context.WithValue(ctx, soPINKey{}, &soPIN{}), func() {}, nil
	}
	// NewSecret consumes its argument, so it is handed a copy: the caller's
	// buffer stays the caller's.
	sealed, err := secure.NewSecret(append([]byte(nil), pin...))
	if err != nil {
		return ctx, func() {}, err
	}
	held := &soPIN{pin: sealed}
	release := func() {
		held.mu.Lock()
		held.pin.Destroy()
		held.pin = nil
		held.mu.Unlock()
	}
	return context.WithValue(ctx, soPINKey{}, held), release, nil
}

// SOPINFromContext returns a plaintext copy of the SO PIN bound by WithSOPIN,
// and whether one is bound and not yet released. Transports call it at the
// last moment and wipe the copy once it has been written to the wire. The
// error is a protected-memory failure opening the sealed PIN; a transport
// refuses the call on it rather than sending the call without its PIN.
func SOPINFromContext(ctx context.Context) ([]byte, bool, error) {
	held, ok := ctx.Value(soPINKey{}).(*soPIN)
	if !ok {
		return nil, false, nil
	}
	held.mu.RLock()
	defer held.mu.RUnlock()
	if held.pin == nil {
		return nil, false, nil
	}
	buf, err := held.pin.Open()
	if err != nil {
		return nil, false, err
	}
	defer buf.Destroy()
	pin, err := secure.CopyOut(buf)
	if err != nil {
		return nil, false, err
	}
	return pin, true, nil
}

// SOPINConflictError is returned by the QUIC transport when a request's own
// SOPin field and the PIN bound by WithSOPIN are both set and differ. QUIC
// carries the SO PIN in that one field, so it cannot send both, and choosing
// one silently would verify a PIN the caller did not mean.
type SOPINConflictError struct {
	// Method is the transport method refused.
	Method string
}

// Error returns a description that names the method and never the PIN.
func (e *SOPINConflictError) Error() string {
	return "transport: " + e.Method + ": request SOPin differs from the SO PIN bound by WithSOPIN"
}

// ResolveSOPIN returns the SO PIN a QUIC request carries in its so_pin field:
// the request's own SOPin, or the PIN bound by WithSOPIN when the request has
// none. Both set and different is a *SOPINConflictError.
func ResolveSOPIN(ctx context.Context, method, requestPIN string) (string, error) {
	bound, ok, err := SOPINFromContext(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		return requestPIN, nil
	}
	defer secure.Wipe(bound)
	if requestPIN != "" && requestPIN != string(bound) {
		return "", &SOPINConflictError{Method: method}
	}
	return string(bound), nil
}

// SOPINUnaryClientInterceptor sends the PIN bound by WithSOPIN in the
// x-qrdb-so-pin metadata entry of every unary RPC made with that context --
// when secure reports that the connection uses TLS transport credentials.
// Over a plaintext connection a PIN-bound RPC is refused with
// *SOPINRequiresTLSError before it is sent; an RPC with no bound PIN passes
// either way. Shared by the gRPC and Unix SDK transports, which name
// themselves in transportName. gRPC metadata values are strings, which Go
// cannot wipe; the SDK's byte copy is wiped as soon as the string is built.
func SOPINUnaryClientInterceptor(transportName string, secure bool) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx, err := withSOPINMetadata(ctx, transportName, secure)
		if err != nil {
			return err
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// SOPINStreamClientInterceptor is SOPINUnaryClientInterceptor for streaming
// RPCs.
func SOPINStreamClientInterceptor(transportName string, secure bool) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		ctx, err := withSOPINMetadata(ctx, transportName, secure)
		if err != nil {
			return nil, err
		}
		return streamer(ctx, desc, cc, method, opts...)
	}
}

func withSOPINMetadata(ctx context.Context, transportName string, tlsConn bool) (context.Context, error) {
	pin, ok, err := SOPINFromContext(ctx)
	if err != nil {
		return ctx, err
	}
	if !ok {
		return ctx, nil
	}
	defer secure.Wipe(pin)
	if !tlsConn {
		return ctx, &SOPINRequiresTLSError{Transport: transportName}
	}
	return metadata.AppendToOutgoingContext(ctx, SOPINMetadataKey, string(pin)), nil
}

// ConfiguredHost returns the host[:port] of a transport's configured address,
// the only host SOPINRoundTripper sends the SO PIN to. An address that does
// not parse yields "", which matches no request, so a bound PIN is refused
// rather than sent.
func ConfiguredHost(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return ""
	}
	return u.Host
}

// SOPINRoundTripper wraps base so that a request made with a context bound by
// WithSOPIN carries the PIN in the X-QRDB-SO-PIN header -- but only on a hop
// that is https AND addressed to configuredHost, the host the transport was
// configured with. The REST and MCP transports install it, naming themselves
// in transportName for the errors.
//
// Every hop is checked, because an http.Client calls RoundTrip again for each
// redirect it follows: a PIN-bound request whose hop is plaintext is refused
// with *SOPINRequiresTLSError, and one whose hop names another host with
// *SOPINForeignHostError, before anything is sent. A request without a bound
// PIN passes through untouched.
func SOPINRoundTripper(base http.RoundTripper, transportName, configuredHost string) http.RoundTripper {
	return &soPINRoundTripper{base: base, transport: transportName, host: configuredHost}
}

type soPINRoundTripper struct {
	base      http.RoundTripper
	transport string
	host      string
}

// RoundTrip implements http.RoundTripper.
func (rt *soPINRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	pin, ok, err := SOPINFromContext(req.Context())
	if err != nil {
		return nil, err
	}
	if !ok {
		return rt.base.RoundTrip(req)
	}
	if req.URL.Scheme != "https" {
		secure.Wipe(pin)
		return nil, &SOPINRequiresTLSError{Transport: rt.transport}
	}
	if req.URL.Host != rt.host {
		secure.Wipe(pin)
		return nil, &SOPINForeignHostError{Transport: rt.transport, Host: req.URL.Host}
	}
	// Clone before mutating headers, per the RoundTripper contract.
	clone := req.Clone(req.Context())
	clone.Header.Set(SOPINHeader, string(pin))
	secure.Wipe(pin)
	return rt.base.RoundTrip(clone)
}

// SOPINRequiresTLSError is returned when a call made with a context bound by
// WithSOPIN would travel over a connection that is not TLS: a plaintext gRPC
// or Unix socket connection, or an http:// hop on REST or MCP -- including a
// redirect to one. The request is refused before it is sent, so the PIN never
// crosses a plaintext connection.
type SOPINRequiresTLSError struct {
	// Transport names the SDK transport that refused ("rest", "grpc", ...).
	Transport string
}

// Error returns a description that never contains the PIN.
func (e *SOPINRequiresTLSError) Error() string {
	return "transport: " + e.Transport + ": the SO PIN is sent only over TLS; this connection is not TLS"
}

// SOPINForeignHostError is returned when a REST or MCP call made with a
// context bound by WithSOPIN is redirected to a host other than the one the
// transport was configured with. The redirected request is refused before it
// is sent, so the PIN never reaches a host the caller did not name.
type SOPINForeignHostError struct {
	// Transport names the SDK transport that refused.
	Transport string
	// Host is the host the redirect named.
	Host string
}

// Error returns a description that never contains the PIN.
func (e *SOPINForeignHostError) Error() string {
	return "transport: " + e.Transport + ": refusing to send the SO PIN to " + e.Host + ", which is not the configured host"
}

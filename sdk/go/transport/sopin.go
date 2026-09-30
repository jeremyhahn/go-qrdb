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
// context. The copy is wiped by the release function WithSOPIN returns, and
// it is never logged: it has no String method and its field is unexported.
type soPIN struct {
	mu  sync.RWMutex
	pin []byte
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
// over TLS. WithSOPIN copies pin, so the caller may wipe its own buffer at
// once; release wipes the SDK's copy and must be called when the calls are
// done (defer it). After release the context presents no PIN. Scope the
// context to the SO operation: a PIN bound to a long-lived context rides every
// request made with it.
func WithSOPIN(ctx context.Context, pin []byte) (context.Context, func()) {
	held := &soPIN{pin: append([]byte(nil), pin...)}
	release := func() {
		held.mu.Lock()
		secure.Wipe(held.pin)
		held.pin = nil
		held.mu.Unlock()
	}
	return context.WithValue(ctx, soPINKey{}, held), release
}

// SOPINFromContext returns a copy of the SO PIN bound by WithSOPIN, and
// whether one is bound and not yet released. Transports call it at the last
// moment and wipe the copy once it has been written to the wire.
func SOPINFromContext(ctx context.Context) ([]byte, bool) {
	held, ok := ctx.Value(soPINKey{}).(*soPIN)
	if !ok {
		return nil, false
	}
	held.mu.RLock()
	defer held.mu.RUnlock()
	if len(held.pin) == 0 {
		return nil, false
	}
	return append([]byte(nil), held.pin...), true
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
	bound, ok := SOPINFromContext(ctx)
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
// x-qrdb-so-pin metadata entry of every unary RPC made with that context.
// Shared by the gRPC and Unix SDK transports. gRPC metadata values are
// strings, which Go cannot wipe; the SDK's byte copy is wiped as soon as the
// string is built.
func SOPINUnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(withSOPINMetadata(ctx), method, req, reply, cc, opts...)
	}
}

// SOPINStreamClientInterceptor is SOPINUnaryClientInterceptor for streaming
// RPCs.
func SOPINStreamClientInterceptor() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(withSOPINMetadata(ctx), desc, cc, method, opts...)
	}
}

func withSOPINMetadata(ctx context.Context) context.Context {
	pin, ok := SOPINFromContext(ctx)
	if !ok {
		return ctx
	}
	defer secure.Wipe(pin)
	return metadata.AppendToOutgoingContext(ctx, SOPINMetadataKey, string(pin))
}

// SOPINHeaders returns the X-QRDB-SO-PIN header for the PIN bound by
// WithSOPIN, or nil when none is bound. It is the header function the MCP
// transport installs, which its HTTP client calls per request with that
// request's context.
func SOPINHeaders(ctx context.Context) map[string]string {
	pin, ok := SOPINFromContext(ctx)
	if !ok {
		return nil
	}
	defer secure.Wipe(pin)
	return map[string]string{SOPINHeader: string(pin)}
}

// SOPINRoundTripper wraps base so that a request made with a context bound by
// WithSOPIN carries the PIN in the X-QRDB-SO-PIN header. The REST transport
// installs it. A request without a bound PIN passes through untouched.
func SOPINRoundTripper(base http.RoundTripper) http.RoundTripper {
	return &soPINRoundTripper{base: base}
}

type soPINRoundTripper struct{ base http.RoundTripper }

// RoundTrip implements http.RoundTripper.
func (rt *soPINRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	pin, ok := SOPINFromContext(req.Context())
	if !ok {
		return rt.base.RoundTrip(req)
	}
	// Clone before mutating headers, per the RoundTripper contract.
	clone := req.Clone(req.Context())
	clone.Header.Set(SOPINHeader, string(pin))
	secure.Wipe(pin)
	return rt.base.RoundTrip(clone)
}

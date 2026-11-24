// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sdk

import (
	"crypto/tls"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ClientOption configures the SDK client.
type ClientOption func(*clientOptions)

// clientOptions holds resolved SDK client configuration.
type clientOptions struct {
	transportOpts []transport.Option
}

// WithAddress sets the server address for the underlying transport.
func WithAddress(addr string) ClientOption {
	return func(o *clientOptions) {
		o.transportOpts = append(o.transportOpts, transport.WithAddress(addr))
	}
}

// WithTLS enables TLS with the specified certificate and key files.
func WithTLS(certFile, keyFile, caFile string) ClientOption {
	return func(o *clientOptions) {
		o.transportOpts = append(o.transportOpts, transport.WithTLS(certFile, keyFile, caFile))
	}
}

// WithTLSConfig sets a custom tls.Config on the underlying transport.
func WithTLSConfig(cfg *tls.Config) ClientOption {
	return func(o *clientOptions) {
		o.transportOpts = append(o.transportOpts, transport.WithTLSConfig(cfg))
	}
}

// WithTimeout sets the request timeout on the underlying transport.
func WithTimeout(d time.Duration) ClientOption {
	return func(o *clientOptions) {
		o.transportOpts = append(o.transportOpts, transport.WithTimeout(d))
	}
}

// WithBearerToken sends "Authorization: Bearer <token>" on every request of
// the REST, gRPC, Unix and MCP transports. It mirrors
// transport.WithBearerToken: the token is sent only over TLS and, on REST and
// MCP, only to the configured host. The QUIC transport refuses it.
func WithBearerToken(token string) ClientOption {
	return func(o *clientOptions) {
		o.transportOpts = append(o.transportOpts, transport.WithBearerToken(token))
	}
}

// WithTokenSource is WithBearerToken with a token that src supplies; src is
// consulted once per request, so a refreshed token is used at once. It
// mirrors transport.WithTokenSource.
func WithTokenSource(src transport.TokenSource) ClientOption {
	return func(o *clientOptions) {
		o.transportOpts = append(o.transportOpts, transport.WithTokenSource(src))
	}
}

// TransportOptions turns root client options into transport options. The
// result is passed to a transport constructor such as rest.New, grpc.New,
// unix.New or mcp.New; nil options are skipped.
func TransportOptions(opts ...ClientOption) []transport.Option {
	o := &clientOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
	out := make([]transport.Option, len(o.transportOpts))
	copy(out, o.transportOpts)
	return out
}

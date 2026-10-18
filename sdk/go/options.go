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

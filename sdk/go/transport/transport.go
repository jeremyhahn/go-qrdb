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

// Package transport defines the transport abstraction layer for the QRDB
// SDK. Transports implement the communication mechanism between the SDK
// client and the QRDB server, supporting REST (HTTP/2), gRPC, and
// embedded (in-process) modes.
package transport

import (
	"context"
	"crypto/tls"
	"time"
)

// Transport is the interface that all SDK transports must implement.
// It provides connection lifecycle management and a generic request
// method for transport-agnostic RPC dispatch.
type Transport interface {
	// Connect establishes the transport connection. For network
	// transports this performs the initial handshake; for embedded
	// transports this is typically a no-op.
	Connect(ctx context.Context) error

	// Close shuts down the transport and releases all resources.
	Close() error

	// Healthy returns true if the transport connection is active
	// and capable of serving requests.
	Healthy(ctx context.Context) bool

	// Request performs a generic RPC call using the given method name.
	// The req and resp parameters are transport-specific; network
	// transports serialize them while embedded transports pass them
	// directly. This method is primarily used for extensibility;
	// callers should prefer the typed KVClient and AdminClient
	// interfaces.
	Request(ctx context.Context, method string, req, resp any) error
}

// Config holds transport configuration. All transports share this
// common configuration structure.
type Config struct {
	// Address is the server address (e.g., "https://localhost:63002").
	Address string

	// TLSEnabled enables TLS for the transport connection.
	TLSEnabled bool

	// TLSCertFile is the path to the client TLS certificate for mTLS.
	TLSCertFile string

	// TLSKeyFile is the path to the client TLS private key for mTLS.
	TLSKeyFile string

	// TLSCAFile is the path to the CA certificate file for server
	// verification.
	TLSCAFile string

	// TLSConfig is a custom tls.Config that overrides the file-based
	// TLS configuration when set.
	TLSConfig *tls.Config

	// Timeout is the default timeout for individual requests. When
	// zero, a default of 30 seconds is used.
	Timeout time.Duration
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Timeout: 30 * time.Second,
	}
}

// Validate checks the configuration for consistency and returns an error
// if the configuration is invalid.
func (c *Config) Validate() error {
	if c.Address == "" && c.TLSConfig == nil {
		return &ConfigError{Field: "Address", Reason: "address is required for network transports"}
	}
	if c.TLSEnabled && c.TLSConfig == nil && c.TLSCAFile == "" {
		return &ConfigError{Field: "TLSCAFile", Reason: "CA file is required when TLS is enabled without custom TLSConfig"}
	}
	if c.TLSCertFile != "" && c.TLSKeyFile == "" {
		return &ConfigError{Field: "TLSKeyFile", Reason: "key file is required when cert file is specified"}
	}
	if c.TLSKeyFile != "" && c.TLSCertFile == "" {
		return &ConfigError{Field: "TLSCertFile", Reason: "cert file is required when key file is specified"}
	}
	return nil
}

// Option configures a transport Config using the functional options pattern.
type Option func(*Config)

// WithAddress sets the server address.
func WithAddress(addr string) Option {
	return func(c *Config) {
		c.Address = addr
	}
}

// WithTLS enables TLS and sets the certificate, key, and CA file paths.
// Providing cert and key files enables mutual TLS (mTLS).
func WithTLS(certFile, keyFile, caFile string) Option {
	return func(c *Config) {
		c.TLSEnabled = true
		c.TLSCertFile = certFile
		c.TLSKeyFile = keyFile
		c.TLSCAFile = caFile
	}
}

// WithMTLS enables mutual TLS with the given client certificate, key,
// and CA certificate file paths.
func WithMTLS(certFile, keyFile, caFile string) Option {
	return func(c *Config) {
		c.TLSEnabled = true
		c.TLSCertFile = certFile
		c.TLSKeyFile = keyFile
		c.TLSCAFile = caFile
	}
}

// WithTLSConfig sets a custom tls.Config that overrides file-based TLS
// configuration.
func WithTLSConfig(cfg *tls.Config) Option {
	return func(c *Config) {
		c.TLSEnabled = true
		c.TLSConfig = cfg
	}
}

// WithTimeout sets the request timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Config) {
		c.Timeout = d
	}
}

// ApplyOptions applies the given options to the default configuration
// and returns the resulting Config.
func ApplyOptions(opts ...Option) *Config {
	cfg := DefaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

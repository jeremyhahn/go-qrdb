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

package transport

import (
	"crypto/tls"
	"crypto/x509"
	"os"
)

// CAFileReadError is returned when a CA certificate file cannot be read.
type CAFileReadError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *CAFileReadError) Error() string {
	return "transport: failed to read CA file " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CAFileReadError) Unwrap() error {
	return e.Err
}

// CAParseError is returned when no CA certificate can be parsed from the
// PEM data in a CA file.
type CAParseError struct {
	Path string
}

// Error returns a human-readable description.
func (e *CAParseError) Error() string {
	return "transport: failed to parse CA certificate from " + e.Path
}

// CertificateLoadError is returned when a client TLS certificate and key
// pair cannot be loaded.
type CertificateLoadError struct {
	Err error
}

// Error returns a human-readable description.
func (e *CertificateLoadError) Error() string {
	return "transport: failed to load client certificate: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CertificateLoadError) Unwrap() error {
	return e.Err
}

// TLSBuildOptions configures BuildTLSConfig. The zero value disables TLS.
type TLSBuildOptions struct {
	// Custom is a pre-built tls.Config that takes precedence over all
	// file-based fields. When set with no NextProtos it is returned
	// unmodified; when NextProtos is also set, a clone with the required
	// ALPN protocols appended is returned instead.
	Custom *tls.Config

	// Enabled controls whether a file-based configuration is built when
	// Custom is nil. When false, BuildTLSConfig returns a nil config so
	// callers can fall back to plaintext transports.
	Enabled bool

	// CAFile is the path to a PEM-encoded CA bundle used for server
	// certificate verification. Optional; when empty the host root pool
	// is used.
	CAFile string

	// CertFile is the path to a PEM-encoded client certificate for mTLS.
	// It is only loaded when KeyFile is also set.
	CertFile string

	// KeyFile is the path to the PEM-encoded private key for CertFile.
	// It is only loaded when CertFile is also set.
	KeyFile string

	// NextProtos lists ALPN protocols the connection must advertise
	// (e.g. "qrdb-quic" for the QUIC transport). Protocols already
	// present in a Custom config are not duplicated.
	NextProtos []string
}

// BuildTLSConfig constructs a client *tls.Config shared by all SDK
// transports. Precedence: a Custom config wins over the file-based
// fields; when Custom is nil and Enabled is false the result is nil.
// File-based configs enforce TLS 1.3 as the minimum version. All
// failures are returned as *TLSError wrapping a typed cause
// (*CAFileReadError, *CAParseError, or *CertificateLoadError).
func BuildTLSConfig(opts TLSBuildOptions) (*tls.Config, error) {
	if opts.Custom != nil {
		if len(opts.NextProtos) == 0 {
			return opts.Custom, nil
		}
		cfg := opts.Custom.Clone()
		cfg.NextProtos = mergeALPN(cfg.NextProtos, opts.NextProtos)
		return cfg, nil
	}

	if !opts.Enabled {
		return nil, nil
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: opts.NextProtos,
	}

	// Load CA certificate pool.
	if opts.CAFile != "" {
		pool, err := LoadCertPool(opts.CAFile)
		if err != nil {
			return nil, &TLSError{Err: err}
		}
		tlsCfg.RootCAs = pool
	}

	// Load client certificate for mTLS.
	if opts.CertFile != "" && opts.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
		if err != nil {
			return nil, &TLSError{Err: &CertificateLoadError{Err: err}}
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	return tlsCfg, nil
}

// LoadCertPool reads a PEM-encoded CA bundle from caFile and returns it
// as an *x509.CertPool. It returns *CAFileReadError when the file cannot
// be read and *CAParseError when the file contains no parsable CA
// certificate.
func LoadCertPool(caFile string) (*x509.CertPool, error) {
	pemData, err := os.ReadFile(caFile)
	if err != nil {
		return nil, &CAFileReadError{Path: caFile, Err: err}
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, &CAParseError{Path: caFile}
	}
	return pool, nil
}

// mergeALPN appends each required ALPN protocol to protos unless it is
// already present, preserving the existing order.
func mergeALPN(protos, required []string) []string {
	for _, want := range required {
		present := false
		for _, p := range protos {
			if p == want {
				present = true
				break
			}
		}
		if !present {
			protos = append(protos, want)
		}
	}
	return protos
}

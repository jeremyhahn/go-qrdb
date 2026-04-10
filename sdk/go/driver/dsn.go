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

package driver

import (
	"net/url"
	"strings"
)

// Supported DSN schemes.
const (
	SchemeEmbedded = "embedded"
	SchemeREST     = "rest"
	SchemeGRPC     = "grpc"
)

// supportedSchemes is the set of recognized DSN schemes, used for O(1) lookup.
var supportedSchemes = map[string]struct{}{
	SchemeEmbedded: {},
	SchemeREST:     {},
	SchemeGRPC:     {},
}

// DSNConfig holds the parsed components of a driver DSN string.
type DSNConfig struct {
	// Scheme is the connection mode: "embedded", "rest", or "grpc".
	Scheme string

	// Host is the server address for network modes (e.g., "localhost:63001").
	// For embedded mode with a named backend, this holds the backend name
	// (e.g., "memory").
	Host string

	// Database is the target database name.
	Database string

	// Backend is the storage backend type for embedded mode (memory, file, pebble).
	Backend string

	// Engine is the engine type for embedded mode (kv, timeseries, vector).
	Engine string

	// DataDir is the data directory for file/pebble backends in embedded mode.
	DataDir string

	// CAFile is the path to the CA certificate for TLS in network mode.
	CAFile string

	// CertFile is the path to the client certificate for mTLS in network mode.
	CertFile string

	// KeyFile is the path to the client key for mTLS in network mode.
	KeyFile string
}

// ParseDSN parses a DSN string into a DSNConfig.
//
// Supported formats:
//
//	embedded://memory?database=mydb
//	embedded:///path/to/data?backend=pebble&database=mydb&engine=kv
//	rest://host:port/dbname?ca=path&cert=path&key=path
//	grpc://host:port/dbname?ca=path&cert=path&key=path
func ParseDSN(dsn string) (*DSNConfig, error) {
	if dsn == "" {
		return nil, &DSNParseError{DSN: dsn, Reason: "empty DSN"}
	}

	u, err := url.Parse(dsn)
	if err != nil {
		return nil, &DSNParseError{DSN: dsn, Reason: err.Error()}
	}

	scheme := strings.ToLower(u.Scheme)
	if _, ok := supportedSchemes[scheme]; !ok {
		return nil, &UnsupportedSchemeError{Scheme: scheme}
	}

	cfg := &DSNConfig{Scheme: scheme}

	parsers := map[string]func(*url.URL, *DSNConfig) error{
		SchemeEmbedded: parseEmbeddedDSN,
		SchemeREST:     parseNetworkDSN,
		SchemeGRPC:     parseNetworkDSN,
	}

	if parse, ok := parsers[scheme]; ok {
		if err := parse(u, cfg); err != nil {
			return nil, &DSNParseError{DSN: dsn, Reason: err.Error()}
		}
	}

	return cfg, nil
}

// parseEmbeddedDSN extracts embedded-mode fields from the URL.
func parseEmbeddedDSN(u *url.URL, cfg *DSNConfig) error {
	q := u.Query()
	cfg.Database = q.Get("database")
	if cfg.Database == "" {
		return &DSNParseError{DSN: u.String(), Reason: "missing required parameter: database"}
	}

	cfg.Backend = q.Get("backend")
	cfg.Engine = q.Get("engine")

	// The host component determines the backend when it is a known name
	// (e.g., "memory"). Otherwise, the path is the data directory.
	host := u.Host
	path := u.Path

	if host != "" {
		// embedded://memory?database=mydb
		cfg.Host = host
		if cfg.Backend == "" {
			cfg.Backend = host
		}
	}

	if path != "" && path != "/" {
		// embedded:///path/to/data?backend=pebble&database=mydb
		cfg.DataDir = path
	}

	// Default backend to memory when not specified.
	if cfg.Backend == "" {
		cfg.Backend = "memory"
	}

	// Default engine to kv when not specified.
	if cfg.Engine == "" {
		cfg.Engine = "kv"
	}

	return nil
}

// parseNetworkDSN extracts network-mode fields from the URL.
func parseNetworkDSN(u *url.URL, cfg *DSNConfig) error {
	cfg.Host = u.Host
	if cfg.Host == "" {
		return &DSNParseError{DSN: u.String(), Reason: "missing host"}
	}

	// Database name comes from the URL path (strip leading slash).
	cfg.Database = strings.TrimPrefix(u.Path, "/")
	if cfg.Database == "" {
		return &DSNParseError{DSN: u.String(), Reason: "missing database name in path"}
	}

	q := u.Query()
	cfg.CAFile = q.Get("ca")
	cfg.CertFile = q.Get("cert")
	cfg.KeyFile = q.Get("key")

	return nil
}

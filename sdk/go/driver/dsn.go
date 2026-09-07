//go:build qrdb_sql

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

// DSN schemes built into this package. Additional schemes are contributed by
// subpackages through RegisterScheme.
const (
	SchemeEmbedded = "embedded"
	SchemeREST     = "rest"
	SchemeGRPC     = "grpc"
)

// DSNConfig holds the parsed components of a driver DSN string.
type DSNConfig struct {
	// Scheme is the connection mode, e.g. "embedded", "rest", or "grpc".
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

	// Tenant scopes every statement on the connection to a tenant namespace.
	// Empty means the session runs as the system tenant.
	Tenant string

	// User is the subject recorded on the session's identity. It is
	// informational for embedded connections, which are not authenticated by
	// the driver.
	User string

	// Location names the time zone used to render time.Time arguments as SQL
	// datetime literals (an IANA name, or "Local"). Empty means UTC.
	Location string
}

// ParseDSN parses a DSN string into a DSNConfig, dispatching to the parser
// the scheme was registered with (see RegisterScheme).
//
// Formats served by the built-in schemes:
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
	registered, ok := lookupScheme(scheme)
	if !ok {
		return nil, &UnsupportedSchemeError{Scheme: scheme}
	}

	cfg := &DSNConfig{Scheme: scheme}
	if err := registered.Parse(u, cfg); err != nil {
		return nil, &DSNParseError{DSN: dsn, Reason: err.Error()}
	}

	return cfg, nil
}

// ParseEmbeddedDSN extracts in-process-mode fields (database, backend,
// engine, data directory, tenant, user, location) from the URL.
//
// It is exported because it is the DSN grammar of an in-process connection,
// not of one particular scheme: sdk/go/driver/node's "node" scheme accepts
// exactly the same DSN and registers this function verbatim rather than
// maintaining a second copy that could drift from it.
func ParseEmbeddedDSN(u *url.URL, cfg *DSNConfig) error {
	q := u.Query()
	cfg.Database = q.Get("database")
	if cfg.Database == "" {
		return &DSNParseError{DSN: u.String(), Reason: "missing required parameter: database"}
	}

	cfg.Backend = q.Get("backend")
	cfg.Engine = q.Get("engine")
	cfg.Tenant = q.Get("tenant")
	cfg.User = q.Get("user")
	cfg.Location = q.Get("loc")

	// The host component determines the backend when it is a known name
	// (e.g., "memory"). Otherwise, the path is the data directory.
	host := u.Host
	path := u.Path

	if host != "" {
		// As in "embedded://memory?database=mydb", where the host names the
		// backend rather than a network peer.
		cfg.Host = host
		if cfg.Backend == "" {
			cfg.Backend = host
		}
	}

	if path != "" && path != "/" {
		// As in "embedded:///path/to/data?backend=pebble&database=mydb", where
		// the empty host leaves the path to name the data directory.
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

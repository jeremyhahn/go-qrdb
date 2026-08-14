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
	"database/sql/driver"
	"errors"
	"net/url"
	"testing"
)

// noopScheme returns a Scheme whose halves are valid but inert, for
// registration tests that never open a connection through it.
func noopScheme() Scheme {
	return Scheme{
		Parse:   func(*url.URL, *DSNConfig) error { return nil },
		Connect: func(*DSNConfig) (driver.Connector, error) { return nil, nil },
	}
}

// TestRegisterScheme_AddsParserAndConnector verifies a newly registered
// scheme is reachable through BOTH ParseDSN and OpenConnector — the coupling
// the single registry exists to guarantee.
func TestRegisterScheme_AddsParserAndConnector(t *testing.T) {
	const name = "testscheme"

	var connectorBuilt bool
	RegisterScheme(name, Scheme{
		Parse: func(u *url.URL, cfg *DSNConfig) error {
			cfg.Database = u.Query().Get("database")
			return nil
		},
		Connect: func(cfg *DSNConfig) (driver.Connector, error) {
			connectorBuilt = true
			return NewEmbeddedConnector(cfg), nil
		},
	})

	cfg, err := ParseDSN(name + "://host?database=viaregistry")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if cfg.Scheme != name {
		t.Errorf("Scheme = %q, want %q", cfg.Scheme, name)
	}
	if cfg.Database != "viaregistry" {
		t.Errorf("Database = %q, want %q", cfg.Database, "viaregistry")
	}

	connector, err := (&Driver{}).OpenConnector(name + "://host?database=viaregistry")
	if err != nil {
		t.Fatalf("OpenConnector: %v", err)
	}
	if !connectorBuilt {
		t.Error("OpenConnector did not use the registered connector factory")
	}
	if c, ok := connector.(*EmbeddedConnector); ok {
		_ = c.Close()
	}
}

// TestRegisterScheme_PropagatesParserError verifies a scheme parser's error
// is reported as a *DSNParseError, exactly like the built-in parsers'.
func TestRegisterScheme_PropagatesParserError(t *testing.T) {
	const name = "failingscheme"
	sentinel := errors.New("scheme parser refused it")

	RegisterScheme(name, Scheme{
		Parse:   func(*url.URL, *DSNConfig) error { return sentinel },
		Connect: func(*DSNConfig) (driver.Connector, error) { return nil, nil },
	})

	_, err := ParseDSN(name + "://host")
	if err == nil {
		t.Fatal("expected the parser's error to be reported")
	}
	var parseErr *DSNParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error = %v (%T), want *DSNParseError", err, err)
	}
	if parseErr.Reason != sentinel.Error() {
		t.Errorf("Reason = %q, want %q", parseErr.Reason, sentinel.Error())
	}
}

// TestRegisterScheme_Panics verifies every invalid registration fails at
// init-time rather than producing a half-registered scheme.
func TestRegisterScheme_Panics(t *testing.T) {
	tests := []struct {
		name   string
		scheme string
		s      Scheme
	}{
		{name: "empty name", scheme: "", s: noopScheme()},
		{
			name:   "nil parse",
			scheme: "nilparse",
			s:      Scheme{Connect: func(*DSNConfig) (driver.Connector, error) { return nil, nil }},
		},
		{
			name:   "nil connect",
			scheme: "nilconnect",
			s:      Scheme{Parse: func(*url.URL, *DSNConfig) error { return nil }},
		},
		{name: "duplicate", scheme: SchemeEmbedded, s: noopScheme()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("RegisterScheme(%q) did not panic", tt.scheme)
				}
			}()
			RegisterScheme(tt.scheme, tt.s)
		})
	}
}

// TestParseDSN_UnregisteredScheme verifies an unknown scheme is rejected
// before any connector is built.
func TestParseDSN_UnregisteredScheme(t *testing.T) {
	_, err := ParseDSN("neverregistered://host/db")
	var schemeErr *UnsupportedSchemeError
	if !errors.As(err, &schemeErr) {
		t.Fatalf("error = %v (%T), want *UnsupportedSchemeError", err, err)
	}
	if schemeErr.Scheme != "neverregistered" {
		t.Errorf("Scheme = %q, want %q", schemeErr.Scheme, "neverregistered")
	}
}

// TestParseEmbeddedDSN_Exported verifies the exported in-process parser is
// usable directly, which is how the node scheme reuses it.
func TestParseEmbeddedDSN_Exported(t *testing.T) {
	u, err := url.Parse("anything://memory?database=direct&tenant=acme&user=bob&loc=UTC")
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}

	var cfg DSNConfig
	if err := ParseEmbeddedDSN(u, &cfg); err != nil {
		t.Fatalf("ParseEmbeddedDSN: %v", err)
	}
	if cfg.Database != "direct" || cfg.Backend != "memory" || cfg.Engine != "kv" {
		t.Errorf("cfg = %+v, want database=direct backend=memory engine=kv", cfg)
	}
	if cfg.Tenant != "acme" || cfg.User != "bob" || cfg.Location != "UTC" {
		t.Errorf("cfg = %+v, want tenant=acme user=bob loc=UTC", cfg)
	}
}

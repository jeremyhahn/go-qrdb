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
	"net/url"
	"sort"
	"sync"
)

// SchemeParser fills the scheme-specific fields of cfg from a parsed DSN URL.
// cfg already carries the lower-cased scheme; everything else is the parser's
// responsibility. A returned error is wrapped in a *DSNParseError by ParseDSN.
type SchemeParser func(u *url.URL, cfg *DSNConfig) error

// SchemeConnector builds the driver.Connector that serves a parsed DSN.
type SchemeConnector func(cfg *DSNConfig) (driver.Connector, error)

// Scheme is one DSN scheme's complete implementation: how its DSN is parsed
// and how a connector for it is built.
//
// Both halves live in a single value on purpose. They used to be two parallel
// maps (plus a third recording which schemes existed), which made it possible
// to add a scheme to two of the three and have it fail at whichever step was
// missed -- a "scheme is supported but has no connector" state the type system
// now makes unrepresentable.
type Scheme struct {
	// Parse extracts the scheme's DSN fields. Required.
	Parse SchemeParser

	// Connect builds a connector for an already-parsed DSN. Required.
	Connect SchemeConnector
}

// schemes is the single registry of DSN schemes, keyed by lower-cased scheme
// name. It is guarded by schemesMu because RegisterScheme may run from an
// imported package's init() while another package's init() is already
// resolving a DSN.
var (
	schemesMu sync.RWMutex
	schemes   = make(map[string]Scheme)
)

// RegisterScheme makes a DSN scheme available to ParseDSN and
// Driver.OpenConnector. It is the extension point a subpackage uses to add a
// scheme whose dependencies do not belong in this package's import graph --
// see sdk/go/driver/node, which registers "node" from its own init().
//
// It panics if name is empty, if either half of s is nil, or if name is
// already registered. This mirrors database/sql.Register, and for the same
// reason: registration happens from package init(), where there is no caller
// to return an error to, and every one of those conditions is a programming
// error in the registering package that no amount of runtime handling can
// make correct. Failing at process start is strictly better than serving a
// half-registered scheme.
func RegisterScheme(name string, s Scheme) {
	if name == "" {
		panic("driver: RegisterScheme called with an empty scheme name")
	}
	if s.Parse == nil {
		panic("driver: RegisterScheme(" + name + ") called with a nil Parse")
	}
	if s.Connect == nil {
		panic("driver: RegisterScheme(" + name + ") called with a nil Connect")
	}

	schemesMu.Lock()
	defer schemesMu.Unlock()
	if _, dup := schemes[name]; dup {
		panic("driver: RegisterScheme called twice for scheme " + name)
	}
	schemes[name] = s
}

// RegisteredSchemes returns the sorted names of every registered DSN scheme.
func RegisteredSchemes() []string {
	schemesMu.RLock()
	names := make([]string, 0, len(schemes))
	for name := range schemes {
		names = append(names, name)
	}
	schemesMu.RUnlock()

	sort.Strings(names)
	return names
}

// lookupScheme returns the registered Scheme for name.
func lookupScheme(name string) (Scheme, bool) {
	schemesMu.RLock()
	defer schemesMu.RUnlock()
	s, ok := schemes[name]
	return s, ok
}

// init registers the schemes built into this package. They go through
// RegisterScheme like any out-of-package scheme so there is no privileged
// path: if the mechanism breaks, embedded:// breaks with it and is noticed
// immediately.
func init() {
	RegisterScheme(SchemeEmbedded, Scheme{
		Parse: ParseEmbeddedDSN,
		Connect: func(cfg *DSNConfig) (driver.Connector, error) {
			return NewEmbeddedConnector(cfg), nil
		},
	})
	networkScheme := Scheme{
		Parse: parseNetworkDSN,
		Connect: func(cfg *DSNConfig) (driver.Connector, error) {
			return NewNetworkConnector(cfg), nil
		},
	}
	RegisterScheme(SchemeREST, networkScheme)
	RegisterScheme(SchemeGRPC, networkScheme)
}

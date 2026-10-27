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

//go:build clusterproof

package workload

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

// Config is everything a run needs to know about its targets and its load.
// Nothing here has a built-in host: the endpoints and credentials always come
// from the operator's configuration file.
type Config struct {
	// Endpoints are the node API addresses, in the address form of
	// Protocol (host:port for quic and grpc, https://host:port for rest).
	// Client i drives Endpoints[i % len(Endpoints)].
	Endpoints []string `json:"endpoints"`
	// Protocol selects the SDK transport NewTransportDialer builds: one of
	// quic, grpc or rest.
	Protocol string `json:"protocol,omitempty"`
	// Credentials are the client TLS files. Paths only: the workload hands
	// them to the transport and never reads key material itself.
	Credentials Credentials `json:"credentials"`

	// Clients is the number of concurrent clients.
	Clients int `json:"clients"`
	// Keys is the size of the key space the clients contend on.
	Keys int `json:"keys"`
	// KeyPrefix namespaces this run's keys, so a rerun against the same
	// cluster starts from absent keys. Empty means a fresh random prefix.
	KeyPrefix string `json:"key_prefix,omitempty"`
	// Mix weighs the operations each client picks.
	Mix Mix `json:"mix"`
	// Seed makes the operation sequence reproducible; each client derives
	// its own stream from it.
	Seed uint64 `json:"seed"`

	// OpTimeout bounds one operation. A write still unanswered then is
	// recorded indeterminate.
	OpTimeout Duration `json:"op_timeout"`
	// Duration bounds the load phase; zero means until the context ends or
	// every client has issued OpsPerClient operations.
	Duration Duration `json:"duration,omitempty"`
	// OpsPerClient bounds each client's operation count; zero is unbounded.
	OpsPerClient int `json:"ops_per_client,omitempty"`
	// RedialBackoff is the first pause after a failed dial or a failed
	// operation. Each further failure in a row doubles it, up to MaxBackoff,
	// and each pause is jittered; an operation that succeeds resets it.
	RedialBackoff Duration `json:"redial_backoff,omitempty"`
	// MaxBackoff caps the pause RedialBackoff grows to; zero means
	// DefaultMaxBackoff, or RedialBackoff when that is larger. It bounds how
	// often a client retries a node that stays down, and so the history it
	// records.
	MaxBackoff Duration `json:"max_backoff,omitempty"`
}

// Credentials name the client's TLS files. CAFile alone verifies the server;
// with CertFile and KeyFile the client also presents a certificate (mTLS).
type Credentials struct {
	CAFile   string `json:"ca_file,omitempty"`
	CertFile string `json:"cert_file,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`
}

// Mix holds relative weights; a zero weight disables that operation.
type Mix struct {
	Put    int `json:"put"`
	Get    int `json:"get"`
	Delete int `json:"delete"`
	CAS    int `json:"cas"`
}

// DefaultMix is a write-heavy mix: enough deletes and swaps to exercise every
// write path, enough reads to observe them.
var DefaultMix = Mix{Put: 4, Get: 3, Delete: 1, CAS: 2}

// Defaults applied by Normalize where the configuration leaves a field zero.
const (
	DefaultClients       = 8
	DefaultKeys          = 16
	DefaultOpTimeout     = 5 * time.Second
	DefaultRedialBackoff = 250 * time.Millisecond
	DefaultMaxBackoff    = 4 * time.Second
)

// Duration is a time.Duration that reads and writes as a Go duration string
// ("30s", "24h") in JSON.
type Duration time.Duration

// MarshalJSON writes d as a duration string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON reads a duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return &ConfigError{Field: "duration", Reason: "must be a duration string such as \"30s\""}
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return &ConfigError{Field: "duration", Reason: "invalid duration " + s}
	}
	*d = Duration(v)
	return nil
}

// LoadConfig reads a JSON configuration file, applies defaults and validates.
func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, &ConfigError{Field: "path", Reason: err.Error()}
	}
	defer f.Close()
	return ParseConfig(f)
}

// ParseConfig reads a JSON configuration, applies defaults and validates. An
// unknown field is an error, so a misspelt setting is not silently dropped.
func ParseConfig(r io.Reader) (Config, error) {
	var cfg Config
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		if ce, ok := errors.AsType[*ConfigError](err); ok {
			return Config{}, ce
		}
		return Config{}, &ConfigError{Field: "json", Reason: err.Error()}
	}
	if err := cfg.Normalize(); err != nil {
		return Config{}, err
	}
	return cfg, cfg.Validate()
}

// Normalize fills the zero fields that have a default, and gives the run a
// random key prefix when none is configured.
func (c *Config) Normalize() error {
	if c.Clients == 0 {
		c.Clients = DefaultClients
	}
	if c.Keys == 0 {
		c.Keys = DefaultKeys
	}
	if c.Mix == (Mix{}) {
		c.Mix = DefaultMix
	}
	if c.OpTimeout == 0 {
		c.OpTimeout = Duration(DefaultOpTimeout)
	}
	if c.RedialBackoff == 0 {
		c.RedialBackoff = Duration(DefaultRedialBackoff)
	}
	if c.MaxBackoff == 0 {
		c.MaxBackoff = Duration(c.backoffLimit())
	}
	if c.KeyPrefix == "" {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return &ConfigError{Field: "key_prefix", Reason: "generate run id: " + err.Error()}
		}
		c.KeyPrefix = "clusterproof/" + hex.EncodeToString(b[:])
	}
	return nil
}

// Validate checks c after Normalize.
func (c Config) Validate() error {
	if len(c.Endpoints) == 0 {
		return &ConfigError{Field: "endpoints", Reason: "at least one endpoint is required"}
	}
	for _, e := range c.Endpoints {
		if e == "" {
			return &ConfigError{Field: "endpoints", Reason: "empty endpoint"}
		}
	}
	switch {
	case c.Clients < 1:
		return &ConfigError{Field: "clients", Reason: "must be at least 1"}
	case c.Keys < 1:
		return &ConfigError{Field: "keys", Reason: "must be at least 1"}
	case c.KeyPrefix == "":
		return &ConfigError{Field: "key_prefix", Reason: "must not be empty"}
	case c.Mix.Put < 0 || c.Mix.Get < 0 || c.Mix.Delete < 0 || c.Mix.CAS < 0:
		return &ConfigError{Field: "mix", Reason: "weights must not be negative"}
	case c.Mix.Put+c.Mix.Get+c.Mix.Delete+c.Mix.CAS == 0:
		return &ConfigError{Field: "mix", Reason: "at least one weight must be positive"}
	case c.OpTimeout <= 0:
		return &ConfigError{Field: "op_timeout", Reason: "must be positive"}
	case c.Duration < 0:
		return &ConfigError{Field: "duration", Reason: "must not be negative"}
	case c.OpsPerClient < 0:
		return &ConfigError{Field: "ops_per_client", Reason: "must not be negative"}
	case c.RedialBackoff <= 0:
		return &ConfigError{Field: "redial_backoff", Reason: "must be positive"}
	case c.MaxBackoff < 0, c.MaxBackoff > 0 && c.MaxBackoff < c.RedialBackoff:
		return &ConfigError{Field: "max_backoff", Reason: "must not be less than redial_backoff"}
	case (c.Credentials.CertFile == "") != (c.Credentials.KeyFile == ""):
		return &ConfigError{Field: "credentials", Reason: "cert_file and key_file go together"}
	case c.Credentials.CertFile != "" && c.Credentials.CAFile == "":
		return &ConfigError{Field: "credentials", Reason: "a client certificate needs ca_file to verify the server"}
	}
	return nil
}

// backoffLimit is the cap a client's backoff grows to: MaxBackoff, or when it
// is zero the larger of DefaultMaxBackoff and RedialBackoff.
func (c Config) backoffLimit() time.Duration {
	if c.MaxBackoff == 0 {
		return max(DefaultMaxBackoff, time.Duration(c.RedialBackoff))
	}
	return time.Duration(c.MaxBackoff)
}

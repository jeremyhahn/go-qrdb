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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseConfig(t *testing.T) {
	src := `{
		"endpoints": ["db-1.example.com:7000", "db-2.example.com:7000"],
		"protocol": "quic",
		"credentials": {"ca_file": "ca.pem", "cert_file": "client.pem", "key_file": "client-key.pem"},
		"clients": 3,
		"keys": 5,
		"key_prefix": "run-1",
		"mix": {"put": 1, "get": 2, "delete": 0, "cas": 3},
		"seed": 42,
		"op_timeout": "750ms",
		"duration": "2m",
		"ops_per_client": 10,
		"redial_backoff": "1s"
	}`
	cfg, err := ParseConfig(strings.NewReader(src))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	want := Config{
		Endpoints:     []string{"db-1.example.com:7000", "db-2.example.com:7000"},
		Protocol:      "quic",
		Credentials:   Credentials{CAFile: "ca.pem", CertFile: "client.pem", KeyFile: "client-key.pem"},
		Clients:       3,
		Keys:          5,
		KeyPrefix:     "run-1",
		Mix:           Mix{Put: 1, Get: 2, CAS: 3},
		Seed:          42,
		OpTimeout:     Duration(750 * time.Millisecond),
		Duration:      Duration(2 * time.Minute),
		OpsPerClient:  10,
		RedialBackoff: Duration(time.Second),
	}
	if !equalConfig(cfg, want) {
		t.Fatalf("cfg = %+v\nwant  %+v", cfg, want)
	}
}

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := ParseConfig(strings.NewReader(`{"endpoints": ["db.example.com:7000"]}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Clients != DefaultClients || cfg.Keys != DefaultKeys || cfg.Mix != DefaultMix ||
		cfg.OpTimeout != Duration(DefaultOpTimeout) || cfg.RedialBackoff != Duration(DefaultRedialBackoff) {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if !strings.HasPrefix(cfg.KeyPrefix, "clusterproof/") || len(cfg.KeyPrefix) != len("clusterproof/")+16 {
		t.Fatalf("KeyPrefix = %q, want clusterproof/ and 16 hex digits", cfg.KeyPrefix)
	}
	again, err := ParseConfig(strings.NewReader(`{"endpoints": ["db.example.com:7000"]}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if again.KeyPrefix == cfg.KeyPrefix {
		t.Fatalf("two runs share key prefix %q", cfg.KeyPrefix)
	}
}

func TestParseConfigRejects(t *testing.T) {
	cases := map[string]struct {
		src   string
		field string
	}{
		"unknown field":       {`{"endpoints": ["a:1"], "client": 3}`, "json"},
		"not json":            {`endpoints: a`, "json"},
		"duration not string": {`{"endpoints": ["a:1"], "op_timeout": 5}`, "duration"},
		"duration malformed":  {`{"endpoints": ["a:1"], "op_timeout": "soon"}`, "duration"},
		"no endpoints":        {`{}`, "endpoints"},
		"negative clients":    {`{"endpoints": ["a:1"], "clients": -1}`, "clients"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConfig(strings.NewReader(tc.src))
			var ce *ConfigError
			if !errors.As(err, &ce) || ce.Field != tc.field {
				t.Fatalf("err = %v, want ConfigError on %q", err, tc.field)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	valid := func() Config {
		c := Config{Endpoints: []string{"a:1"}, KeyPrefix: "p"}
		if err := c.Normalize(); err != nil {
			t.Fatalf("Normalize: %v", err)
		}
		return c
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cases := map[string]struct {
		mutate func(*Config)
		field  string
	}{
		"empty endpoint":    {func(c *Config) { c.Endpoints = []string{"a:1", ""} }, "endpoints"},
		"zero keys":         {func(c *Config) { c.Keys = 0 }, "keys"},
		"zero clients":      {func(c *Config) { c.Clients = 0 }, "clients"},
		"empty prefix":      {func(c *Config) { c.KeyPrefix = "" }, "key_prefix"},
		"negative weight":   {func(c *Config) { c.Mix.Delete = -1 }, "mix"},
		"all weights zero":  {func(c *Config) { c.Mix = Mix{} }, "mix"},
		"zero op timeout":   {func(c *Config) { c.OpTimeout = 0 }, "op_timeout"},
		"negative duration": {func(c *Config) { c.Duration = -1 }, "duration"},
		"negative ops":      {func(c *Config) { c.OpsPerClient = -1 }, "ops_per_client"},
		"zero backoff":      {func(c *Config) { c.RedialBackoff = 0 }, "redial_backoff"},
		"cert without key":  {func(c *Config) { c.Credentials = Credentials{CAFile: "ca", CertFile: "c"} }, "credentials"},
		"key without cert":  {func(c *Config) { c.Credentials = Credentials{CAFile: "ca", KeyFile: "k"} }, "credentials"},
		"mtls without ca":   {func(c *Config) { c.Credentials = Credentials{CertFile: "c", KeyFile: "k"} }, "credentials"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := valid()
			tc.mutate(&c)
			var ce *ConfigError
			if err := c.Validate(); !errors.As(err, &ce) || ce.Field != tc.field {
				t.Fatalf("err = %v, want ConfigError on %q", err, tc.field)
			}
		})
	}
}

func TestNormalizeKeepsConfiguredValues(t *testing.T) {
	c := Config{Clients: 2, Keys: 3, KeyPrefix: "mine", Mix: Mix{Get: 1}, OpTimeout: 7, RedialBackoff: 9}
	if err := c.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if c.Clients != 2 || c.Keys != 3 || c.KeyPrefix != "mine" || c.Mix != (Mix{Get: 1}) || c.OpTimeout != 7 || c.RedialBackoff != 9 {
		t.Fatalf("Normalize overwrote configured values: %+v", c)
	}
}

func TestLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workload.json")
	if err := os.WriteFile(path, []byte(`{"endpoints": ["db.example.com:7000"], "key_prefix": "x"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.KeyPrefix != "x" || len(cfg.Endpoints) != 1 {
		t.Fatalf("cfg = %+v", cfg)
	}

	var ce *ConfigError
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "missing.json")); !errors.As(err, &ce) || ce.Field != "path" {
		t.Fatalf("missing file: err = %v, want ConfigError on path", err)
	}
}

func TestDurationJSON(t *testing.T) {
	b, err := json.Marshal(Duration(1500 * time.Millisecond))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(b) != `"1.5s"` {
		t.Fatalf("Marshal = %s, want \"1.5s\"", b)
	}
	var d Duration
	if err := json.Unmarshal(b, &d); err != nil || d != Duration(1500*time.Millisecond) {
		t.Fatalf("Unmarshal = %v, %v", d, err)
	}
}

func TestConfigErrorMessage(t *testing.T) {
	err := &ConfigError{Field: "keys", Reason: "must be at least 1"}
	if got := err.Error(); got != "workload: config keys: must be at least 1" {
		t.Fatalf("Error() = %q", got)
	}
}

// equalConfig compares two configs field by field; Config holds a slice, so
// it is not comparable with ==.
func equalConfig(a, b Config) bool {
	if len(a.Endpoints) != len(b.Endpoints) {
		return false
	}
	for i := range a.Endpoints {
		if a.Endpoints[i] != b.Endpoints[i] {
			return false
		}
	}
	a.Endpoints, b.Endpoints = nil, nil
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

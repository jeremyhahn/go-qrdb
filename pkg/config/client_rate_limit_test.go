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

package config

import (
	"errors"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestClientRateLimit_DefaultIsDisabledAndValid(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Transport.ClientRateLimit.Enabled() {
		t.Fatal("client_rate_limit is on by default")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestClientRateLimit_Validate(t *testing.T) {
	cases := []struct {
		name  string
		block ClientRateLimitConfig
		field string
	}{
		{"valid", ClientRateLimitConfig{RatePerSecond: 10, Burst: 20, TrustedProxies: []TrustedProxyConfig{xff("10.0.0.0/8"), fwd("192.0.2.1"), {Address: "2001:db8::/32", Header: " X-Forwarded-For "}}}, ""},
		{"rate without burst", ClientRateLimitConfig{RatePerSecond: 10}, "transport.client_rate_limit.burst"},
		{"burst without rate", ClientRateLimitConfig{Burst: 10}, "transport.client_rate_limit.rate_per_second"},
		{"negative rate", ClientRateLimitConfig{RatePerSecond: -1, Burst: 1}, "transport.client_rate_limit.rate_per_second"},
		{"NaN rate", ClientRateLimitConfig{RatePerSecond: math.NaN(), Burst: 1}, "transport.client_rate_limit.rate_per_second"},
		{"infinite rate", ClientRateLimitConfig{RatePerSecond: math.Inf(1), Burst: 1}, "transport.client_rate_limit.rate_per_second"},
		{"negative burst", ClientRateLimitConfig{RatePerSecond: 1, Burst: -1}, "transport.client_rate_limit.burst"},
		{"proxies without limit", ClientRateLimitConfig{TrustedProxies: []TrustedProxyConfig{xff("10.0.0.1")}}, "transport.client_rate_limit.trusted_proxies"},
		{"bad CIDR", ClientRateLimitConfig{RatePerSecond: 1, Burst: 1, TrustedProxies: []TrustedProxyConfig{xff("10.0.0.0/33")}}, "transport.client_rate_limit.trusted_proxies"},
		{"bad address", ClientRateLimitConfig{RatePerSecond: 1, Burst: 1, TrustedProxies: []TrustedProxyConfig{xff("proxy.example.com")}}, "transport.client_rate_limit.trusted_proxies"},
		{"no header", ClientRateLimitConfig{RatePerSecond: 1, Burst: 1, TrustedProxies: []TrustedProxyConfig{{Address: "10.0.0.1"}}}, "transport.client_rate_limit.trusted_proxies"},
		{"unknown header", ClientRateLimitConfig{RatePerSecond: 1, Burst: 1, TrustedProxies: []TrustedProxyConfig{{Address: "10.0.0.1", Header: "x-real-ip"}}}, "transport.client_rate_limit.trusted_proxies"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Transport.ClientRateLimit = tc.block
			err := cfg.Validate()
			if tc.field == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != tc.field {
				t.Fatalf("Validate = %v, want *ValidationError on %s", err, tc.field)
			}
		})
	}
}

func xff(address string) TrustedProxyConfig {
	return TrustedProxyConfig{Address: address, Header: "x-forwarded-for"}
}

func fwd(address string) TrustedProxyConfig {
	return TrustedProxyConfig{Address: address, Header: "forwarded"}
}

func TestClientRateLimit_ClientLimitProxies(t *testing.T) {
	got, err := ClientRateLimitConfig{TrustedProxies: []TrustedProxyConfig{
		xff(" 10.1.2.3/8 "),
		{Address: "192.0.2.1", Header: "Forwarded"},
		fwd("2001:db8::1"),
		xff("fe80::1%eth0"),
	}}.ClientLimitProxies()
	if err != nil {
		t.Fatal(err)
	}
	want := []TrustedProxy{
		{Prefix: netip.MustParsePrefix("10.0.0.0/8"), Header: headerXForwardedFor},
		{Prefix: netip.MustParsePrefix("192.0.2.1/32"), Header: headerForwarded},
		{Prefix: netip.MustParsePrefix("2001:db8::1/128"), Header: headerForwarded},
		{Prefix: netip.MustParsePrefix("fe80::1/128"), Header: headerXForwardedFor},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestClientRateLimit_ClientLimitProxiesRefusesUnknownHeader(t *testing.T) {
	_, err := ClientRateLimitConfig{TrustedProxies: []TrustedProxyConfig{{Address: "10.0.0.1", Header: "x-real-ip"}}}.ClientLimitProxies()
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "transport.client_rate_limit.trusted_proxies" {
		t.Fatalf("ClientLimitProxies = %v, want *ValidationError on trusted_proxies", err)
	}
}

func TestClientRateLimit_LoadsFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qrdb.yaml")
	yaml := "transport:\n  client_rate_limit:\n    rate_per_second: 2.5\n    burst: 40\n    trusted_proxies:\n      - address: 127.0.0.1\n        header: forwarded\n      - address: 10.0.0.0/8\n        header: x-forwarded-for\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.Transport.ClientRateLimit
	if got.RatePerSecond != 2.5 || got.Burst != 40 || !slices.Equal(got.TrustedProxies, []TrustedProxyConfig{fwd("127.0.0.1"), xff("10.0.0.0/8")}) || !got.Enabled() {
		t.Fatalf("loaded %+v", got)
	}
}

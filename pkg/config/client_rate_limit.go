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
	"math"
	"net/netip"
	"strconv"
	"strings"
)

// ClientRateLimitConfig is transport.client_rate_limit: the
// pre-authentication request-rate limit applied per client address on every
// network transport (REST over HTTP/2 and HTTP/3, gRPC and gRPC-Web, native
// QUIC, MCP, and the Unix socket per local peer), before authentication or
// any public-route allowlist runs (go-qrdb#1197). One budget per address is
// shared by every transport.
//
// The block is off by default, consistent with the rate-limit feature's "no
// default policies" rule: an unset block (rate and burst both zero) applies
// no limit. Setting either turns it on, and then both must be positive.
type ClientRateLimitConfig struct {
	// RatePerSecond is the sustained number of requests one client address
	// may make per second.
	RatePerSecond float64 `yaml:"rate_per_second"`

	// Burst is the number of requests one client address may make at once.
	Burst int `yaml:"burst"`

	// TrustedProxies lists the proxies whose connections may name the
	// client they forward for, each with the one header field it appends
	// to. A request from any other peer is keyed by that peer's own
	// address whatever its headers say. Empty, the default, means no header
	// is ever read.
	TrustedProxies []TrustedProxyConfig `yaml:"trusted_proxies"`
}

// TrustedProxyConfig is one transport.client_rate_limit.trusted_proxies
// entry.
type TrustedProxyConfig struct {
	// Address is the proxy's CIDR prefix or single address.
	Address string `yaml:"address"`

	// Header names the one field the proxy appends the address it received
	// a request from to: "forwarded" (RFC 7239) or "x-forwarded-for". A
	// hop the proxy reports is read from that field only; the other
	// reaches the server as the client wrote it and is never read on the
	// proxy's behalf.
	Header string `yaml:"header"`
}

// TrustedProxy is a parsed TrustedProxyConfig: the proxy's prefix and the
// canonical name of the header field it appends to, "Forwarded" or
// "X-Forwarded-For" -- the names pkg/clientlimit reads.
type TrustedProxy struct {
	Prefix netip.Prefix
	Header string
}

// The canonical names of the header fields a trusted proxy can append to.
const (
	headerForwarded     = "Forwarded"
	headerXForwardedFor = "X-Forwarded-For"
)

// trustedProxyHeaders maps each accepted Header value onto the canonical
// field name.
var trustedProxyHeaders = map[string]string{
	"forwarded":       headerForwarded,
	"x-forwarded-for": headerXForwardedFor,
}

// Enabled reports whether the block configures a limit.
func (c ClientRateLimitConfig) Enabled() bool {
	return c.RatePerSecond != 0 || c.Burst != 0
}

// ClientLimitProxies parses TrustedProxies into the limiter's form. A
// single address is the prefix holding only it (/32 or /128); Header is
// matched case-insensitively.
func (c ClientRateLimitConfig) ClientLimitProxies() ([]TrustedProxy, error) {
	const field = "transport.client_rate_limit.trusted_proxies"
	out := make([]TrustedProxy, 0, len(c.TrustedProxies))
	for _, tp := range c.TrustedProxies {
		s := strings.TrimSpace(tp.Address)
		var prefix netip.Prefix
		if strings.Contains(s, "/") {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return nil, &ValidationError{Field: field, Reason: "invalid CIDR prefix " + strconv.Quote(s)}
			}
			prefix = p.Masked()
		} else {
			ip, err := netip.ParseAddr(s)
			if err != nil {
				return nil, &ValidationError{Field: field, Reason: "invalid address " + strconv.Quote(s)}
			}
			prefix = netip.PrefixFrom(ip.WithZone(""), ip.BitLen())
		}
		header, ok := trustedProxyHeaders[strings.ToLower(strings.TrimSpace(tp.Header))]
		if !ok {
			return nil, &ValidationError{Field: field, Reason: "proxy " + strconv.Quote(s) + " has header " + strconv.Quote(tp.Header) + "; want \"forwarded\" or \"x-forwarded-for\""}
		}
		out = append(out, TrustedProxy{Prefix: prefix, Header: header})
	}
	return out, nil
}

// validate refuses a block the limiter cannot enforce. An unset block is
// valid and disables the limit; trusted proxies without a limit are refused
// because they would silently do nothing.
func (c ClientRateLimitConfig) validate() error {
	if !c.Enabled() {
		if len(c.TrustedProxies) > 0 {
			return &ValidationError{Field: "transport.client_rate_limit.trusted_proxies", Reason: "set without rate_per_second and burst, so no limit would apply"}
		}
		return nil
	}
	if !(c.RatePerSecond > 0) || math.IsInf(c.RatePerSecond, 0) {
		return &ValidationError{Field: "transport.client_rate_limit.rate_per_second", Reason: "must be a positive, finite number when the limit is set"}
	}
	if c.Burst <= 0 {
		return &ValidationError{Field: "transport.client_rate_limit.burst", Reason: "must be positive when the limit is set"}
	}
	_, err := c.ClientLimitProxies()
	return err
}

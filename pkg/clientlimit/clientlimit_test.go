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

package clientlimit

import (
	"errors"
	"math"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// slow is a policy whose bucket does not refill within a test's lifetime.
func slow(burst int) ratelimit.Policy {
	return ratelimit.Policy{RatePerSecond: 1e-6, Burst: burst}
}

func newLimiter(t *testing.T, burst int, trusted ...TrustedProxy) *Limiter {
	t.Helper()
	l, err := New(Config{Policy: slow(burst), TrustedProxies: trusted})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// xffProxy is a trusted proxy network appending to X-Forwarded-For.
func xffProxy(prefix string) TrustedProxy {
	return TrustedProxy{Prefix: netip.MustParsePrefix(prefix), Header: HeaderXForwardedFor}
}

// fwdProxy is a trusted proxy network appending to Forwarded.
func fwdProxy(prefix string) TrustedProxy {
	return TrustedProxy{Prefix: netip.MustParsePrefix(prefix), Header: HeaderForwarded}
}

func tcp(s string) net.Addr {
	return net.TCPAddrFromAddrPort(netip.MustParseAddrPort(s))
}

func headers(kv ...string) HeaderFunc {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h.Values
}

func TestNew_RejectsUnenforceablePolicy(t *testing.T) {
	cases := map[string]ratelimit.Policy{
		"zero rate":     {RatePerSecond: 0, Burst: 1},
		"negative rate": {RatePerSecond: -1, Burst: 1},
		"NaN rate":      {RatePerSecond: math.NaN(), Burst: 1},
		"infinite rate": {RatePerSecond: math.Inf(1), Burst: 1},
		"zero burst":    {RatePerSecond: 1, Burst: 0},
		"negative":      {RatePerSecond: 1, Burst: -3},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New(Config{Policy: p})
			var ce *ConfigError
			if !errors.As(err, &ce) {
				t.Fatalf("New(%+v) = %v, want *ConfigError", p, err)
			}
			if !strings.HasPrefix(ce.Error(), "clientlimit: invalid ") {
				t.Fatalf("Error() = %q", ce.Error())
			}
		})
	}
}

func TestNew_RejectsInvalidTrustedProxy(t *testing.T) {
	for name, proxies := range map[string][]TrustedProxy{
		"invalid prefix":  {{Header: HeaderForwarded}},
		"no header":       {{Prefix: netip.MustParsePrefix("10.0.0.0/8")}},
		"unknown header":  {{Prefix: netip.MustParsePrefix("10.0.0.0/8"), Header: "X-Real-IP"}},
		"header case":     {{Prefix: netip.MustParsePrefix("10.0.0.0/8"), Header: "forwarded"}},
		"overlap differs": {fwdProxy("10.0.0.0/8"), xffProxy("10.1.0.0/16")},
		"mapped overlap":  {fwdProxy("10.0.0.0/8"), xffProxy("::ffff:10.1.0.0/112")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(Config{Policy: slow(1), TrustedProxies: proxies})
			var ce *ConfigError
			if !errors.As(err, &ce) || ce.Field != "trusted_proxies" {
				t.Fatalf("New = %v, want trusted_proxies *ConfigError", err)
			}
		})
	}
}

func TestNew_AcceptsOverlapNamingOneHeader(t *testing.T) {
	l := newLimiter(t, 1, xffProxy("10.0.0.0/8"), xffProxy("10.1.0.0/16"), fwdProxy("192.0.2.0/24"))
	if got := l.ClientAddress(tcp("10.1.0.1:1"), headers(HeaderXForwardedFor, "198.51.100.7")); got != "198.51.100.7" {
		t.Fatalf("key = %q, want the forwarded client", got)
	}
}

func TestAdmit_RefusesPastBurstAndLeavesOtherAddressesAlone(t *testing.T) {
	l := newLimiter(t, 2)
	a, b := tcp("192.0.2.1:4000"), tcp("192.0.2.2:4000")
	for i := 0; i < 2; i++ {
		if err := l.Admit(a, nil); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	err := l.Admit(a, nil)
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("third request = %v, want *RateLimitedError", err)
	}
	if rl.Client != "192.0.2.1" || rl.RetryAfter <= 0 {
		t.Fatalf("refusal = %+v", rl)
	}
	if !strings.Contains(rl.Error(), "rate limit exceeded for client 192.0.2.1") {
		t.Fatalf("Error() = %q", rl.Error())
	}
	if err := l.Admit(b, nil); err != nil {
		t.Fatalf("other address refused: %v", err)
	}
}

func TestAdmit_KeysByHostNotPort(t *testing.T) {
	l := newLimiter(t, 1)
	if err := l.Admit(tcp("192.0.2.1:1"), nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Admit(tcp("192.0.2.1:2"), nil); err == nil {
		t.Fatal("a second source port opened a fresh bucket")
	}
}

func TestAdmit_IPv6KeyedByPrefix64(t *testing.T) {
	l := newLimiter(t, 1)
	if err := l.Admit(tcp("[2001:db8:1:2::1]:1"), nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Admit(tcp("[2001:db8:1:2:ffff::9]:1"), nil); err == nil {
		t.Fatal("another address in the same /64 opened a fresh bucket")
	}
	if err := l.Admit(tcp("[2001:db8:1:3::1]:1"), nil); err != nil {
		t.Fatalf("another /64 refused: %v", err)
	}
}

func TestAdmit_IPv4MappedSharesBucket(t *testing.T) {
	l := newLimiter(t, 1)
	if err := l.Admit(tcp("192.0.2.1:1"), nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Admit(tcp("[::ffff:192.0.2.1]:1"), nil); err == nil {
		t.Fatal("the IPv4-mapped form opened a fresh bucket")
	}
}

func TestAdmit_UDPAndUnixPeers(t *testing.T) {
	l := newLimiter(t, 1)
	udp := net.UDPAddrFromAddrPort(netip.MustParseAddrPort("198.51.100.1:443"))
	if got := l.ClientAddress(udp, nil); got != "198.51.100.1" {
		t.Fatalf("UDP key = %q", got)
	}
	unix := &net.UnixAddr{Name: "/run/qrdb.sock", Net: "unix"}
	if got := l.ClientAddress(unix, nil); got != "unix:/run/qrdb.sock" {
		t.Fatalf("unix key = %q", got)
	}
	if got := l.ClientAddress(nil, nil); got != "unknown" {
		t.Fatalf("nil key = %q", got)
	}
	if err := l.Admit(unix, nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Admit(unix, nil); err == nil {
		t.Fatal("unix peer not limited")
	}
}

func TestAdmitRemoteAddr(t *testing.T) {
	l := newLimiter(t, 1)
	if err := l.AdmitRemoteAddr("203.0.113.5:80", nil); err != nil {
		t.Fatal(err)
	}
	if err := l.AdmitRemoteAddr("203.0.113.5:81", nil); err == nil {
		t.Fatal("same host, second port admitted")
	}
	if got := l.ClientAddress(remoteAddrOf("pipe"), nil); got != "remote:pipe" {
		t.Fatalf("unparseable remote key = %q", got)
	}
	if got := l.ClientAddress(remoteAddrOf("203.0.113.9"), nil); got != "203.0.113.9" {
		t.Fatalf("bare address key = %q", got)
	}
}

// TestClientAddress_UntrustedPeerHeadersIgnored is the #1197 verification:
// a forwarded-for header from a peer that is not a trusted proxy does not
// change the key, whichever field carries it.
func TestClientAddress_UntrustedPeerHeadersIgnored(t *testing.T) {
	for name, trusted := range map[string][]TrustedProxy{
		"no proxies configured":      nil,
		"peer outside the proxy set": {xffProxy("10.0.0.0/8"), fwdProxy("172.16.0.0/12")},
	} {
		t.Run(name, func(t *testing.T) {
			l := newLimiter(t, 1, trusted...)
			peer := tcp("192.0.2.1:5000")
			for _, h := range []HeaderFunc{
				headers(HeaderXForwardedFor, "198.51.100.7"),
				headers(HeaderForwarded, "for=198.51.100.7"),
			} {
				if got := l.ClientAddress(peer, h); got != "192.0.2.1" {
					t.Fatalf("key = %q, want the peer's own address", got)
				}
			}
			if err := l.Admit(peer, headers(HeaderXForwardedFor, "198.51.100.7")); err != nil {
				t.Fatal(err)
			}
			if err := l.Admit(peer, headers(HeaderXForwardedFor, "198.51.100.8")); err == nil {
				t.Fatal("a rotating forwarded-for header bought a fresh bucket")
			}
		})
	}
}

func TestClientAddress_TrustedProxyChains(t *testing.T) {
	xff := newLimiter(t, 1, xffProxy("10.0.0.0/8"), xffProxy("2001:db8:ffff::/48"))
	fwd := newLimiter(t, 1, fwdProxy("10.0.0.0/8"), fwdProxy("2001:db8:ffff::/48"))
	proxy := tcp("10.0.0.1:443")
	cases := []struct {
		name string
		h    HeaderFunc
		want string
	}{
		{"no header", nil, "10.0.0.1"},
		{"empty header set", headers(), "10.0.0.1"},
		// The finding of PR #1253's third review: a proxy appending only
		// X-Forwarded-For passes a client-written Forwarded through.
		{"xff proxy ignores client forwarded", headers(HeaderForwarded, "for=203.0.113.1", HeaderXForwardedFor, "198.51.100.7"), "198.51.100.7"},
		{"xff proxy with only forwarded", headers(HeaderForwarded, "for=203.0.113.1"), "10.0.0.1"},
		{"xff proxy ignores malformed forwarded", headers(HeaderForwarded, `for="203.0.113.1`, HeaderXForwardedFor, "198.51.100.7"), "198.51.100.7"},
		{"forwarded proxy with only xff", headers(HeaderXForwardedFor, "203.0.113.1"), "10.0.0.1"},
		{"forwarded proxy without for in chain ignores xff", headers(HeaderForwarded, "proto=https", HeaderXForwardedFor, "203.0.113.1"), "10.0.0.1"},
		{"xff single", headers(HeaderXForwardedFor, "198.51.100.7"), "198.51.100.7"},
		{"xff spoofed left hop ignored", headers(HeaderXForwardedFor, "1.1.1.1, 198.51.100.7"), "198.51.100.7"},
		{"xff skips trusted hops", headers(HeaderXForwardedFor, "198.51.100.7, 10.1.1.1"), "198.51.100.7"},
		{"xff across field lines", headers(HeaderXForwardedFor, "198.51.100.7", HeaderXForwardedFor, "10.2.2.2"), "198.51.100.7"},
		{"xff all trusted is leftmost", headers(HeaderXForwardedFor, "10.3.3.3, 10.1.1.1"), "10.3.3.3"},
		{"xff garbage stops at reporter", headers(HeaderXForwardedFor, "198.51.100.7, bogus"), "10.0.0.1"},
		{"xff garbage behind trusted hop", headers(HeaderXForwardedFor, "bogus, 10.1.1.1"), "10.1.1.1"},
		{"xff bare ipv6", headers(HeaderXForwardedFor, "2001:db8:1:2::7"), "2001:db8:1:2::/64"},
		{"xff ipv4 with port", headers(HeaderXForwardedFor, "198.51.100.7:8443"), "198.51.100.7"},
		{"forwarded token", headers(HeaderForwarded, "for=198.51.100.7"), "198.51.100.7"},
		{"forwarded wins over xff", headers(HeaderForwarded, "for=198.51.100.7", HeaderXForwardedFor, "203.0.113.1"), "198.51.100.7"},
		{"forwarded case-insensitive name", headers(HeaderForwarded, "proto=https;For=198.51.100.7;by=10.0.0.1"), "198.51.100.7"},
		{"forwarded quoted ipv6 with port", headers(HeaderForwarded, `for="[2001:db8:1:2::7]:4711"`), "2001:db8:1:2::/64"},
		{"forwarded quoted ipv4 with port", headers(HeaderForwarded, `for="198.51.100.7:4711"`), "198.51.100.7"},
		{"forwarded chain", headers(HeaderForwarded, "for=1.1.1.1, for=198.51.100.7;proto=https, for=10.9.9.9"), "198.51.100.7"},
		{"forwarded quoted comma", headers(HeaderForwarded, `for=198.51.100.7;x="a,b"`), "198.51.100.7"},
		{"forwarded trusted ipv6 hop", headers(HeaderForwarded, `for=198.51.100.7, for="[2001:db8:ffff::1]"`), "198.51.100.7"},
		{"forwarded unknown stops", headers(HeaderForwarded, "for=198.51.100.7, for=unknown"), "10.0.0.1"},
		{"forwarded obfuscated stops", headers(HeaderForwarded, "for=_hidden"), "10.0.0.1"},
		{"forwarded obfuscated port", headers(HeaderForwarded, `for="198.51.100.7:_p1"`), "198.51.100.7"},
		{"forwarded no for stops", headers(HeaderForwarded, "proto=https"), "10.0.0.1"},
		{"forwarded empty element skipped", headers(HeaderForwarded, "for=198.51.100.7,,"), "198.51.100.7"},
		{"forwarded bad ipv6 port", headers(HeaderForwarded, `for="[2001:db8::1]:x"`), "10.0.0.1"},
		{"forwarded unbracketed close", headers(HeaderForwarded, `for="[2001:db8::1"`), "10.0.0.1"},
		{"forwarded bracketed ipv4", headers(HeaderForwarded, `for="[192.0.2.9]"`), "10.0.0.1"},
		{"forwarded port out of range", headers(HeaderForwarded, `for="198.51.100.7:70000"`), "10.0.0.1"},
		{"forwarded escaped quote", headers(HeaderForwarded, `for="198.51.100.\7"`), "198.51.100.7"},
		{"forwarded escaped dquote in other param", headers(HeaderForwarded, `for=198.51.100.7;x="a\"b, for=1.1.1.1"`), "198.51.100.7"},
		{"forwarded quoted ipv6 with port and params", headers(HeaderForwarded, `for="[2001:db8:1:2::7]:4711";proto=https, for=10.9.9.9`), "2001:db8:1:2::/64"},
		// A client behind an appending proxy opens a quoted-string the
		// proxy's ", for=<client>" would land inside.
		{"forwarded unterminated quote swallows appended hop", headers(HeaderForwarded, `for=192.0.2.10;proto=", for=198.51.100.7`), "10.0.0.1"},
		{"forwarded unterminated quote at end", headers(HeaderForwarded, `for=198.51.100.7, for="198.51.100.8`), "10.0.0.1"},
		{"forwarded dangling backslash", headers(HeaderForwarded, `for="198.51.100.7\`), "10.0.0.1"},
		{"forwarded malformed line among valid ones", headers(HeaderForwarded, `for=192.0.2.10;proto="`, HeaderForwarded, "for=198.51.100.7"), "10.0.0.1"},
		{"forwarded malformed does not fall back to xff", headers(HeaderForwarded, `for="198.51.100.7`, HeaderXForwardedFor, "203.0.113.1"), "10.0.0.1"},
		{"forwarded unquoted port is not a token", headers(HeaderForwarded, "for=198.51.100.7:4711"), "10.0.0.1"},
		{"forwarded duplicate for", headers(HeaderForwarded, "for=198.51.100.7;for=1.1.1.1"), "10.0.0.1"},
		{"forwarded whitespace around semicolon", headers(HeaderForwarded, "for=198.51.100.7 ;proto=https"), "10.0.0.1"},
		{"forwarded control byte in quoted-string", headers(HeaderForwarded, "for=\"198.51.100.7\x01\""), "10.0.0.1"},
		{"forwarded empty field", headers(HeaderForwarded, ""), "10.0.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := xff
			if strings.HasPrefix(tc.name, "forwarded") {
				l = fwd
			}
			if got := l.ClientAddress(proxy, tc.h); got != tc.want {
				t.Fatalf("key = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClientAddress_MixedProxyChain walks a chain whose proxies append to
// different fields: a load balancer at 10.0.0.0/8 appending Forwarded,
// behind a CDN at 192.0.2.0/24 appending X-Forwarded-For. Each hop is read
// from the field of the proxy that reported it.
func TestClientAddress_MixedProxyChain(t *testing.T) {
	l := newLimiter(t, 1, fwdProxy("10.0.0.0/8"), xffProxy("192.0.2.0/24"))
	lb := tcp("10.0.0.1:443")
	cases := []struct {
		name string
		h    HeaderFunc
		want string
	}{
		{"cdn then client", headers(HeaderForwarded, "for=203.0.113.9, for=192.0.2.5", HeaderXForwardedFor, "203.0.113.1, 198.51.100.7"), "198.51.100.7"},
		{"cdn reported nothing", headers(HeaderForwarded, "for=203.0.113.9, for=192.0.2.5"), "192.0.2.5"},
		{"cdn reported garbage", headers(HeaderForwarded, "for=192.0.2.5", HeaderXForwardedFor, "bogus"), "192.0.2.5"},
		{"lb chain of two", headers(HeaderForwarded, "for=198.51.100.7, for=10.1.1.1"), "198.51.100.7"},
		{"lb then cdn then lb", headers(HeaderForwarded, "for=10.2.2.2, for=192.0.2.5", HeaderXForwardedFor, "10.3.3.3"), "10.2.2.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := l.ClientAddress(lb, tc.h); got != tc.want {
				t.Fatalf("key = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClientAddress_MalformedForwardedFallsBackToPeer is the regression for
// an unterminated quoted-string: a client behind a proxy that appends to
// Forwarded sends for=<chosen>;x=" and the open quote swallows the hop the
// proxy appends, so the client's own choice of address was taken. RFC 7239
// §4 builds the field from RFC 7230 quoted-strings, so such a field is
// malformed as a whole and the request is keyed by the trusted peer -- never
// by X-Forwarded-For, which the same client may have written.
func TestClientAddress_MalformedForwardedFallsBackToPeer(t *testing.T) {
	fwd := newLimiter(t, 1, fwdProxy("10.0.0.0/8"))
	xff := newLimiter(t, 1, xffProxy("10.0.0.0/8"))
	proxy := tcp("10.0.0.1:443")
	cases := []struct {
		name string
		l    *Limiter
		h    HeaderFunc
		want string
	}{
		{"open quote hides appended hop", fwd, headers(HeaderForwarded, `for=203.0.113.1;x=", for=198.51.100.7`), "10.0.0.1"},
		{"open quote across field lines", fwd, headers(HeaderForwarded, `for=203.0.113.1;x="`, HeaderForwarded, "for=198.51.100.7"), "10.0.0.1"},
		{"trailing backslash in quoted-pair", fwd, headers(HeaderForwarded, `for=198.51.100.7;x="a\`), "10.0.0.1"},
		{"quote in last element", fwd, headers(HeaderForwarded, `for=198.51.100.7, for=203.0.113.1;x="`), "10.0.0.1"},
		{"open quote in for value", fwd, headers(HeaderForwarded, `for="203.0.113.1`), "10.0.0.1"},
		{"malformed forwarded does not fall back to xff", fwd, headers(HeaderForwarded, `for=198.51.100.7;x="`, HeaderXForwardedFor, "203.0.113.9"), "10.0.0.1"},
		{"escaped quote inside closed string", fwd, headers(HeaderForwarded, `for=198.51.100.7;x="a\",b"`), "198.51.100.7"},
		{"xff has no quoting to hide a hop", xff, headers(HeaderXForwardedFor, `203.0.113.1";x="`, HeaderXForwardedFor, "198.51.100.7"), "198.51.100.7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.l.ClientAddress(proxy, tc.h); got != tc.want {
				t.Fatalf("key = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAppendForwarded_ReportsUnterminatedQuoting holds the quoting cases of
// the splitter the RFC 7239 grammar parser replaced: a separator inside a
// closed quoted-string does not split, and a line that ends inside one --
// unterminated, or with a quoted-pair left open -- is refused.
func TestAppendForwarded_ReportsUnterminatedQuoting(t *testing.T) {
	cases := []struct {
		in       string
		wantHops int
		wantOK   bool
	}{
		{`a=1,b=2`, 2, true},
		{`a="x,y",b=2`, 2, true},
		{`a="x\"y",b=2`, 2, true},
		{`a="x`, 0, false},
		{`a="x\`, 0, false},
		{`a="x\"`, 0, false},
	}
	for _, tc := range cases {
		hops, ok := appendForwarded(nil, tc.in)
		if ok != tc.wantOK || (ok && len(hops) != tc.wantHops) {
			t.Errorf("appendForwarded(%q) = %d hops, ok=%v; want %d, %v", tc.in, len(hops), ok, tc.wantHops, tc.wantOK)
		}
	}
}

// TestAdmit_ClientForwardedCannotBypassXFFProxy is the bypass of PR #1253's
// third review: behind a proxy appending only X-Forwarded-For, a client
// rotating its own Forwarded field must not buy a fresh bucket.
func TestAdmit_ClientForwardedCannotBypassXFFProxy(t *testing.T) {
	l := newLimiter(t, 1, xffProxy("10.0.0.0/8"))
	proxy := tcp("10.0.0.1:443")
	if err := l.Admit(proxy, headers(HeaderForwarded, "for=203.0.113.1", HeaderXForwardedFor, "198.51.100.7")); err != nil {
		t.Fatal(err)
	}
	err := l.Admit(proxy, headers(HeaderForwarded, "for=203.0.113.2", HeaderXForwardedFor, "198.51.100.7"))
	var rl *RateLimitedError
	if !errors.As(err, &rl) || rl.Client != "198.51.100.7" {
		t.Fatalf("Admit = %v, want the proxy-reported client refused", err)
	}
}

func TestAppendForwarded_Grammar(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []string
		ok   bool
	}{
		{"single token", "for=192.0.2.1", []string{"192.0.2.1"}, true},
		{"rfc 7239 example", "for=192.0.2.43, for=198.51.100.17;by=203.0.113.60;proto=http;host=example.com", []string{"192.0.2.43", "198.51.100.17"}, true},
		{"element without for", "proto=https", []string{""}, true},
		{"empty pairs and list elements", " , ;for=192.0.2.1; ,\t,", []string{"192.0.2.1"}, true},
		{"quoted-pair resolved", `for="\[2001:db8::1\]"`, []string{"[2001:db8::1]"}, true},
		{"obs-text in quoted-string", "x=\"\x80\";for=192.0.2.1", []string{"192.0.2.1"}, true},
		{"empty line", "", nil, false},
		{"only commas", " , ,", nil, false},
		{"missing equals", "for", nil, false},
		{"empty value", "for=", nil, false},
		{"whitespace after equals", "for= 192.0.2.1", nil, false},
		{"duplicate name case-insensitively", "For=192.0.2.1;FOR=192.0.2.2", nil, false},
		{"junk after element", "for=192.0.2.1 x", nil, false},
		{"unterminated quote", `for="192.0.2.1`, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := appendForwarded(nil, tc.line)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (hops %q)", ok, tc.ok, got)
			}
			if ok && !slices.Equal(got, tc.want) {
				t.Fatalf("hops = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestForwardedParser_QuotedString(t *testing.T) {
	cases := []struct {
		name, in, want string
		ok             bool
		rest           string
	}{
		{"empty", `""x`, "", true, "x"},
		{"plain", `"a b"`, "a b", true, ""},
		{"escaped dquote and backslash", `"a\"\\b";`, `a"\b`, true, ";"},
		{"escaped tab", "\"\\\t\"", "\t", true, ""},
		{"unterminated", `"abc`, "", false, ""},
		{"backslash at end", `"abc\`, "", false, ""},
		{"escaped control byte", "\"\\\x01\"", "", false, ""},
		{"bare DEL", "\"\x7f\"", "", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := forwardedParser{s: tc.in}
			got, ok := p.quotedString()
			if ok != tc.ok || got != tc.want {
				t.Fatalf("quotedString(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
			}
			if ok && p.s[p.i:] != tc.rest {
				t.Fatalf("rest = %q, want %q", p.s[p.i:], tc.rest)
			}
		})
	}
}

// manyParams returns one Forwarded element of n distinct extension
// parameters, x0=v;x1=v;..., followed by tail.
func manyParams(n int, tail string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteString("x")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("=v")
	}
	b.WriteString(tail)
	return b.String()
}

// TestForwardedParser_ManyDistinctParamsOneSetEntryEach is the third
// finding of PR #1253's interim review: the duplicate check holds the
// element's names in a set, one entry per name, so each parameter costs
// one lookup rather than a scan of every name before it. A duplicate is
// still refused however many distinct names precede it, and the set
// starts empty for the next element.
func TestForwardedParser_ManyDistinctParamsOneSetEntryEach(t *testing.T) {
	const n = 1 << 14
	p := forwardedParser{s: manyParams(n, ";for=192.0.2.1")}
	hop, ok := p.element()
	if !ok || hop != "192.0.2.1" {
		t.Fatalf("element() = %q, %v; want the for hop", hop, ok)
	}
	if len(p.seen) != n+1 {
		t.Fatalf("seen holds %d names, want %d", len(p.seen), n+1)
	}
	if _, ok := appendForwarded(nil, manyParams(n, ";X5=dup")); ok {
		t.Fatal("a duplicate after many distinct names was accepted")
	}
	hops, ok := appendForwarded(nil, manyParams(n, "")+", x0=v;for=192.0.2.2")
	if !ok || !slices.Equal(hops, []string{"", "192.0.2.2"}) {
		t.Fatalf("second element reused the first's names: hops %q ok %v", hops, ok)
	}
}

// BenchmarkAppendForwarded_DistinctParams measures one element of many
// distinct extension parameters; ns/op grows linearly in the count.
func BenchmarkAppendForwarded_DistinctParams(b *testing.B) {
	line := manyParams(1<<12, ";for=192.0.2.1")
	b.SetBytes(int64(len(line)))
	for b.Loop() {
		if _, ok := appendForwarded(nil, line); !ok {
			b.Fatal("parse failed")
		}
	}
}

func TestForwardedFor_Validity(t *testing.T) {
	if hops, valid := forwardedFor(nil); !valid || hops != nil {
		t.Fatalf("no field: hops %q valid %v, want an empty valid chain", hops, valid)
	}
	hops, valid := forwardedFor([]string{"for=192.0.2.1", "for=192.0.2.2"})
	if !valid || !slices.Equal(hops, []string{"192.0.2.1", "192.0.2.2"}) {
		t.Fatalf("two lines: hops %q valid %v", hops, valid)
	}
	hops, valid = forwardedFor([]string{"for=192.0.2.1", `for="`})
	if valid || hops != nil {
		t.Fatalf("malformed second line: hops %q valid %v", hops, valid)
	}
}

func TestXForwardedFor_SplitsEveryLine(t *testing.T) {
	if hops, valid := xForwardedFor(nil); !valid || hops != nil {
		t.Fatalf("no field: hops %q valid %v", hops, valid)
	}
	hops, valid := xForwardedFor([]string{"192.0.2.1, 192.0.2.2", " bogus "})
	if !valid || !slices.Equal(hops, []string{"192.0.2.1", "192.0.2.2", "bogus"}) {
		t.Fatalf("hops %q valid %v", hops, valid)
	}
}

func TestClientAddress_IPv4MappedProxyPrefix(t *testing.T) {
	l := newLimiter(t, 1, xffProxy("::ffff:10.0.0.0/104"))
	if got := l.ClientAddress(tcp("10.0.0.1:1"), headers(HeaderXForwardedFor, "198.51.100.7")); got != "198.51.100.7" {
		t.Fatalf("mapped proxy prefix did not match the unmapped peer: key %q", got)
	}
}

func TestNilLimiter_AdmitsEverything(t *testing.T) {
	var l *Limiter
	if err := l.Admit(tcp("192.0.2.1:1"), nil); err != nil {
		t.Fatal(err)
	}
	if err := l.AdmitRemoteAddr("192.0.2.1:1", nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	h := http.NotFoundHandler()
	if got := l.HTTP(h); got == nil {
		t.Fatal("nil Limiter HTTP returned nil")
	}
}

type countingEngine struct {
	calls  atomic.Int64
	closed atomic.Bool
	keys   sync.Map
}

func (e *countingEngine) Allow(key string, _ ratelimit.Policy) (bool, time.Duration) {
	e.calls.Add(1)
	e.keys.Store(key, true)
	return true, 0
}

func (e *countingEngine) Close() error { e.closed.Store(true); return nil }

func TestWithEngine_SharedEngineStaysOpenAndKeysAreNamespaced(t *testing.T) {
	engine := &countingEngine{}
	l, err := New(Config{Policy: slow(1)}, WithEngine(engine), WithEngine(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Admit(tcp("192.0.2.1:1"), nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.keys.Load(keyPrefix + "192.0.2.1"); !ok {
		t.Fatal("key not namespaced")
	}
	if err := l.Close(); err != nil || engine.closed.Load() {
		t.Fatalf("Close closed a caller-owned engine (err %v)", err)
	}
}

func TestAdmit_ConcurrentCallersSpendExactlyTheBurst(t *testing.T) {
	const burst, workers, perWorker = 50, 16, 20
	l := newLimiter(t, burst)
	peer := tcp("192.0.2.1:1")
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if l.Admit(peer, nil) == nil {
					admitted.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if got := admitted.Load(); got != burst {
		t.Fatalf("admitted %d, want exactly the burst %d", got, burst)
	}
}

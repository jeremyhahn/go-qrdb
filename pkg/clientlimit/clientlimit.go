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

// Package clientlimit is the pre-authentication request-rate limiter keyed
// by client address (go-qrdb#1197).
//
// The per-permission limiter (pkg/auth/middleware, go-qrdb#201) throttles an
// authenticated principal, so it cannot see a flood that never gets that
// far: handshake, login and enrollment attempts reach a transport before any
// principal exists. Limiter closes that gap once, for every transport: each
// one asks it to Admit a request before anything else runs on the request's
// behalf -- authentication, the public-route allowlist, the sealed-window and
// awaiting-admin gates -- and every transport shares the one Limiter a
// server builds, so an address's budget is the same whichever protocol
// carries its requests.
//
// The key is the client's address, never a header the client chose: the
// immediate peer's address, unless that peer is one of the configured
// trusted proxies, in which case the forwarding chain is walked from the
// right to the first address that is not itself a trusted proxy. Each
// trusted proxy is configured with the one field it appends to -- RFC 7239
// Forwarded or X-Forwarded-For -- and the hop it reports is read from that
// field alone: the other passes through the proxy as the client wrote it, so
// it is never consulted, not even as a fallback. With no trusted proxies
// configured -- the default -- no header is ever read.
//
// Limiter is the canonical home of this control; pkg/api/connlimit bounds
// concurrent connections per peer, a different resource. The package sits
// outside pkg/api because pkg/api/errclass, which the SDK's lean root links,
// classifies its RateLimitedError: under pkg/api it would pull server code
// into every SDK caller (test/buildgates/lean_root_test.go).
package clientlimit

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// keyPrefix namespaces this limiter's buckets inside a ratelimit.Limiter an
// embedder may share with other subsystems, so a client-address key can
// never collide with a per-permission one.
const keyPrefix = "client-addr\x00"

// ipv6KeyBits is the prefix length an IPv6 client is keyed by. A /64 is the
// subnet one end site's link is assigned (RFC 4291 §2.5.1, RFC 6177), so a
// host that owns one can mint 2^64 source addresses at will; keying the
// full /128 would hand it as many fresh buckets.
const ipv6KeyBits = 64

// Header fields a trusted proxy may append to, and so the only ones ever
// consulted -- and only for a hop a trusted proxy configured with that field
// reported.
const (
	// HeaderForwarded is the RFC 7239 field.
	HeaderForwarded = "Forwarded"
	// HeaderXForwardedFor is the de facto field most proxies append.
	HeaderXForwardedFor = "X-Forwarded-For"
)

// chainParsers maps each field a trusted proxy may append to onto the parser
// that splits its values into hops, left to right. A parser reports false
// when the values cannot be delimited exactly.
var chainParsers = map[string]func(values []string) ([]string, bool){
	HeaderForwarded:     forwardedFor,
	HeaderXForwardedFor: xForwardedFor,
}

// HeaderFunc returns every value of the named request header field, in the
// order received. http.Header.Values satisfies it; a gRPC adapter wraps
// metadata.MD.Get. A nil HeaderFunc means the transport carries no header
// fields at all (native QUIC, a Unix socket).
type HeaderFunc func(name string) []string

// Config configures a Limiter.
type Config struct {
	// Policy is the token bucket applied per client address: the sustained
	// requests per second and the burst one address may make.
	Policy ratelimit.Policy

	// TrustedProxies lists the proxies whose connections may name the
	// client they forward for. A request from any other peer is keyed by
	// that peer's own address, whatever its headers say. Empty -- the
	// default -- trusts no peer.
	TrustedProxies []TrustedProxy
}

// TrustedProxy is a network of proxies trusted to name the client they
// forward for, in the one header field they append to.
type TrustedProxy struct {
	// Prefix is the network the proxies connect from.
	Prefix netip.Prefix

	// Header is the field the proxies append the address they received a
	// request from to: HeaderForwarded or HeaderXForwardedFor. A hop one
	// of them reports is read from this field only. The other field is
	// one the proxy passes through unmodified, wholly client-written, so
	// it is never read on the proxy's behalf -- not even when this one is
	// absent or malformed.
	Header string
}

// Limiter admits or refuses a request by its client address. A nil *Limiter
// admits every request, so a transport can call it unconditionally.
type Limiter struct {
	engine  ratelimit.Limiter
	owned   bool
	policy  ratelimit.Policy
	trusted []TrustedProxy
}

// Option configures New.
type Option func(*Limiter)

// WithEngine runs the limiter against an existing ratelimit.Limiter rather
// than a TokenBucketLimiter of its own. The caller keeps ownership: Close
// does not close it.
func WithEngine(engine ratelimit.Limiter) Option {
	return func(l *Limiter) {
		if engine != nil {
			l.engine, l.owned = engine, false
		}
	}
}

// New returns a Limiter enforcing cfg. The policy's rate and burst must both
// be positive and finite, and the trusted proxies as
// NormalizeTrustedProxies accepts them.
func New(cfg Config, opts ...Option) (*Limiter, error) {
	if err := validatePolicy(cfg.Policy); err != nil {
		return nil, err
	}
	trusted, err := NormalizeTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		return nil, err
	}
	l := &Limiter{policy: cfg.Policy, trusted: trusted}
	for _, opt := range opts {
		opt(l)
	}
	if l.engine == nil {
		l.engine, l.owned = ratelimit.NewTokenBucketLimiter(), true
	}
	return l, nil
}

// NormalizeTrustedProxies validates proxies and returns them in the form the
// Limiter matches peers against. Every prefix must be valid and name a
// field chainParsers knows, and no two overlapping prefixes may name
// different fields: a peer in both would have no one field to read.
func NormalizeTrustedProxies(proxies []TrustedProxy) ([]TrustedProxy, error) {
	trusted := make([]TrustedProxy, 0, len(proxies))
	for _, p := range proxies {
		if !p.Prefix.IsValid() {
			return nil, &ConfigError{Field: "trusted_proxies", Reason: "invalid prefix " + strconv.Quote(p.Prefix.String())}
		}
		if _, ok := chainParsers[p.Header]; !ok {
			return nil, &ConfigError{Field: "trusted_proxies", Reason: "proxy " + p.Prefix.String() + " names header " + strconv.Quote(p.Header) + "; want " + HeaderForwarded + " or " + HeaderXForwardedFor}
		}
		// Masked and unmapped, so an IPv4 proxy listed as 192.0.2.1/32
		// matches the same peer whether the socket reports it as
		// 192.0.2.1 or ::ffff:192.0.2.1.
		p.Prefix = unmapPrefix(p.Prefix.Masked())
		for _, q := range trusted {
			if q.Header != p.Header && q.Prefix.Overlaps(p.Prefix) {
				return nil, &ConfigError{Field: "trusted_proxies", Reason: "proxies " + q.Prefix.String() + " and " + p.Prefix.String() + " overlap but name different headers"}
			}
		}
		trusted = append(trusted, p)
	}
	return trusted, nil
}

// Close releases the engine New constructed. An engine supplied through
// WithEngine stays open. Close on a nil Limiter is a no-op.
func (l *Limiter) Close() error {
	if l == nil || !l.owned {
		return nil
	}
	return l.engine.Close()
}

// Admit charges one request from peer, whose header fields header returns,
// against the client address's bucket. It returns a *RateLimitedError when
// the bucket is empty, and nil otherwise -- always nil on a nil Limiter.
func (l *Limiter) Admit(peer net.Addr, header HeaderFunc) error {
	if l == nil {
		return nil
	}
	client := l.ClientAddress(peer, header)
	if allowed, retryAfter := l.engine.Allow(keyPrefix+client, l.policy); !allowed {
		return &RateLimitedError{Client: client, RetryAfter: retryAfter}
	}
	return nil
}

// AdmitRemoteAddr is Admit for a transport that knows its peer only as a
// "host:port" string -- net/http's Request.RemoteAddr.
func (l *Limiter) AdmitRemoteAddr(remoteAddr string, header HeaderFunc) error {
	if l == nil {
		return nil
	}
	return l.Admit(remoteAddrOf(remoteAddr), header)
}

// ClientAddress returns the client address a request from peer is keyed by:
// an IPv4 address, an IPv6 /64 prefix, or, for a peer with no IP address (a
// Unix socket), its network and address.
func (l *Limiter) ClientAddress(peer net.Addr, header HeaderFunc) string {
	ip, ok := peerIP(peer)
	if !ok {
		if peer == nil {
			return "unknown"
		}
		return peer.Network() + ":" + peer.String()
	}
	if header != nil {
		if proxy, ok := l.trustedProxy(ip); ok {
			ip = l.forwardedClient(ip, proxy.Header, header)
		}
	}
	return keyOf(ip)
}

// forwardedClient walks the forwarding chain right to left from the trusted
// peer, whose hops are appended to field, and returns the first hop that is
// not itself a trusted proxy. Each hop is the rightmost one not yet consumed
// in the field the trusted proxy reporting it appends to, so a chain may mix
// proxies that append to different fields, and a field no proxy on the walk
// appends to is never read. A hop that does not parse as an IP address (an
// RFC 7239 "unknown" or obfuscated node, a malformed value), a field with no
// hop left, and a Forwarded field that does not parse as RFC 7239 §4 at all
// each end the walk at the trusted hop that was to report it: nothing to its
// left can be attributed to anyone trustworthy.
func (l *Limiter) forwardedClient(peer netip.Addr, field string, header HeaderFunc) netip.Addr {
	chains := make(map[string][]string, len(chainParsers))
	client := peer
	for {
		chain, parsed := chains[field]
		if !parsed {
			var valid bool
			if chain, valid = chainParsers[field](header(field)); !valid {
				return client
			}
		}
		if len(chain) == 0 {
			return client
		}
		hop, ok := parseNode(chain[len(chain)-1])
		chains[field] = chain[:len(chain)-1]
		if !ok {
			return client
		}
		proxy, trusted := l.trustedProxy(hop)
		if !trusted {
			return hop
		}
		client, field = hop, proxy.Header
	}
}

// trustedProxy returns the trusted proxy entry holding ip. New refuses
// overlapping entries that name different fields, so whichever entry
// matches names the same one.
func (l *Limiter) trustedProxy(ip netip.Addr) (TrustedProxy, bool) {
	for _, p := range l.trusted {
		if p.Prefix.Contains(ip) {
			return p, true
		}
	}
	return TrustedProxy{}, false
}

// validatePolicy refuses a policy the token bucket cannot honor: a rate or
// burst that is zero, negative, NaN or infinite would either refuse every
// request or none.
func validatePolicy(p ratelimit.Policy) error {
	if !(p.RatePerSecond > 0) || p.RatePerSecond > maxRate {
		return &ConfigError{Field: "rate_per_second", Reason: "must be a positive, finite number"}
	}
	if p.Burst <= 0 {
		return &ConfigError{Field: "burst", Reason: "must be positive"}
	}
	return nil
}

// maxRate is the largest finite float64; anything above it is +Inf.
const maxRate = 1.7976931348623157e308

func peerIP(peer net.Addr) (netip.Addr, bool) {
	switch a := peer.(type) {
	case *net.TCPAddr:
		if ip, ok := netip.AddrFromSlice(a.IP); ok {
			return ip.Unmap().WithZone(""), true
		}
	case *net.UDPAddr:
		if ip, ok := netip.AddrFromSlice(a.IP); ok {
			return ip.Unmap().WithZone(""), true
		}
	case nil:
		return netip.Addr{}, false
	}
	if peer == nil {
		return netip.Addr{}, false
	}
	return parseHostPort(peer.String())
}

// parseHostPort parses "host:port" or a bare address.
func parseHostPort(s string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	if ip, err := netip.ParseAddr(s); err == nil {
		return ip.Unmap().WithZone(""), true
	}
	return netip.Addr{}, false
}

func keyOf(ip netip.Addr) string {
	if ip.Is4() {
		return ip.String()
	}
	p, err := ip.Prefix(ipv6KeyBits)
	if err != nil {
		return ip.String()
	}
	return p.String()
}

func unmapPrefix(p netip.Prefix) netip.Prefix {
	if !p.Addr().Is4In6() {
		return p
	}
	bits := p.Bits() - 96
	if bits < 0 {
		bits = 0
	}
	return netip.PrefixFrom(p.Addr().Unmap(), bits).Masked()
}

// remoteAddr is a net.Addr for a peer known only by its string form.
type remoteAddr string

func (a remoteAddr) Network() string { return "remote" }
func (a remoteAddr) String() string  { return string(a) }

func remoteAddrOf(s string) net.Addr {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return net.TCPAddrFromAddrPort(ap)
	}
	return remoteAddr(s)
}

// xForwardedFor splits X-Forwarded-For field values into hops, left to
// right, across every field line in order. X-Forwarded-For has no grammar
// beyond the comma list, so the split always succeeds; a hop that is not an
// address is refused by parseNode.
func xForwardedFor(values []string) ([]string, bool) {
	var hops []string
	for _, v := range values {
		for _, hop := range strings.Split(v, ",") {
			hops = append(hops, strings.TrimSpace(hop))
		}
	}
	return hops, true
}

// forwardedFor extracts the "for" parameter of every forwarded-element in
// RFC 7239 Forwarded field values, left to right, parsing each field line on
// its own; no field at all is an empty chain. valid is false, and hops nil,
// when any field line does not match the grammar appendForwarded
// implements: a value the parser cannot delimit exactly cannot say which
// bytes a trusted proxy appended, so none of it is believed. An element
// with no "for" parameter contributes an empty hop, which parseNode refuses,
// so a proxy that does not disclose its client ends the walk rather than
// being skipped.
func forwardedFor(values []string) (hops []string, valid bool) {
	for _, v := range values {
		var ok bool
		if hops, ok = appendForwarded(hops, v); !ok {
			return nil, false
		}
	}
	return hops, true
}

// appendForwarded parses one Forwarded field line,
//
//	Forwarded         = 1#forwarded-element
//	forwarded-element = [ forwarded-pair ] *( ";" [ forwarded-pair ] )
//	forwarded-pair    = token "=" value
//	value             = token / quoted-string
//
// (RFC 7239 §4, with the RFC 7230 §7 list rule: empty list elements and
// optional whitespace around the commas, none around ";" or "="), and
// appends each element's "for" value to hops. It reports false on any byte
// outside that grammar -- a quoted-string with no closing quote among them,
// which would otherwise swallow whatever a proxy appended after it -- on a
// parameter that occurs twice in one element, which §4 forbids, and on a
// line with no element at all.
func appendForwarded(hops []string, line string) ([]string, bool) {
	p := forwardedParser{s: line}
	elements := 0
	for {
		p.skipOWS()
		if p.done() {
			return hops, elements > 0
		}
		if p.peek() == ',' {
			p.i++
			continue
		}
		hop, ok := p.element()
		if !ok {
			return hops, false
		}
		hops = append(hops, hop)
		elements++
		p.skipOWS()
		if p.done() {
			return hops, true
		}
		if p.peek() != ',' {
			return hops, false
		}
		p.i++
	}
}

// forwardedParser is a cursor over one Forwarded field line.
type forwardedParser struct {
	s string
	i int
	// seen holds the parameter names of the element being parsed, so the
	// duplicate check (RFC 7239 section 4) costs one lookup per parameter
	// and the parse stays linear in the header however many distinct
	// extension parameters a client sends. It is allocated on the first
	// parameter and cleared, not reallocated, for each later element.
	seen map[string]struct{}
}

func (p *forwardedParser) done() bool { return p.i >= len(p.s) }

func (p *forwardedParser) peek() byte { return p.s[p.i] }

// skipOWS consumes RFC 7230 optional whitespace: SP and HTAB.
func (p *forwardedParser) skipOWS() {
	for !p.done() && (p.peek() == ' ' || p.peek() == '\t') {
		p.i++
	}
}

// element parses one forwarded-element and returns its "for" value, "" when
// it has none. Parameter names are case-insensitive (RFC 7239 §4).
func (p *forwardedParser) element() (string, bool) {
	var hop string
	if p.seen == nil {
		p.seen = make(map[string]struct{})
	} else {
		clear(p.seen)
	}
	for {
		if !p.done() && byteClass[p.peek()]&classTchar != 0 {
			name, value, ok := p.pair()
			if !ok {
				return "", false
			}
			name = strings.ToLower(name)
			if _, dup := p.seen[name]; dup {
				return "", false
			}
			p.seen[name] = struct{}{}
			if name == "for" {
				hop = value
			}
		}
		if p.done() || p.peek() != ';' {
			return hop, true
		}
		p.i++
	}
}

// pair parses a forwarded-pair whose first byte is a tchar, returning its
// name and its value with any quoting removed.
func (p *forwardedParser) pair() (name, value string, ok bool) {
	name = p.token()
	if p.done() || p.peek() != '=' {
		return "", "", false
	}
	p.i++
	if !p.done() && p.peek() == '"' {
		value, ok = p.quotedString()
		return name, value, ok
	}
	value = p.token()
	return name, value, value != ""
}

// token consumes 1*tchar (RFC 7230 §3.2.6) and returns it, "" when the
// cursor is not on a tchar.
func (p *forwardedParser) token() string {
	start := p.i
	for !p.done() && byteClass[p.peek()]&classTchar != 0 {
		p.i++
	}
	return p.s[start:p.i]
}

// quotedString consumes an RFC 7230 §3.2.6 quoted-string,
//
//	quoted-string = DQUOTE *( qdtext / quoted-pair ) DQUOTE
//	qdtext        = HTAB / SP / %x21 / %x23-5B / %x5D-7E / obs-text
//	quoted-pair   = "\" ( HTAB / SP / VCHAR / obs-text )
//
// with the cursor on its opening DQUOTE, and returns its content with every
// quoted-pair resolved to the octet it escapes. It reports false on a byte
// neither production admits and when the line ends before the closing
// DQUOTE.
func (p *forwardedParser) quotedString() (string, bool) {
	p.i++
	var b strings.Builder
	for !p.done() {
		c := p.peek()
		p.i++
		switch {
		case c == '"':
			return b.String(), true
		case c == '\\':
			if p.done() || byteClass[p.peek()]&classQuotedPair == 0 {
				return "", false
			}
			b.WriteByte(p.peek())
			p.i++
		case byteClass[c]&classQdtext != 0:
			b.WriteByte(c)
		default:
			return "", false
		}
	}
	return "", false
}

// Octet classes of the RFC 7230 §3.2.6 productions the Forwarded grammar
// uses.
const (
	classTchar = 1 << iota
	classQdtext
	classQuotedPair
)

// byteClass maps every octet to the classes it belongs to.
var byteClass = func() (t [256]uint8) {
	for c := 0; c < 256; c++ {
		visible := c >= 0x21 && c <= 0x7e
		obsText := c >= 0x80
		blank := c == ' ' || c == '\t'
		if blank || obsText || (visible && c != '"' && c != '\\') {
			t[c] |= classQdtext
		}
		if blank || visible || obsText {
			t[c] |= classQuotedPair
		}
	}
	for _, c := range []byte("!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		t[c] |= classTchar
	}
	return t
}()

// parseNode parses an RFC 7239 node (§6: IPv4address [":" port], or "["
// IPv6address "]" [":" port]) or an X-Forwarded-For hop, which some proxies
// write as a bare IPv6 address without brackets. "unknown", an obfuscated
// identifier and anything malformed are refused.
func parseNode(node string) (netip.Addr, bool) {
	if strings.HasPrefix(node, "[") {
		end := strings.IndexByte(node, ']')
		if end < 0 {
			return netip.Addr{}, false
		}
		if rest := node[end+1:]; rest != "" && !validPort(rest) {
			return netip.Addr{}, false
		}
		ip, err := netip.ParseAddr(node[1:end])
		if err != nil || !ip.Is6() {
			return netip.Addr{}, false
		}
		return ip.Unmap().WithZone(""), true
	}
	if ip, err := netip.ParseAddr(node); err == nil {
		return ip.Unmap().WithZone(""), true
	}
	host, port, found := strings.Cut(node, ":")
	if !found || !validPort(":"+port) {
		return netip.Addr{}, false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.Is4() {
		return netip.Addr{}, false
	}
	return ip, true
}

// validPort reports whether s is ":" followed by an RFC 7239 node-port: a
// decimal port or an obfuscated port ("_" followed by ALPHA, DIGIT, ".",
// "_" or "-").
func validPort(s string) bool {
	if len(s) < 2 || s[0] != ':' {
		return false
	}
	p := s[1:]
	if p[0] == '_' {
		for i := 1; i < len(p); i++ {
			if !isObfChar(p[i]) {
				return false
			}
		}
		return len(p) > 1
	}
	n, err := strconv.ParseUint(p, 10, 16)
	return err == nil && n <= 65535 && len(p) <= 5
}

// isObfChar reports whether c may follow the leading "_" of an obfuscated
// node or port (RFC 7239 §6.3: ALPHA, DIGIT, ".", "_" or "-").
func isObfChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
}

// RateLimitedError refuses a request whose client address has exhausted its
// bucket. It is returned before the request is authenticated, so it names
// the address, never a principal.
type RateLimitedError struct {
	// Client is the address the request was keyed by.
	Client string
	// RetryAfter is how long until the bucket holds a token again.
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return "clientlimit: rate limit exceeded for client " + e.Client +
		"; retry after " + e.RetryAfter.Round(time.Millisecond).String()
}

// ConfigError refuses a Config New cannot enforce.
type ConfigError struct {
	// Field names the offending setting.
	Field string
	// Reason says what is wrong with it.
	Reason string
}

func (e *ConfigError) Error() string {
	return "clientlimit: invalid " + e.Field + ": " + e.Reason
}

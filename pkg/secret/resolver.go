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

package secret

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// ResolvedStrategy identifies how a SecretValue was produced during resolution.
type ResolvedStrategy string

const (
	// StrategyLiteral indicates the value was used verbatim without transformation.
	// This is the fallback when no registered resolver claims the raw reference.
	StrategyLiteral ResolvedStrategy = "literal"

	// StrategyCredential indicates the value was fetched from the qrdb
	// credential store.
	StrategyCredential ResolvedStrategy = "credential"

	// StrategyLocalKMS indicates the value was decrypted by a local KMS.
	StrategyLocalKMS ResolvedStrategy = "local_kms"

	// StrategyRemoteKMS indicates the value was decrypted by a remote KMS.
	StrategyRemoteKMS ResolvedStrategy = "remote_kms"

	// StrategyAutoGen indicates the value was auto-generated.
	StrategyAutoGen ResolvedStrategy = "autogen"
)

// literalPrefix is the qrdb-owned scheme that forces a value to be treated as a
// verbatim literal. It is stripped during resolution so an operator can quote a
// value that would otherwise be mistaken for a secret reference.
const literalPrefix = "literal://"

// schemeSeparator divides the scheme from the name in a secret reference.
const schemeSeparator = "://"

// redactedMarker is what Redacted yields when the raw reference cannot be shown
// because it is, or may be, the secret itself rather than a pointer to one.
const redactedMarker = "[redacted]"

// literalValue strips an optional "literal://" scheme prefix, yielding the
// verbatim value a literal reference resolves to.
func literalValue(raw string) string {
	return strings.TrimPrefix(raw, literalPrefix)
}

// SecretValue holds a resolved secret value together with resolution metadata.
// The plaintext is sealed in a go-enclave sealed enclave (secure.Secret), not
// in a garbage-collected slice. At rest that is AES-256-GCM ciphertext plus a
// wrapped data key on the ordinary (unlocked) heap, by design — there is no
// plaintext there to protect with a page lock; only the moment an accessor
// materializes it is it mlocked, guard-paged memory, wiped and unmapped the
// instant the call returns. Callers should call Clear once they are done so
// the enclave is destroyed promptly.
//
// SecretValue is single-owner for its working life; concurrent Clear with
// any accessor is undefined.
//
// A SecretValue is also the processor's protected resolution target: a config
// field declared as SecretValue (or *SecretValue) rather than string decodes
// the reference into Raw and is filled in place by ConfigProcessor.Process, so
// the plaintext reaches the consumer without ever resting in a Go string.
type SecretValue struct {
	// Raw is the original unresolved reference (e.g. "credential://db-password").
	Raw string

	// Strategy identifies how this value was produced.
	Strategy ResolvedStrategy

	// ExpiresAt is the optional expiry time for the resolved value.
	// A zero value means the secret does not expire.
	ExpiresAt time.Time

	// secret seals the plaintext inside a go-enclave sealed enclave. It is nil
	// for an empty value and after Clear.
	secret *secure.Secret
}

// NewSecretValue seals value into a go-enclave sealed enclave and returns a
// SecretValue. The value is copied into the enclave, so the caller retains
// ownership of its own slice; callers holding sensitive source bytes should
// wipe them (e.g. via secure.Wipe) once NewSecretValue returns. An empty
// value yields a SecretValue whose accessors report empty.
//
// Returns (nil, *SealFailedError) if sealing fails — a resolved secret that
// cannot be protected must fail the resolution rather than silently produce
// a SecretValue whose accessors report empty, which a caller cannot tell
// apart from a reference that legitimately resolves to nothing (go-qrdb#114
// review).
func NewSecretValue(ctx context.Context, raw string, value []byte, strategy ResolvedStrategy) (*SecretValue, error) {
	sv := &SecretValue{Raw: raw, Strategy: strategy}
	if len(value) == 0 {
		return sv, nil
	}
	// secure.NewSecret wipes the slice it seals, so hand it a private copy and
	// leave the caller's slice intact. NewSecret only errors on empty input,
	// which is guarded above, or on a sealing failure, handled below.
	cp := make([]byte, len(value))
	copy(cp, value)
	s, err := secure.NewSecret(ctx, cp)
	if err != nil {
		return nil, &SealFailedError{Ref: redactRaw(raw, strategy), Err: err}
	}
	sv.secret = s
	return sv, nil
}

// NewSecretValueFromBuffer seals buf into a new SecretValue, consuming buf: it
// is destroyed on every return path, so the caller must not use or Destroy it
// afterwards. Unlike NewSecretValue the plaintext is sealed straight out of
// buf's protected pages and never exists as a plain []byte.
//
// A nil or destroyed buf, or a sealing failure, returns *SealFailedError.
func NewSecretValueFromBuffer(ctx context.Context, raw string, buf *secure.Buffer, strategy ResolvedStrategy) (*SecretValue, error) {
	s, err := secure.NewSecretFromBuffer(ctx, buf)
	if err != nil {
		return nil, &SealFailedError{Ref: redactRaw(raw, strategy), Err: err}
	}
	return &SecretValue{Raw: raw, Strategy: strategy, secret: s}, nil
}

// WithBytes opens the enclave into a transient mlocked buffer, invokes fn with
// the plaintext, then destroys the buffer before returning. The slice handed to
// fn is valid only for the duration of the call and must not be retained. fn
// receives a nil slice for an empty value. This is the preferred,
// enclave-respecting accessor: the plaintext never enters the Go heap.
//
// # The borrow is held for fn's entire call
//
// The transient Buffer's own WithBytes, not Bytes, reads the plaintext: Bytes
// aliases the enclave's locked pages and returns with NOTHING holding them —
// exactly its own documented warning — so a concurrent enclave.WipeAll (or any
// other Destroy racing this call) can unmap the pages while fn is still
// reading them, faulting the process (go-qrdb#114 review, round 3). Buffer's
// own WithBytes holds go-enclave's per-object read lock for the whole call,
// which blocks a racing Destroy until fn returns instead.
//
// This is safe for a caller-supplied fn precisely because the buffer here is
// TRANSIENT and never escapes to fn: fn receives only the []byte view, never
// the *secure.Buffer itself, so it has no handle to reenter THIS buffer's own
// lock and cannot deadlock against it. fn allocating or reading an unrelated
// Buffer or Secret from inside the callback is unaffected — go-enclave gives
// every enclave its own independent lock, which is what removed the
// process-wide contention the earlier, memguard-era version of this method
// was written to avoid (go-qrdb#74, #145): that concern does not apply here.
func (v *SecretValue) WithBytes(ctx context.Context, fn func([]byte) error) error {
	if v.secret == nil {
		return fn(nil)
	}
	buf, err := v.secret.Open(ctx)
	if err != nil {
		return err
	}
	defer buf.Destroy()
	return buf.WithBytes(fn)
}

// Open decrypts the secret into a new mlocked Buffer that the CALLER must
// Destroy. Returns (nil, nil) for an empty value. Prefer WithBytes unless the
// buffer must outlive a single callback.
func (v *SecretValue) Open(ctx context.Context) (*secure.Buffer, error) {
	if v.secret == nil {
		return nil, nil
	}
	return v.secret.Open(ctx)
}

// readString returns a resolved value's plaintext as a Go string, with the
// failure kept rather than discarded. It returns ("", nil) only for
// a genuinely empty value, and an error — never "" — when the sealed value
// exists but cannot be opened or read: a wiped process Sealer, a refused lock,
// or a buffer destroyed out from under the copy.
//
// The conversion runs inside a COORDINATED borrow (go-qrdb#74), via
// secure.CopyOut, so a concurrent Destroy of buf cannot unmap the pages
// mid-conversion. CopyOut holds the borrow for the whole copy and refuses an
// empty source, which is what lets this tell "unmapped mid-read" from "an
// empty secret". The intermediate slice is wiped; the string it produces can
// never be, which is why every caller of this method is itself careful: the
// config walker (walker.go's readResolved) writes the result into a plain
// config field and destroys the SecretValue either way, rather than handing
// the string back out to a caller that might retain it indefinitely.
func (v *SecretValue) readString(ctx context.Context) (string, error) {
	if v.secret == nil {
		return "", nil
	}
	buf, err := v.secret.Open(ctx)
	if err != nil {
		return "", err
	}
	defer buf.Destroy()
	out, err := secure.CopyOut(buf)
	if err != nil {
		return "", err
	}
	defer secure.Wipe(out)
	return string(out), nil
}

// Redacted returns a printable description of the secret that never contains
// plaintext. It yields Raw only when a resolver has established that Raw POINTS
// AT a secret held elsewhere, e.g. "credential://db-key"; otherwise it yields
// "[redacted]". A type that holds a SecretValue should route its String and
// logging paths through Redacted.
//
// Provenance decides this, not syntax. Looking like a URI proves nothing: the
// registry falls back to StrategyLiteral for any value no resolver claims, so
// "https://user:hunter2@example.test" and "unknown://hunter2" are references in
// shape only and are the secret in fact. A value is therefore shown only when
// its Strategy says a resolver produced it from a reference — an unresolved
// value, whose Strategy is not yet set, is redacted too, because nothing has
// vouched for where its Raw came from.
func (v *SecretValue) Redacted() string {
	if v == nil {
		return redactedMarker
	}
	return redactRaw(v.Raw, v.Strategy)
}

// redactRaw applies the same provenance-based rule as SecretValue.Redacted to
// a raw reference string and the strategy that would produce (or attempted to
// produce) a value from it. It exists as its own function, rather than being
// inlined into Redacted, because NewSecretValue's sealing-failure path needs
// the identical rule to compute SealFailedError.Ref: NewSecretValue can fail
// AFTER resolving a literal value, at which point raw IS the plaintext secret
// rather than a pointer to one, and SealFailedError must never retain that
// plaintext under any field, printed or not.
func redactRaw(raw string, strategy ResolvedStrategy) string {
	// StrategyLiteral means raw carries the secret rather than pointing at it,
	// whether it arrived through the "literal://" scheme or the registry's
	// fallback for an unclaimed value.
	if strategy == "" || strategy == StrategyLiteral {
		return redactedMarker
	}
	if strings.HasPrefix(raw, literalPrefix) {
		return redactedMarker
	}
	if i := strings.Index(raw, schemeSeparator); i > 0 {
		return raw
	}
	return redactedMarker
}

// Compile-time proof that a SecretValue renders redacted down every default
// display path — fmt verbs and structured logging alike — whether a config
// holds it by value or by pointer.
var (
	_ fmt.Formatter  = SecretValue{}
	_ slog.LogValuer = SecretValue{}
	_ fmt.Formatter  = (*SecretValue)(nil)
	_ slog.LogValuer = (*SecretValue)(nil)
)

// Format implements fmt.Formatter, which fmt consults ahead of any Stringer
// for every verb. Formatting a SecretValue — or any struct that holds one —
// with %v, %s, %+v or fmt.Print therefore yields the redacted description, so
// a stray log line over a config struct cannot spill a resolved secret. There
// is no SecretValue.String to race against: the plaintext is reached only
// through WithBytes/Open, both of which require the caller to hold the
// buffer deliberately.
func (v SecretValue) Format(f fmt.State, verb rune) {
	// A write error on a fmt.State is the caller's output problem, not this
	// value's, and fmt itself discards it — there is nothing useful to do with
	// it here beyond not pretending otherwise.
	if verb == 'q' {
		_, _ = fmt.Fprintf(f, "%q", v.Redacted())
		return
	}
	_, _ = io.WriteString(f, v.Redacted())
}

// LogValue implements slog.LogValuer so a SecretValue passed to a structured
// logger records the redacted description rather than the plaintext.
func (v SecretValue) LogValue() slog.Value {
	return slog.StringValue(v.Redacted())
}

// adopt moves src's enclave and resolution metadata into v, destroying whatever
// enclave v held. v keeps its own Raw: Raw records where the secret came FROM
// and must never be overwritten with the plaintext it resolved to. src is left
// empty — its enclave now belongs to v — and must not be used afterwards.
func (v *SecretValue) adopt(src *SecretValue) {
	if v == nil || src == nil {
		return
	}
	v.Clear()
	v.secret = src.secret
	v.Strategy = src.Strategy
	v.ExpiresAt = src.ExpiresAt
	src.secret = nil
}

// Clear destroys the backing enclave and releases its mlocked memory. After
// Clear the accessors report empty. It is idempotent and safe on a nil
// receiver.
func (v *SecretValue) Clear() {
	if v == nil || v.secret == nil {
		return
	}
	v.secret.Destroy()
	v.secret = nil
}

// Expired reports whether the resolved value has passed its expiry time.
// Returns false when ExpiresAt is the zero value (no expiry set).
func (v *SecretValue) Expired() bool {
	if v.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(v.ExpiresAt)
}

// SecretResolver resolves raw secret reference strings to their plaintext
// SecretValue. Implementations handle a specific URI scheme
// (e.g. "credential://", "kms://").
type SecretResolver interface {
	// Resolve decodes raw into its plaintext SecretValue. Implementations
	// must return a non-nil SecretValue on success.
	Resolve(ctx context.Context, raw string) (*SecretValue, error)

	// CanResolve reports whether this resolver is capable of handling the
	// given raw reference. Typically implemented as a URI scheme prefix
	// check. Must be cheap and side-effect free.
	CanResolve(raw string) bool

	// Scheme returns the URI scheme this resolver handles
	// (e.g. "credential", "kms"). Used for error attribution.
	Scheme() string
}

// ResolverRegistry dispatches secret resolution to the first registered
// SecretResolver whose CanResolve returns true for the raw value.
// Resolvers are checked in registration order. It is safe for concurrent use.
type ResolverRegistry struct {
	mu        sync.RWMutex
	resolvers []SecretResolver
}

// NewResolverRegistry creates an empty ResolverRegistry.
func NewResolverRegistry() *ResolverRegistry {
	return &ResolverRegistry{
		resolvers: make([]SecretResolver, 0),
	}
}

// Register appends resolver to the ordered resolver list.
// A nil resolver is silently ignored.
// Resolvers are consulted in registration order during Resolve.
func (r *ResolverRegistry) Register(resolver SecretResolver) {
	if resolver == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolvers = append(r.resolvers, resolver)
}

// Resolve returns the plaintext SecretValue for raw. The first registered
// resolver whose CanResolve returns true is used. If a matching resolver
// returns an error — or breaks the SecretResolver contract by reporting
// success with a nil value — Resolve wraps it in ResolutionFailedError and
// returns it without consulting subsequent resolvers. When no resolver matches, raw is
// returned verbatim as a literal SecretValue with StrategyLiteral.
//
// Returns ErrNilContext when ctx is nil.
func (r *ResolverRegistry) Resolve(ctx context.Context, raw string) (*SecretValue, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	r.mu.RLock()
	snapshot := make([]SecretResolver, len(r.resolvers))
	copy(snapshot, r.resolvers)
	r.mu.RUnlock()

	for _, res := range snapshot {
		if res.CanResolve(raw) {
			sv, err := res.Resolve(ctx, raw)
			if err != nil {
				return nil, &ResolutionFailedError{
					Scheme: res.Scheme(),
					Raw:    raw,
					Err:    err,
				}
			}
			if sv == nil {
				// SecretResolver requires a non-nil value on success. A
				// resolver that breaks that contract must fail here rather
				// than hand the caller a nil that reads as a resolved secret
				// everywhere downstream.
				return nil, &ResolutionFailedError{
					Scheme: res.Scheme(),
					Raw:    raw,
					Err:    errNilResolvedValue,
				}
			}
			return sv, nil
		}
	}
	// Literal fallback: no resolver claimed this value. A leading "literal://"
	// scheme (owned by qrdb) is stripped so an operator can force-quote a value
	// that would otherwise look like a secret reference; a bare value is used
	// verbatim. Either way the result is tagged StrategyLiteral.
	return NewSecretValue(ctx, raw, []byte(literalValue(raw)), StrategyLiteral)
}

// IsSecretURI reports whether raw is a resolvable reference: either a
// registered resolver claims it, or it carries the built-in "literal://"
// scheme. Returns false for a bare value with no recognized scheme.
func (r *ResolverRegistry) IsSecretURI(raw string) bool {
	if strings.HasPrefix(raw, literalPrefix) {
		return true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, res := range r.resolvers {
		if res.CanResolve(raw) {
			return true
		}
	}
	return false
}

// Resolvers returns a snapshot copy of all registered resolvers in
// registration order.
func (r *ResolverRegistry) Resolvers() []SecretResolver {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SecretResolver, len(r.resolvers))
	copy(out, r.resolvers)
	return out
}

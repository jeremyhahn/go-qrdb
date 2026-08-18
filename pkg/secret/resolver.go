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
// The plaintext is sealed in a memguard enclave (secure.Secret), not in a
// garbage-collected slice: it lives in mlocked, guard-paged memory and is
// materialized only transiently when an accessor is called. Callers should
// call Clear once they are done so the enclave is destroyed promptly.
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

	// secret seals the plaintext inside a memguard enclave. It is nil for an
	// empty value and after Clear.
	secret *secure.Secret
}

// NewSecretValue seals value into a memguard enclave and returns a SecretValue.
// The value is copied into the enclave, so the caller retains ownership of its
// own slice; callers holding sensitive source bytes should wipe them (e.g. via
// secure.Wipe) once NewSecretValue returns. An empty value yields a SecretValue
// whose accessors report empty.
func NewSecretValue(raw string, value []byte, strategy ResolvedStrategy) *SecretValue {
	sv := &SecretValue{Raw: raw, Strategy: strategy}
	if len(value) == 0 {
		return sv
	}
	// secure.NewSecret wipes the slice it seals, so hand it a private copy and
	// leave the caller's slice intact. NewSecret only errors on empty input,
	// which is guarded above.
	cp := make([]byte, len(value))
	copy(cp, value)
	if s, err := secure.NewSecret(cp); err == nil {
		sv.secret = s
	}
	return sv
}

// WithBytes opens the enclave into a transient mlocked buffer, invokes fn with
// the plaintext, then destroys the buffer before returning. The slice handed to
// fn is valid only for the duration of the call and must not be retained. fn
// receives a nil slice for an empty value. This is the preferred,
// enclave-respecting accessor: the plaintext never enters the Go heap.
func (v *SecretValue) WithBytes(fn func([]byte) error) error {
	if v.secret == nil {
		return fn(nil)
	}
	buf, err := v.secret.Open()
	if err != nil {
		return err
	}
	defer buf.Destroy()
	return fn(buf.Bytes())
}

// Open decrypts the secret into a new mlocked Buffer that the CALLER must
// Destroy. Returns (nil, nil) for an empty value. Prefer WithBytes unless the
// buffer must outlive a single callback.
func (v *SecretValue) Open() (*secure.Buffer, error) {
	if v.secret == nil {
		return nil, nil
	}
	return v.secret.Open()
}

// Bytes materializes the plaintext into a NEW garbage-collected slice and
// returns it. This copies the secret OUT of mlocked memory into the Go heap,
// where it cannot be reliably wiped, so prefer WithBytes or Open for sensitive
// handling. Returns nil for an empty value; each call decrypts afresh.
//
// Deprecated: Bytes hands the plaintext to the garbage collector, which is the
// plain path this package is moving away from. Use WithBytes, or Open when the
// buffer must outlive a single callback — both keep the plaintext in mlocked
// memory. Bytes remains for callers bound to an API that takes a raw []byte,
// and is retained until the stack's coordinated breaking release.
func (v *SecretValue) Bytes() []byte {
	if v.secret == nil {
		return nil
	}
	buf, err := v.secret.Open()
	if err != nil {
		return nil
	}
	defer buf.Destroy()
	out := make([]byte, buf.Size())
	copy(out, buf.Bytes())
	return out
}

// String materializes the plaintext into a Go string. Like Bytes, this copies
// the secret into the garbage-collected heap as an immutable string that cannot
// be wiped; prefer WithBytes. Returns "" for an empty value.
//
// A direct String call is the only way to reach the plaintext this way.
// Formatting a SecretValue — or a struct holding one — with %v, %s or
// fmt.Sprint is redacted, because Format satisfies fmt.Formatter, which fmt
// consults ahead of this Stringer. Changing what String itself returns is a
// behaviour break reserved for the stack's coordinated breaking release.
//
// Deprecated: String copies the plaintext into an immutable heap string that
// can never be wiped. Use WithBytes, or Redacted when the value is only being
// displayed. String remains for callers bound to an API that takes a raw
// string, and is retained until the stack's coordinated breaking release.
func (v *SecretValue) String() string {
	if v.secret == nil {
		return ""
	}
	buf, err := v.secret.Open()
	if err != nil {
		return ""
	}
	defer buf.Destroy()
	return string(buf.Bytes())
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
	// StrategyLiteral means Raw carries the secret rather than pointing at it,
	// whether it arrived through the "literal://" scheme or the registry's
	// fallback for an unclaimed value.
	if v.Strategy == "" || v.Strategy == StrategyLiteral {
		return redactedMarker
	}
	if strings.HasPrefix(v.Raw, literalPrefix) {
		return redactedMarker
	}
	if i := strings.Index(v.Raw, schemeSeparator); i > 0 {
		return v.Raw
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

// Format implements fmt.Formatter, which fmt consults ahead of Stringer for
// every verb. Formatting a SecretValue — or any struct that holds one — with
// %v, %s, %+v or fmt.Print therefore yields the redacted description, so a
// stray log line over a config struct cannot spill a resolved secret.
//
// String is deliberately left returning the plaintext: it is the documented way
// to hand a secret to an API that takes a string, and silently redacting it
// would break those callers at run time with no compile error to warn them.
// That change belongs to the stack's coordinated breaking release; until then
// the accidental paths are closed here and the deliberate one stays deprecated.
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
	return NewSecretValue(raw, []byte(literalValue(raw)), StrategyLiteral), nil
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

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
	"reflect"
	"strings"
)

// ProcessResult summarises the outcome of a ConfigProcessor walk.
type ProcessResult struct {
	// Resolved is the count of fields successfully resolved in place, counting
	// both plain string fields and protected SecretValue targets.
	Resolved int

	// Unresolved contains the dot-notation field paths (e.g.
	// "Database.Password") that still hold a known-scheme secret URI after
	// the walk because resolution failed or was skipped.
	Unresolved []string

	// ProtectedUnresolved contains the dot-notation paths of protected
	// SecretValue targets the walk reached but did not resolve, for any reason:
	// the target held nothing resolvable, the walk could not write to it, or
	// the resolver failed. It is always a subset of Unresolved.
	//
	// Process returns ErrUnresolvedSecretURI whenever it is non-empty, with or
	// without WithFailClosed. A field that asked for protection and did not get
	// it is an error by construction, not a diagnostic to be opted into.
	ProtectedUnresolved []string

	// PlaintextSkipped contains the dot-notation paths of plain string fields
	// that held a resolvable reference but were deliberately left alone
	// because WithNoPlaintextStrings is active. It is always a subset of
	// Unresolved — the reference really is still sitting in the field — and is
	// reported separately so a consumer can inventory exactly which fields
	// still need converting to a protected SecretValue target.
	PlaintextSkipped []string

	// Errors records every error encountered during the walk. Processing
	// continues after individual errors so the caller sees the full picture
	// before deciding how to react.
	Errors []error
}

// ProcessorOption is a functional option for ConfigProcessor.
type ProcessorOption func(*ConfigProcessor)

// WithFailClosed configures the processor to return ErrUnresolvedSecretURI
// as the error from Process when any field in Unresolved is non-empty or
// any resolution error was recorded. The *ProcessResult is still returned
// alongside the error so callers can inspect diagnostics.
func WithFailClosed() ProcessorOption {
	return func(p *ConfigProcessor) {
		p.failClosed = true
	}
}

// WithNoPlaintextStrings configures the processor to refuse to write resolved
// plaintext into a plain string field. Such a field keeps its reference, is
// recorded in both ProcessResult.PlaintextSkipped and ProcessResult.Unresolved,
// and its secret is never fetched from the backend at all.
//
// This is the migration ratchet for a consumer moving off plain string fields:
// turn the option on, read PlaintextSkipped to get the exact list of fields
// that still leak, and convert them to a SecretValue target one at a time until
// the list is empty. Combine it with WithFailClosed once it is, and the
// processor then fails any boot that would have produced a plaintext string.
func WithNoPlaintextStrings() ProcessorOption {
	return func(p *ConfigProcessor) {
		p.noPlaintextStrings = true
	}
}

// ConfigProcessor walks arbitrary configuration structs via reflection and
// resolves fields that carry a known-scheme secret URI. It recognises two
// targets:
//
//   - a SecretValue (or *SecretValue) field, the protected target: the
//     reference is taken from its Raw field and the resolved plaintext is
//     sealed into the value's own memguard enclave, so it never rests in a Go
//     string. Raw is left untouched — it records where the secret came from.
//     A protected target must reference a secret held elsewhere; one holding
//     nothing, an inline "literal://" secret, a bare value or an unclaimed
//     scheme is reported rather than skipped. An optional secret is spelled as
//     a *SecretValue whose pointer is nil.
//   - a settable exported string field, the plain target: the resolved
//     plaintext is written back over the reference, where it lives in the
//     garbage-collected heap for the lifetime of the process.
//
// The plain target is deprecated in favour of the protected one; see
// WithNoPlaintextStrings for the migration path. Every other field kind, and
// any non-settable field, is skipped.
type ConfigProcessor struct {
	registry           *ResolverRegistry
	failClosed         bool
	noPlaintextStrings bool
}

// NewConfigProcessor creates a ConfigProcessor backed by reg.
// Options are applied in order.
func NewConfigProcessor(reg *ResolverRegistry, opts ...ProcessorOption) *ConfigProcessor {
	p := &ConfigProcessor{registry: reg}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Process walks cfgPtr (must be a non-nil pointer to a struct), resolves every
// SecretValue target and every settable exported string field whose value
// IsSecretURI in place, and returns a ProcessResult.
//
// Returns ErrNilContext when ctx is nil.
// Returns ProcessorInputError when cfgPtr is nil, not a pointer, or
// points to a non-struct value.
// Returns ErrUnresolvedSecretURI, alongside the non-nil result, whenever a
// protected SecretValue target failed to resolve — unconditionally — or when
// WithFailClosed is active and Unresolved or Errors is non-empty.
func (p *ConfigProcessor) Process(ctx context.Context, cfgPtr any) (*ProcessResult, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	if cfgPtr == nil {
		return nil, &ProcessorInputError{Msg: "config must not be nil"}
	}
	rv := reflect.ValueOf(cfgPtr)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return nil, &ProcessorInputError{Msg: "config must be a non-nil pointer to a struct"}
	}
	elem := rv.Elem()
	if elem.Kind() != reflect.Struct {
		return nil, &ProcessorInputError{Msg: "config must be a pointer to a struct"}
	}

	result := &ProcessResult{
		Unresolved:          make([]string, 0),
		ProtectedUnresolved: make([]string, 0),
		PlaintextSkipped:    make([]string, 0),
		Errors:              make([]error, 0),
	}
	p.processValue(ctx, elem, "", result, 0)

	// A protected target that did not resolve fails the call whatever the
	// options say. Choosing the type IS the request to fail closed on it, and a
	// caller who reads only the error — the normal Go contract — must not be
	// told a config is ready when a secret it declared protected is missing,
	// stale or unverified.
	if len(result.ProtectedUnresolved) > 0 {
		return result, ErrUnresolvedSecretURI
	}
	if p.failClosed && (len(result.Unresolved) > 0 || len(result.Errors) > 0) {
		return result, ErrUnresolvedSecretURI
	}
	return result, nil
}

// maxWalkDepth bounds how deeply processValue recurses. Config graphs are
// shallow trees, so a generous limit catches reference cycles (which would
// otherwise recurse until the stack overflows) without rejecting any realistic
// configuration.
const maxWalkDepth = 64

// processValue dispatches to the type-appropriate handler. depth is the current
// recursion depth; when it exceeds maxWalkDepth the walk records an error and
// stops descending, guarding against cyclic config graphs.
func (p *ConfigProcessor) processValue(ctx context.Context, v reflect.Value, path string, result *ProcessResult, depth int) {
	if depth > maxWalkDepth {
		result.Errors = append(result.Errors, &MaxDepthExceededError{Path: path})
		return
	}
	// A SecretValue is a protected leaf, resolved into its own enclave and
	// never descended into. Descending would treat its exported Raw reference
	// as an ordinary string field and overwrite it with the plaintext, which
	// would both destroy the reference and defeat the whole point of the type.
	if v.Type() == secretValueType {
		p.processSecretValue(ctx, v, path, result)
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		p.processStruct(ctx, v, path, result, depth)
	case reflect.Pointer:
		// A nil pointer is skipped, not descended -- there is nothing here to
		// walk. That is also the boundary of what this package guarantees:
		// a required SecretValue nested inside a nil pointer (a whole config
		// section deleted, e.g. `Database *DatabaseConfig`) is never reached,
		// so its requiredness is never checked here. This package walks the
		// VALUE it is handed, not the TYPE against a schema, so it cannot
		// tell "this section is optional and was omitted" from "this section
		// is required and was deleted" -- that is a decision about the shape
		// of the config, not about one field's secret, and it belongs to the
		// consumer's own Validate(), the same place it already checks that
		// other required sections are present (docs/credentials/
		// secret-resolution.md, "The boundary: requiredness only for a leaf
		// the walk reaches").
		if !v.IsNil() {
			p.processValue(ctx, v.Elem(), path, result, depth+1)
		}
	case reflect.Slice:
		p.processSlice(ctx, v, path, result, depth)
	case reflect.Map:
		p.processMap(ctx, v, path, result, depth)
	case reflect.String:
		p.processString(ctx, v, path, result)
	case reflect.Interface:
		if !v.IsNil() {
			p.processInterface(ctx, v, path, result, depth)
		}
	}
}

// processInterface descends through a non-nil interface value. A SecretValue
// stored in an interface by value is not addressable, so it cannot be resolved
// where it sits: it is resolved through an addressable copy that is assigned
// back when the interface itself is settable, and recorded as unresolved when
// it is not. Every other dynamic type is walked exactly as before.
func (p *ConfigProcessor) processInterface(ctx context.Context, v reflect.Value, path string, result *ProcessResult, depth int) {
	elem := v.Elem()
	if elem.Type() != secretValueType {
		p.processValue(ctx, elem, path, result, depth+1)
		return
	}
	if !v.CanSet() {
		p.recordUnwritableTarget(elem, path, result)
		return
	}
	target := reflect.New(elem.Type()).Elem()
	target.Set(elem)
	p.processValue(ctx, target, path, result, depth+1)
	v.Set(target)
}

// resolvableTarget reports whether raw is something a protected target can
// actually resolve, recording an UnresolvableTargetError when it is not.
//
// A protected target must REFERENCE a secret held elsewhere. An inline
// "literal://" secret and a bare value both carry the secret in Raw itself —
// an ordinary Go string that outlives the walk and can never be wiped, which
// is precisely what the target exists to prevent — and an empty value or an
// unclaimed scheme means no secret arrives at all. None of those can be
// honoured, so none of them is passed over in silence.
func (p *ConfigProcessor) resolvableTarget(raw, path string, result *ProcessResult) bool {
	if raw != "" && !strings.HasPrefix(raw, literalPrefix) && p.registry.IsSecretURI(raw) {
		return true
	}
	p.recordProtectedFailure(path, &UnresolvableTargetError{Path: path}, result)
	return false
}

// recordUnwritableTarget reports a protected target the walk can reach but
// cannot write to, so a fail-closed boot sees it instead of silently accepting
// a config whose secret was never resolved.
func (p *ConfigProcessor) recordUnwritableTarget(v reflect.Value, path string, result *ProcessResult) {
	if !v.CanInterface() {
		return
	}
	if _, ok := v.Interface().(SecretValue); !ok {
		return
	}
	p.recordProtectedFailure(path, &UnwritableTargetError{Path: path}, result)
}

// processStruct iterates over exported struct fields and recurses into each.
func (p *ConfigProcessor) processStruct(ctx context.Context, v reflect.Value, path string, result *ProcessResult, depth int) {
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		ft := t.Field(i)
		if !ft.IsExported() {
			continue
		}
		fieldPath := ft.Name
		if path != "" {
			fieldPath = path + "." + ft.Name
		}
		p.processValue(ctx, field, fieldPath, result, depth+1)
	}
}

// processSlice iterates over slice elements.
func (p *ConfigProcessor) processSlice(ctx context.Context, v reflect.Value, path string, result *ProcessResult, depth int) {
	for i := 0; i < v.Len(); i++ {
		p.processValue(ctx, v.Index(i), fmt.Sprintf("%s[%d]", path, i), result, depth+1)
	}
}

// processMap iterates map key-value pairs. String map values are resolved and
// written back via SetMapIndex because map values are not addressable. Struct
// map values — including a SecretValue target — are processed via an
// addressable copy that is then stored back. Other value kinds are recursed
// into directly.
func (p *ConfigProcessor) processMap(ctx context.Context, v reflect.Value, path string, result *ProcessResult, depth int) {
	iter := v.MapRange()
	for iter.Next() {
		key := iter.Key()
		val := iter.Value()
		keyPath := fmt.Sprintf("%s[%v]", path, key.Interface())

		switch val.Kind() {
		case reflect.String:
			raw := val.String()
			if raw == "" || !p.registry.IsSecretURI(raw) {
				continue
			}
			if p.noPlaintextStrings {
				result.PlaintextSkipped = append(result.PlaintextSkipped, keyPath)
				result.Unresolved = append(result.Unresolved, keyPath)
				continue
			}
			sv, err := p.registry.Resolve(ctx, raw)
			if err != nil {
				result.Errors = append(result.Errors, err)
				result.Unresolved = append(result.Unresolved, keyPath)
				continue
			}
			// Build a value of the map's element type so a named string type
			// (e.g. map[string]Password) is assignable; a bare string is not
			// assignable to a defined string type and would panic SetMapIndex.
			nv := reflect.New(val.Type()).Elem()
			nv.SetString(sv.String())
			v.SetMapIndex(key, nv)
			sv.Clear()
			result.Resolved++
		case reflect.Struct:
			// Map struct values are not addressable; work on an addressable copy
			// and store it back once processing completes.
			newVal := reflect.New(val.Type())
			newVal.Elem().Set(val)
			p.processValue(ctx, newVal.Elem(), keyPath, result, depth+1)
			v.SetMapIndex(key, newVal.Elem())
		case reflect.Interface:
			// A SecretValue boxed in an interface map value is doubly
			// unaddressable, so it gets the same addressable-copy treatment and
			// is stored back under its key. Any other dynamic type is walked as
			// before.
			if val.IsNil() {
				continue
			}
			if dyn := val.Elem(); dyn.Type() == secretValueType {
				newVal := reflect.New(dyn.Type()).Elem()
				newVal.Set(dyn)
				p.processValue(ctx, newVal, keyPath, result, depth+1)
				v.SetMapIndex(key, newVal)
				continue
			}
			p.processValue(ctx, val, keyPath, result, depth+1)
		default:
			p.processValue(ctx, val, keyPath, result, depth+1)
		}
	}
}

// secretValueType is the reflect type of the protected resolution target. It
// is compared by identity rather than by assignability: a type that merely
// embeds a SecretValue is an ordinary struct to the walker, and its embedded
// field is reached — and protected — by the normal recursion.
var secretValueType = reflect.TypeOf(SecretValue{})

// processSecretValue resolves a protected target in place: the reference is
// read from the value's Raw field and the resolved plaintext is sealed into the
// value's own enclave, so it never passes through a Go string. Raw is left as
// it was — it records where the secret came from.
//
// Every non-nil target the walk reaches must end in one of two outcomes:
// resolved, or reported. A target it cannot address — and so cannot write the
// enclave into — is recorded rather than skipped, and so is one that holds
// nothing resolvable. Silence is reserved for the nil pointer, which is how an
// optional secret is spelled.
func (p *ConfigProcessor) processSecretValue(ctx context.Context, v reflect.Value, path string, result *ProcessResult) {
	if !v.CanAddr() || !v.CanInterface() {
		p.recordUnwritableTarget(v, path, result)
		return
	}
	target, ok := v.Addr().Interface().(*SecretValue)
	if !ok {
		return
	}
	// Destroy whatever the target already held before trying to fill it again.
	// A second Process over an already-resolved config — a refresh after a
	// rotation, a re-read after a reload — must not leave the previous secret
	// usable when the new one cannot be fetched: a revoked credential would go
	// on authenticating for as long as its backend stayed down.
	target.Clear()
	if !p.resolvableTarget(target.Raw, path, result) {
		return
	}
	resolved, err := p.registry.Resolve(ctx, target.Raw)
	if err != nil {
		p.recordProtectedFailure(path, err, result)
		return
	}
	target.adopt(resolved)
	result.Resolved++
}

// recordProtectedFailure reports a protected target that was reached and not
// resolved. Every such target lands in Unresolved and in ProtectedUnresolved,
// which is what makes Process return an error whether or not the caller asked
// for WithFailClosed.
func (p *ConfigProcessor) recordProtectedFailure(path string, err error, result *ProcessResult) {
	result.Errors = append(result.Errors, err)
	result.Unresolved = append(result.Unresolved, path)
	result.ProtectedUnresolved = append(result.ProtectedUnresolved, path)
}

// processString resolves a settable string field in place when it contains a
// known-scheme secret URI. Non-settable fields are silently skipped. Under
// WithNoPlaintextStrings the field is recorded and left untouched instead.
func (p *ConfigProcessor) processString(ctx context.Context, v reflect.Value, path string, result *ProcessResult) {
	if !v.CanSet() {
		return
	}
	raw := v.String()
	if raw == "" || !p.registry.IsSecretURI(raw) {
		return
	}
	if p.noPlaintextStrings {
		result.PlaintextSkipped = append(result.PlaintextSkipped, path)
		result.Unresolved = append(result.Unresolved, path)
		return
	}
	sv, err := p.registry.Resolve(ctx, raw)
	if err != nil {
		result.Errors = append(result.Errors, err)
		result.Unresolved = append(result.Unresolved, path)
		return
	}
	v.SetString(sv.String())
	sv.Clear()
	result.Resolved++
}

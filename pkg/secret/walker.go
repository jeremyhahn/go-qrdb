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
)

// ProcessResult summarises the outcome of a ConfigProcessor walk.
type ProcessResult struct {
	// Resolved is the count of string fields successfully resolved and
	// written back in place.
	Resolved int

	// Unresolved contains the dot-notation field paths (e.g.
	// "Database.Password") that still hold a known-scheme secret URI after
	// the walk because resolution failed or was skipped.
	Unresolved []string

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

// ConfigProcessor walks arbitrary configuration structs via reflection and
// resolves settable exported string fields that contain known-scheme secret
// URIs. Non-string fields and non-settable fields are skipped.
type ConfigProcessor struct {
	registry   *ResolverRegistry
	failClosed bool
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

// Process walks cfgPtr (must be a non-nil pointer to a struct), resolves
// every settable exported string field that IsSecretURI in place, and
// returns a ProcessResult.
//
// Returns ErrNilContext when ctx is nil.
// Returns ProcessorInputError when cfgPtr is nil, not a pointer, or
// points to a non-struct value.
// When WithFailClosed is active and Unresolved is non-empty or Errors is
// non-empty, returns ErrUnresolvedSecretURI alongside the non-nil result.
func (p *ConfigProcessor) Process(ctx context.Context, cfgPtr any) (*ProcessResult, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	if cfgPtr == nil {
		return nil, &ProcessorInputError{Msg: "config must not be nil"}
	}
	rv := reflect.ValueOf(cfgPtr)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return nil, &ProcessorInputError{Msg: "config must be a non-nil pointer to a struct"}
	}
	elem := rv.Elem()
	if elem.Kind() != reflect.Struct {
		return nil, &ProcessorInputError{Msg: "config must be a pointer to a struct"}
	}

	result := &ProcessResult{
		Unresolved: make([]string, 0),
		Errors:     make([]error, 0),
	}
	p.processValue(ctx, elem, "", result, 0)

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
	switch v.Kind() {
	case reflect.Struct:
		p.processStruct(ctx, v, path, result, depth)
	case reflect.Ptr:
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
			p.processValue(ctx, v.Elem(), path, result, depth+1)
		}
	}
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
// map values are processed via an addressable copy that is then stored back.
// Other value kinds are recursed into directly.
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
		default:
			p.processValue(ctx, val, keyPath, result, depth+1)
		}
	}
}

// processString resolves a settable string field in place when it contains a
// known-scheme secret URI. Non-settable fields are silently skipped.
func (p *ConfigProcessor) processString(ctx context.Context, v reflect.Value, path string, result *ProcessResult) {
	if !v.CanSet() {
		return
	}
	raw := v.String()
	if raw == "" || !p.registry.IsSecretURI(raw) {
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

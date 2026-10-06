// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"sort"
	"sync"
)

// Registry maps provider names to Providers, following the same pattern as
// seal.StrategyRegistry: qrdb's daemon owns a default registry pre-populated
// with the "dir" provider, and a higher layer (go-xkms) registers its own
// provider by name at composition time, through WithVolumeProvider.
type Registry interface {
	// Register installs p under p.Name(). Returns
	// *ProviderAlreadyRegisteredError on a duplicate name, so a provider can
	// never be replaced silently; a caller wanting replacement builds a new
	// Registry.
	Register(p Provider) error
	// Lookup returns the Provider registered under name. Returns
	// *ProviderNotRegisteredError when absent.
	Lookup(name string) (Provider, error)
	// ListAvailable returns every registered provider name in
	// lexicographic order. The returned slice is a fresh copy.
	ListAvailable() []string
}

// defaultRegistry is the concrete Registry. Safe for concurrent use.
type defaultRegistry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry returns a Registry pre-populated with the "dir" provider.
// Every other provider (e.g. go-xkms's "luks2") is added by calling
// Register at composition time.
func NewRegistry() Registry {
	r := &defaultRegistry{providers: make(map[string]Provider)}
	_ = r.Register(NewDirProvider())
	return r
}

// Register installs p under p.Name().
func (r *defaultRegistry) Register(p Provider) error {
	if p == nil {
		return &InvalidProviderError{Reason: "nil provider"}
	}
	name := p.Name()
	if name == "" {
		return &InvalidProviderError{Reason: "empty provider name"}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[name]; exists {
		return &ProviderAlreadyRegisteredError{Provider: name}
	}
	r.providers[name] = p
	return nil
}

// Lookup returns the Provider registered under name.
func (r *defaultRegistry) Lookup(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	if !ok {
		return nil, &ProviderNotRegisteredError{Provider: name, Available: r.listAvailableLocked()}
	}
	return p, nil
}

// ListAvailable returns every registered provider name, sorted.
func (r *defaultRegistry) ListAvailable() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.listAvailableLocked()
}

// listAvailableLocked must be called with r.mu held (for reading or writing).
func (r *defaultRegistry) listAvailableLocked() []string {
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

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

package keywrap

import (
	"sort"
	"sync"
)

// ProviderFactory constructs a Provider on demand, the same lazy-
// construction shape seal.StrategyFactory uses -- a TPM2 or PKCS#11
// provider may need to acquire a hardware handle only when first used, not
// at registration time.
type ProviderFactory func() (Provider, error)

// Registry is the canonical lookup table for KEK providers by ID. go-qrdb
// owns a default registry pre-populated with the one software provider it
// ships; go-xkms registers hardware and cloud-KMS providers at composition
// time by calling Register, the identical shape seal.StrategyRegistry
// already uses for sealing strategies.
type Registry interface {
	// Register installs factory under id. Returns
	// *ProviderAlreadyRegisteredError when id is already present; callers
	// wanting replacement must Unregister first.
	Register(id string, factory ProviderFactory) error

	// Unregister removes the provider registered under id. No-op when id
	// is absent.
	Unregister(id string)

	// LookupFactory returns the ProviderFactory registered under id.
	// Returns *ProviderNotRegisteredError when id is absent.
	LookupFactory(id string) (ProviderFactory, error)

	// ListAvailable returns every registered provider ID in
	// lexicographic order.
	ListAvailable() []string
}

// Lookup resolves and constructs a Provider from r in one call -- the
// shape every caller other than a registry inspector actually wants. It is
// a package function (not a Registry method) so a caller holding only the
// Registry interface -- not the concrete *DefaultRegistry -- can still
// resolve and construct without an extra type assertion.
func Lookup(r Registry, id string) (Provider, error) {
	factory, err := r.LookupFactory(id)
	if err != nil {
		return nil, err
	}
	return factory()
}

// DefaultRegistry is the concrete Registry. Safe for concurrent use;
// Register/Unregister take a short write lock, LookupFactory/ListAvailable
// a short read lock -- mirroring seal.DefaultStrategyRegistry.
type DefaultRegistry struct {
	mu        sync.RWMutex
	factories map[string]ProviderFactory
}

// NewRegistry returns an empty DefaultRegistry. Use NewDefaultRegistry to
// obtain a registry pre-populated with the "software" provider.
func NewRegistry() *DefaultRegistry {
	return &DefaultRegistry{factories: make(map[string]ProviderFactory)}
}

// NewDefaultRegistry returns a DefaultRegistry pre-populated with the one
// provider go-qrdb owns: "software" (SoftwareProvider). go-xkms and other
// embedders add hardware or cloud-KMS providers on top by calling
// Register.
func NewDefaultRegistry() *DefaultRegistry {
	r := NewRegistry()
	_ = r.Register(SoftwareProviderID, func() (Provider, error) {
		return NewSoftwareProvider(), nil
	})
	return r
}

// Register installs factory under id. Returns
// *ProviderAlreadyRegisteredError on duplicate registration.
func (r *DefaultRegistry) Register(id string, factory ProviderFactory) error {
	if factory == nil {
		return &ProviderRegisterError{Reason: "nil factory"}
	}
	if id == "" {
		return &ProviderRegisterError{Reason: "empty provider id"}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[id]; exists {
		return &ProviderAlreadyRegisteredError{ID: id}
	}
	r.factories[id] = factory
	return nil
}

// Unregister removes the provider registered under id. No-op when id is
// absent.
func (r *DefaultRegistry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.factories, id)
}

// LookupFactory returns the ProviderFactory registered under id.
func (r *DefaultRegistry) LookupFactory(id string) (ProviderFactory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.factories[id]
	if !ok {
		return nil, &ProviderNotRegisteredError{ID: id, Available: r.listAvailableLocked()}
	}
	return f, nil
}

// ListAvailable returns every registered provider ID in lexicographic
// order. Returned slice is a fresh copy; mutating it does not affect the
// registry.
func (r *DefaultRegistry) ListAvailable() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.listAvailableLocked()
}

// listAvailableLocked must be called with r.mu held.
func (r *DefaultRegistry) listAvailableLocked() []string {
	ids := make([]string, 0, len(r.factories))
	for id := range r.factories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Lookup resolves and constructs the Provider registered under id in one
// call.
func (r *DefaultRegistry) Lookup(id string) (Provider, error) {
	return Lookup(r, id)
}

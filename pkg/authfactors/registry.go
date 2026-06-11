// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package authfactors

import (
	"sort"
	"sync"
)

// FactorRegistry maps a factor name (e.g., "fido2", "piv", "pkcs11", "mtls")
// to a concrete Factor implementation. NewFactorRegistry returns an empty
// registry; there are no default entries. Consumers (go-quicbaas / xkmsd)
// register go-xkms hardware factor plugins (fido2, piv, pkcs11, mtls) and
// inject the populated registry via server.WithFactorRegistry. When no
// explicit server.WithAuthFactorService is supplied, the server and the SDK
// node builders construct the unified AuthFactorService from the registry
// automatically so every transport exposes the enrollment/challenge/verify
// surface. mTLS authentication uses this factor ("mtls") for ceremony/enrollment
// possession proofs; the login resolution path (auth.LoginResolver FactorVerifiers)
// handles mTLS session identity separately.
//
// FactorRegistry is safe for concurrent use; Register / Unregister take a
// short write lock and Lookup / List take a short read lock.
type FactorRegistry struct {
	mu      sync.RWMutex
	factors map[string]Factor
}

// NewFactorRegistry returns an empty FactorRegistry.
func NewFactorRegistry() *FactorRegistry {
	return &FactorRegistry{factors: make(map[string]Factor)}
}

// Register installs f under f.Name(). It returns FactorAlreadyRegisteredError
// when the name is already registered; callers that want replacement
// semantics must Unregister first.
func (r *FactorRegistry) Register(f Factor) error {
	if f == nil {
		return &FactorRegisterError{Reason: "nil factor"}
	}
	name := f.Name()
	if name == "" {
		return &FactorRegisterError{Reason: "factor returned empty Name()"}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factors[name]; exists {
		return &FactorAlreadyRegisteredError{Name: name}
	}
	r.factors[name] = f
	return nil
}

// Unregister removes the factor previously registered under name. It is a
// no-op when the name is not present.
func (r *FactorRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.factors, name)
}

// Lookup returns the Factor registered under name. It returns
// FactorNotFoundError when no factor is registered with that name.
func (r *FactorRegistry) Lookup(name string) (Factor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.factors[name]
	if !ok {
		return nil, &FactorNotFoundError{Name: name}
	}
	return f, nil
}

// List returns the names of every registered factor in lexicographic order.
// The returned slice is a fresh copy; mutating it does not affect the registry.
func (r *FactorRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.factors))
	for name := range r.factors {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// All returns a snapshot of every registered Factor in lexicographic-by-name
// order. The returned slice is a fresh copy.
func (r *FactorRegistry) All() []Factor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.factors))
	for name := range r.factors {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Factor, len(names))
	for i, n := range names {
		out[i] = r.factors[n]
	}
	return out
}

// Copyright (c) 2026 Jeremy Hahn
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

package ceremony

import (
	"sort"
	"sync"
)

// ThresholdFactory is a constructor function that returns a
// ThresholdInitializer for a specific vendor.
type ThresholdFactory func() ThresholdInitializer

// ThresholdRegistry manages the registration and lookup of threshold
// initializer implementations by vendor name.
type ThresholdRegistry struct {
	mu        sync.RWMutex
	factories map[string]ThresholdFactory
}

// NewThresholdRegistry creates a new empty ThresholdRegistry.
func NewThresholdRegistry() *ThresholdRegistry {
	return &ThresholdRegistry{
		factories: make(map[string]ThresholdFactory),
	}
}

// Register adds a ThresholdFactory for the given vendor name. If a
// factory is already registered for the vendor, it is replaced.
func (r *ThresholdRegistry) Register(vendor string, factory ThresholdFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[vendor] = factory
}

// Get returns a ThresholdInitializer for the given vendor. It returns
// ErrVendorNotRegistered if no factory is registered for the vendor,
// or ErrVendorNotAvailable if the factory produces an initializer
// that reports itself as not available.
func (r *ThresholdRegistry) Get(vendor string) (ThresholdInitializer, error) {
	r.mu.RLock()
	factory, ok := r.factories[vendor]
	r.mu.RUnlock()
	if !ok {
		return nil, ErrVendorNotRegistered
	}
	initializer := factory()
	if !initializer.Available() {
		return nil, ErrVendorNotAvailable
	}
	return initializer, nil
}

// List returns all registered vendor names in sorted order.
func (r *ThresholdRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	vendors := make([]string, 0, len(r.factories))
	for vendor := range r.factories {
		vendors = append(vendors, vendor)
	}
	sort.Strings(vendors)
	return vendors
}

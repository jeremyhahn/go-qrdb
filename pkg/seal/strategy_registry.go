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

package seal

import (
	"sort"
	"sync"
)

// StrategyFactory constructs a SealingStrategy on demand. Registered with a
// StrategyRegistry under a canonical name so qrdb's daemon can resolve the
// strategy chosen by `--barrier-strategy <name>` without coupling the daemon
// to specific implementations.
type StrategyFactory func() (SealingStrategy, error)

// StrategyRegistry is the canonical lookup table for sealing strategies by
// name. qrdb's daemon owns the default registry pre-populated with
// software-only strategies (Passphrase, Shamir, None); higher layers (xkms,
// quicbaas) register hardware-backed strategies (TPM2, PKCS#11, AWS KMS,
// GCP KMS, Azure KV, Vault) by calling Register at composition time.
//
// The registry is intentionally separate from BarrierRegistry: the latter
// manages per-tenant barrier lifecycle, the former owns strategy
// discovery. Keeping them apart preserves single-responsibility and
// allows StrategyRegistry to own the canonical "allowed strategies" list
// reported via ListAvailable.
type StrategyRegistry interface {
	// Register installs factory under name. Returns
	// StrategyAlreadyRegisteredError when name is already present;
	// callers wanting replacement must Unregister first.
	Register(name StrategyID, factory StrategyFactory) error

	// Unregister removes the strategy registered under name. No-op when
	// the name is absent.
	Unregister(name StrategyID)

	// LookupFactory returns the StrategyFactory registered under name.
	// Returns StrategyNotRegisteredError when the name is absent so
	// callers can surface InvalidStrategyError to operators.
	LookupFactory(name StrategyID) (StrategyFactory, error)

	// ListAvailable returns every registered strategy name in
	// lexicographic order. Used to populate the Allowed slice on
	// validation errors so the operator sees what is configured.
	ListAvailable() []StrategyID
}

// DefaultStrategyRegistry is the concrete StrategyRegistry. Safe for
// concurrent use; Register / Unregister hold a short write lock, and
// LookupFactory / ListAvailable hold a short read lock.
type DefaultStrategyRegistry struct {
	mu        sync.RWMutex
	factories map[StrategyID]StrategyFactory
}

// NewStrategyRegistry returns an empty DefaultStrategyRegistry. Use
// NewDefaultStrategyRegistry to obtain a registry pre-populated with
// qrdb's software-only strategies.
func NewStrategyRegistry() *DefaultStrategyRegistry {
	return &DefaultStrategyRegistry{factories: make(map[StrategyID]StrategyFactory)}
}

// NewDefaultStrategyRegistry returns a DefaultStrategyRegistry
// pre-populated with the strategies qrdb owns: StrategyPassphrase
// and StrategyNone. Higher layers add hardware strategies (TPM2, PKCS#11,
// cloud KMS, Vault) on top by calling Register.
//
// The passphrase factory uses NewDeferredSoftwareStrategy so the
// passphrase can be supplied at Initialize/Unseal time rather than at
// construction. It is registered under StrategyPassphrase. Shamir
// registration is intentionally omitted from this
// helper because the Shamir strategy requires a Threshold and Shares
// configuration that only the daemon's init command can supply; the
// daemon constructs it inline at init time and does not look it up via
// the registry.
func NewDefaultStrategyRegistry() *DefaultStrategyRegistry {
	r := NewStrategyRegistry()
	deferredSoftware := func() (SealingStrategy, error) {
		return NewDeferredSoftwareStrategy(), nil
	}
	_ = r.Register(StrategyPassphrase, deferredSoftware)
	_ = r.Register(StrategyNone, func() (SealingStrategy, error) {
		return NewNoneStrategy(), nil
	})
	return r
}

// Register installs factory under name. Returns
// StrategyAlreadyRegisteredError on duplicate registration.
func (r *DefaultStrategyRegistry) Register(name StrategyID, factory StrategyFactory) error {
	if factory == nil {
		return &StrategyRegisterError{Reason: "nil factory"}
	}
	if name == "" {
		return &StrategyRegisterError{Reason: "empty strategy name"}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[name]; exists {
		return &StrategyAlreadyRegisteredError{Name: name}
	}
	r.factories[name] = factory
	return nil
}

// Unregister removes the strategy registered under name. No-op when the
// name is absent.
func (r *DefaultStrategyRegistry) Unregister(name StrategyID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.factories, name)
}

// LookupFactory returns the StrategyFactory registered under name.
func (r *DefaultStrategyRegistry) LookupFactory(name StrategyID) (StrategyFactory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.factories[name]
	if !ok {
		return nil, &StrategyNotRegisteredError{Name: name, Available: r.listAvailableLocked()}
	}
	return f, nil
}

// ListAvailable returns every registered strategy name in lexicographic
// order. Returned slice is a fresh copy; mutating it does not affect the
// registry.
func (r *DefaultStrategyRegistry) ListAvailable() []StrategyID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.listAvailableLocked()
}

// listAvailableLocked must be called with r.mu held.
func (r *DefaultStrategyRegistry) listAvailableLocked() []StrategyID {
	names := make([]StrategyID, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}

// StrategyRegisterError is returned by Register when the supplied factory
// or name is malformed (nil factory, empty name).
type StrategyRegisterError struct {
	Reason string
}

// Error implements the error interface.
func (e *StrategyRegisterError) Error() string {
	return "qrdb/seal: strategy register failed: " + e.Reason
}

// StrategyAlreadyRegisteredError is returned by Register when the name is
// already registered. Callers wanting replacement semantics must
// Unregister first.
type StrategyAlreadyRegisteredError struct {
	Name StrategyID
}

// Error implements the error interface.
func (e *StrategyAlreadyRegisteredError) Error() string {
	return "qrdb/seal: strategy already registered: " + string(e.Name)
}

// StrategyNotRegisteredError is returned by LookupFactory when the
// requested name has no registered factory. The Available list carries
// the lexicographically-sorted set of registered names so callers can
// surface a useful error.
type StrategyNotRegisteredError struct {
	Name      StrategyID
	Available []StrategyID
}

// Error implements the error interface.
func (e *StrategyNotRegisteredError) Error() string {
	avail := make([]string, len(e.Available))
	for i, n := range e.Available {
		avail[i] = string(n)
	}
	return "qrdb/seal: strategy not registered: " + string(e.Name) +
		" (available: " + joinNames(avail) + ")"
}

// joinNames renders a slice of strategy names as a comma-separated list
// without depending on strings.Join (avoids a transitive import for one
// helper call).
func joinNames(names []string) string {
	if len(names) == 0 {
		return ""
	}
	out := names[0]
	for _, n := range names[1:] {
		out += ", " + n
	}
	return out
}

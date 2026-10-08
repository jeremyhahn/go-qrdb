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

// NewSystemStrategyRegistry returns the registry a node's system barrier
// resolves its sealing strategies through: qrdb's software strategies --
// passphrase (deferred software), none, and shamir with the configured
// threshold and total -- overlaid by every strategy composer holds, which
// wins on a name both hold. This is how a composer's hardware strategy
// (TPM2, PKCS#11, a cloud KMS) reaches the system barrier: the same
// registry it passes for tenant barriers. A nil composer leaves the
// software strategies alone.
//
// The result is a snapshot: a strategy composer registers afterwards does
// not reach it. Shamir parameters are validated when the strategy is built,
// so a node whose configuration names no threshold still gets a registry.
func NewSystemStrategyRegistry(composer StrategyRegistry, threshold, total int) (*DefaultStrategyRegistry, error) {
	r := NewDefaultStrategyRegistry()
	shamir := func() (SealingStrategy, error) {
		s, err := NewShamirStrategy(threshold, total)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	if err := r.Register(StrategyShamir, shamir); err != nil {
		return nil, err
	}
	if composer == nil {
		return r, nil
	}
	for _, name := range composer.ListAvailable() {
		factory, err := composer.LookupFactory(name)
		if err != nil {
			return nil, err
		}
		r.Unregister(name)
		if err := r.Register(name, factory); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// ResolveSystemStrategies returns the strategies a system barrier is built
// with, resolved through registry: first the configured strategy, which must
// resolve and build, then the strategy the persisted root key names when it
// differs. The persisted one is what a completed enable-encryption migration
// leaves behind while the configuration still names the source strategy, so
// it is registered for the node to unseal after a restart. It is resolved
// best effort: one that is not registered, or cannot be built (shamir with
// no threshold configured), is left out, and the barrier reports the
// mismatch when it unseals.
func ResolveSystemStrategies(registry StrategyRegistry, configured, persisted StrategyID) ([]SealingStrategy, error) {
	factory, err := registry.LookupFactory(configured)
	if err != nil {
		return nil, err
	}
	primary, err := factory()
	if err != nil {
		return nil, err
	}
	strategies := []SealingStrategy{primary}
	if persisted == "" || persisted == primary.ID() {
		return strategies, nil
	}
	if factory, err := registry.LookupFactory(persisted); err == nil {
		if s, err := factory(); err == nil {
			strategies = append(strategies, s)
		}
	}
	return strategies, nil
}

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
	"context"
	"errors"
	"testing"
)

// phantomRegistry lists a name it cannot resolve, the way a composer's
// registry might between a ListAvailable and a LookupFactory.
type phantomRegistry struct {
	*DefaultStrategyRegistry
}

func (phantomRegistry) ListAvailable() []StrategyID { return []StrategyID{"phantom"} }

var _ StrategyRegistry = phantomRegistry{}

func TestNewSystemStrategyRegistry_SoftwareDefaults(t *testing.T) {
	r, err := NewSystemStrategyRegistry(nil, 2, 3)
	if err != nil {
		t.Fatalf("NewSystemStrategyRegistry: %v", err)
	}
	for _, id := range []StrategyID{StrategyPassphrase, StrategyNone, StrategyShamir} {
		factory, err := r.LookupFactory(id)
		if err != nil {
			t.Fatalf("LookupFactory(%s): %v", id, err)
		}
		s, err := factory()
		if err != nil {
			t.Fatalf("build %s: %v", id, err)
		}
		if s.ID() != id {
			t.Errorf("%s factory built %s", id, s.ID())
		}
	}
}

func TestNewSystemStrategyRegistry_ComposerOverlays(t *testing.T) {
	hsm := newMockHSMStrategy(t, "mock-hsm")
	wrapped := newMockHSMStrategy(t, string(StrategyPassphrase))
	composer := NewStrategyRegistry()
	for _, s := range []SealingStrategy{hsm, wrapped} {
		if err := composer.Register(s.ID(), func() (SealingStrategy, error) { return s, nil }); err != nil {
			t.Fatalf("Register(%s): %v", s.ID(), err)
		}
	}

	r, err := NewSystemStrategyRegistry(composer, 2, 3)
	if err != nil {
		t.Fatalf("NewSystemStrategyRegistry: %v", err)
	}
	for id, want := range map[StrategyID]SealingStrategy{"mock-hsm": hsm, StrategyPassphrase: wrapped} {
		factory, err := r.LookupFactory(id)
		if err != nil {
			t.Fatalf("LookupFactory(%s): %v", id, err)
		}
		if got, _ := factory(); got != want {
			t.Errorf("%s resolved to %T, want the composer's strategy", id, got)
		}
	}
	if _, err := r.LookupFactory(StrategyShamir); err != nil {
		t.Errorf("software shamir lost under the overlay: %v", err)
	}
}

func TestNewSystemStrategyRegistry_ComposerLookupFails(t *testing.T) {
	_, err := NewSystemStrategyRegistry(phantomRegistry{NewStrategyRegistry()}, 2, 3)
	var notRegistered *StrategyNotRegisteredError
	if !errors.As(err, &notRegistered) {
		t.Fatalf("err = %v, want *StrategyNotRegisteredError", err)
	}
}

func TestResolveSystemStrategies_Configured(t *testing.T) {
	r, _ := NewSystemStrategyRegistry(nil, 2, 3)
	for _, persisted := range []StrategyID{"", StrategyNone} {
		got, err := ResolveSystemStrategies(r, StrategyNone, persisted)
		if err != nil {
			t.Fatalf("persisted %q: %v", persisted, err)
		}
		if len(got) != 1 || got[0].ID() != StrategyNone {
			t.Errorf("persisted %q: got %d strategies, want only none", persisted, len(got))
		}
	}
}

func TestResolveSystemStrategies_ConfiguredMustResolve(t *testing.T) {
	r, _ := NewSystemStrategyRegistry(nil, 5, 3)
	var notRegistered *StrategyNotRegisteredError
	if _, err := ResolveSystemStrategies(r, "tpm2", ""); !errors.As(err, &notRegistered) {
		t.Errorf("unregistered: err = %v, want *StrategyNotRegisteredError", err)
	}
	if _, err := ResolveSystemStrategies(r, StrategyShamir, ""); err == nil {
		t.Error("shamir with threshold above total built")
	}
}

func TestResolveSystemStrategies_Persisted(t *testing.T) {
	r, _ := NewSystemStrategyRegistry(nil, 0, 0)
	got, err := ResolveSystemStrategies(r, StrategyNone, StrategyPassphrase)
	if err != nil {
		t.Fatalf("ResolveSystemStrategies: %v", err)
	}
	if len(got) != 2 || got[0].ID() != StrategyNone || got[1].ID() != StrategyPassphrase {
		t.Fatalf("got %d strategies, want none then passphrase", len(got))
	}
	// A persisted strategy that is not registered, or cannot be built, is
	// left for the barrier to report at unseal.
	for _, persisted := range []StrategyID{"tpm2", StrategyShamir} {
		got, err := ResolveSystemStrategies(r, StrategyNone, persisted)
		if err != nil {
			t.Fatalf("persisted %s: %v", persisted, err)
		}
		if len(got) != 1 {
			t.Errorf("persisted %s: got %d strategies, want 1", persisted, len(got))
		}
	}
}

// A composer's device-held strategy reaches the system barrier by name
// through the registry, and the barrier initializes and unseals through it
// with no credentials: the key never leaves the device.
func TestResolveSystemStrategies_ComposerStrategySealsSystemBarrier(t *testing.T) {
	ctx := context.Background()
	hsm := newMockHSMStrategy(t, "mock-hsm")
	composer := NewStrategyRegistry()
	if err := composer.Register(hsm.ID(), func() (SealingStrategy, error) { return hsm, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	r, err := NewSystemStrategyRegistry(composer, 0, 0)
	if err != nil {
		t.Fatalf("NewSystemStrategyRegistry: %v", err)
	}
	strategies, err := ResolveSystemStrategies(r, hsm.ID(), "")
	if err != nil {
		t.Fatalf("ResolveSystemStrategies: %v", err)
	}
	backend := NewMemoryBackend()
	cfg := BarrierConfig{RootKeyPath: "root-key", PreferenceOrder: []StrategyID{hsm.ID()}}
	b, err := NewStorageBarrier(xkmsTestLogger(), backend, cfg, strategies...)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := PersistedStrategyFrom(ctx, backend, cfg.RootKeyPath); got != hsm.ID() {
		t.Fatalf("root key sealed by %q, want %q", got, hsm.ID())
	}
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := b.Unseal(ctx, Credentials{}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
}

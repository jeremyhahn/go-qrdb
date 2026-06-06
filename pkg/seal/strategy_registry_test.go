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
	"errors"
	"reflect"
	"testing"
)

func TestStrategyRegistry_RegisterAndLookup(t *testing.T) {
	r := NewStrategyRegistry()
	called := false
	factory := func() (SealingStrategy, error) {
		called = true
		return NewNoneStrategy(), nil
	}
	if err := r.Register(StrategyNone, factory); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := r.LookupFactory(StrategyNone)
	if err != nil {
		t.Fatalf("LookupFactory: %v", err)
	}
	if _, err := got(); err != nil {
		t.Fatalf("factory invocation: %v", err)
	}
	if !called {
		t.Fatal("factory not invoked")
	}
}

func TestStrategyRegistry_RegisterNilFactoryRejected(t *testing.T) {
	r := NewStrategyRegistry()
	var ne *StrategyRegisterError
	if err := r.Register(StrategyPassphrase, nil); !errors.As(err, &ne) {
		t.Fatalf("Register(nil) err = %v, want StrategyRegisterError", err)
	}
}

func TestStrategyRegistry_RegisterEmptyNameRejected(t *testing.T) {
	r := NewStrategyRegistry()
	var ne *StrategyRegisterError
	if err := r.Register("", func() (SealingStrategy, error) { return nil, nil }); !errors.As(err, &ne) {
		t.Fatalf("Register('') err = %v, want StrategyRegisterError", err)
	}
}

func TestStrategyRegistry_DuplicateRejected(t *testing.T) {
	r := NewStrategyRegistry()
	factory := func() (SealingStrategy, error) { return NewNoneStrategy(), nil }
	_ = r.Register(StrategyNone, factory)
	var dup *StrategyAlreadyRegisteredError
	if err := r.Register(StrategyNone, factory); !errors.As(err, &dup) {
		t.Fatalf("duplicate Register: want StrategyAlreadyRegisteredError, got %v", err)
	}
	if dup.Name != StrategyNone {
		t.Fatalf("dup.Name = %q, want %q", dup.Name, StrategyNone)
	}
}

func TestStrategyRegistry_LookupMissingReturnsNotRegistered(t *testing.T) {
	r := NewStrategyRegistry()
	_ = r.Register(StrategyPassphrase, func() (SealingStrategy, error) { return NewDeferredSoftwareStrategy(), nil })
	var nf *StrategyNotRegisteredError
	if _, err := r.LookupFactory(StrategyID("tpm2")); !errors.As(err, &nf) {
		t.Fatalf("Lookup(tpm2) err = %v, want StrategyNotRegisteredError", err)
	}
	want := []StrategyID{StrategyPassphrase}
	if !reflect.DeepEqual(nf.Available, want) {
		t.Fatalf("nf.Available = %v, want %v", nf.Available, want)
	}
}

func TestStrategyRegistry_UnregisterRemoves(t *testing.T) {
	r := NewStrategyRegistry()
	_ = r.Register(StrategyNone, func() (SealingStrategy, error) { return NewNoneStrategy(), nil })
	r.Unregister(StrategyNone)
	if _, err := r.LookupFactory(StrategyNone); err == nil {
		t.Fatal("Lookup after Unregister: expected err, got nil")
	}
}

func TestStrategyRegistry_UnregisterMissingIsNoop(t *testing.T) {
	r := NewStrategyRegistry()
	r.Unregister(StrategyID("tpm2")) // must not panic
}

func TestStrategyRegistry_ListSorted(t *testing.T) {
	r := NewStrategyRegistry()
	noop := func() (SealingStrategy, error) { return NewNoneStrategy(), nil }
	for _, n := range []StrategyID{"tpm2", "passphrase", "none", "shamir"} {
		_ = r.Register(n, noop)
	}
	want := []StrategyID{"none", "passphrase", "shamir", "tpm2"}
	if got := r.ListAvailable(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ListAvailable = %v, want %v", got, want)
	}
}

func TestStrategyRegistry_DefaultPrepopulated(t *testing.T) {
	r := NewDefaultStrategyRegistry()
	// The default registry pre-populates None plus the passphrase strategy
	// under its canonical name (StrategyPassphrase); ListAvailable returns
	// them sorted.
	want := []StrategyID{StrategyNone, StrategyPassphrase}
	if got := r.ListAvailable(); !reflect.DeepEqual(got, want) {
		t.Fatalf("default ListAvailable = %v, want %v", got, want)
	}
	for _, name := range want {
		if _, err := r.LookupFactory(name); err != nil {
			t.Fatalf("default registry missing %q: %v", name, err)
		}
	}
}

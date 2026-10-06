// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"context"
	"errors"
	"testing"
)

// fakeProvider is a minimal Provider double for registry tests.
type fakeProvider struct{ name string }

func (p *fakeProvider) Name() string { return p.name }
func (p *fakeProvider) Open(context.Context, Spec) (Volume, error) {
	return nil, nil
}

func TestNewRegistry_PrePopulatedWithDir(t *testing.T) {
	r := NewRegistry()
	p, err := r.Lookup("dir")
	if err != nil {
		t.Fatalf("Lookup(dir): %v", err)
	}
	if p.Name() != "dir" {
		t.Fatalf("Lookup(dir).Name() = %q, want dir", p.Name())
	}
}

func TestRegistry_RegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&fakeProvider{name: "luks2"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	p, err := r.Lookup("luks2")
	if err != nil {
		t.Fatalf("Lookup(luks2): %v", err)
	}
	if p.Name() != "luks2" {
		t.Fatalf("Lookup(luks2).Name() = %q, want luks2", p.Name())
	}
}

func TestRegistry_RegisterDuplicateRefused(t *testing.T) {
	r := NewRegistry()
	err := r.Register(&fakeProvider{name: "dir"})
	var target *ProviderAlreadyRegisteredError
	if !errors.As(err, &target) {
		t.Fatalf("Register(dir) again error = %v, want *ProviderAlreadyRegisteredError", err)
	}
	if target.Provider != "dir" {
		t.Fatalf("ProviderAlreadyRegisteredError.Provider = %q, want dir", target.Provider)
	}
}

func TestRegistry_RegisterInvalid(t *testing.T) {
	r := NewRegistry()

	if err := r.Register(nil); err == nil {
		t.Fatal("Register(nil) should fail")
	}
	if err := r.Register(&fakeProvider{name: ""}); err == nil {
		t.Fatal("Register(empty name) should fail")
	}
}

func TestRegistry_LookupUnknown(t *testing.T) {
	r := NewRegistry()
	_, err := r.Lookup("luks2")
	var target *ProviderNotRegisteredError
	if !errors.As(err, &target) {
		t.Fatalf("Lookup(luks2) error = %v, want *ProviderNotRegisteredError", err)
	}
	if len(target.Available) != 1 || target.Available[0] != "dir" {
		t.Fatalf("ProviderNotRegisteredError.Available = %v, want [dir]", target.Available)
	}
}

func TestRegistry_ListAvailableSorted(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&fakeProvider{name: "luks2"})
	_ = r.Register(&fakeProvider{name: "bulk"})

	got := r.ListAvailable()
	want := []string{"bulk", "dir", "luks2"}
	if len(got) != len(want) {
		t.Fatalf("ListAvailable() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListAvailable() = %v, want %v", got, want)
		}
	}
}

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
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeHardwareProvider is a same-shaped test double standing in for a real
// go-xkms hardware or cloud-KMS provider. Its presence here, registered
// through Registry exactly as a real one would be, proves the registry
// path works for ANY Provider, not only SoftwareProvider -- go-qrdb never
// imports go-xkms, so this is how the fit is verified.
type fakeHardwareProvider struct {
	id string
}

var _ Provider = (*fakeHardwareProvider)(nil)

func (p *fakeHardwareProvider) ProviderID() string { return p.id }

func (p *fakeHardwareProvider) Wrap(_ context.Context, keyRef string, dek []byte) ([]byte, error) {
	wrapped := make([]byte, len(dek))
	copy(wrapped, dek)
	return wrapped, nil
}

func (p *fakeHardwareProvider) Unwrap(_ context.Context, keyRef string, wrapped []byte) ([]byte, error) {
	dek := make([]byte, len(wrapped))
	copy(dek, wrapped)
	return dek, nil
}

func TestNewRegistry_StartsEmpty(t *testing.T) {
	r := NewRegistry()
	if got := r.ListAvailable(); len(got) != 0 {
		t.Fatalf("ListAvailable() = %v, want empty", got)
	}
}

func TestNewDefaultRegistry_HasExactlySoftware(t *testing.T) {
	r, err := NewDefaultRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	got := r.ListAvailable()
	want := []string{SoftwareProviderID}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("ListAvailable() = %v, want %v", got, want)
	}
}

func TestDefaultRegistry_RegisterRefusesDuplicate(t *testing.T) {
	r := NewRegistry()
	factory := func() (Provider, error) { return &fakeHardwareProvider{id: "tpm2"}, nil }

	if err := r.Register("tpm2", factory); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	err := r.Register("tpm2", factory)
	var already *ProviderAlreadyRegisteredError
	if !errors.As(err, &already) {
		t.Fatalf("second Register err = %v, want *ProviderAlreadyRegisteredError", err)
	}
	if already.ID != "tpm2" {
		t.Fatalf("already.ID = %q, want %q", already.ID, "tpm2")
	}
}

func TestDefaultRegistry_RegisterRefusesNilFactoryOrEmptyID(t *testing.T) {
	r := NewRegistry()

	if err := r.Register("x", nil); err == nil {
		t.Fatal("Register with nil factory: want error, got nil")
	}
	if err := r.Register("", func() (Provider, error) { return nil, nil }); err == nil {
		t.Fatal("Register with empty id: want error, got nil")
	}
}

func TestDefaultRegistry_LookupFactoryUnregistered(t *testing.T) {
	r, err := NewDefaultRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}

	_, err = r.LookupFactory("aws-kms")
	var notReg *ProviderNotRegisteredError
	if !errors.As(err, &notReg) {
		t.Fatalf("LookupFactory err = %v, want *ProviderNotRegisteredError", err)
	}
	if notReg.ID != "aws-kms" {
		t.Fatalf("notReg.ID = %q, want %q", notReg.ID, "aws-kms")
	}
	if len(notReg.Available) != 1 || notReg.Available[0] != SoftwareProviderID {
		t.Fatalf("notReg.Available = %v, want [%q]", notReg.Available, SoftwareProviderID)
	}
}

func TestDefaultRegistry_UnregisterIsNoopWhenAbsent(t *testing.T) {
	r := NewRegistry()
	r.Unregister("nothing-here") // must not panic
}

func TestDefaultRegistry_Lookup_ResolvesAndConstructsAnyProvider(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("tpm2", func() (Provider, error) { return &fakeHardwareProvider{id: "tpm2"}, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p, err := r.Lookup("tpm2")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if p.ProviderID() != "tpm2" {
		t.Fatalf("p.ProviderID() = %q, want %q", p.ProviderID(), "tpm2")
	}
}

func TestLookup_PackageFunctionWorksThroughInterface(t *testing.T) {
	// Exercise Lookup via the Registry INTERFACE, not the concrete type,
	// proving a caller holding only keywrap.Registry (e.g. a composed
	// *DefaultRegistry passed as the interface through WithKeyWrapRegistry)
	// can still resolve and construct.
	reg, err := NewDefaultRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	var r Registry = reg

	p, err := Lookup(r, SoftwareProviderID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if p.ProviderID() != SoftwareProviderID {
		t.Fatalf("p.ProviderID() = %q, want %q", p.ProviderID(), SoftwareProviderID)
	}
}

// TestNewDefaultRegistry_RefusesUnopenableKeyDir proves a key directory
// that cannot be opened as an *os.Root (here, a path through a file rather
// than a directory) fails registry construction with *KeyDirectoryError --
// the construction-time, operator-facing error -- rather than silently
// deferring the failure to first use.
func TestNewDefaultRegistry_RefusesUnopenableKeyDir(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := NewDefaultRegistry(filepath.Join(notADir, "sub"))
	var dirErr *KeyDirectoryError
	if !errors.As(err, &dirErr) {
		t.Fatalf("NewDefaultRegistry err = %v, want *KeyDirectoryError", err)
	}
	if dirErr.Dir == "" {
		t.Fatal("KeyDirectoryError.Dir is empty")
	}
}

func TestLookup_PropagatesNotRegistered(t *testing.T) {
	var r Registry = NewRegistry()
	_, err := Lookup(r, "missing")
	var notReg *ProviderNotRegisteredError
	if !errors.As(err, &notReg) {
		t.Fatalf("Lookup err = %v, want *ProviderNotRegisteredError", err)
	}
}

func TestDefaultRegistry_ListAvailableIsSortedAndIsACopy(t *testing.T) {
	r := NewRegistry()
	_ = r.Register("zzz", func() (Provider, error) { return nil, nil })
	_ = r.Register("aaa", func() (Provider, error) { return nil, nil })

	got := r.ListAvailable()
	want := []string{"aaa", "zzz"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ListAvailable() = %v, want %v", got, want)
	}

	got[0] = "mutated"
	again := r.ListAvailable()
	if again[0] == "mutated" {
		t.Fatal("ListAvailable() returned a slice aliasing internal state")
	}
}

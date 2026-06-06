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
	"context"
	"errors"
	"reflect"
	"testing"
)

// stubFactor is the minimal Factor implementation used to exercise the
// FactorRegistry. It carries a name so multiple instances can be
// distinguished but does NOT run real crypto.
type stubFactor struct{ name string }

func (f *stubFactor) Name() string { return f.name }

func (f *stubFactor) Enroll(_ context.Context, _ *EnrollParams) (*Enrollment, error) {
	return &Enrollment{FactorName: f.name}, nil
}

func (f *stubFactor) Challenge(_ context.Context, _ *Enrollment) (*Challenge, error) {
	return &Challenge{}, nil
}

func (f *stubFactor) Verify(_ context.Context, _ *Challenge, _ *Response) error { return nil }

func TestFactorRegistry_RegisterAndLookup(t *testing.T) {
	r := NewFactorRegistry()
	f := &stubFactor{name: "fido2"}

	if err := r.Register(f); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := r.Lookup("fido2")
	if err != nil {
		t.Fatalf("Lookup(fido2): %v", err)
	}
	if got != f {
		t.Fatalf("Lookup returned %v, want %v", got, f)
	}
}

func TestFactorRegistry_RegisterNilRejected(t *testing.T) {
	r := NewFactorRegistry()
	var nf *FactorRegisterError
	if err := r.Register(nil); !errors.As(err, &nf) {
		t.Fatalf("Register(nil): want FactorRegisterError, got %T %v", err, err)
	}
}

func TestFactorRegistry_RegisterEmptyNameRejected(t *testing.T) {
	r := NewFactorRegistry()
	var nf *FactorRegisterError
	if err := r.Register(&stubFactor{name: ""}); !errors.As(err, &nf) {
		t.Fatalf("Register(empty name): want FactorRegisterError, got %T %v", err, err)
	}
}

func TestFactorRegistry_DuplicateRegistrationRejected(t *testing.T) {
	r := NewFactorRegistry()
	if err := r.Register(&stubFactor{name: "piv"}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	var dup *FactorAlreadyRegisteredError
	err := r.Register(&stubFactor{name: "piv"})
	if !errors.As(err, &dup) {
		t.Fatalf("duplicate Register: want FactorAlreadyRegisteredError, got %T %v", err, err)
	}
	if dup.Name != "piv" {
		t.Fatalf("dup.Name = %q, want %q", dup.Name, "piv")
	}
}

func TestFactorRegistry_LookupMissingReturnsNotFound(t *testing.T) {
	r := NewFactorRegistry()
	var nf *FactorNotFoundError
	if _, err := r.Lookup("nope"); !errors.As(err, &nf) {
		t.Fatalf("Lookup missing: want FactorNotFoundError, got %T %v", err, err)
	}
}

func TestFactorRegistry_UnregisterRemoves(t *testing.T) {
	r := NewFactorRegistry()
	if err := r.Register(&stubFactor{name: "pkcs11"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	r.Unregister("pkcs11")
	if _, err := r.Lookup("pkcs11"); err == nil {
		t.Fatal("Lookup after Unregister: expected error, got nil")
	}
}

func TestFactorRegistry_UnregisterMissingIsNoop(t *testing.T) {
	r := NewFactorRegistry()
	// Should not panic; should be a no-op.
	r.Unregister("never-registered")
}

func TestFactorRegistry_ListSorted(t *testing.T) {
	r := NewFactorRegistry()
	for _, n := range []string{"piv", "fido2", "mtls", "pkcs11"} {
		if err := r.Register(&stubFactor{name: n}); err != nil {
			t.Fatalf("Register(%q): %v", n, err)
		}
	}
	want := []string{"fido2", "mtls", "piv", "pkcs11"}
	if got := r.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %v, want %v", got, want)
	}
}

func TestFactorRegistry_AllReturnsCopiedSlice(t *testing.T) {
	r := NewFactorRegistry()
	a := &stubFactor{name: "fido2"}
	b := &stubFactor{name: "piv"}
	_ = r.Register(a)
	_ = r.Register(b)

	all := r.All()
	if len(all) != 2 {
		t.Fatalf("All() len = %d, want 2", len(all))
	}
	// Mutating the returned slice must not affect the registry.
	all[0] = nil
	again, err := r.Lookup("fido2")
	if err != nil || again != a {
		t.Fatalf("after mutating All() result, Lookup(fido2) = %v err=%v, want %v nil", again, err, a)
	}
}

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
	"errors"
	"testing"
)

// TestFactorRegistry_NamesAreNormalised: the registry keys a factor by the
// same normalisation the auth-factor service dispatches on, so "PIV" and
// "piv" are one factor -- a second registration is refused, and a lookup in
// either spelling finds it.
func TestFactorRegistry_NamesAreNormalised(t *testing.T) {
	r := NewFactorRegistry()
	upper := &stubFactor{name: "PIV"}
	if err := r.Register(upper); err != nil {
		t.Fatalf("Register(PIV): %v", err)
	}
	var dup *FactorAlreadyRegisteredError
	if err := r.Register(&stubFactor{name: "piv"}); !errors.As(err, &dup) {
		t.Fatalf("Register(piv) after PIV = %v, want *FactorAlreadyRegisteredError", err)
	}
	for _, name := range []string{"piv", "PIV", "Piv"} {
		got, err := r.Lookup(name)
		if err != nil || got != upper {
			t.Fatalf("Lookup(%q) = %v, %v; want the registered factor", name, got, err)
		}
	}
	r.Unregister("Piv")
	if _, err := r.Lookup("piv"); err == nil {
		t.Fatal("Unregister in another spelling left the factor registered")
	}
}

func TestNormalizeName(t *testing.T) {
	if NormalizeName("FIDO2") != NormalizeName("fido2") || NormalizeName("PIV") != "piv" {
		t.Fatal("NormalizeName does not fold case")
	}
}

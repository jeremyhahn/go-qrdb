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

package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestParseMTLSBindingPolicy(t *testing.T) {
	cases := []struct {
		in   string
		want MTLSBindingPolicy
	}{
		{"", MTLSBindingEnrolled},
		{"enrolled", MTLSBindingEnrolled},
		{"  Enrolled ", MTLSBindingEnrolled},
		{"required", MTLSBindingRequired},
		{"REQUIRED", MTLSBindingRequired},
	}
	for _, tc := range cases {
		got, err := ParseMTLSBindingPolicy(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("ParseMTLSBindingPolicy(%q) = (%v, %v), want (%v, nil)", tc.in, got, err, tc.want)
		}
	}
}

// TestParseMTLSBindingPolicy_UnknownFailsRatherThanGuessing: a typo resolves to
// neither posture, because either guess is wrong for somebody.
func TestParseMTLSBindingPolicy_UnknownFailsRatherThanGuessing(t *testing.T) {
	for _, in := range []string{"require", "strict", "on", "enrolled,required"} {
		_, err := ParseMTLSBindingPolicy(in)
		var inv *InvalidMTLSBindingPolicyError
		if !errors.As(err, &inv) || inv.Value != in {
			t.Fatalf("ParseMTLSBindingPolicy(%q) err = %v, want *InvalidMTLSBindingPolicyError", in, err)
		}
		if !strings.Contains(err.Error(), `"enrolled" or "required"`) {
			t.Fatalf("error %q does not name the accepted values", err.Error())
		}
	}
}

func TestMTLSBindingPolicy_String(t *testing.T) {
	if MTLSBindingEnrolled.String() != "enrolled" || MTLSBindingRequired.String() != "required" {
		t.Fatalf("String() = %q/%q", MTLSBindingEnrolled, MTLSBindingRequired)
	}
	// The zero value is the historical posture.
	var zero MTLSBindingPolicy
	if zero != MTLSBindingEnrolled {
		t.Fatal("zero value is not MTLSBindingEnrolled")
	}
}

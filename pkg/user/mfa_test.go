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

package user

import (
	"testing"
	"time"
)

func TestHasMFA(t *testing.T) {
	tests := []struct {
		name string
		u    *User
		want bool
	}{
		{"nil user", nil, false},
		{"no factors", &User{}, false},
		{"one factor", &User{Factors: []FactorEnrollment{{ID: "f1", FactorName: "fido2"}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.u.HasMFA(); got != tt.want {
				t.Errorf("HasMFA() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMFAMethods(t *testing.T) {
	u := &User{Factors: []FactorEnrollment{
		{ID: "f1", FactorName: "fido2"},
		{ID: "f2", FactorName: "piv"},
		{ID: "f3", FactorName: "fido2"}, // duplicate name, deduplicated
	}}
	got := u.MFAMethods()
	want := []string{"fido2", "piv"}
	if len(got) != len(want) {
		t.Fatalf("MFAMethods() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("MFAMethods()[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if got := (&User{}).MFAMethods(); got != nil {
		t.Errorf("MFAMethods() on no factors = %v, want nil", got)
	}
	var nilUser *User
	if got := nilUser.MFAMethods(); got != nil {
		t.Errorf("MFAMethods() on nil user = %v, want nil", got)
	}
}

func TestMFAEnrolledAt(t *testing.T) {
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	u := &User{Factors: []FactorEnrollment{
		{ID: "f1", FactorName: "piv", CreatedAt: late},
		{ID: "f2", FactorName: "fido2", CreatedAt: early},
	}}
	got, ok := u.MFAEnrolledAt()
	if !ok {
		t.Fatal("MFAEnrolledAt() ok = false, want true")
	}
	if !got.Equal(early) {
		t.Errorf("MFAEnrolledAt() = %v, want the earliest enrollment %v", got, early)
	}

	if _, ok := (&User{}).MFAEnrolledAt(); ok {
		t.Error("MFAEnrolledAt() on no factors: ok = true, want false")
	}
}

func TestWireFactors(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	factors := []FactorEnrollment{
		{ID: "f1", FactorName: "fido2", Data: []byte(`{"secret":"do-not-leak"}`), CreatedAt: now},
	}
	out := WireFactors(factors)
	if len(out) != 1 {
		t.Fatalf("WireFactors() len = %d, want 1", len(out))
	}
	if out[0].ID != "f1" || out[0].FactorName != "fido2" {
		t.Errorf("WireFactors()[0] = %+v, want ID=f1 FactorName=fido2", out[0])
	}
	if out[0].CreatedAt != now.Format(time.RFC3339) {
		t.Errorf("WireFactors()[0].CreatedAt = %q, want %q", out[0].CreatedAt, now.Format(time.RFC3339))
	}

	if got := WireFactors(nil); got != nil {
		t.Errorf("WireFactors(nil) = %v, want nil", got)
	}
}

func TestWireMFA(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	u := &User{Factors: []FactorEnrollment{{ID: "f1", FactorName: "piv", CreatedAt: now}}}

	enabled, methods, enrolledAt := WireMFA(u)
	if !enabled {
		t.Error("WireMFA() enabled = false, want true")
	}
	if len(methods) != 1 || methods[0] != "piv" {
		t.Errorf("WireMFA() methods = %v, want [piv]", methods)
	}
	if enrolledAt != now.Format(time.RFC3339) {
		t.Errorf("WireMFA() enrolledAt = %q, want %q", enrolledAt, now.Format(time.RFC3339))
	}

	enabled, methods, enrolledAt = WireMFA(&User{})
	if enabled || methods != nil || enrolledAt != "" {
		t.Errorf("WireMFA(no factors) = (%v, %v, %q), want (false, nil, \"\")", enabled, methods, enrolledAt)
	}

	enabled, methods, enrolledAt = WireMFA(nil)
	if enabled || methods != nil || enrolledAt != "" {
		t.Errorf("WireMFA(nil) = (%v, %v, %q), want (false, nil, \"\")", enabled, methods, enrolledAt)
	}
}

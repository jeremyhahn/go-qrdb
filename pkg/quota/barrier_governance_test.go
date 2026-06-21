// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package quota

import "testing"

// TestEntitlements_AllowsBarrierMode_Default proves an empty AllowedBarrierModes
// permits the built-in software strategies and fail-closes hardware ones.
func TestEntitlements_AllowsBarrierMode_Default(t *testing.T) {
	e := &Entitlements{} // empty -> software defaults
	for _, mode := range []string{"none", "passphrase", "shamir"} {
		if !e.AllowsBarrierMode(mode) {
			t.Errorf("default AllowsBarrierMode(%q) = false, want true", mode)
		}
	}
	for _, mode := range []string{"tpm2", "pkcs11", "fido2", "cloud-kms"} {
		if e.AllowsBarrierMode(mode) {
			t.Errorf("default AllowsBarrierMode(%q) = true, want false (hardware must be granted)", mode)
		}
	}
	// "software" is normalized to "passphrase".
	if !e.AllowsBarrierMode("software") {
		t.Error(`AllowsBarrierMode("software") = false, want true (alias of passphrase)`)
	}
}

// TestEntitlements_AllowsBarrierMode_Explicit proves a non-empty list is the
// exact allowed set (software defaults no longer implied).
func TestEntitlements_AllowsBarrierMode_Explicit(t *testing.T) {
	e := &Entitlements{AllowedBarrierModes: []string{"passphrase", "tpm2"}}
	if !e.AllowsBarrierMode("passphrase") || !e.AllowsBarrierMode("tpm2") {
		t.Fatal("explicitly listed modes must be allowed")
	}
	if e.AllowsBarrierMode("shamir") {
		t.Error("shamir not in explicit list must be denied")
	}
	if e.AllowsBarrierMode("none") {
		t.Error("none not in explicit list must be denied")
	}
	// software alias matches a "software" entry too.
	e2 := &Entitlements{AllowedBarrierModes: []string{"software"}}
	if !e2.AllowsBarrierMode("passphrase") {
		t.Error(`AllowedBarrierModes ["software"] should allow "passphrase"`)
	}
}

// TestQuotas_AllowsSystemBarrier proves the quota gates the system-barrier option:
// 0 disallows; >0 and -1 allow.
func TestQuotas_AllowsSystemBarrier(t *testing.T) {
	if (&Quotas{SystemBarrierQuota: 0}).AllowsSystemBarrier() {
		t.Error("SystemBarrierQuota=0 must disallow the system-barrier option")
	}
	if !(&Quotas{SystemBarrierQuota: 1024}).AllowsSystemBarrier() {
		t.Error("SystemBarrierQuota>0 must allow the option")
	}
	if !(&Quotas{SystemBarrierQuota: -1}).AllowsSystemBarrier() {
		t.Error("SystemBarrierQuota=-1 (unlimited) must allow the option")
	}
}

// TestQuotas_SystemBarrierBytesRemaining covers unlimited, capped, and exhausted.
func TestQuotas_SystemBarrierBytesRemaining(t *testing.T) {
	if got := (&Quotas{SystemBarrierQuota: -1}).SystemBarrierBytesRemaining(); got <= 0 {
		t.Errorf("unlimited remaining = %d, want large positive", got)
	}
	if got := (&Quotas{SystemBarrierQuota: 1000, CurrentSystemBarrierBytes: 300}).SystemBarrierBytesRemaining(); got != 700 {
		t.Errorf("remaining = %d, want 700", got)
	}
	if got := (&Quotas{SystemBarrierQuota: 1000, CurrentSystemBarrierBytes: 1500}).SystemBarrierBytesRemaining(); got != 0 {
		t.Errorf("exhausted remaining = %d, want 0", got)
	}
}

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

package config

import (
	"errors"
	"math"
	"testing"
)

func TestInvitationRedeemLimit_ZeroTakesTheDefaults(t *testing.T) {
	src, ten, err := InvitationRedeemLimitConfig{}.Resolved()
	if err != nil || src != DefaultRedeemPerSource || ten != DefaultRedeemPerTenant {
		t.Fatalf("Resolved() = %+v, %+v, %v; want the defaults", src, ten, err)
	}
	src, ten, err = InvitationRedeemLimitConfig{
		PerSource: RatePolicyConfig{Burst: 3},
		PerTenant: RatePolicyConfig{RatePerSecond: 5, Burst: 50},
	}.Resolved()
	if err != nil || src != (RatePolicyConfig{RatePerSecond: DefaultRedeemPerSource.RatePerSecond, Burst: 3}) || ten != (RatePolicyConfig{RatePerSecond: 5, Burst: 50}) {
		t.Fatalf("Resolved() = %+v, %+v, %v", src, ten, err)
	}
}

func TestInvitationRedeemLimit_RefusesNegativeAndNonFinite(t *testing.T) {
	var ve *ValidationError
	for _, c := range []InvitationRedeemLimitConfig{
		{PerSource: RatePolicyConfig{Burst: -1}},
		{PerTenant: RatePolicyConfig{RatePerSecond: -1}},
		{PerSource: RatePolicyConfig{RatePerSecond: math.Inf(1)}},
		{PerTenant: RatePolicyConfig{RatePerSecond: math.NaN()}},
	} {
		if _, _, err := c.Resolved(); !errors.As(err, &ve) {
			t.Fatalf("%+v: err = %v, want *ValidationError", c, err)
		}
	}
	cfg := DefaultConfig()
	cfg.Auth.InvitationRedeemLimit.PerSource.Burst = -2
	if err := cfg.Validate(); !errors.As(err, &ve) || ve.Field != "auth.invitation_redeem_limit.per_source" {
		t.Fatalf("Validate() = %v, want a ValidationError on per_source", err)
	}
}

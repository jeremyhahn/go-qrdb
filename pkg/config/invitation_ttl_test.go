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
	"testing"
	"time"
)

func TestParseInvitationTTL_DefaultsAndParses(t *testing.T) {
	d, err := ParseInvitationTTL("")
	if err != nil || d != 24*time.Hour {
		t.Fatalf("empty: %v, %v; want 24h", d, err)
	}
	d, err = ParseInvitationTTL(" 15m ")
	if err != nil || d != 15*time.Minute {
		t.Fatalf("15m: %v, %v", d, err)
	}
	if d, err = ParseInvitationTTL("168h"); err != nil || d != MaxInvitationTTL {
		t.Fatalf("168h (the maximum): %v, %v", d, err)
	}
}

func TestParseInvitationTTL_RefusesNonPositiveTooLongAndGarbage(t *testing.T) {
	for _, in := range []string{"0", "-1h", "169h", "a day"} {
		if _, err := ParseInvitationTTL(in); err == nil {
			t.Errorf("ParseInvitationTTL(%q) accepted", in)
		}
	}
	var ve *ValidationError
	if _, err := ParseInvitationTTL("720h"); !errors.As(err, &ve) || ve.Field != "auth.invitation_ttl" {
		t.Fatalf("720h: err = %v, want *ValidationError on auth.invitation_ttl", err)
	}
}

func TestValidate_RefusesABadInvitationTTL(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Auth.InvitationTTL = "30d"
	var ve *ValidationError
	if err := cfg.Validate(); !errors.As(err, &ve) || ve.Field != "auth.invitation_ttl" {
		t.Fatalf("Validate() = %v, want a ValidationError on auth.invitation_ttl", err)
	}
	cfg.Auth.InvitationTTL = "2h"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with 2h = %v", err)
	}
}

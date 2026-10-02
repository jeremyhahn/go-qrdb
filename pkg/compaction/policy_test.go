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

package compaction

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPolicy_Validate(t *testing.T) {
	tests := []struct {
		name   string
		p      Policy
		reason string // "" means valid
	}{
		{"empty is off", Policy{}, ""},
		{"off", Policy{Mode: Off}, ""},
		{"revision", Policy{Mode: Revision, RetainRevisions: 1}, ""},
		{"period", Policy{Mode: Period, RetainPeriod: "24h"}, ""},
		{"unknown mode", Policy{Mode: "weekly"}, "unknown mode"},
		{"off with a retention", Policy{Mode: Off, RetainRevisions: 5}, "takes no retention"},
		{"off with a period", Policy{RetainPeriod: "1h"}, "takes no retention"},
		{"revision without a count", Policy{Mode: Revision}, "at least 1"},
		{"revision with a period", Policy{Mode: Revision, RetainRevisions: 3, RetainPeriod: "1h"}, "belongs to period"},
		{"period with a count", Policy{Mode: Period, RetainPeriod: "1h", RetainRevisions: 3}, "belongs to revision"},
		{"period unparseable", Policy{Mode: Period, RetainPeriod: "soon"}, "not a duration"},
		{"period too short", Policy{Mode: Period, RetainPeriod: "10ms"}, "at least"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.p.Validate()
			if tt.reason == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want valid", err)
				}
				return
			}
			var invalid *InvalidPolicyError
			if !errors.As(err, &invalid) || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("Validate = %v, want an InvalidPolicyError containing %q", err, tt.reason)
			}
		})
	}
}

func TestPolicy_EnabledAndRetainDuration(t *testing.T) {
	var none *Policy
	if none.Enabled() || none.RetainDuration() != 0 || none.Normalized() != nil {
		t.Fatal("a nil policy is enabled")
	}
	if (&Policy{Mode: Off}).Enabled() || (&Policy{Mode: Off}).Normalized() != nil {
		t.Fatal("an off policy is enabled or stored")
	}
	if (&Policy{Mode: Revision}).Enabled() {
		t.Fatal("an invalid policy is enabled")
	}
	rev := &Policy{Mode: Revision, RetainRevisions: 2}
	if !rev.Enabled() || rev.RetainDuration() != 0 {
		t.Fatal("a revision policy is not enabled, or has a period")
	}
	if n := rev.Normalized(); n == nil || n == rev || *n != *rev {
		t.Fatalf("Normalized = %+v, want an equal copy", n)
	}
	p := &Policy{Mode: Period, RetainPeriod: "90m"}
	if !p.Enabled() || p.RetainDuration() != 90*time.Minute {
		t.Fatalf("period policy: enabled=%v period=%v", p.Enabled(), p.RetainDuration())
	}
	if (&Policy{Mode: Period, RetainPeriod: "x"}).RetainDuration() != 0 {
		t.Fatal("an unparseable period has a duration")
	}
}

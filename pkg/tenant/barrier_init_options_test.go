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

package tenant

import "testing"

func TestResolveTenantBarrierInitOptions_NoOptionsIsTheZeroSettings(t *testing.T) {
	got := ResolveTenantBarrierInitOptions()
	if got.KeyRef != "" || got.InitStatement != nil || got.PreparedRoster != nil || got.PreparedUnsealKeyCheck != nil {
		t.Fatalf("settings = %+v, want zero", got)
	}
	// An empty echo is the first request of a Shamir init and a nil option
	// is skipped: neither sets anything.
	got = ResolveTenantBarrierInitOptions(nil, WithPreparedShamir(nil, nil))
	if got.PreparedRoster != nil || got.PreparedUnsealKeyCheck != nil {
		t.Fatalf("settings = %+v, want no prepared split", got)
	}
}

func TestResolveTenantBarrierInitOptions_ReportsEverySetting(t *testing.T) {
	got := ResolveTenantBarrierInitOptions(
		WithBarrierKeyRef("kms://key"),
		WithInitStatement([]byte("statement")),
		WithPreparedShamir([]byte("roster"), []byte("check")),
	)
	if got.KeyRef != "kms://key" || string(got.InitStatement) != "statement" ||
		string(got.PreparedRoster) != "roster" || string(got.PreparedUnsealKeyCheck) != "check" {
		t.Fatalf("settings = %+v, want every option's value", got)
	}
}

func TestResolveTenantBarrierInitOptions_LaterOptionWins(t *testing.T) {
	got := ResolveTenantBarrierInitOptions(
		WithPreparedShamir([]byte("first"), []byte("first")),
		WithPreparedShamir([]byte("second"), []byte("second")),
	)
	if string(got.PreparedRoster) != "second" || string(got.PreparedUnsealKeyCheck) != "second" {
		t.Fatalf("settings = %+v, want the later echo", got)
	}
}

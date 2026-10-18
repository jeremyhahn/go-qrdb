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

package transport

import (
	"encoding/json"
	"testing"
)

func TestIdentity_TenantOwnerJSONRoundTrip(t *testing.T) {
	for _, owner := range []bool{true, false} {
		data, err := json.Marshal(&Identity{Subject: "1", TenantID: "acme", TenantOwner: owner})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatalf("Unmarshal map: %v", err)
		}
		if got, ok := fields["tenantOwner"]; !ok || got != owner {
			t.Fatalf("tenantOwner key = %v (present=%v), want %v in %s", got, ok, owner, data)
		}
		var back Identity
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if back.TenantOwner != owner {
			t.Fatalf("round trip TenantOwner = %v, want %v", back.TenantOwner, owner)
		}
	}
}

// TenantOwner is a typed field, never a Metadata entry.
func TestIdentity_TenantOwnerNotInMetadata(t *testing.T) {
	data, err := json.Marshal(&Identity{Subject: "1", TenantOwner: true, Metadata: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back Identity
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := back.Metadata["tenantOwner"]; ok {
		t.Fatalf("Metadata must not carry tenantOwner: %s", data)
	}
}

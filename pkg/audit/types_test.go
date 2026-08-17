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

package audit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestEntry_JSONRoundTrip proves the middleware decision Entry survives a
// marshal/unmarshal cycle with every field intact.
func TestEntry_JSONRoundTrip(t *testing.T) {
	in := Entry{
		Timestamp: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
		TenantID:  "t1",
		Subject:   "alice",
		Action:    "auth.authorize",
		Resource:  "keys:sign",
		Result:    ResultDenied,
		Metadata:  map[string]string{"transport": "rest"},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Entry
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.TenantID != in.TenantID || out.Subject != in.Subject ||
		out.Action != in.Action || out.Resource != in.Resource ||
		out.Result != in.Result || out.Metadata["transport"] != "rest" ||
		!out.Timestamp.Equal(in.Timestamp) {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

// TestEntry_JSONOmitsEmptyMetadata proves empty metadata is omitted from the
// wire form rather than serialized as an empty object.
func TestEntry_JSONOmitsEmptyMetadata(t *testing.T) {
	b, err := json.Marshal(Entry{Subject: "a", Action: "x", Resource: "y", Result: ResultSuccess})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "metadata") {
		t.Fatalf("empty metadata must be omitted: %s", b)
	}
}

// TestResultConstants pins the wire vocabulary the middleware and every
// audit consumer agree on.
func TestResultConstants(t *testing.T) {
	if ResultSuccess != "success" || ResultDenied != "denied" {
		t.Fatalf("result constants changed: %q %q", ResultSuccess, ResultDenied)
	}
}

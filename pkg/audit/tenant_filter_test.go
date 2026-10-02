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
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// tenantFilterEvents is one event per owner, "" being a platform event.
func tenantFilterEvents() []AuditEvent {
	base := time.Unix(1_700_000_000, 0).UTC()
	owners := []string{"acme", "", "globex", "system"}
	events := make([]AuditEvent, len(owners))
	for i, owner := range owners {
		events[i] = AuditEvent{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Actor:     "seed",
			TenantID:  owner,
			Resource:  "owner:" + owner,
			Action:    "seed",
			Outcome:   OutcomeSuccess,
		}
	}
	return events
}

func resources(p *Page) []string {
	out := make([]string, 0, len(p.Events))
	for _, ev := range p.Events {
		out = append(out, ev.Resource)
	}
	return out
}

// TestQuery_TenantFilter pins the tenant match on every sink that answers
// Query: a tenant filter reaches exactly that tenant's events, and Untenanted
// adds the platform events that carry no tenant -- and nothing else, whatever
// the page size.
func TestQuery_TenantFilter(t *testing.T) {
	file, err := NewFileLogger(filepath.Join(t.TempDir(), "audit.ndjson"))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })
	sinks := map[string]AuditLogger{
		"ring": NewRingBuffer(16),
		"file": file,
	}

	tests := []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"one tenant", Filter{TenantID: "acme"}, []string{"owner:acme"}},
		{"untenanted adds only the platform events", Filter{TenantID: "acme", Untenanted: true}, []string{"owner:acme", "owner:"}},
		{"system without untenanted", Filter{TenantID: "system"}, []string{"owner:system"}},
		{"system with untenanted", Filter{TenantID: "system", Untenanted: true}, []string{"owner:", "owner:system"}},
		{"no tenant filter reads everything", Filter{}, []string{"owner:acme", "owner:", "owner:globex", "owner:system"}},
	}

	for name, sink := range sinks {
		for _, ev := range tenantFilterEvents() {
			if err := sink.Emit(context.Background(), ev); err != nil {
				t.Fatalf("%s: Emit: %v", name, err)
			}
		}
		for _, tc := range tests {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				var got []string
				f := tc.filter
				f.Limit = 1
				for range 8 {
					page, err := sink.Query(context.Background(), f)
					if err != nil {
						t.Fatalf("Query: %v", err)
					}
					got = append(got, resources(page)...)
					if page.Next == "" {
						break
					}
					f.Cursor = page.Next
				}
				slices.Sort(got)
				want := slices.Clone(tc.want)
				slices.Sort(want)
				if !slices.Equal(got, want) {
					t.Errorf("paged result = %v, want %v", got, want)
				}
			})
		}
	}
}

// TestFilter_UntenantedIsNeverDecoded: Untenanted widens a query to the
// platform's events, so no request body may set it. The QUIC frame decodes
// its payload straight into a Filter.
func TestFilter_UntenantedIsNeverDecoded(t *testing.T) {
	var f Filter
	if err := json.Unmarshal([]byte(`{"tenant_id":"acme","Untenanted":true,"untenanted":true}`), &f); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if f.Untenanted {
		t.Fatal("Untenanted was decoded from a request payload")
	}
	b, err := json.Marshal(Filter{TenantID: "acme", Untenanted: true})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(strings.ToLower(string(b)), "untenanted") {
		t.Errorf("encoded = %s; Untenanted must not appear on the wire", b)
	}
}

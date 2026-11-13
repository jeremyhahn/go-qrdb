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

package publicverbs

import (
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

func TestVerbsMatchTable(t *testing.T) {
	if len(Verbs) != len(Table) {
		t.Fatalf("len(Verbs) = %d, len(Table) = %d", len(Verbs), len(Table))
	}
	seen := make(map[Verb]bool, len(Verbs))
	for _, v := range Verbs {
		if seen[v] {
			t.Errorf("verb %q listed twice in Verbs", v)
		}
		seen[v] = true
		n, ok := Table[v]
		if !ok {
			t.Errorf("verb %q in Verbs has no Table row", v)
			continue
		}
		if n.REST == "" || n.GRPC == "" || n.QUIC == "" || n.MCP == "" || n.Embedded == "" {
			t.Errorf("verb %q has an empty transport name: %+v", v, n)
		}
	}
	want := []Verb{VerbHealth, VerbInvitationRedeem}
	if !reflect.DeepEqual(Verbs, want) {
		t.Errorf("Verbs = %v, want %v", Verbs, want)
	}
}

func TestTableRows(t *testing.T) {
	tests := []struct {
		verb Verb
		want Names
	}{
		{VerbHealth, Names{
			REST:      "GET /health",
			GRPC:      "Health",
			QUIC:      "admin.Health",
			MCP:       "qrdb_health",
			Embedded:  "Health",
			RateLimit: permission.AdminHealth,
		}},
		{VerbInvitationRedeem, Names{
			REST:     "POST /invitations/redeem",
			GRPC:     "RedeemInvitation",
			QUIC:     "invitation.Redeem",
			MCP:      "qrdb_invitation_redeem",
			Embedded: "RedeemInvitation",
		}},
	}
	for _, tt := range tests {
		t.Run(string(tt.verb), func(t *testing.T) {
			if got := Table[tt.verb]; got != tt.want {
				t.Errorf("Table[%q] = %+v, want %+v", tt.verb, got, tt.want)
			}
		})
	}
}

func TestRESTRoute(t *testing.T) {
	tests := []struct {
		verb       Verb
		wantMethod string
		wantPath   string
	}{
		{VerbHealth, "GET", "/health"},
		{VerbInvitationRedeem, "POST", "/invitations/redeem"},
		{Verb("no-such-verb"), "", ""},
	}
	for _, tt := range tests {
		t.Run(string(tt.verb), func(t *testing.T) {
			method, path := RESTRoute(tt.verb)
			if method != tt.wantMethod || path != tt.wantPath {
				t.Errorf("RESTRoute(%q) = (%q, %q), want (%q, %q)",
					tt.verb, method, path, tt.wantMethod, tt.wantPath)
			}
		})
	}
}

func TestRESTRoutes(t *testing.T) {
	want := []string{"GET /health", "POST /invitations/redeem"}
	if got := RESTRoutes(); !reflect.DeepEqual(got, want) {
		t.Errorf("RESTRoutes() = %v, want %v", got, want)
	}
}

func TestGRPCFullMethods(t *testing.T) {
	tests := []struct {
		service string
		want    []string
	}{
		{"qrdb.v1.QRDB", []string{"/qrdb.v1.QRDB/Health", "/qrdb.v1.QRDB/RedeemInvitation"}},
		{"svc", []string{"/svc/Health", "/svc/RedeemInvitation"}},
	}
	for _, tt := range tests {
		t.Run(tt.service, func(t *testing.T) {
			if got := GRPCFullMethods(tt.service); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GRPCFullMethods(%q) = %v, want %v", tt.service, got, tt.want)
			}
		})
	}
}

func TestNameSets(t *testing.T) {
	set := func(names ...string) map[string]struct{} {
		out := make(map[string]struct{}, len(names))
		for _, n := range names {
			out[n] = struct{}{}
		}
		return out
	}
	tests := []struct {
		name string
		got  map[string]struct{}
		want map[string]struct{}
	}{
		{"QUICMethods", QUICMethods(), set("admin.Health", "invitation.Redeem")},
		{"MCPTools", MCPTools(), set("qrdb_health", "qrdb_invitation_redeem")},
		{"EmbeddedMethods", EmbeddedMethods(), set("Health", "RedeemInvitation")},
		{"names/GRPC", names(func(n Names) string { return n.GRPC }), set("Health", "RedeemInvitation")},
		{"names/REST", names(func(n Names) string { return n.REST }), set("GET /health", "POST /invitations/redeem")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
			}
		})
	}
}

func TestQUICVerb(t *testing.T) {
	tests := []struct {
		method string
		want   Verb
		ok     bool
	}{
		{"admin.Health", VerbHealth, true},
		{"invitation.Redeem", VerbInvitationRedeem, true},
		{"admin.Shutdown", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got, ok := QUICVerb(tt.method)
			if got != tt.want || ok != tt.ok {
				t.Errorf("QUICVerb(%q) = (%q, %v), want (%q, %v)", tt.method, got, ok, tt.want, tt.ok)
			}
		})
	}
}

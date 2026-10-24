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

package policywire

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
)

func boolPtr(b bool) *bool { return &b }

func fullPolicy(enabled *bool) *abac.Policy {
	created := time.Date(2026, 10, 21, 9, 30, 0, 123456789, time.UTC)
	return &abac.Policy{
		APIVersion: "abac.qrdb.io/v1",
		Kind:       abac.PolicyKind,
		Metadata: abac.PolicyMetadata{
			Name:        "deny-secrets",
			Tenant:      "acme",
			Description: "deny auditors the secrets tree",
			Labels:      map[string]string{"team": "sec"},
			Priority:    7,
			Created:     created,
			Updated:     created.Add(time.Minute),
		},
		Spec: abac.PolicySpec{
			Enabled: enabled,
			Subjects: abac.SubjectSelector{
				Users:         []string{"alice"},
				Groups:        []string{"ops"},
				Roles:         []string{"auditor"},
				Services:      []string{"svc-a"},
				Match:         map[string]interface{}{"department": "finance", "level": float64(3)},
				Authenticated: true,
			},
			Variables: map[string]string{"root": "/secrets"},
			Rules: []abac.Rule{{
				Name:      "deny-read",
				Effect:    abac.EffectDeny,
				Priority:  2,
				Actions:   []string{"read"},
				Resources: []string{"${root}/**"},
				When:      []abac.Condition{{Expression: `subject.department == "finance"`}},
				Unless:    []abac.Condition{{Expression: `context.break_glass == true`}},
			}},
		},
	}
}

// TestRoundTrip_EnabledPresence pins that spec.enabled keeps its presence
// across the wire: unset stays unset (nil), false stays false, true stays
// true, and the rest of the document round-trips unchanged.
func TestRoundTrip_EnabledPresence(t *testing.T) {
	cases := map[string]*bool{"unset": nil, "false": boolPtr(false), "true": boolPtr(true)}
	for name, enabled := range cases {
		t.Run(name, func(t *testing.T) {
			in := fullPolicy(enabled)
			m, err := ToProto(in)
			if err != nil {
				t.Fatalf("ToProto: %v", err)
			}
			if (m.GetSpec().Enabled == nil) != (enabled == nil) {
				t.Fatalf("wire presence of enabled = %v, want %v", m.GetSpec().Enabled != nil, enabled != nil)
			}
			out, err := FromProto(m)
			if err != nil {
				t.Fatalf("FromProto: %v", err)
			}
			if !reflect.DeepEqual(in, out) {
				t.Fatalf("round trip changed the document:\n in=%+v\nout=%+v", in, out)
			}
			if out.Spec.EnabledSet() != (enabled != nil) {
				t.Fatalf("EnabledSet = %v, want %v", out.Spec.EnabledSet(), enabled != nil)
			}
			if enabled != nil && out.Spec.Enabled == enabled {
				t.Fatal("FromProto aliased the caller's flag instead of copying it")
			}
		})
	}
}

// TestToProto_EncodesWireForms pins the text forms the gRPC message carries:
// subjects.match as JSON, timestamps as RFC 3339 in UTC.
func TestToProto_EncodesWireForms(t *testing.T) {
	m, err := ToProto(fullPolicy(nil))
	if err != nil {
		t.Fatalf("ToProto: %v", err)
	}
	if got, want := m.GetSpec().GetSubjects().GetMatchJson(), `{"department":"finance","level":3}`; got != want {
		t.Fatalf("match_json = %s, want %s", got, want)
	}
	if got, want := m.GetMetadata().GetCreated(), "2026-10-21T09:30:00.123456789Z"; got != want {
		t.Fatalf("created = %s, want %s", got, want)
	}
	if got := m.GetSpec().GetRules()[0].GetEffect(); got != "deny" {
		t.Fatalf("effect = %s, want deny", got)
	}
}

// TestRoundTrip_EmptyOptionalParts pins that absent parts stay absent: no
// match criteria, no timestamps and no conditions convert to empty wire
// values and back to nil.
func TestRoundTrip_EmptyOptionalParts(t *testing.T) {
	in := fullPolicy(nil)
	in.Metadata.Created, in.Metadata.Updated = time.Time{}, time.Time{}
	in.Spec.Subjects.Match = nil
	in.Spec.Rules[0].When, in.Spec.Rules[0].Unless = nil, nil
	m, err := ToProto(in)
	if err != nil {
		t.Fatalf("ToProto: %v", err)
	}
	if m.GetMetadata().GetCreated() != "" || m.GetSpec().GetSubjects().GetMatchJson() != "" {
		t.Fatalf("empty parts encoded as %q / %q", m.GetMetadata().GetCreated(), m.GetSpec().GetSubjects().GetMatchJson())
	}
	out, err := FromProto(m)
	if err != nil {
		t.Fatalf("FromProto: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip changed the document:\n in=%+v\nout=%+v", in, out)
	}
}

// TestNilConverts pins that nil converts to nil in both directions.
func TestNilConverts(t *testing.T) {
	if m, err := ToProto(nil); m != nil || err != nil {
		t.Fatalf("ToProto(nil) = %v, %v", m, err)
	}
	if p, err := FromProto(nil); p != nil || err != nil {
		t.Fatalf("FromProto(nil) = %v, %v", p, err)
	}
}

// TestToProto_RefusesUnencodableMatch pins that a match value JSON cannot
// encode is a *MatchEncodingError.
func TestToProto_RefusesUnencodableMatch(t *testing.T) {
	in := fullPolicy(nil)
	in.Spec.Subjects.Match = map[string]interface{}{"bad": make(chan int)}
	_, err := ToProto(in)
	var me *MatchEncodingError
	if !errors.As(err, &me) || me.Unwrap() == nil {
		t.Fatalf("ToProto error = %v, want *MatchEncodingError", err)
	}
	if !strings.Contains(err.Error(), "subjects.match") {
		t.Fatalf("message %q does not name the field", err.Error())
	}
}

// TestFromProto_RefusesMalformedWire pins the typed errors for a match text
// that is not a JSON object and for a timestamp that is not RFC 3339.
func TestFromProto_RefusesMalformedWire(t *testing.T) {
	base := func() *pb.ABACPolicy {
		m, err := ToProto(fullPolicy(nil))
		if err != nil {
			t.Fatalf("ToProto: %v", err)
		}
		return m
	}
	t.Run("match not an object", func(t *testing.T) {
		m := base()
		m.Spec.Subjects.MatchJson = `["a"]`
		var me *MatchEncodingError
		if _, err := FromProto(m); !errors.As(err, &me) {
			t.Fatalf("FromProto error = %v, want *MatchEncodingError", err)
		}
	})
	for _, field := range []string{"created", "updated"} {
		t.Run("bad "+field, func(t *testing.T) {
			m := base()
			if field == "created" {
				m.Metadata.Created = "yesterday"
			} else {
				m.Metadata.Updated = "yesterday"
			}
			_, err := FromProto(m)
			var te *TimestampError
			if !errors.As(err, &te) || te.Field != field || te.Value != "yesterday" || te.Unwrap() == nil {
				t.Fatalf("FromProto error = %v, want *TimestampError on %s", err, field)
			}
			if !strings.Contains(err.Error(), "metadata."+field) {
				t.Fatalf("message %q does not name the field", err.Error())
			}
		})
	}
}

// TestFromProto_EmptyMessage pins that a message with no metadata or spec
// converts to a zero document rather than failing, leaving validation to
// the service.
func TestFromProto_EmptyMessage(t *testing.T) {
	p, err := FromProto(&pb.ABACPolicy{})
	if err != nil {
		t.Fatalf("FromProto: %v", err)
	}
	if p.Metadata.Name != "" || p.Spec.Enabled != nil || p.Spec.Rules != nil {
		t.Fatalf("empty message converted to %+v", p)
	}
}

// TestListRoundTrip pins the list conversion, including the empty list.
func TestListRoundTrip(t *testing.T) {
	in := []*abac.Policy{fullPolicy(nil), fullPolicy(boolPtr(false))}
	in[1].Metadata.Name = "second"
	resp, err := ListToProto(in)
	if err != nil {
		t.Fatalf("ListToProto: %v", err)
	}
	out, err := ListFromProto(resp)
	if err != nil {
		t.Fatalf("ListFromProto: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("list round trip changed the documents")
	}
	empty, err := ListToProto(nil)
	if err != nil || empty.Policies == nil || len(empty.Policies) != 0 {
		t.Fatalf("ListToProto(nil) = %v, %v; want an empty list", empty, err)
	}
	if got, err := ListFromProto(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("ListFromProto(nil) = %v, %v; want an empty list", got, err)
	}
}

// TestListConversionsPropagateErrors pins that one bad element fails the
// whole list in both directions.
func TestListConversionsPropagateErrors(t *testing.T) {
	bad := fullPolicy(nil)
	bad.Spec.Subjects.Match = map[string]interface{}{"bad": make(chan int)}
	if _, err := ListToProto([]*abac.Policy{fullPolicy(nil), bad}); err == nil {
		t.Fatal("ListToProto accepted an unencodable element")
	}
	resp := &pb.ABACPolicyListResponse{Policies: []*pb.ABACPolicy{{Metadata: &pb.ABACPolicyMetadata{Created: "never"}}}}
	if _, err := ListFromProto(resp); err == nil {
		t.Fatal("ListFromProto accepted a malformed element")
	}
}

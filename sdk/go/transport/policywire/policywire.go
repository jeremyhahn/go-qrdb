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

// Package policywire is the one conversion between an ABAC policy document
// (pkg/auth/abac.Policy) and its gRPC form (qrdbv1.ABACPolicy), shared by the
// gRPC and QUIC servers and the SDK's gRPC, unix and QUIC transports so the
// two ends cannot drift, and the client that issues the policy RPCs over a
// QRDBService client. REST and MCP carry abac.Policy's own JSON encoding and
// need no conversion.
//
// The conversion is lossless. spec.enabled keeps its presence: a document
// without it converts to an unset optional field and back to nil, and false
// stays false. subjects.match travels as its JSON text (RFC 8259), and the
// timestamps as RFC 3339.
package policywire

import (
	"encoding/json"
	"fmt"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
)

// MatchEncodingError reports a subjects.match value that does not encode
// to, or decode from, a JSON object.
type MatchEncodingError struct {
	Err error
}

func (e *MatchEncodingError) Error() string {
	return fmt.Sprintf("policywire: subjects.match is not a JSON object: %v", e.Err)
}

func (e *MatchEncodingError) Unwrap() error { return e.Err }

// TimestampError reports a metadata timestamp that is not RFC 3339.
type TimestampError struct {
	Field string
	Value string
	Err   error
}

func (e *TimestampError) Error() string {
	return fmt.Sprintf("policywire: metadata.%s %q is not an RFC 3339 timestamp: %v", e.Field, e.Value, e.Err)
}

func (e *TimestampError) Unwrap() error { return e.Err }

// ToProto converts a policy document to its gRPC form. A nil policy
// converts to nil.
func ToProto(p *abac.Policy) (*pb.ABACPolicy, error) {
	if p == nil {
		return nil, nil
	}
	match, err := encodeMatch(p.Spec.Subjects.Match)
	if err != nil {
		return nil, err
	}
	rules := make([]*pb.ABACRule, 0, len(p.Spec.Rules))
	for _, r := range p.Spec.Rules {
		rules = append(rules, &pb.ABACRule{
			Name:      r.Name,
			Effect:    string(r.Effect),
			Priority:  int64(r.Priority),
			Actions:   r.Actions,
			Resources: r.Resources,
			When:      conditionsToProto(r.When),
			Unless:    conditionsToProto(r.Unless),
		})
	}
	return &pb.ABACPolicy{
		ApiVersion: p.APIVersion,
		Kind:       p.Kind,
		Metadata: &pb.ABACPolicyMetadata{
			Name:        p.Metadata.Name,
			Tenant:      p.Metadata.Tenant,
			Description: p.Metadata.Description,
			Labels:      p.Metadata.Labels,
			Priority:    int64(p.Metadata.Priority),
			Created:     formatTime(p.Metadata.Created),
			Updated:     formatTime(p.Metadata.Updated),
		},
		Spec: &pb.ABACPolicySpec{
			Enabled: copyBool(p.Spec.Enabled),
			Subjects: &pb.ABACSubjectSelector{
				Users:         p.Spec.Subjects.Users,
				Groups:        p.Spec.Subjects.Groups,
				Roles:         p.Spec.Subjects.Roles,
				Services:      p.Spec.Subjects.Services,
				MatchJson:     match,
				Authenticated: p.Spec.Subjects.Authenticated,
				Any:           p.Spec.Subjects.Any,
			},
			Variables: p.Spec.Variables,
			Rules:     rules,
		},
	}, nil
}

// FromProto converts a gRPC policy to the document it carries. A nil
// message converts to nil.
func FromProto(m *pb.ABACPolicy) (*abac.Policy, error) {
	if m == nil {
		return nil, nil
	}
	md, spec, subjects := m.GetMetadata(), m.GetSpec(), m.GetSpec().GetSubjects()
	match, err := decodeMatch(subjects.GetMatchJson())
	if err != nil {
		return nil, err
	}
	created, err := parseTime("created", md.GetCreated())
	if err != nil {
		return nil, err
	}
	updated, err := parseTime("updated", md.GetUpdated())
	if err != nil {
		return nil, err
	}
	var rules []abac.Rule
	for _, r := range spec.GetRules() {
		rules = append(rules, abac.Rule{
			Name:      r.GetName(),
			Effect:    abac.Effect(r.GetEffect()),
			Priority:  int(r.GetPriority()),
			Actions:   r.GetActions(),
			Resources: r.GetResources(),
			When:      conditionsFromProto(r.GetWhen()),
			Unless:    conditionsFromProto(r.GetUnless()),
		})
	}
	return &abac.Policy{
		APIVersion: m.GetApiVersion(),
		Kind:       m.GetKind(),
		Metadata: abac.PolicyMetadata{
			Name:        md.GetName(),
			Tenant:      md.GetTenant(),
			Description: md.GetDescription(),
			Labels:      md.GetLabels(),
			Priority:    int(md.GetPriority()),
			Created:     created,
			Updated:     updated,
		},
		Spec: abac.PolicySpec{
			Enabled: specEnabled(spec),
			Subjects: abac.SubjectSelector{
				Users:         subjects.GetUsers(),
				Groups:        subjects.GetGroups(),
				Roles:         subjects.GetRoles(),
				Services:      subjects.GetServices(),
				Match:         match,
				Authenticated: subjects.GetAuthenticated(),
				Any:           subjects.GetAny(),
			},
			Variables: spec.GetVariables(),
			Rules:     rules,
		},
	}, nil
}

// ListToProto converts a list of policy documents to the gRPC list response.
func ListToProto(policies []*abac.Policy) (*pb.ABACPolicyListResponse, error) {
	out := &pb.ABACPolicyListResponse{Policies: make([]*pb.ABACPolicy, 0, len(policies))}
	for _, p := range policies {
		m, err := ToProto(p)
		if err != nil {
			return nil, err
		}
		out.Policies = append(out.Policies, m)
	}
	return out, nil
}

// ListFromProto converts the gRPC list response to the documents it carries.
func ListFromProto(resp *pb.ABACPolicyListResponse) ([]*abac.Policy, error) {
	out := make([]*abac.Policy, 0, len(resp.GetPolicies()))
	for _, m := range resp.GetPolicies() {
		p, err := FromProto(m)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func conditionsToProto(cs []abac.Condition) []*pb.ABACCondition {
	if len(cs) == 0 {
		return nil
	}
	out := make([]*pb.ABACCondition, 0, len(cs))
	for _, c := range cs {
		out = append(out, &pb.ABACCondition{Expression: c.Expression})
	}
	return out
}

func conditionsFromProto(cs []*pb.ABACCondition) []abac.Condition {
	if len(cs) == 0 {
		return nil
	}
	out := make([]abac.Condition, 0, len(cs))
	for _, c := range cs {
		out = append(out, abac.Condition{Expression: c.GetExpression()})
	}
	return out
}

// encodeMatch renders subjects.match as JSON text, empty for no criteria.
func encodeMatch(match map[string]interface{}) (string, error) {
	if len(match) == 0 {
		return "", nil
	}
	data, err := json.Marshal(match)
	if err != nil {
		return "", &MatchEncodingError{Err: err}
	}
	return string(data), nil
}

// decodeMatch parses subjects.match from JSON text, nil for empty text.
func decodeMatch(text string) (map[string]interface{}, error) {
	if text == "" {
		return nil, nil
	}
	var match map[string]interface{}
	if err := json.Unmarshal([]byte(text), &match); err != nil {
		return nil, &MatchEncodingError{Err: err}
	}
	return match, nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(field, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, &TimestampError{Field: field, Value: value, Err: err}
	}
	return t, nil
}

// specEnabled reads spec.enabled with its presence: GetEnabled collapses an
// unset field to false, so the optional pointer is read directly, nil-safe
// for a message without a spec.
func specEnabled(spec *pb.ABACPolicySpec) *bool {
	if spec == nil {
		return nil
	}
	return copyBool(spec.Enabled)
}

// copyBool copies a presence-tracked flag, keeping nil nil.
func copyBool(b *bool) *bool {
	if b == nil {
		return nil
	}
	v := *b
	return &v
}

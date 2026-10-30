// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package delegation

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func testPolicy() Policy {
	return Policy{Tenant: "acme", Principal: "billing", Scope: []string{"kv:get", "kv:put"}, TTL: 30 * time.Minute}
}

func TestPolicy_Validate(t *testing.T) {
	if err := testPolicy().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	zero := testPolicy()
	zero.TTL = 0
	if err := zero.Validate(); err != nil {
		t.Fatalf("Validate zero ttl: %v", err)
	}
	if got := zero.Lifetime(); got != DefaultLifetime {
		t.Fatalf("Lifetime = %s, want %s", got, DefaultLifetime)
	}
	for name, mutate := range map[string]func(*Policy){
		"ttl past the maximum": func(p *Policy) { p.TTL = MaxLifetime + time.Second },
		"negative ttl":         func(p *Policy) { p.TTL = -time.Second },
		"no tenant":            func(p *Policy) { p.Tenant = "" },
		"no principal":         func(p *Policy) { p.Principal = "" },
		"no scope":             func(p *Policy) { p.Scope = nil },
	} {
		p := testPolicy()
		mutate(&p)
		var pe *PolicyError
		if err := p.Validate(); !errors.As(err, &pe) {
			t.Fatalf("%s: err = %v, want *PolicyError", name, err)
		}
	}
}

func TestPolicy_BoundNarrowsNeverWidens(t *testing.T) {
	p := testPolicy()
	g, ttl, err := p.Bound(nil, 0)
	if err != nil || !slices.Equal(g.Scope, p.Scope) || ttl != p.TTL || g.Tenant != "acme" || g.Principal != "billing" {
		t.Fatalf("Bound(nil, 0) = %+v, %s, %v; want the whole policy", g, ttl, err)
	}
	g, ttl, err = p.Bound([]string{"kv:get"}, 5*time.Minute)
	if err != nil || !slices.Equal(g.Scope, []string{"kv:get"}) || ttl != 5*time.Minute {
		t.Fatalf("Bound(narrower) = %+v, %s, %v", g, ttl, err)
	}
	for name, tc := range map[string]struct {
		scope []string
		ttl   time.Duration
		field string
	}{
		"a permission the policy lacks": {[]string{"kv:delete"}, 0, "scope"},
		"longer than the policy":        {nil, p.TTL + time.Second, "ttl"},
		"negative":                      {nil, -time.Second, "ttl"},
	} {
		var pe *PolicyError
		if _, _, err := p.Bound(tc.scope, tc.ttl); !errors.As(err, &pe) || pe.Field != tc.field {
			t.Fatalf("%s: err = %v, want *PolicyError on %s", name, err, tc.field)
		}
	}
}

func TestPolicyError_Message(t *testing.T) {
	cause := errors.New("cause")
	err := &PolicyError{Field: "grant", Reason: "invalid", Err: cause}
	if !strings.Contains(err.Error(), "grant") || !strings.Contains(err.Error(), "cause") || !errors.Is(err, cause) {
		t.Fatalf("PolicyError = %q, unwrap %v", err.Error(), err.Unwrap())
	}
	if (&PolicyError{Field: "ttl", Reason: "r"}).Unwrap() != nil {
		t.Fatal("Unwrap without a cause is not nil")
	}
}

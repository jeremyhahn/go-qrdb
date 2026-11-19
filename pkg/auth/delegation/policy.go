// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package delegation

import (
	"slices"
	"time"
)

// Policy is a tenant's standing delegation policy: while it stands, the node
// mints short-lived delegation certificates naming Principal in Tenant, from
// the tenant's own login CA, on the principal's request. It is the one bound
// the "a delegation cannot renew itself" rule is relaxed within: every
// certificate minted under it carries a subset of Scope and lives at most
// TTL, and the tenant revokes the policy by deleting it.
//
// A policy is not a credential. Nothing reads it in place of the
// certificate; it only decides whether a certificate is minted, and the
// minted certificate goes through the one delegation verifier.
type Policy struct {
	// Tenant is the delegating tenant, the one whose login CA signs.
	Tenant string `json:"tenant"`

	// Principal is the embedder's service principal the policy names.
	Principal string `json:"principal"`

	// Scope is the most a minted certificate may delegate.
	Scope []string `json:"scope"`

	// TTL is the longest a minted certificate may live; zero is
	// DefaultLifetime, and more than MaxLifetime is refused.
	TTL time.Duration `json:"ttl"`
}

// PolicyError reports a policy, or a mint under one, that is refused.
type PolicyError struct {
	// Field is the policy field at fault.
	Field string

	// Reason says why.
	Reason string

	// Err is the underlying cause, if any.
	Err error
}

// Error implements error.
func (e *PolicyError) Error() string {
	msg := "delegation: policy " + e.Field + ": " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying cause.
func (e *PolicyError) Unwrap() error { return e.Err }

// Grant is the widest grant the policy permits.
func (p Policy) Grant() Grant {
	return Grant{Tenant: p.Tenant, Principal: p.Principal, Scope: slices.Clone(p.Scope)}
}

// Lifetime is the policy's TTL with the default applied.
func (p Policy) Lifetime() time.Duration {
	if p.TTL == 0 {
		return DefaultLifetime
	}
	return p.TTL
}

// Validate reports whether the policy is well formed: a valid grant and a
// lifetime no longer than MaxLifetime.
func (p Policy) Validate() error {
	if p.TTL < 0 || p.TTL > MaxLifetime {
		return &PolicyError{Field: "ttl", Reason: "must be at most " + MaxLifetime.String()}
	}
	if err := p.Grant().Validate(); err != nil {
		return &PolicyError{Field: "grant", Reason: "invalid", Err: err}
	}
	return nil
}

// Bound returns the grant and lifetime a certificate minted under the
// policy carries for a request asking for scope and ttl. An empty scope is
// the policy's whole scope and a zero ttl its whole lifetime; a scope the
// policy does not hold, or a ttl past its lifetime, is refused, so a mint
// can narrow the policy and never widen it.
func (p Policy) Bound(scope []string, ttl time.Duration) (Grant, time.Duration, error) {
	g := p.Grant()
	if len(scope) > 0 {
		for _, perm := range scope {
			if !slices.Contains(p.Scope, perm) {
				return Grant{}, 0, &PolicyError{Field: "scope", Reason: perm + " is not in the policy"}
			}
		}
		g.Scope = slices.Clone(scope)
	}
	limit := p.Lifetime()
	if ttl < 0 || ttl > limit {
		return Grant{}, 0, &PolicyError{Field: "ttl", Reason: "must be at most the policy's " + limit.String()}
	}
	if ttl == 0 {
		ttl = limit
	}
	return g, ttl, nil
}

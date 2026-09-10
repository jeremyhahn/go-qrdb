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

package ratelimit

import (
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// Registry is the single source of truth for which permission carries a
// Policy, declared once -- mirroring pkg/permission's own global registry
// (every permission exists exactly once) and pkg/auth/middleware's
// per-transport permission registries (every route/method/tool declares its
// permission exactly once, at its registration site).
//
// Keying by permission.Permission rather than by route, gRPC method, or MCP
// tool name is deliberate: several names across REST, gRPC, unix, QUIC and
// MCP already resolve to the same permission (pkg/auth/middleware's own
// registries are what resolve a transport-specific name down to one), and a
// Policy declared per name would need declaring again, identically, for
// every transport that exposes the same operation -- exactly the
// duplication go-qrdb#201 exists to remove. A Policy declared once per
// permission is enforced identically wherever that permission is checked,
// with no second declaration to drift out of step with the first.
//
// An unregistered permission is UNLIMITED, not denied. Rate limiting is a
// capacity control an operation's owner opts into when it actually needs
// one; it is not a security boundary, so its absence must never read as
// authorization's default-deny.
type Registry struct {
	mu       sync.RWMutex
	policies map[permission.Permission]Policy
}

// NewRegistry returns an empty policy registry: no permission is throttled
// until Register is called.
func NewRegistry() *Registry {
	return &Registry{policies: make(map[permission.Permission]Policy)}
}

// Register declares that perm is throttled under policy. It panics on an
// empty permission, an invalid policy (RatePerSecond or Burst <= 0), or a
// duplicate registration for the same permission: all three are
// construction-time bugs, and registering a limit an operation's owner did
// not intend to enforce (or silently overwriting one that already was
// declared) is a defect that must fail loudly at startup rather than change
// behavior for every caller of that permission from then on.
func (r *Registry) Register(perm permission.Permission, policy Policy) {
	if perm == "" {
		panic("ratelimit: cannot register a policy for an empty permission")
	}
	if policy.RatePerSecond <= 0 || policy.Burst <= 0 {
		panic("ratelimit: policy for " + perm.String() + " must have a positive rate and burst")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.policies[perm]; dup {
		panic("ratelimit: duplicate policy registration for " + perm.String())
	}
	r.policies[perm] = policy
}

// Lookup resolves the Policy declared for perm. ok is false when no Policy
// is registered, meaning perm is unlimited. A nil Registry declares nothing
// for any permission, so it always answers unlimited -- the "rate limiting
// not configured" posture, matching a nil Limiter.
func (r *Registry) Lookup(perm permission.Permission) (policy Policy, ok bool) {
	if r == nil {
		return Policy{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	policy, ok = r.policies[perm]
	return policy, ok
}

// Entries returns a copy of the registered permission -> policy map, for
// tests and startup introspection.
func (r *Registry) Entries() map[permission.Permission]Policy {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[permission.Permission]Policy, len(r.policies))
	for p, policy := range r.policies {
		out[p] = policy
	}
	return out
}

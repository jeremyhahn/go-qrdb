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

// perNodeBarrierKeysGate holds the per-node-barrier-keys activation check.
// Every serving node initialises an isolated tenant's barrier with its own key
// from the owner's signed init statement (pkg/barrierinit); a node that
// predates the feature does neither, so until every node runs a binary that
// does, no tenant may become isolated. A nil check is a node that gates
// nothing -- a standalone node, which holds only this binary -- and is active
// from the start.
type perNodeBarrierKeysGate struct {
	perNodeBarrierKeysRequired func() error
}

// admitIsolated refuses, with the check's error, to make a tenant of
// isolationType before the feature is active. A prefix tenant lives in the
// shared store under the platform's barrier and has no key of its own, so it
// is never refused.
func (g perNodeBarrierKeysGate) admitIsolated(isolationType IsolationType) error {
	if isolationType == "" || isolationType == IsolationPrefix || g.perNodeBarrierKeysRequired == nil {
		return nil
	}
	return g.perNodeBarrierKeysRequired()
}

// WithPerNodeBarrierKeysGate injects the per-node-barrier-keys activation
// check: while required returns an error (the composer's
// *featuregate.NotActiveError), creating an isolated tenant, or changing a
// tenant to an isolated type, is refused with it. Without it nothing is
// refused, which is right for a node whose groups hold no other binary.
// The node composition passes the same check to the pkg/barrierinit Book, so
// no statement is recorded before the feature is active either.
func WithPerNodeBarrierKeysGate(required func() error) TenantServiceOption {
	return func(s *TenantService) { s.perNodeBarrierKeysRequired = required }
}

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

// Package servicetest holds test doubles of pkg/service ports for the
// transport packages' tests.
package servicetest

import (
	"context"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// CompactionPolicies is an in-memory service.CompactionPolicies. It holds
// each (tenant, database)'s policy as the database's own store does: a
// policy is validated before it is kept, and an off or nil policy removes
// it, so off and never-set are one state.
//
// A server composes the real port over its registry, which writes the
// policy into the database's replicated store; a transport test that drives
// DatabaseService.UpdateCompactionForTenant without a server installs this
// one instead (DatabaseService.SetCompactionPolicies).
type CompactionPolicies struct {
	mu       sync.Mutex
	policies map[policyKey]*compaction.Policy
}

type policyKey struct{ tenant, db string }

var _ service.CompactionPolicies = (*CompactionPolicies)(nil)

// NewDatabaseService returns service.NewDatabaseService(manager, opts...)
// with an in-memory CompactionPolicies installed, so a transport test can
// drive a database's compaction policy without composing a server.
func NewDatabaseService(manager *database.Manager, opts ...service.DatabaseServiceOption) service.DatabaseServicer {
	svc := service.NewDatabaseService(manager, opts...)
	svc.(service.CompactionPolicyWiring).SetCompactionPolicies(NewCompactionPolicies())
	return svc
}

// NewCompactionPolicies returns an empty CompactionPolicies.
func NewCompactionPolicies() *CompactionPolicies {
	return &CompactionPolicies{policies: make(map[policyKey]*compaction.Policy)}
}

// SetCompactionPolicy implements service.CompactionPolicies.
func (p *CompactionPolicies) SetCompactionPolicy(_ context.Context, tenantID, db string, policy *compaction.Policy) error {
	if policy != nil {
		if err := policy.Validate(); err != nil {
			return err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	key := policyKey{tenantID, db}
	if !policy.Enabled() {
		delete(p.policies, key)
		return nil
	}
	held := *policy
	p.policies[key] = &held
	return nil
}

// CompactionPolicy implements service.CompactionPolicies.
func (p *CompactionPolicies) CompactionPolicy(_ context.Context, tenantID, db string) (*compaction.Policy, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	held, ok := p.policies[policyKey{tenantID, db}]
	if !ok {
		return nil, nil
	}
	out := *held
	return &out, nil
}

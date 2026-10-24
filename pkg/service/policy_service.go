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

package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// policyKeyPrefix is the reserved keyspace holding one tenant's ABAC policy
// documents, one record per policy name. The key carries no tenant segment:
// every record is written through the tenant's resolver-served store, which
// confines it to that tenant (docs/multi-tenancy/architecture.md).
const policyKeyPrefix = "__sys:abac_policy/"

// maxPolicyNameLen bounds a policy name, which is also a key segment.
const maxPolicyNameLen = 253

// PolicyServicer manages ABAC policy documents (pkg/auth/abac.Policy) as
// tenant-scoped records stored in qrdb. Every transport's policy endpoints
// delegate to it.
//
// A tenant argument, or a document's metadata.tenant, may be empty, meaning
// the caller's own tenant (tenant.FromContext); any other tenant is refused
// with *PolicyCrossTenantError, the system tenant's caller included.
//
// The service stores the document it is given and returns it unchanged on
// every read: it never normalises the spec. In particular spec.enabled is
// presence-tracked, so a policy written without it reads back with
// PolicySpec.Enabled nil (absent, meaning enabled) and one written with
// false reads back false. The service sets only metadata.tenant (when empty)
// and the metadata.created and metadata.updated timestamps.
type PolicyServicer interface {
	// CreatePolicy validates, compiles and stores a new policy, refusing a
	// name the tenant already holds with *PolicyExistsError. It returns
	// the stored document.
	CreatePolicy(ctx context.Context, policy *abac.Policy) (*abac.Policy, error)

	// GetPolicy returns the stored document of one policy, or
	// *PolicyNotFoundError.
	GetPolicy(ctx context.Context, tenantID, name string) (*abac.Policy, error)

	// ListPolicies returns every policy document the tenant holds, sorted
	// by name.
	ListPolicies(ctx context.Context, tenantID string) ([]*abac.Policy, error)

	// UpdatePolicy replaces an existing policy's document as a guarded
	// swap, keeping its creation time. It returns *PolicyNotFoundError when
	// the policy does not exist and *PolicyConflictError when the stored
	// document changed concurrently.
	UpdatePolicy(ctx context.Context, policy *abac.Policy) (*abac.Policy, error)

	// DeletePolicy removes one policy as a guarded delete. It returns
	// *PolicyNotFoundError when the policy does not exist and
	// *PolicyConflictError when the stored document changed concurrently.
	DeletePolicy(ctx context.Context, tenantID, name string) error
}

// PolicyService implements PolicyServicer over a tenant.StoreResolver, so
// each tenant's policies live in that tenant's own store.
type PolicyService struct {
	resolver tenant.StoreResolver
	now      func() time.Time
}

var _ PolicyServicer = (*PolicyService)(nil)

// NewPolicyService creates a PolicyService over resolver.
func NewPolicyService(resolver tenant.StoreResolver) (*PolicyService, error) {
	if resolver == nil {
		return nil, &SubsystemNotConfiguredError{Subsystem: "abac policy store resolver"}
	}
	return &PolicyService{resolver: resolver, now: func() time.Time { return time.Now().UTC() }}, nil
}

// CreatePolicy implements PolicyServicer.
func (s *PolicyService) CreatePolicy(ctx context.Context, policy *abac.Policy) (*abac.Policy, error) {
	doc, tenantID, err := s.prepare(ctx, policy)
	if err != nil {
		return nil, err
	}
	store, err := s.atomicStore(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	doc.Metadata.Created = now
	doc.Metadata.Updated = now
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, &PolicyInvalidError{Name: doc.Metadata.Name, Reason: "encode", Err: err}
	}
	stored, err := store.PutIfAbsent(ctx, policyKey(doc.Metadata.Name), data)
	if err != nil {
		return nil, err
	}
	if !stored {
		return nil, &PolicyExistsError{Tenant: tenantID, Name: doc.Metadata.Name}
	}
	return decodePolicy(tenantID, policyKey(doc.Metadata.Name), data)
}

// GetPolicy implements PolicyServicer.
func (s *PolicyService) GetPolicy(ctx context.Context, tenantID, name string) (*abac.Policy, error) {
	target, err := policyTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if err := validatePolicyName(name); err != nil {
		return nil, err
	}
	store, err := s.resolver.Resolve(ctx, target)
	if err != nil {
		return nil, err
	}
	data, err := store.Get(ctx, policyKey(name))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, &PolicyNotFoundError{Tenant: target, Name: name}
	}
	return decodePolicy(target, policyKey(name), data)
}

// ListPolicies implements PolicyServicer.
func (s *PolicyService) ListPolicies(ctx context.Context, tenantID string) ([]*abac.Policy, error) {
	target, err := policyTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	store, err := s.resolver.Resolve(ctx, target)
	if err != nil {
		return nil, err
	}
	policies := make([]*abac.Policy, 0)
	err = store.Scan(ctx, policyKeyPrefix, func(key string, value []byte) error {
		p, derr := decodePolicy(target, key, value)
		if derr != nil {
			return derr
		}
		policies = append(policies, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].Metadata.Name < policies[j].Metadata.Name })
	return policies, nil
}

// UpdatePolicy implements PolicyServicer.
func (s *PolicyService) UpdatePolicy(ctx context.Context, policy *abac.Policy) (*abac.Policy, error) {
	doc, tenantID, err := s.prepare(ctx, policy)
	if err != nil {
		return nil, err
	}
	store, err := s.atomicStore(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	key := policyKey(doc.Metadata.Name)
	current, err := store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, &PolicyNotFoundError{Tenant: tenantID, Name: doc.Metadata.Name}
	}
	existing, err := decodePolicy(tenantID, key, current)
	if err != nil {
		return nil, err
	}
	doc.Metadata.Created = existing.Metadata.Created
	doc.Metadata.Updated = s.now()
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, &PolicyInvalidError{Name: doc.Metadata.Name, Reason: "encode", Err: err}
	}
	swapped, err := store.CompareAndSwap(ctx, key, current, data)
	if err != nil {
		return nil, err
	}
	if !swapped {
		return nil, &PolicyConflictError{Tenant: tenantID, Name: doc.Metadata.Name}
	}
	return decodePolicy(tenantID, key, data)
}

// DeletePolicy implements PolicyServicer.
func (s *PolicyService) DeletePolicy(ctx context.Context, tenantID, name string) error {
	target, err := policyTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	if err := validatePolicyName(name); err != nil {
		return err
	}
	store, err := s.atomicStore(ctx, target)
	if err != nil {
		return err
	}
	key := policyKey(name)
	current, err := store.Get(ctx, key)
	if err != nil {
		return err
	}
	if current == nil {
		return &PolicyNotFoundError{Tenant: target, Name: name}
	}
	deleted, err := store.CompareAndDelete(ctx, key, current)
	if err != nil {
		return err
	}
	if !deleted {
		return &PolicyConflictError{Tenant: target, Name: name}
	}
	return nil
}

// prepare resolves the document's tenant, fills an empty metadata.tenant
// with it, and validates and compiles the document. It returns a copy: the
// caller's policy is never modified.
func (s *PolicyService) prepare(ctx context.Context, policy *abac.Policy) (*abac.Policy, string, error) {
	if policy == nil {
		return nil, "", ErrNilRequest
	}
	tenantID, err := policyTenant(ctx, policy.Metadata.Tenant)
	if err != nil {
		return nil, "", err
	}
	if err := validatePolicyName(policy.Metadata.Name); err != nil {
		return nil, "", err
	}
	doc := *policy
	doc.Metadata.Tenant = tenantID
	if _, err := abac.NewPolicyCompiler().Compile(&doc); err != nil {
		return nil, "", &PolicyInvalidError{Name: doc.Metadata.Name, Reason: "compile", Err: err}
	}
	return &doc, tenantID, nil
}

// atomicStore resolves the tenant's store and asserts the guarded-write
// capability every policy write depends on, failing closed without it.
func (s *PolicyService) atomicStore(ctx context.Context, tenantID string) (kvstore.AtomicKVStore, error) {
	store, err := s.resolver.Resolve(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	atomicStore, ok := store.(kvstore.AtomicKVStore)
	if !ok {
		return nil, &PolicyStoreUnsupportedError{Tenant: tenantID}
	}
	return atomicStore, nil
}

// policyTenant resolves the tenant a request acts on: the caller's own when
// requested is empty or names it, and a *PolicyCrossTenantError otherwise.
func policyTenant(ctx context.Context, requested string) (string, error) {
	caller := tenant.FromContext(ctx)
	if requested == "" || tenantid.Normalize(requested) == caller {
		return caller, nil
	}
	return "", &PolicyCrossTenantError{CallerTenant: caller, RequestedTenant: requested}
}

// validatePolicyName accepts a name of 1 to maxPolicyNameLen bytes drawn
// from letters, digits, '.', '_' and '-', starting with a letter or digit.
// The name is a key segment, so a '/' or any byte outside the set is refused.
func validatePolicyName(name string) error {
	if name == "" {
		return &PolicyInvalidError{Reason: "policy name is required"}
	}
	if len(name) > maxPolicyNameLen {
		return &PolicyInvalidError{Name: name, Reason: "policy name is longer than 253 bytes"}
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if alnum || i > 0 && (c == '.' || c == '_' || c == '-') {
			continue
		}
		return &PolicyInvalidError{Name: name, Reason: "policy name must be letters, digits, '.', '_' or '-', starting with a letter or digit"}
	}
	return nil
}

func policyKey(name string) string { return policyKeyPrefix + name }

// decodePolicy decodes one stored record, refusing one that does not decode
// or whose name does not match its key.
func decodePolicy(tenantID, key string, data []byte) (*abac.Policy, error) {
	var p abac.Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, &PolicyCorruptError{Tenant: tenantID, Key: key, Err: err}
	}
	if policyKey(p.Metadata.Name) != key || strings.TrimSpace(p.Metadata.Name) == "" {
		return nil, &PolicyCorruptError{Tenant: tenantID, Key: key, Err: &PolicyInvalidError{Name: p.Metadata.Name, Reason: "record name does not match its key"}}
	}
	return &p, nil
}

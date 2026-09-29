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
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TenantDataResolver is the part of Registry a TenantDataProbe needs: the
// tenant's default-database KV and MVCC services.
type TenantDataResolver interface {
	KVService(tenant, db string) (KVServicer, error)
	MVCCService(tenant, db string) (MVCCKVServicer, error)
}

// TenantDataProbe answers whether a tenant holds any stored data, for the
// tenant service's isolation-change precondition (tenant.TenantDataProbe):
// flipping isolation on a data-bearing tenant orphans that data in the old
// namespace.
type TenantDataProbe struct {
	resolver TenantDataResolver
}

var _ tenant.TenantDataProbe = (*TenantDataProbe)(nil)

// NewTenantDataProbe returns a probe over resolver, typically the node's
// Registry.
func NewTenantDataProbe(resolver TenantDataResolver) *TenantDataProbe {
	return &TenantDataProbe{resolver: resolver}
}

// TenantHasData reports whether tenantID holds any stored data at all.
//
// The probe has to see RESERVED keys as well as user keys. Relational tables
// live under the reserved __tbl:/__idx:/__seq: prefixes, and KVServicer.List
// deliberately filters those out (see FilterInternalKeys) -- so a tenant whose
// only content is relational tables lists as empty. Probing with List alone is
// what would let such a tenant flip isolation and orphan every table it owns.
//
// MVCCKVServicer.Range is the unfiltered, namespace-confined count, so it is
// used wherever it exists. Where it does NOT exist the KV list is not a weaker
// substitute but a complete answer: relational tables require the MVCC state
// machine (their catalog resolves TransactionServicer and MVCCKVServicer, and
// DDL against a non-MVCC shard fails with "statemachine: mvcc not
// configured"), so a shard that cannot serve Range cannot be holding
// relational data either. Each probe is exact for the engine it runs on;
// neither is a fallback for the other.
//
// Every error is returned, so the precondition fails CLOSED: an unreachable
// data plane rejects the isolation change rather than admitting it on an
// unverified tenant.
func (p *TenantDataProbe) TenantHasData(ctx context.Context, tenantID string) (bool, error) {
	// A nil servicer with a nil error is a legitimate resolver outcome
	// (nothing to hand back for this scope), so both are checked before the
	// call rather than the error alone.
	if mvccSvc, err := p.resolver.MVCCService(tenantID, ""); err == nil && mvccSvc != nil {
		_, count, _, rangeErr := mvccSvc.Range(ctx, nil, nil, WithCountOnly())
		if rangeErr == nil {
			return count > 0, nil
		}
		var backendErr *MVCCBackendRequiredError
		if !errors.As(rangeErr, &backendErr) {
			return false, rangeErr
		}
		// Fall through: this shard has no MVCC backend, so it can hold no
		// relational data and the KV list below is exact.
	}

	kvSvc, err := p.resolver.KVService(tenantID, "")
	if err != nil {
		return false, err
	}
	keys, err := kvSvc.List(ctx, "")
	if err != nil {
		return false, err
	}
	return len(keys) > 0, nil
}

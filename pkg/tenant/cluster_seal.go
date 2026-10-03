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

import (
	"context"
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

// This file holds the cluster sealing half of the tenant barrier: who may seal
// and unseal a tenant's barrier, and how a tenant seal reaches every node once
// the cluster has activated cluster-seal (docs/barrier-seal/architecture.md,
// "Sealing in a cluster").

// WithClusterSealer makes a tenant seal and seal vote reach every node once
// the cluster has activated cluster-seal, and makes an unseal clear the
// tenant's cluster seal record. Without it every seal is node-local.
func WithClusterSealer(s clusterseal.Sealer) TenantServiceOption {
	return func(svc *TenantService) { svc.sealer = s }
}

// enforceTenantOwner refuses every caller but the target tenant itself: a
// tenant's barrier is sealed, unsealed and voted on by that tenant's own
// administrators, and by no one else -- the platform operator included.
// Unlike enforceTenantScope it has no system-tenant lift.
func enforceTenantOwner(ctx context.Context, targetTenantID, op string) error {
	if caller := FromContext(ctx); caller == targetTenantID {
		return nil
	}
	return &TenantScopeError{
		CallerTenantID: FromContext(ctx),
		TargetTenantID: targetTenantID,
		Op:             op,
	}
}

// clearClusterSeal ends a cluster seal of tenantID's barrier after this node
// has unsealed it. If the clear record cannot be written the barrier is
// sealed again here, so this node never serves a tenant whose record still
// says sealed.
func (s *TenantService) clearClusterSeal(ctx context.Context, tenantID string) error {
	b := clusterseal.TenantBarrier(tenantID)
	if !clusterseal.ActiveIn(s.sealer) {
		return nil
	}
	if _, err := s.sealer.Clear(ctx, b, clusterseal.PrincipalFrom(ctx)); err != nil {
		if serr := s.registry.SealTenant(tenantID); serr != nil {
			s.logger.Error("tenant barrier could not be sealed again after a failed cluster unseal",
				slog.String("tenant_id", tenantID), slog.Any("error", serr))
		}
		return &BarrierError{TenantID: tenantID, Op: "clear-cluster-seal", Err: err}
	}
	return nil
}

// sealTenant seals tenantID's barrier: cluster-wide through the sealer when
// active, on this node only otherwise.
func (s *TenantService) sealTenant(ctx context.Context, tenantID string) error {
	if !clusterseal.ActiveIn(s.sealer) {
		if err := s.registry.SealTenant(tenantID); err != nil {
			return err
		}
		// A node-local seal ends the tenant's open streams and SQL sessions,
		// as a cluster seal does -- after the barrier is sealed, so a stream
		// opening between the two finds it refused.
		if streams := clusterseal.StreamsOf(s.sealer); streams != nil {
			streams.EndTenant(tenantID, &clusterseal.SealedError{Barrier: clusterseal.TenantBarrier(tenantID)})
		}
		return nil
	}
	_, err := s.sealer.Seal(ctx, clusterseal.TenantBarrier(tenantID), clusterseal.PrincipalFrom(ctx))
	return err
}

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

package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// tenantRuntimeCloser stops what this node runs for one tenant -- its
// IsolationVolume host and its IsolationRaft database shards -- so the
// deleted-tenant reconcile can remove the directories under them. It closes;
// it never purges: which directories may go is the reconcile's decision.
type tenantRuntimeCloser struct {
	hosts *TenantHostRegistry
	raft  *RaftBackendFactory
}

// Compile-time check.
var _ tenant.TenantRuntimeCloser = (*tenantRuntimeCloser)(nil)

// NewTenantRuntimeCloser returns the tenant.TenantRuntimeCloser over this
// node's per-tenant runtimes. Either registry may be nil.
func NewTenantRuntimeCloser(hosts *TenantHostRegistry, raft *RaftBackendFactory) tenant.TenantRuntimeCloser {
	return &tenantRuntimeCloser{hosts: hosts, raft: raft}
}

// CloseTenantRuntime closes the tenant's host and every dedicated shard it has
// started here. A tenant with nothing running is the success case.
func (c *tenantRuntimeCloser) CloseTenantRuntime(_ context.Context, tenantID string) error {
	if c.hosts != nil {
		if err := c.hosts.Close(tenantID); err != nil {
			return err
		}
	}
	if c.raft != nil {
		for _, s := range c.raft.ListStarted() {
			if s.TenantID != tenantID {
				continue
			}
			if err := c.raft.Close(tenantID, s.Database); err != nil {
				return err
			}
		}
	}
	return nil
}

// WireTenantRuntimeCloser gives svc the runtime closer over hosts and raft, so
// its ReconcileDeletedTenants stops a retired tenant's runtimes before
// removing their directories. A service without the wiring surface is left
// alone. A composition that rebuilds its tenant service in place calls it
// again for each new service.
func WireTenantRuntimeCloser(svc tenant.TenantServicer, hosts *TenantHostRegistry, raft *RaftBackendFactory) {
	if w, ok := svc.(tenant.ResidueWiring); ok {
		w.SetTenantRuntimeCloser(NewTenantRuntimeCloser(hosts, raft))
	}
}

// reconcileDeletedTenantsAtBoot is the boot-time half of the deleted-tenant
// reconcile. It runs BEFORE any per-tenant barrier is restored, so a node that
// missed a delete -- down for maintenance, or not hosting the shard -- reclaims
// the retired tenant's directories and forgets its barrier instead of bringing
// it back.
//
// It does not fail the boot. What it reclaims is disk, never correctness: a
// retired id resolves for nobody whether or not its bytes are still here, and
// the same pass is available on demand as the operator verb. A failure is
// logged with the step that failed, so the operator knows to run it; a pass
// that failed only for some ids still logs what it reclaimed for the rest.
func reconcileDeletedTenantsAtBoot(ctx context.Context, svc tenant.TenantServicer, logger *slog.Logger) {
	report, err := whenReadIndexReady(ctx, func() (*tenant.ReconcileReport, error) {
		return svc.ReconcileDeletedTenants(ctx)
	})
	if err != nil {
		logger.Warn("start: deleted-tenant reconcile failed; run the reconcile-deleted-tenants verb to reclaim the residue",
			slog.Any("error", err))
	}
	// A pass that failed for some ids still reports what it did for the rest.
	if report != nil && (len(report.Reclaimed) > 0 || len(report.Skipped) > 0) {
		logger.Info("start: deleted-tenant reconcile",
			slog.Int("inspected", report.Inspected),
			slog.Int("reclaimed", len(report.Reclaimed)),
			slog.Int("skipped", len(report.Skipped)))
	}
}

// whenReadIndexReady runs read, retrying only while a freshly elected leader
// is still inside its ReadIndex warm-up window (quicraft.ErrReadIndexNotReady),
// which resolves as soon as its first entry of the term commits. Any other
// error, and the transient outlasting the deadline, is returned.
func whenReadIndexReady[T any](ctx context.Context, read func() (T, error)) (T, error) {
	const (
		readyDeadline = 15 * time.Second
		retryInterval = 100 * time.Millisecond
	)
	deadline := time.Now().Add(readyDeadline)
	for {
		v, err := read()
		if err == nil || !errors.Is(err, quicraft.ErrReadIndexNotReady) || time.Now().After(deadline) {
			return v, err
		}
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-time.After(retryInterval):
		}
	}
}

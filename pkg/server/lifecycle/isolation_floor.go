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

package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// The cluster isolation-policy floor is applied by every composer that
// serves tenants -- qrdbd and the SDK node alike -- through the helpers in
// this file, so no node serves under a floor weaker than its configuration
// asks for, and no FIPS-140 node serves under a floor that is not strict.

var (
	// isolationFloorWait bounds how long a start waits for the configured
	// isolation-policy floor before serving; past it the floor is applied in
	// the background.
	isolationFloorWait = 30 * time.Second
	// isolationFloorRetry is the pause between attempts to apply it.
	isolationFloorRetry = 250 * time.Millisecond
)

// ErrFIPSFloorNotStrict refuses a FIPS-140 start whose cluster-wide
// isolation-policy floor is not confirmed strict.
var ErrFIPSFloorNotStrict = errors.New("tenant isolation: the cluster isolation floor is not strict under FIPS-140; refusing to serve")

// clusterIsolationFloor reads and sets the tenant service's cluster-wide
// isolation-policy floor (pkg/tenant/isolation_policy.go). Both tenant
// services implement it.
type clusterIsolationFloor interface {
	tenant.ClusterIsolationPolicyReader
	SetClusterIsolationPolicy(ctx context.Context, next tenant.ClusterIsolationPolicy) error
}

// ApplyIsolationFloor applies tenant.isolation_policy, want, to the cluster
// floor before the node serves, so no tenant is created on this node under a
// floor weaker than its configuration asks for.
//
// Every node applies it. Setting the value the floor already holds is a read
// and nothing else, so a follower whose write the system shard refuses keeps
// trying until it sees the leader's write replicated, and returns then. A
// refused transition -- the floor is tighten-only, and cannot become strict
// over tenants that are not -- is final: it is logged and the stored floor
// stands. When the wait runs out, the remaining attempts continue in the
// background until ctx ends. A tenant service that holds no floor is logged
// and left alone.
//
// Like every isolation-policy verb on every transport, the floor waits for
// the strict-isolation activation check (tenant.RequireStrictIsolation): a
// clustered node writes it only once strict isolation and guarded writes are
// both active on group 0, so a configured tightening never commits while the
// write guards are dropped. Until then the write is retried like a transient
// failure -- activation is permanent -- and a floor that already holds want
// needs no write. A standalone node is active from the start.
func ApplyIsolationFloor(ctx context.Context, svc tenant.TenantServicer, want string) {
	if want == "" {
		return
	}
	setter, ok := svc.(clusterIsolationFloor)
	if !ok {
		slog.Warn("tenant.isolation_policy not applied: the tenant service holds no cluster isolation floor",
			slog.String("isolation_policy", want))
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, isolationFloorWait)
	defer cancel()
	if setIsolationFloor(waitCtx, setter, tenant.ClusterIsolationPolicy(want)) {
		return
	}
	if ctx.Err() != nil {
		return
	}
	slog.Warn("tenant.isolation_policy not applied yet; still trying in the background",
		slog.String("isolation_policy", want))
	go setIsolationFloor(ctx, setter, tenant.ClusterIsolationPolicy(want))
}

// setIsolationFloor tries to set want until it is set or refused, reporting
// true, or until ctx ends, reporting false. Each write attempt waits for the
// strict-isolation activation check; a floor that already holds want is
// done without one.
func setIsolationFloor(ctx context.Context, setter clusterIsolationFloor, want tenant.ClusterIsolationPolicy) bool {
	loggedInactive := false
	for {
		var current tenant.ClusterIsolationPolicy
		if inactive := tenant.RequireStrictIsolation(setter); inactive != nil {
			if current, _ = setter.ClusterIsolationPolicy(ctx); current == want {
				return true
			}
			if !loggedInactive {
				slog.Info("tenant.isolation_policy waits for the strict-isolation and guarded-writes features; retrying",
					slog.String("isolation_policy", string(want)), slog.Any("error", inactive))
				loggedInactive = true
			}
			select {
			case <-ctx.Done():
				return false
			case <-time.After(isolationFloorRetry):
			}
			continue
		}
		err := setter.SetClusterIsolationPolicy(ctx, want)
		if err != nil {
			// A concurrent setter -- another node applying the same
			// configuration -- wins the race as a refused transition; what
			// the floor holds afterwards says whether this one was refused.
			current, _ = setter.ClusterIsolationPolicy(ctx)
			if current == want {
				err = nil
			}
		}
		if err == nil {
			slog.Info("cluster isolation-policy floor applied", slog.String("isolation_policy", string(want)))
			return true
		}
		if isolationFloorRefused(err, current) {
			slog.Warn("tenant.isolation_policy refused; the stored cluster floor stands",
				slog.String("isolation_policy", string(want)), slog.Any("error", err))
			return true
		}
		slog.Debug("cluster isolation-policy floor not applied; retrying", slog.Any("error", err))
		select {
		case <-ctx.Done():
			return false
		case <-time.After(isolationFloorRetry):
		}
	}
}

// isolationFloorRefused reports whether err, with the floor now reading
// current, is a final refusal of the requested floor rather than a lost race
// or a transient failure to write it. A refused transition is final only
// once the floor is strict, the terminal value; before that it is a race a
// retry resolves.
func isolationFloorRefused(err error, current tenant.ClusterIsolationPolicy) bool {
	var (
		change  *tenant.IsolationPolicyChangeError
		invalid *tenant.InvalidIsolationPolicyError
	)
	if errors.As(err, &change) {
		return current == tenant.ClusterIsolationPolicyStrict
	}
	return errors.As(err, &invalid)
}

// ApplyFIPSIsolationFloor tightens the cluster isolation floor to strict
// on a FIPS-140 node and reads it back. The write may lose to a peer that
// already moved the floor, so only the read decides: a floor that is not
// strict afterwards -- the tightening was refused (a governed tenant's
// databases still on the node-keyed WAL, or no way to verify them), the
// floor cannot be read, or the tenant service holds no floor -- refuses
// the start (ErrFIPSFloorNotStrict), naming the cause. With FIPS off it
// does nothing.
func ApplyFIPSIsolationFloor(ctx context.Context, fipsOn bool, svc any) error {
	if !fipsOn {
		return nil
	}
	floor, ok := svc.(clusterIsolationFloor)
	if !ok {
		return fmt.Errorf("%w: the tenant service (%T) holds no cluster isolation floor", ErrFIPSFloorNotStrict, svc)
	}
	setErr := floor.SetClusterIsolationPolicy(ctx, tenant.ClusterIsolationPolicyStrict)
	got, err := floor.ClusterIsolationPolicy(ctx)
	if err != nil {
		if setErr != nil {
			return fmt.Errorf("%w: reading the floor back: %w (write: %w)", ErrFIPSFloorNotStrict, err, setErr)
		}
		return fmt.Errorf("%w: reading the floor back: %w", ErrFIPSFloorNotStrict, err)
	}
	if got != tenant.ClusterIsolationPolicyStrict {
		if setErr != nil {
			return fmt.Errorf("%w: the floor is %q: %w", ErrFIPSFloorNotStrict, got, setErr)
		}
		return fmt.Errorf("%w: the floor is %q after the write", ErrFIPSFloorNotStrict, got)
	}
	if setErr != nil {
		slog.Info("tenant isolation: the cluster floor was already strict under FIPS-140", slog.Any("write_error", setErr))
	}
	return nil
}

// AttachTenantHomesThenApplyFIPSFloor attaches the isolated tenants' home
// stores (attach) and only then applies the FIPS-140 isolation floor
// (ApplyFIPSIsolationFloor): tightening to strict probes the node-key log
// of each isolated tenant, whose catalog resolves through its home store,
// so a floor applied before the homes are attached fails for a valid
// tenant. It returns attach's result and the floor's error.
func AttachTenantHomesThenApplyFIPSFloor(ctx context.Context, fipsOn bool, attach func() bool, svc any) (bool, error) {
	attached := attach()
	return attached, ApplyFIPSIsolationFloor(ctx, fipsOn, svc)
}

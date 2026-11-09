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

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

var (
	// isolationFloorWait bounds how long start waits for the configured
	// isolation-policy floor before serving; past it the floor is applied in
	// the background.
	isolationFloorWait = 30 * time.Second
	// isolationFloorRetry is the pause between attempts to apply it.
	isolationFloorRetry = 250 * time.Millisecond
)

// isolationFloorSetter reads and sets the cluster isolation-policy floor.
// Both tenant services implement it.
type isolationFloorSetter interface {
	tenant.ClusterIsolationPolicyReader
	SetClusterIsolationPolicy(ctx context.Context, next tenant.ClusterIsolationPolicy) error
}

// applyIsolationFloor applies tenant.isolation_policy, want, to the cluster
// floor before the node serves, so no tenant is created on this node under a
// floor weaker than its configuration asks for.
//
// Every node applies it. Setting the value the floor already holds is a read
// and nothing else, so a follower whose write the system shard refuses keeps
// trying until it sees the leader's write replicated, and returns then. A
// refused transition -- the floor is tighten-only, and cannot become strict
// over tenants that are not -- is final: it is logged and the stored floor
// stands. When the wait runs out, the remaining attempts continue in the
// background until ctx ends.
//
// The configuration goes through the strict-isolation gate the
// isolation-policy verbs do, failing closed: until the feature is active a
// non-empty want is a startup error naming featuregate.StrictIsolation, and
// the floor is not touched. The gate is re-read within the wait, so a node
// whose replicated activation has not been applied yet at boot sees it once
// it is.
func applyIsolationFloor(ctx context.Context, svc tenant.TenantServicer, want string) error {
	if want == "" {
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, isolationFloorWait)
	defer cancel()
	if err := awaitStrictIsolation(waitCtx, svc); err != nil {
		return fmt.Errorf("tenant.isolation_policy %q: %s is not active: %w", want, featuregate.StrictIsolation, err)
	}
	if setIsolationFloor(waitCtx, svc, tenant.ClusterIsolationPolicy(want)) {
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	slog.Warn("tenant.isolation_policy not applied yet; still trying in the background",
		slog.String("isolation_policy", want))
	go setIsolationFloor(ctx, svc, tenant.ClusterIsolationPolicy(want))
	return nil
}

// awaitStrictIsolation re-reads the strict-isolation gate of svc until it
// passes, returning nil, or ctx ends, returning the gate's last refusal.
func awaitStrictIsolation(ctx context.Context, svc tenant.TenantServicer) error {
	for {
		err := featuregate.RequireStrictIsolation(svc)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(isolationFloorRetry):
		}
	}
}

// setIsolationFloor tries to set want until it is set or refused, reporting
// true, or until ctx ends, reporting false.
func setIsolationFloor(ctx context.Context, setter isolationFloorSetter, want tenant.ClusterIsolationPolicy) bool {
	for {
		err := setter.SetClusterIsolationPolicy(ctx, want)
		var current tenant.ClusterIsolationPolicy
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

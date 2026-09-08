// Copyright 2026 Jeremy Hahn
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

package embedded

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// MissingVolumePoolManagerError is returned when VolumePoolClient is
// invoked without an injected pool.Manager.
type MissingVolumePoolManagerError struct{}

// Error returns a human-readable description.
func (e *MissingVolumePoolManagerError) Error() string {
	return "embedded: volume pool manager is not configured"
}

// MissingVolumePoolStateStoreError is returned when RegisterPool is
// invoked without an injected pool.StateStore (the round-robin cursor
// strategy requires one).
type MissingVolumePoolStateStoreError struct{}

// Error returns a human-readable description.
func (e *MissingVolumePoolStateStoreError) Error() string {
	return "embedded: volume pool state store is not configured"
}

// volumePoolClient is the embedded implementation of
// transport.VolumePoolClient. It wraps an objstore pool.Manager and a
// pool.StateStore so RegisterPool can construct strategies that
// persist their cursor across daemon restarts.
type volumePoolClient struct {
	transport *Transport
}

// Compile-time interface assertion.
var _ transport.VolumePoolClient = (*volumePoolClient)(nil)

// poolToTransport projects an objstore Pool into the SDK projection.
func poolToTransport(p objpool.Pool) transport.VolumePool {
	out := transport.VolumePool{
		Name:         p.Name,
		BackendCount: len(p.Candidates),
	}
	if p.Strategy != nil {
		out.Strategy = p.Strategy.Name()
	}
	for _, c := range p.Candidates {
		if accessor, ok := c.(common.PathAccessor); ok {
			path := accessor.LocalPath()
			if path != "" {
				out.LocalPaths = append(out.LocalPaths, path)
			}
		}
	}
	return out
}

// List returns every registered pool sorted by name.
func (c *volumePoolClient) List(ctx context.Context) ([]transport.VolumePool, error) {
	if c.transport.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if c.transport.poolManager == nil {
		return nil, &MissingVolumePoolManagerError{}
	}
	pools, err := c.transport.poolManager.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]transport.VolumePool, len(pools))
	for i, p := range pools {
		out[i] = poolToTransport(p)
	}
	return out, nil
}

// Pick returns one backend from the named pool.
func (c *volumePoolClient) Pick(ctx context.Context, poolName string, hint transport.VolumePoolHint) (*transport.VolumePoolBackend, error) {
	if c.transport.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if c.transport.poolManager == nil {
		return nil, &MissingVolumePoolManagerError{}
	}
	picked, err := c.transport.poolManager.Pick(ctx, poolName, objpool.Hint{
		TenantID: hint.TenantID,
		SizeHint: hint.SizeHint,
		Region:   hint.Region,
	})
	if err != nil {
		return nil, mapPoolError(poolName, err)
	}
	out := &transport.VolumePoolBackend{PoolName: poolName}
	if accessor, ok := picked.(common.PathAccessor); ok {
		out.LocalPath = accessor.LocalPath()
	} else {
		return nil, &transport.PoolBackendNotLocalError{Name: poolName}
	}
	return out, nil
}

// RegisterPool registers a new pool. The strategy defaults to
// round_robin when omitted; least_utilization is recognised but its
// concrete implementation returns ErrStrategyNotImplemented at Pick
// time.
//
// Each path is wrapped in an objstore local-filesystem Storage and
// added as a candidate. The path is created with 0700 permissions if
// it does not exist — matching the daemon's normal volume-pool
// initialisation.
func (c *volumePoolClient) RegisterPool(ctx context.Context, req transport.RegisterVolumePoolRequest) error {
	if c.transport.closed.Load() {
		return &transport.ClosedError{}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.transport.poolManager == nil {
		return &MissingVolumePoolManagerError{}
	}
	if req.Name == "" {
		return &objpool.InvalidPoolNameError{}
	}

	strategy, err := buildPoolStrategy(req.Strategy, c.transport.poolStateStore)
	if err != nil {
		return err
	}

	candidates, err := buildLocalCandidates(req.Name, req.Paths)
	if err != nil {
		return err
	}

	if err := c.transport.poolManager.Register(req.Name, candidates, strategy); err != nil {
		return mapPoolError(req.Name, err)
	}
	return nil
}

// buildPoolStrategy constructs a pool.Strategy from the SDK request.
// Empty / "round_robin" yields the production strategy; "least_utilization"
// yields the stub today.
func buildPoolStrategy(name string, state objpool.StateStore) (objpool.Strategy, error) {
	switch name {
	case "", "round_robin":
		if state == nil {
			return nil, &MissingVolumePoolStateStoreError{}
		}
		return &objpool.RoundRobinStrategy{State: state}, nil
	case "least_utilization":
		return &objpool.LeastUtilizationStrategy{}, nil
	default:
		return nil, &transport.InvalidPoolStrategyError{Strategy: name}
	}
}

// buildLocalCandidates delegates to pkg/storage/pool.BuildLocalCandidates,
// which validates every path before creating it (#292).
//
// It used to be a second implementation of that function, and the two had
// already drifted: this one silently SKIPPED an empty path while the canonical
// one did not. #292 asked for one shared guard and specifically for it not to
// become a third copy, so the duplicate is gone rather than guarded.
//
// The empty-path skip goes with it, deliberately. A pool spec carrying "" is a
// malformed request, and answering it with a typed error beats registering a
// pool with fewer volumes than the caller asked for and saying nothing.
func buildLocalCandidates(name string, paths []string) ([]common.Storage, error) {
	return qrdbpool.BuildLocalCandidates(name, paths)
}

// mapPoolError translates objstore pool errors into SDK transport
// equivalents. Unknown errors pass through.
func mapPoolError(name string, err error) error {
	if err == nil {
		return nil
	}
	var notFound *objpool.PoolNotFoundError
	if errors.As(err, &notFound) {
		return &transport.PoolNotConfiguredError{Name: notFound.Name}
	}
	var empty *objpool.EmptyPoolError
	if errors.As(err, &empty) {
		return &transport.PoolEmptyError{Name: empty.Name}
	}
	var dup *objpool.DuplicatePoolError
	if errors.As(err, &dup) {
		return &transport.PoolAlreadyRegisteredError{Name: dup.Name}
	}
	var nonLocal *objpool.NonLocalCandidateError
	if errors.As(err, &nonLocal) {
		return &transport.PoolBackendNotLocalError{Name: nonLocal.Pool}
	}
	return err
}

// VolumePoolClient returns the embedded volume-pool client projection.
func (t *Transport) VolumePoolClient() transport.VolumePoolClient {
	return &volumePoolClient{transport: t}
}

// WithVolumePoolManager injects the objstore pool.Manager into the
// embedded transport. The manager is consumed by VolumePoolClient.
func WithVolumePoolManager(mgr objpool.Manager) Option {
	return func(t *Transport) {
		t.poolManager = mgr
	}
}

// WithVolumePoolStateStore injects the objstore pool.StateStore into
// the embedded transport. The state store is consumed by RegisterPool
// to construct the round-robin cursor strategy.
func WithVolumePoolStateStore(s objpool.StateStore) Option {
	return func(t *Transport) {
		t.poolStateStore = s
	}
}

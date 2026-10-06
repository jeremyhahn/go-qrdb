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

package node

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// This file mirrors cmd/qrdbd/volume_pool.go: the node package's equivalent
// composition of the volume-pools feature (docs/volume-pools), duplicated
// rather than shared because cmd/qrdbd (package main, the daemon module) and
// sdk/go (its own module) cannot import one another. Keep the two in step.

// newVolumeRegistry builds a volume.Registry pre-populated with the "dir"
// provider, plus every extra provider supplied via WithVolumeProvider. A
// hardware-backed provider (e.g. go-xkms's "luks2") reaches here that way;
// the SDK node itself ships no hardware provider (layer ownership, see
// CLAUDE.md). A registration failure (a duplicate Provider.Name(), or a
// nil) is returned rather than silently dropped.
func newVolumeRegistry(extra ...volume.Provider) (volume.Registry, error) {
	registry := volume.NewRegistry()
	for _, p := range extra {
		if err := registry.Register(p); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// openedVolumePool is one configured pool entry after this node has tried
// to open it: either a live Volume, or the error that kept it from opening.
// A node health report (volume.NodeReport) carries one PoolOnNode row per
// entry regardless of which case this is -- invariant 6: a pool that
// fails to open refuses only its OWN tenants on this node, never the
// node's boot.
type openedVolumePool struct {
	name string
	// provider is the provider name THIS NODE's own configuration
	// selected for name (defaulting to "dir" when unset), regardless of
	// whether Open succeeded. checkProviderConsistency compares it
	// against the catalog's own record once the catalog is available.
	provider string
	vol      volume.Volume
	err      error
}

// openConfiguredVolumes opens every pool this node's configuration maps
// (storage.volume_pools) through registry, and returns the populated
// volume.OpenVolumes (satisfying seal.VolumeResolver) plus one
// openedVolumePool row per configured entry, in configuration order,
// including the ones that failed to open.
func openConfiguredVolumes(ctx context.Context, nodeID string, pools []qrdbconfig.VolumePoolConfig, registry volume.Registry, logger *slog.Logger) (*volume.OpenVolumes, []openedVolumePool) {
	open := volume.NewOpenVolumes(nodeID)
	rows := make([]openedVolumePool, 0, len(pools))
	for _, p := range pools {
		providerName := p.Provider
		if providerName == "" {
			providerName = "dir"
		}
		provider, err := registry.Lookup(providerName)
		if err != nil {
			logger.Warn("volume pool: provider not registered; pool will not be served on this node",
				slog.String("pool", p.Name), slog.String("provider", providerName), slog.Any("error", err))
			rows = append(rows, openedVolumePool{name: p.Name, provider: providerName, err: err})
			continue
		}
		vol, err := provider.Open(ctx, volume.Spec{Pool: p.Name, NodeID: nodeID, Path: p.Path, Options: p.Options})
		if err != nil {
			logger.Warn("volume pool: failed to open volume; pool will not be served on this node",
				slog.String("pool", p.Name), slog.Any("error", err))
			rows = append(rows, openedVolumePool{name: p.Name, provider: providerName, err: err})
			continue
		}
		if err := open.Add(p.Name, vol); err != nil {
			logger.Warn("volume pool: failed to register opened volume",
				slog.String("pool", p.Name), slog.Any("error", err))
			rows = append(rows, openedVolumePool{name: p.Name, provider: providerName, err: err})
			continue
		}
		rows = append(rows, openedVolumePool{name: p.Name, provider: providerName, vol: vol})
	}
	return open, rows
}

// raftAddrToNodeIDMap inverts a NodeID->address member map (as passed to
// NewClusteredNode) into the address->decimal-id-string shape
// lifecycle.Infra.RaftAddrToNodeID wants (the same string form
// volume.NodeReport.NodeID carries) -- mirrors cmd/qrdbd's identically-named
// helper, which inverts its own CLI-string-parsed map; the two cannot share
// code (separate Go modules).
func raftAddrToNodeIDMap(members map[uint64]string) map[string]string {
	out := make(map[string]string, len(members))
	for id, addr := range members {
		out[addr] = strconv.FormatUint(id, 10)
	}
	return out
}

// checkProviderConsistency compares each successfully opened row's local
// provider against the catalog's own record for that pool name (set at
// CreatePool time), once the catalog is available (it is NOT at the time
// openConfiguredVolumes itself runs). A mismatch (security review finding
// 7) means this node would otherwise silently serve the pool's tenants
// under the wrong storage semantics -- most seriously, plaintext "dir"
// where every other node in the cluster serves encrypted "luks2" -- so
// the mismatched row's Volume is closed and removed from open, and a
// *volume.ProviderMismatchError is returned for it (invariant 6: only
// that pool's tenants on this node are refused, never the whole node's
// boot). Mirrors pkg/server/lifecycle.CheckProviderConsistency.
func checkProviderConsistency(ctx context.Context, catalog volume.Service, open *volume.OpenVolumes, rows []openedVolumePool) []error {
	var errs []error
	for _, row := range rows {
		if row.vol == nil {
			continue
		}
		detail, err := catalog.GetPool(ctx, row.name)
		if err != nil {
			continue
		}
		if detail.Provider == row.provider {
			continue
		}
		open.Remove(row.name)
		_ = row.vol.Close(ctx)
		errs = append(errs, &volume.ProviderMismatchError{
			Pool:               row.name,
			CatalogProvider:    detail.Provider,
			ConfiguredProvider: row.provider,
		})
	}
	return errs
}

// closeOpenedVolumes closes every successfully opened volume in rows,
// logging (not failing on) a close error since this runs during shutdown.
func closeOpenedVolumes(ctx context.Context, rows []openedVolumePool, logger *slog.Logger) {
	for _, row := range rows {
		if row.vol == nil {
			continue
		}
		if err := row.vol.Close(ctx); err != nil {
			logger.Warn("volume pool: failed to close volume",
				slog.String("pool", row.name), slog.Any("error", err))
		}
	}
}

// buildNodeReport constructs this node's volume.NodeReport from rows,
// querying each opened Volume's live Usage and Health; a row that failed
// to open reports Mapped=true (the node's configuration DOES map it) with
// a Health built from its open error so the catalog's health view shows
// why, never a path or device (invariant: "no replicated record names a
// filesystem path").
func buildNodeReport(ctx context.Context, nodeID string, rows []openedVolumePool) volume.NodeReport {
	pools := make(map[string]volume.PoolOnNode, len(rows))
	for _, row := range rows {
		if row.vol == nil {
			pools[row.name] = volume.PoolOnNode{
				Mapped: true,
				Health: volume.Health{
					State:     volume.StateUnavailable,
					Detail:    row.err.Error(),
					CheckedAt: time.Now(),
				},
			}
			continue
		}
		usage, uerr := row.vol.Usage(ctx)
		health := row.vol.Health(ctx)
		if uerr != nil && health.Detail == "" {
			health.Detail = uerr.Error()
		}
		pools[row.name] = volume.PoolOnNode{Mapped: true, Usage: usage, Health: health}
	}
	return volume.NodeReport{NodeID: nodeID, Pools: pools}
}

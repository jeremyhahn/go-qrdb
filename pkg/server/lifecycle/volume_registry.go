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
	"log/slog"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// NewVolumeRegistry builds a volume.Registry pre-populated with the "dir"
// provider, plus every extra provider supplied (e.g. go-xkms's "luks2").
// This is the ONE place in the main module that builds a volume.Registry;
// cmd/qrdbd delegates to it so a composer that builds its own daemon
// directly over pkg/server (bypassing cmd/qrdbd's binary entirely) gets
// the identical mechanism -- the main-module half of the WithVolumeProvider
// seam (sdk/go/node, a separate module, carries its own copy; see that
// package's doc comment for why the two cannot share this function).
// A registration failure (a duplicate Provider.Name(), or a nil) is
// returned rather than silently dropped.
func NewVolumeRegistry(extra ...volume.Provider) (volume.Registry, error) {
	registry := volume.NewRegistry()
	for _, p := range extra {
		if err := registry.Register(p); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// OpenedVolumePool is one configured pool entry after this node has tried
// to open it: either a live Volume, or the error that kept it from
// opening. A node health report (volume.NodeReport) carries one PoolOnNode
// row per entry regardless of which case this is -- invariant 6: a pool
// that fails to open refuses only its OWN tenants on this node, never the
// node's boot.
type OpenedVolumePool struct {
	Name string
	// Provider is the provider name THIS NODE's own configuration
	// selected for Name (defaulting to "dir" when unset), regardless of
	// whether Open succeeded. CheckProviderConsistency compares it
	// against the catalog's own record once the catalog is available.
	Provider string
	Vol      volume.Volume
	Err      error
}

// OpenConfiguredVolumes opens every pool this node's configuration maps
// (storage.volume_pools) through registry, and returns the populated
// volume.OpenVolumes (satisfying seal.VolumeResolver) plus one
// OpenedVolumePool row per configured entry, in configuration order,
// including the ones that failed to open.
func OpenConfiguredVolumes(ctx context.Context, nodeID string, pools []qrdbconfig.VolumePoolConfig, registry volume.Registry, logger *slog.Logger) (*volume.OpenVolumes, []OpenedVolumePool) {
	open := volume.NewOpenVolumes(nodeID)
	rows := make([]OpenedVolumePool, 0, len(pools))
	for _, p := range pools {
		providerName := p.Provider
		if providerName == "" {
			providerName = "dir"
		}
		provider, err := registry.Lookup(providerName)
		if err != nil {
			logger.Error("volume pool: provider not registered; pool will not be served on this node",
				slog.String("pool", p.Name), slog.String("provider", providerName), slog.Any("error", err))
			volume.RecordPoolHealth(p.Name, volume.Health{State: volume.StateUnavailable})
			rows = append(rows, OpenedVolumePool{Name: p.Name, Provider: providerName, Err: err})
			continue
		}
		vol, err := provider.Open(ctx, volume.Spec{Pool: p.Name, NodeID: nodeID, Path: p.Path, Options: p.Options})
		if err != nil {
			logger.Error("volume pool: failed to open volume; pool will not be served on this node",
				slog.String("pool", p.Name), slog.Any("error", err))
			volume.RecordPoolHealth(p.Name, volume.Health{State: volume.StateUnavailable})
			rows = append(rows, OpenedVolumePool{Name: p.Name, Provider: providerName, Err: err})
			continue
		}
		if err := open.Add(p.Name, vol); err != nil {
			logger.Error("volume pool: failed to register opened volume",
				slog.String("pool", p.Name), slog.Any("error", err))
			volume.RecordPoolHealth(p.Name, volume.Health{State: volume.StateUnavailable})
			rows = append(rows, OpenedVolumePool{Name: p.Name, Provider: providerName, Err: err})
			continue
		}
		volume.RecordPoolHealth(p.Name, vol.Health(ctx))
		rows = append(rows, OpenedVolumePool{Name: p.Name, Provider: providerName, Vol: vol})
	}
	return open, rows
}

// FailedOpens returns the subset of rows that failed to open, as
// volume.OpenFailure, naming the pool, the provider this node's own
// configuration selected for it, and the reason. Callers use it to build
// a *volume.OpenAtBootError for the default, fail-loud boot path, or to
// decide whether a degraded boot (--allow-degraded-volume-boot) has
// anything to report.
func FailedOpens(rows []OpenedVolumePool) []volume.OpenFailure {
	var fails []volume.OpenFailure
	for _, r := range rows {
		if r.Err != nil {
			fails = append(fails, volume.OpenFailure{Pool: r.Name, Provider: r.Provider, Err: r.Err})
		}
	}
	return fails
}

// CheckProviderConsistency compares each successfully opened row's local
// provider against the catalog's own record for that pool name (set at
// CreatePool time), once the catalog is available (it is NOT at the time
// OpenConfiguredVolumes itself runs -- see cmd/qrdbd/start.go's and
// sdk/go/node's boot sequence comments on why the replicated catalog
// attaches later). A mismatch (security review finding 7) means this
// node would otherwise silently serve the pool's tenants under the wrong
// storage semantics -- most seriously, plaintext "dir" where every other
// node in the cluster serves encrypted "luks2" -- so the mismatched
// row's Volume is closed and removed from open, and a
// *volume.ProviderMismatchError is returned for it (invariant 6: only
// that pool's tenants on this node are refused, never the whole node's
// boot; the caller logs and continues past each one, it does not abort).
// A pool this node could not open at all, or that the catalog does not
// (yet, or any longer) have a record for, is not this function's concern
// and is skipped.
func CheckProviderConsistency(ctx context.Context, catalog volume.Service, open *volume.OpenVolumes, rows []OpenedVolumePool) []error {
	var errs []error
	for _, row := range rows {
		if row.Vol == nil {
			continue
		}
		detail, err := catalog.GetPool(ctx, row.Name)
		if err != nil {
			continue
		}
		if detail.Provider == row.Provider {
			continue
		}
		open.Remove(row.Name)
		_ = row.Vol.Close(ctx)
		errs = append(errs, &volume.ProviderMismatchError{
			Pool:               row.Name,
			CatalogProvider:    detail.Provider,
			ConfiguredProvider: row.Provider,
		})
	}
	return errs
}

// CloseOpenedVolumes closes every successfully opened volume in rows,
// logging (not failing on) a close error since this runs during shutdown.
func CloseOpenedVolumes(ctx context.Context, rows []OpenedVolumePool, logger *slog.Logger) {
	for _, row := range rows {
		if row.Vol == nil {
			continue
		}
		if err := row.Vol.Close(ctx); err != nil {
			logger.Warn("volume pool: failed to close volume",
				slog.String("pool", row.Name), slog.Any("error", err))
		}
	}
}

// BuildNodeReport constructs this node's volume.NodeReport from rows,
// querying each opened Volume's live Usage and Health; a row that failed
// to open reports Mapped=true (the node's configuration DOES map it) with
// a Health built from its open error.
//
// Health.Detail NEVER carries the raw error's message: that message
// routinely embeds this node's own filesystem path (a stat/open error
// names the path it failed on), and NodeReport is a REPLICATED, wire-
// facing record -- every node in the cluster, and every caller of
// `volume-pool get`, reads it. The full error is logged locally, at this
// node, under the pool's name; the report carries only the typed State
// (already StateUnavailable for every open failure) and a detail string
// that names the CAUSE, never the PATH.
func BuildNodeReport(ctx context.Context, nodeID string, rows []OpenedVolumePool, logger *slog.Logger) volume.NodeReport {
	pools := make(map[string]volume.PoolOnNode, len(rows))
	for _, row := range rows {
		if row.Vol == nil {
			logger.Error("volume pool: open failed; full detail is local to this node",
				slog.String("pool", row.Name), slog.Any("error", row.Err))
			health := volume.Health{
				State:     volume.StateUnavailable,
				Detail:    "open failed; see this node's log",
				CheckedAt: time.Now(),
			}
			volume.RecordPoolHealth(row.Name, health)
			pools[row.Name] = volume.PoolOnNode{Mapped: true, Health: health}
			continue
		}
		usage, uerr := row.Vol.Usage(ctx)
		health := row.Vol.Health(ctx)
		if uerr != nil {
			logger.Warn("volume pool: usage query failed; full detail is local to this node",
				slog.String("pool", row.Name), slog.Any("error", uerr))
			if health.Detail == "" {
				health.Detail = "usage query failed; see this node's log"
			}
		}
		volume.RecordPoolHealth(row.Name, health)
		pools[row.Name] = volume.PoolOnNode{Mapped: true, Usage: usage, Health: health}
	}
	return volume.NodeReport{NodeID: nodeID, Pools: pools}
}

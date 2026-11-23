// Copyright (c) 2025 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TenantDatabaseLister is the part of database.Manager a NodeKeyLogProbe
// needs: the catalog's records of a tenant's databases, read without
// opening any of them. ListForTenant is not enough: it leaves out a
// database that does not open, which still writes the node-keyed log once
// it opens again.
type TenantDatabaseLister interface {
	TenantRecords(ctx context.Context, tenantID string) ([]database.DatabaseMeta, error)
}

// NodeKeyLogProbe lists a tenant's databases whose data is on a log under
// the node key (onNodeKeyLog), for the tenant service's refusal to tighten such a
// tenant to strict (tenant.NodeKeyLogProbe).
type NodeKeyLogProbe struct {
	lister TenantDatabaseLister

	// shared reads the shared system shard's composite namespaces; implicit
	// names the databases a tenant reaches there without a catalog record
	// (the default database, the SQL default). Both are optional.
	shared   SharedNamespaceReader
	implicit []string
	// names, when set, lists further implicit databases at call time (the
	// configured SQL default, the shared registry's databases), so a name
	// registered after the probe is built is still read.
	names func() []string
}

var _ tenant.NodeKeyLogProbe = (*NodeKeyLogProbe)(nil)

// SharedNamespaceReader reports whether a tenant holds data in the shared
// system shard under a database's composite namespace
// (tenant.ComposeNamespace). The system shard's WAL is under the node key, so
// such data is on a node-keyed log whether or not the catalog has a record of
// the database: the implicit default database never has one.
type SharedNamespaceReader interface {
	HasTenantData(ctx context.Context, tenantID, db string) (bool, error)
}

// NodeKeyLogProbeOption configures a NodeKeyLogProbe.
type NodeKeyLogProbeOption func(*NodeKeyLogProbe)

// WithSharedNamespaces makes the probe also report each implicit database
// name whose shared composite namespace holds data, read through reader. A
// read error fails the probe, so the tightening fails closed.
func WithSharedNamespaces(reader SharedNamespaceReader, implicit ...string) NodeKeyLogProbeOption {
	return func(p *NodeKeyLogProbe) {
		p.shared = reader
		p.implicit = append([]string(nil), implicit...)
	}
}

// WithSharedNamespaceNames adds a call-time source of implicit database
// names the probe reads in the shared system shard, besides the fixed
// names given to WithSharedNamespaces: the configured SQL default database
// and every database registered in the shared registry. It has no effect
// without a SharedNamespaceReader. Names are deduplicated against the
// catalog records and the fixed names.
func WithSharedNamespaceNames(names func() []string) NodeKeyLogProbeOption {
	return func(p *NodeKeyLogProbe) {
		p.names = names
	}
}

// NewNodeKeyLogProbe returns a probe over lister, typically the node's
// database.Manager.
func NewNodeKeyLogProbe(lister TenantDatabaseLister, opts ...NodeKeyLogProbeOption) *NodeKeyLogProbe {
	p := &NodeKeyLogProbe{lister: lister}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// NodeKeyLogDatabases implements tenant.NodeKeyLogProbe. It reads the
// replicated catalog's records, so a database created through another node
// counts, and so does one that does not open on this node. With
// WithSharedNamespaces it also reports each implicit database whose shared
// namespace holds the tenant's data.
func (p *NodeKeyLogProbe) NodeKeyLogDatabases(ctx context.Context, tenantID string) ([]string, error) {
	tctx := tenant.WithTenantID(ctx, tenantID)
	metas, err := p.lister.TenantRecords(tctx, tenantID)
	if err != nil {
		return nil, err
	}
	var out []string
	seen := make(map[string]struct{})
	for i := range metas {
		if metas[i].TenantID == tenantID && onNodeKeyLog(metas[i].Backend, metas[i].DataDir, metas[i].Barrier, metas[i].DedicatedWAL) {
			out = append(out, metas[i].Name)
			seen[metas[i].Name] = struct{}{}
		}
	}
	if p.shared == nil {
		return out, nil
	}
	implicit := p.implicit
	if p.names != nil {
		implicit = append(append([]string(nil), implicit...), p.names()...)
	}
	for _, name := range implicit {
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		has, err := p.shared.HasTenantData(tctx, tenantID, name)
		if err != nil {
			return nil, err
		}
		if has {
			out = append(out, name)
		}
	}
	return out, nil
}

// SharedNamespaceKV is the part of the system shard's KV store a
// KVSharedNamespaceReader needs.
type SharedNamespaceKV interface {
	ListAll(ctx context.Context, prefix string) ([]string, error)
}

// KVSharedNamespaceReader is a SharedNamespaceReader over the system shard's
// KV store: a tenant holds data in a database's shared namespace when any key
// sits under one of the namespace's physical prefixes (NamespacedKeyPrefixes).
type KVSharedNamespaceReader struct {
	kv SharedNamespaceKV
}

var _ SharedNamespaceReader = (*KVSharedNamespaceReader)(nil)

// NewKVSharedNamespaceReader returns a reader over kv, typically the node's
// system-shard KV service.
func NewKVSharedNamespaceReader(kv SharedNamespaceKV) *KVSharedNamespaceReader {
	return &KVSharedNamespaceReader{kv: kv}
}

// HasTenantData implements SharedNamespaceReader.
func (r *KVSharedNamespaceReader) HasTenantData(ctx context.Context, tenantID, db string) (bool, error) {
	for _, prefix := range NamespacedKeyPrefixes(tenant.ComposeNamespace(tenantID, db)) {
		keys, err := r.kv.ListAll(ctx, prefix)
		if err != nil {
			return false, err
		}
		if len(keys) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// onNodeKeyLog reports whether a raft- or barrier-isolated tenant's
// database writes its data to a log under the node key, judged by the route
// restServiceRegistry.resolve serves it from rather than by its backend
// field alone:
//   - a raft-backed database is a raft group, on the shared node-keyed WAL
//     unless it has a dedicated one;
//   - any other database is served from the shared system shard, whose WAL
//     is under the node key, unless its own barrier wraps the store it is
//     served from (an explicit data dir, database.DatabaseConfig.
//     BarrierOnDataPath).
//
// A barrier tenant never serves a raft database from its own group (its
// dedicated-WAL create is refused, isolationDedicatedWALDataPath), so the
// raft rule only spares a raft tenant's dedicated-WAL databases. A volume
// tenant is never asked: its log is under its own key.
func onNodeKeyLog(backend database.BackendType, dataDir string, barrier *database.DatabaseBarrierConfig, dedicatedWAL bool) bool {
	cfg := database.DatabaseConfig{Backend: backend, DataDir: dataDir, Barrier: barrier, DedicatedWAL: dedicatedWAL}
	if cfg.Backend == database.BackendRaft {
		return !cfg.DedicatedWAL
	}
	return cfg.Barrier == nil || !cfg.Barrier.Enabled || !cfg.BarrierOnDataPath()
}

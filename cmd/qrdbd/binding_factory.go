package main

import (
	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// installBindingFactory builds the node's binding factory and installs it on
// dbManager. Standalone and raft-mode nodes both call it: a memory, file,
// pebble or objstore database of a prefix or barrier tenant (and of the system
// tenant, except its system database) is materialized in its own store under
// its binding root, written through the binding's encryptor
// (docs/databases/architecture.md). On a raft-mode node (raftMode) a
// backend: raft database is left to the Manager's raft path. probe reports
// whether a backend: memory database's data predates its own store, so it
// keeps the shared route it lives in.
func installBindingFactory(dbManager *database.Manager, dataDir string, defaults database.Defaults,
	enc *atrest.Encryptor, storageCfg qrdbconfig.StorageConfig, tenants func() tenant.TenantServicer,
	barriers func() *seal.BarrierRegistry, raftMode bool, probe lifecycle.SharedKeyspaceProbe,
	hubOpts ...watch.HubOption) (*lifecycle.LocalBindingFactory, error) {
	bf := lifecycle.NewNodeBindingFactory(dataDir, defaults, enc, storageCfg, tenants, barriers, hubOpts...).
		WithSharedKeyspaceProbe(probe)
	if raftMode {
		bf = bf.WithRaftMode()
	}
	if err := dbManager.SetBackendFactory(bf); err != nil {
		return nil, err
	}
	return bf, nil
}
